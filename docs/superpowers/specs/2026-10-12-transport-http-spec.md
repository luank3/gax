# Transport spec

## Scope

Owns `internal/transport`: TLS trust and CA overrides (`http.go`, `ca_roots.go`), buffered
`Get`/`Post`/`PostJSON`, streaming `SSEPost`, the SSE parser (`sse.go`), retry classification,
backoff and `Retry-After` parsing (`retry.go`, `http.go`), and error formatting (`api_error.go`).
Inputs are a context, `Name: value` headers, a body, timeouts and a `tick func() bool` probe;
outputs are bytes plus status, a `Response`, or an error. Non-goal: the retry loop and wire
parsing owned by `internal/provider/stream.go`; callers are `internal/provider/client`.

---
### CA overrides fail closed

- **Rule:** `configuredRootCAs` gives `CURL_CA_BUNDLE` priority over `SSL_CERT_FILE` and
  `SSL_CERT_DIR`, and an override that cannot be read still yields a non-nil pool, so verification
  fails instead of falling back to the system store.
- **Source:** `internal/transport/http.go:164` (`configuredRootCAs`),
  `internal/transport/http.go:199` (`rootPoolFromFile`)
- **Assertion:** `TestClientUsesCurlCABundleBeforeSSLOverrides` in
  `internal/transport/http_ca_test.go:23` and `TestClientDoesNotFallBackFromUnreadableCurlCABundle`
  in `internal/transport/http_ca_test.go:40`
- **Rejected:** returning `nil` for a missing bundle file; the client would then trust the system
  store and accept a server the override excluded.

---
### An HTTPS proxy never trusts the origin CA override

- **Rule:** when an origin CA override is configured, `NewClient` verifies the HTTPS proxy against
  the machine store from `systemProxyRootCAs`, so the override applies to the origin only.
- **Source:** `internal/transport/http.go:57` (`NewClient`), `internal/transport/ca_roots.go:19`
- **Assertion:** `TestSSLCertOverridesDoNotReplaceHTTPSProxyTrust` in
  `internal/transport/http_ca_test.go:92` and `TestCurlCABundleDoesNotReplaceHTTPSProxyTrust` in
  `internal/transport/http_ca_test.go:56`
- **Rejected:** leaving the proxy dial on nil roots; Go's Unix loader then reads `SSL_CERT_FILE`
  and `SSL_CERT_DIR`, which is the origin override.

---
### A missing trust store is reported

- **Rule:** with no CA override configured and an empty platform store, `withCAStoreHint` appends
  ` (no CA certificate store found on this system; install the ca-certificates package or set
  SSL_CERT_FILE)` to verification failures, and `SSEPost` returns it in `Response.ErrorBody`.
- **Source:** `internal/transport/http.go:214` (`missingCAStoreHint`),
  `internal/transport/http.go:222` (`withCAStoreHint`)
- **Assertion:** `TestSSETLSFailureIncludesMissingCAAdvice` in
  `internal/transport/http_ca_test.go:183` and `TestBufferedTLSFailureIncludesMissingCAAdvice` in
  `internal/transport/http_ca_test.go:203`
- **Rejected:** appending the hint to every error; only `tls.CertificateVerificationError` and
  `x509.SystemRootsError` are rewritten.

---
### SSE events emit on blank lines

- **Rule:** `Feed` carries partial lines across chunks, drops one leading space after the field
  colon and a trailing `\r`, joins repeated `data:` lines with `\n`, ignores `:` comment lines,
  emits the accumulated `SSEEvent` on each blank line, and delivers nothing after the callback
  returns false.
- **Source:** `internal/transport/sse.go:46` (`processLine`), `internal/transport/sse.go:85`
  (`emit`)
- **Assertion:** `TestHTTPRequestsAndSSEEventsAreTraced` in
  `internal/transport/http_trace_test.go:17`
- **Rejected:** treating each `Feed` chunk as whole lines, since `SSEPost` feeds raw socket chunks
  and a JSON payload can straddle two reads.

---
### Buffered status contract

- **Rule:** `Get` fails with `HTTP request returned status %d or an empty body` for a non-2xx
  status or empty body and returns that status with the error; `PostJSON` fails with the same text
  but drops the status, while `Post` returns any status with its body and a nil error.
- **Source:** `internal/transport/http.go:489` (`buffered`), `internal/transport/http.go:341`
  (`Post`), `internal/transport/http.go:330` (`PostJSON`)
