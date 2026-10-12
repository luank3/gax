# Web tools spec

## Scope

`internal/web` owns the HTTP client, the HTML-to-text converter, and the two search backends;
`internal/tools/web.go` owns argument validation, the `web.*` and `no_web` settings, and the text
the model reads. Inputs are `web_fetch`'s `url` and `strip` and `web_search`'s `query` and `count`;
outputs are a body with a summary line or numbered results with `[%d results · %s]`.
Non-goal: JavaScript execution and cookies, so a client-rendered page yields the served bytes.

## Invariants

### Strip HTML unless the caller opts out

- **Rule:** Convert the body with `web.Text` when `strip` is true (the default) and the media type
  is `text/html` or `application/xhtml+xml`; otherwise pass the decoded bytes through unchanged.
- **Source:** `internal/tools/web.go:106` in `webDocumentText`.
- **Assertion:** `TestWebFetchStripsHTMLByDefault` in `internal/tools/web_test.go:103`.
- **Rejected:** Stripping every textual response, since `Document.HTML` (`internal/web/fetch.go:78`)
  admits only the two HTML media types and would mangle a JSON, XML, or CSV body.

---

### Never return a non-text body

- **Rule:** When `Document.Textual` is false, return the summary line alone, carrying the literal
  `only text responses are returned`; the response bytes never reach the model.
- **Source:** `internal/tools/web.go:108` in `webDocumentText`.
- **Assertion:** `TestWebFetchReportsBinaryStatusAndFailures` in
  `internal/tools/web_test.go:120`.
- **Rejected:** Returning the bytes with a type warning; a PNG body costs context and is unusable.

---

### Convert markup leniently, never inventing text

- **Rule:** Drop tags, break on block elements, bullet list items with `- `, split table cells with
  ` | `, skip `script`/`style`, collapse space outside `pre`, leave unknown references verbatim.
- **Source:** `internal/web/html.go:260` in `Text`.
- **Assertion:** `TestTextConvertsHTMLToReadableText` in `internal/web/html_test.go:9`.
- **Rejected:** Regex tag stripping, which breaks on the `skipped elements` case, whose
  `<script>document.write("<div>x</div>");</script>` would survive as prose.

---

### Follow redirects and report the final URL

- **Rule:** Fetch through an `http.Client` that follows redirects, record `response.Request.URL` as
  `Document.URL`, and let the summary line add `redirected to ` when it differs from the request.
- **Source:** `internal/web/fetch.go:136` in `Fetch`.
- **Assertion:** `TestFetchReturnsTextDocumentWithFinalURLAndHeaders` in
  `internal/web/fetch_test.go:16`.
- **Rejected:** Reusing the provider transport, which never follows redirects
  (`internal/web/fetch.go:38`), so a 302 page would be reported as the requested URL's content.

---

### Cap the download and mark truncation

- **Rule:** Read at most `Limits.MaxBytes` bytes (non-positive limits resolving to
  `DefaultMaxBytes`) and set `Document.Truncated` when bytes were dropped.
- **Source:** `internal/web/fetch.go:208` in `readLimited`, with defaults in `Limits.resolved`
  (`internal/web/fetch.go:56`).
- **Assertion:** `TestFetchReportsStatusAndTruncation` in `internal/web/fetch_test.go:53`.
- **Rejected:** Reading the whole response and slicing it afterwards; `readLimited` reads `limit+1`
  bytes through `io.LimitReader`, so a huge page is never buffered whole.

---

### Choose the backend by key and clamp the count

- **Rule:** Search through Brave when `SearchOptions.APIKey` is non-blank and through DuckDuckGo
  otherwise, with a count below 1 becoming `DefaultSearchResults` and one above 10 clamped to 10.
- **Source:** `internal/web/search.go:57` in `Search`.
- **Assertion:** `TestSearchUsesTheBraveAPIWhenAKeyIsConfigured` in
  `internal/web/search_test.go:113`.
