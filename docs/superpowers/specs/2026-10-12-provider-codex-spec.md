# codex spec

## Scope

- Inputs: the Codex CLI's `~/.codex/auth.json`, gax's store at `<stateHome>/gax/auth.json`, OAuth
  token and device responses, and a `*transport.Client`.
- Outputs: an `Auth` labelled `SourceGax` or `SourceCLI`, a rotated credential written back under
  lock, a `UsageReport` with its rendered rows, and a `LoginResult` carrying the account email.
- Non-goal: the runtime hooks that consume these credentials, and `settings.go`'s `LoadDefaults`
  reader for `~/.codex/config.toml`.

## Invariants

### Refresh is scoped to gax credentials and gated on expiry

- **Rule:** `ensureFresh` refreshes only a `SourceGax` credential, and only when its access token
  expires at or before now plus `refreshMargin` (10 minutes), unless `force` is set.
- **Source:** `internal/provider/codex/refresh.go:40` (`ensureFresh`; margin at refresh.go:25).
- **Assertion:** `TestQueryUsageForceRefreshesAfterOneUnauthorizedResponse` in
  `internal/provider/codex/usage_test.go:205`.

---
### A rotated credential is persisted, and a failed write only warns

- **Rule:** A successful refresh is written back to the store at mode `0600` and then adopted in
  memory, and a failed write returns `refreshFresh` with the warning
  `could not persist refreshed Codex credentials` instead of an error.
- **Source:** `internal/provider/codex/refresh.go:138` (`ensureFresh`; mode at refresh.go:275).
- **Assertion:** `TestQueryUsageProactivelyRefreshesAndPersistsRotatedGaxTokens` in
  `internal/provider/codex/usage_test.go:157`.
- **Rejected:** Keeping the rotation in memory only; the test reloads the store with `LoadAuth`
  (usage_test.go:195) and requires the rotated pair.

---
### Logout removes the local entry before it tries to revoke

- **Rule:** `LogoutManaged` deletes the `codex` key under the same sidecar lock refresh uses and
  only then revokes the refresh token, so an unreachable server still yields `removed=true`.
- **Source:** `internal/provider/codex/login.go:112` (`LogoutManaged`).
- **Assertion:** `TestLogoutManagedRemovesOfflineAndReturnsRevocationWarning` in
  `internal/provider/codex/login_test.go:68`.
- **Rejected:** Revoking first and returning the failure; the test answers 503 and still requires
  the secret gone from disk (login_test.go:86).

---
### LoadAuth prefers gax and falls back to the CLI file

- **Rule:** `LoadAuth` reads the gax store first and falls back to `~/.codex/auth.json` when the
  store is absent, unreadable, or has no complete Codex entry, which yields `SourceCLI`.
- **Source:** `internal/provider/codex/usage.go:87` (`LoadAuth`).
- **Assertion:** `TestLoadAuthPrefersGaxCredentialStoreAndFallsBackWhenItsEntryIsPartial` in
  `internal/provider/codex/usage_test.go:61`.
- **Rejected:** Trusting a partial `codex` entry; the test rewrites the store with
  `{"codex":{"access_token":"partial"}}` and requires the CLI credential to win (usage_test.go:89).

---
### One 401 buys one refresh and one retry

- **Rule:** A 401 from the usage endpoint triggers at most one forced refresh and one retry for a
  gax-managed credential, and a later failure keeps the diagnosis `failed to fetch usage from`.
- **Source:** `internal/provider/codex/usage.go:203` (`QueryUsage`; endpoint at usage.go:27).
- **Assertion:** `TestQueryUsagePreservesRetryServerErrorsAfterRefresh` in
  `internal/provider/codex/usage_test.go:233`.
- **Rejected:** Reporting a login problem after the retry failed; the test requires the error not to
  contain `login expired` (usage_test.go:252).

---
### The callback listener checks the path and the state prefix

- **Rule:** The loopback listener serves only the escaped path `/auth/callback`, and accepts `state`
  only when it equals the issued value or starts with that value followed by `.`.
- **Source:** `internal/provider/codex/auth_flow.go:280`; auth_flow.go:339 (`oauthStateMatches`).
- **Assertion:** `TestLoginBrowserValidatesStateAndInstallsCredential` in
  `internal/provider/codex/auth_flow_test.go:91`.