- **Assertion:** `TestListModelsFormatsHTTPFailureWithoutLeakingKey` in
  `internal/provider/runtime/runtime_test.go:1010` (401 `Get`) and
  `TestHTTPRequestsAndSSEEventsAreTraced` in `internal/transport/http_trace_test.go:17`
- **Rejected:** returning status 0 with the strict error, or making `Post` strict; `ListModels`
  classifies by that status (`internal/provider/runtime/runtime.go:690`) and the Codex device flow
  reads body and status itself (`internal/provider/codex/auth_flow.go:420`).

---
### Retry classification

- **Rule:** `ShouldRetry` retries status 0, 408, 429 and 5xx but never 2xx, and treats a 429 as
  terminal when the body's `error.type` or `error.code`, compared lowercased, is one of
  `usage_limit_reached`, `usage_not_included`, `insufficient_quota`, `quota_exceeded`,
  `gousagelimiterror`.
- **Source:** `internal/transport/retry.go:39` (`ShouldRetry`), `internal/transport/retry.go:119`
- **Assertion:** `TestStreamRetriesTransientStatusThenParsesSSE` in
  `internal/provider/openai/chat_stream_test.go:14` (indirect; only the 503 case)
- **Rejected:** `status == 429` on its own; an exhausted quota would then burn every attempt.

---
### Retry timing is bounded

- **Rule:** `DefaultRetryPolicy(n, base, idle)` yields `MaxAttempts = n+1`, `MaxDelay` of
  `maxRetryDelay` (30s) and an idle timeout rounded up to whole seconds; `Delay` doubles from
  `BaseDelay` per attempt, clamps to `MaxDelay` and returns 0 when either bound is not positive;
  `parseRetryAfter` clamps a numeric or HTTP-date `Retry-After` to `maxRetryAfter` (2 minutes), or
  0 when it is empty, unparsable or elapsed, and `SSEPost` records that in `Response.RetryAfter`.
- **Source:** `internal/transport/retry.go:21`, `internal/transport/retry.go:55`,
  `internal/transport/http.go:645`
- **Assertion:** none in this repository; see Gaps.

---
### API errors are formatted and capped

- **Rule:** `FormatAPIError` renders `HTTP %d: %s`, preferring a JSON string from `error`,
  `error.message` or `message` after an SSE unwrap, else stripping tags and `script`/`style`
  bodies; truncates to `maxAPIErrorBytes` (200 bytes) with `...`; empty yields bare `HTTP %d`.
- **Source:** `internal/transport/api_error.go:12` (`FormatAPIError`),
  `internal/transport/api_error.go:124` (`truncateUTF8`)
- **Assertion:** none in this repository; see Gaps.

## Gaps

- `sse.go` and `ca_roots.go` have no `*_test.go` of their own; nearest coverage is
  `http_trace_test.go`, `http_ca_test.go` and
  `internal/provider/anthropic/stream_test.go:13`
  (`TestStreamRetriesThenTranslatesMessagesEvents`).
- `parseRetryAfter`'s branching is unexercised, and no test drives `Frame`/`Finalize` through
  `SSEParser` directly; `TestFormatAPIErrorPrefersTheJSONMessage` reaches `Finalize` only through
  `unwrapSSEError`.
- `internal/transport/retry_test.go` now pins `DefaultRetryPolicy`, `ShouldRetry`, `Delay`,
  `SleepWithTick`, `parseRetryAfter`, `FormatAPIError`, and `FormatModelListError`.
- The plan text credits `http_trace_test.go` and the provider stream tests with pinning `http.go`;
  `http.go`'s CA precedence, proxy trust and missing-store hint are pinned by `http_ca_test.go`.

## Review

- `internal/provider/stream.go:36` falls back to `transport.DefaultRetryPolicy(4, time.Second,
  10*time.Minute)` when the caller passes a zero policy (which `internal/app/app.go:527` does);
  every test supplies `MaxAttempts`, so production retry pacing is unverified.
- `internal/transport/sse.go:36` (`Finalize`) and the multi-line `data:` join are unasserted; a
  server that omits the trailing blank line depends on `internal/transport/http.go:279`.
- `internal/transport/http.go:489` and `internal/transport/http.go:347` encode opposite status
  policies for `Get`/`PostJSON` and `Post`; check each new caller picks the shape it can handle.
- `internal/transport/retry.go:73` derives jitter from process start, so `Delay` is time-dependent;
  confirm the clamps at `internal/transport/retry.go:79` hold for large attempts.