- **Rejected:** Sending the requested count straight to the backend; the test asks for `Count: 99`
  and expects the wire parameter `count` to be `10`.

---

### Trust only a real DuckDuckGo result page

- **Rule:** Send the browser identity, accept only HTTP 200 as a result page, error with
  `set BRAVE_API_KEY to search through the Brave Search API` when no usable result is parsed, and
  unwrap a `duckduckgo.com` link through its `uddg` parameter.
- **Source:** `internal/web/search.go:150` and `internal/web/search.go:155` in `searchDuckDuckGo`,
  with `browserUserAgent` at `internal/web/search.go:30` and `resultURL` at
  `internal/web/search.go:235`.
- **Assertion:** `TestSearchLimitsResultsAndReportsFailures` in `internal/web/search_test.go:74`.
- **Rejected:** Accepting any 2xx; the endpoint answers a scripted client with HTTP 202 and an
  anomaly page, which would otherwise read as a successful empty search.

---

### Keep the Brave key out of every failure

- **Rule:** Report a rejected key, a rate limit, a non-2xx status, and invalid JSON without ever
  echoing the credential; the messages name `check BRAVE_API_KEY` and `set BRAVE_API_KEY`.
- **Source:** `internal/web/search.go:96` in `searchBrave`.
- **Assertion:** `TestSearchReportsBraveKeyAndRateLimitFailures` in
  `internal/web/search_test.go:150`.
- **Rejected:** Echoing the request or the `X-Subscription-Token` value so the user can check it;
  `TestWebSearchUsesTheConfiguredBraveKey` (`internal/tools/web_test.go:195`) fails any output
  containing the credential.

---

### Apply the tool output cap

- **Rule:** Truncate a tool result at the configured cap on a rune boundary and append the marker
  `[truncated at ` plus the byte limit and the remedy naming `bash` and `curl`.
- **Source:** `internal/tools/web.go:173` in `capWebText`.
- **Assertion:** `TestWebFetchCapsOutput` in `internal/tools/web_test.go:156`.
- **Rejected:** Slicing at the raw byte offset; the test asserts the marker suffix and the length.

## Gaps

- `DefaultTimeout` (30s), `DefaultMaxBytes` (2<<20), and `DefaultSearchResults` (5) have no
  asserting test: `TestFetchReportsStatusAndTruncation` (`internal/web/fetch_test.go:53`) passes
  `MaxBytes: 10`; the rest go unobserved.
- The summary fragments `redirected to ` (`internal/tools/web.go:141`) and `first ` plus
  ` of a larger response` (`internal/tools/web.go:136`) have no asserting test; the nearest is
  `TestFetchReturnsTextDocumentWithFinalURLAndHeaders` (`internal/web/fetch_test.go:16`).
- Resolved 2026-10-12: the plan (`docs/superpowers/plans/2026-10-11-web-tools.md:36`) governs, so
  `searchBrave` now rejects an empty or entry-less listing (`internal/web/search.go:126`) instead of
  returning an empty `Report`; `TestSearchReportsBraveKeyAndRateLimitFailures` pins both shapes.
- `webSearchText`'s `(no results)` branch (`internal/tools/web.go:148`) is unreachable now that both
  backends error on an empty listing; the `(untitled)` fallback (`:157`) and `(empty response)`
  (`:117`) still have no asserting test.
- The `maxEntityLength` bound (`internal/web/html.go:457`) has no asserting test: the `entities`
  case reaches only the missing-`;` and unknown-name paths.

## Review

- `result-snippet` on a `td` (`internal/web/search.go:211`): both DuckDuckGo fixtures use
  `a.result__snippet`, so that accepted form is never parsed in a test.
- `decodeCharset` (`internal/web/fetch.go:234`) writes U+0081 for undefined Windows-1252 positions,
  so the claim at `internal/web/fetch.go:265` of a replacement character does not hold.
- `Document.HTML` (`internal/web/fetch.go:79`) accepts `application/xhtml+xml`, but every test
  server serves `text/html`, so that branch of the strip rule is unverified.