- **Rejected:** Comparing `state` exactly; the callback that completes that login appends
  `.onboarding_entrypoint=test` (auth_flow_test.go:146) and must still be served.

---
### OAuth secrets reach the redactor in both encodings

- **Rule:** Codes, verifiers, device ids, and refresh tokens are registered with the wiretrace
  redactor in raw and percent-encoded form before any request carries them.
- **Source:** `internal/provider/codex/auth_flow.go:568` (also auth_flow.go:492, refresh.go:89).
- **Assertion:** `TestExchangeAuthorizationCodeRedactsRawAndFormEncodedSecrets` in
  `internal/provider/codex/auth_flow_test.go:25`.
- **Rejected:** Registering only the raw value; the test requires `secret%2Fcode` and
  `verify%20secret` to be absent from the trace too (auth_flow_test.go:60).

---
### Device polling classifies the body, not the status

- **Rule:** `403`, `404`, and any error code containing `authorization_pending` stay pending,
  `slow_down` adds five seconds to the interval, and a 2xx carrying only one of
  `authorization_code` or `code_verifier` is a failure.
- **Source:** `internal/provider/codex/auth_flow.go:532` (`classifyDevicePoll`; growth at 525).
- **Assertion:** `TestClassifyDevicePollResponses` in
  `internal/provider/codex/auth_flow_test.go:220`.
- **Rejected:** Treating every 2xx as authorized; the case carrying only `authorization_code` must
  classify as `devicePollFailed` (auth_flow_test.go:231).

---
### Usage windows keep their fallback row

- **Rule:** `ParseUsage` keeps one window per present `primary_window`/`secondary_window`, relabels
  it from `limit_window_seconds` (`weekly` for 604800), and keeps an unusable window so
  `WriteUsageReport` prints `(unrecognized window shape)`.
- **Source:** `internal/provider/codex/usage.go:162` (`ParseUsage`; labels at usage.go:286).
- **Assertion:** `TestParseUsageKeepsPlanAndReportsRateLimitWindows` in
  `internal/provider/codex/usage_test.go:103`.
- **Rejected:** Dropping a window that lacks `used_percent` or `reset_at`; the test requires two
  windows with the second one unrecognized (usage_test.go:115).

## Gaps

- `refresh.go` has no test file: `ensureFresh` is reached only through `QueryUsage`, so its forced
  path is pinned indirectly by usage_test.go:205, while its persistence warning
  (`could not persist refreshed Codex credentials to the gax auth store`) and the terminal-rejection
  deletion (refresh.go:107, `isTerminalRefreshRejection` at refresh.go:233) are unasserted.
- The `auth.Source != SourceGax` early return (refresh.go:40) is unobserved;
  `TestQueryUsageRedactsExpiredTokenAndReportsMalformedResponse` (usage_test.go:257) passes an
  `Auth` with an empty `storePath` and stops at refresh.go:49 first.
- `writeAuthStore`'s temp file, `Chmod`, `Sync`, rename, and directory `Sync` (refresh.go:269) are
  unasserted; only the resulting `0600` mode (usage_test.go:199) and key preservation on the logout
  and login paths (login_test.go:58, auth_flow_test.go:365) are checked.
- The `slow_down` growth (auth_flow.go:525) is unasserted;
  `TestClassifyDevicePollResponses` (auth_flow_test.go:220) covers the classification only.

## Review

- The margin rule is asserted negatively: usage_test.go:205 proves no extra POST for a fresh token,
  not that the margin is 10 minutes, so a shorter `refreshMargin` would still pass.
- Store guarantees are split across suites: mode `0600` on the refresh path (usage_test.go:199) and
  `codex`-only replacement on logout (login_test.go:58) and login (auth_flow_test.go:365); no test
  writes through `writeAuthStore` from a refresh while another key is present.
- The sharpest untested fallback is the terminal rejection deletion (refresh.go:107); it drops the
  stored credential, and `RecoverUnauthorized` (refresh.go:162) and `QueryUsage` (usage.go:186, dead
  branch at usage.go:217) map its result to different messages.
- The corrupt-store branch (auth_flow.go:668) discards every other key, which
  `TestInstallManagedLoginReplacesInvalidCredentialStore` (auth_flow_test.go:371) requires by
  asserting a single key, while the normal path keeps them; confirm the discard is intended.
