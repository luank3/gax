# Web tools spec

## Scope

`internal/web` owns the HTTP client, the HTML-to-text converter, and the search backends (you,
brave, octen, exa, firecrawl, tinyfish, with the DuckDuckGo crawl as the fallback);
`internal/tools/web.go` owns argument validation, the `web.*` and `no_web` settings, and the text
the model reads. Inputs are `web_fetch`'s `url` and `strip` and `web_search`'s `query` and `count`;
outputs are a body with a summary line or numbered results with `[%d results · %s]`.
Non-goal: JavaScript execution and cookies, so a client-rendered page yields the served bytes.

## Invariants

### Strip HTML unless the caller opts out

- **Rule:** Convert the body with `web.Text` when `strip` is true (the default) and the media type
  is `text/html` or `application/xhtml+xml`; otherwise pass the decoded bytes through unchanged.
- **Source:** `internal/tools/web.go:106` in `webDocumentText`.
- **Assertion:** `TestWebFetchStripsHTMLByDefault` in
  `internal/tools/web_test.go:114`.
- **Rejected:** Stripping every textual response, since `Document.HTML` (`internal/web/fetch.go:78`)
  admits only the two HTML media types and would mangle a JSON, XML, or CSV body.

---

### Never return a non-text body

- **Rule:** When `Document.Textual` is false, return the summary line alone, carrying the literal
  `only text responses are returned`; the response bytes never reach the model.
- **Source:** `internal/tools/web.go:108` in `webDocumentText`.
- **Assertion:** `TestWebFetchReportsBinaryStatusAndFailures` in
  `internal/tools/web_test.go:131`.
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

### Choose the backend by name, else by the first configured key

- **Rule:** Search through `SearchOptions.Provider` when it names a backend (anything but empty or
  `auto`), else through the first backend of `SearchProviders` with a non-empty key, else through
  you.com's keyless free profile; a count below 1 becomes `DefaultSearchResults` and one above 10 is
  clamped to 10.
- **Source:** `internal/web/search.go:207` in `SearchOptions.provider`, dispatched by
  `internal/web/search.go:183` in `searchBackend`.
- **Assertion:** `TestSearchSelectsTheBackend` in `internal/web/search_test.go:222`.
- **Rejected:** Sending the requested count straight to a backend; `TestSearchParsesBraveResults`
  (`internal/web/search_test.go:293`) asks for `Count: 99` and expects the wire parameter `count` to
  be `10`.
- **Order:** `SearchProviders` leads with you.com, so you.com's key outranks the other keys and its
  free profile answers when nothing is keyed; a key for another backend still wins over the keyless
  profile, because the loop looks for a key before falling back to it.

---

### Fall back to the keyless crawl

- **Rule:** When the selected backend fails for an availability reason — a rate limit, a server
  error, an unusable response, a refused free profile — retry once through the DuckDuckGo HTML
  crawl and report `Backend: duckduckgo`. A failure the user must act on (a missing or rejected key,
  a payment requirement, an empty listing, an unknown provider name) is marked `settledError` and
  returned as it is, and a cancelled turn is not retried at all.
- **Source:** `internal/web/search.go:148` in `Search`, `internal/web/search.go:175` in
  `shouldFallBack`, and `internal/web/search.go:567` in `searchDuckDuckGo`.
- **Assertion:** `TestSearchFallsBackToDuckDuckGo` in `internal/web/search_test.go:588`,
  `TestSearchKeepsFailuresTheUserMustFix` in `internal/web/search_test.go:634`, and the tool-level
  `TestWebSearchFallsBackToTheDuckDuckGoCrawl` in `internal/tools/web_test.go:264`.
- **Rejected:** Falling back on every failure: a wrong `BRAVE_API_KEY` would silently return crawl
  results, and a query with genuinely no hits would be searched twice. `TestSearchReportsBothFailures`
  (`internal/web/search_test.go:674`) pins the composed message when the fallback fails too.

---

### Trust only a real DuckDuckGo result page

- **Rule:** Send the browser identity, accept only HTTP 200 as a result page, error with
  `DuckDuckGo returned no usable results` when nothing parses, and unwrap a `duckduckgo.com` link
  through its `uddg` parameter.
- **Source:** `internal/web/search.go:567` in `searchDuckDuckGo`, with `browserUserAgent` at
  `internal/web/search.go:40`, `parseHTMLResults` at `internal/web/search.go:597`, and `resultURL`
  at `internal/web/search.go:670`.
- **Assertion:** `TestSearchFallsBackToDuckDuckGo` in `internal/web/search_test.go:588`.
- **Rejected:** Accepting any 2xx; the endpoint answers a scripted client with HTTP 202 and an
  anomaly page, which would otherwise read as a successful empty search.

---

### Call you.com's free profile without a key

- **Rule:** POST one `tools/call` for `you-search` to `https://api.you.com/mcp?profile=free` — no
  `initialize` handshake — and read the reply from `structuredContent`, falling back to the JSON in
  its text block.
- **Source:** `internal/web/search.go:298` in `searchYouFree`, with `parseRPCMessage`
  (`internal/web/search.go:781`) and `(*jsonRPCResult).searchPayload`
  (`internal/web/search.go:750`).
- **Assertion:** `TestSearchParsesYouFreeMCPResults` in
  `internal/web/search_test.go:354`, with the
  failure shapes in `TestSearchReadsYouFreeMCPFailures`
  (`internal/web/search_test.go:351`).
- **Rejected:** Trusting a text block alone; the reply arrives as `text/event-stream` with an
  informational `notifications/message` frame first, so the reader takes the first frame carrying a
  `result` or an `error`.

---

### Name the variable that fixes a refusal, never its value

- **Rule:** Map a non-2xx status onto the action behind it — rejected key, no credit, rate limit, or
  a plain status line — naming the environment variable of `web.<backend>_api_key`, appending the
  service's own message when it wrote one, and refusing a keyed backend with no key before the
  request is sent.
- **Source:** `internal/web/search.go:532` in `statusFailure`, `internal/web/search.go:521` in
  `requiredKey`, `internal/web/search.go:550` in `serviceMessage`, and
  `internal/web/search.go:44` in `CredentialEnv`.
- **Assertion:** `TestSearchReportsBackendFailures` in
  `internal/web/search_test.go:727` and
  `TestSearchRequiresAKeyAndAKnownProvider` in
  `internal/web/search_test.go:825`.
- **Rejected:** Echoing the request or its credential header so the user can check it;
  `TestWebSearchUsesTheConfiguredBraveKey` (`internal/tools/web_test.go:198`) fails any output
  containing the key.

---

### Report a refusal and an empty listing as errors

- **Rule:** An empty or entry-less listing is an error naming the backend, and Octen's non-zero
  business code inside an HTTP 200 body is a refusal rather than an empty listing.
- **Source:** `internal/web/search.go:560` in `finishSearch`, with the business-code check in
  `searchOcten` (`internal/web/search.go:290`).
- **Assertion:** `TestSearchReportsBackendFailures` in
  `internal/web/search_test.go:727`.
- **Rejected:** Returning an empty `Report`; the model reads it as "no results" when the service
  actually refused the request.

---

### Bound what a backend calls a snippet

- **Rule:** Flatten titles and snippets through the HTML converter, drop an entry carrying neither a
  title nor a URL, and cut a snippet longer than 400 bytes at a space on a rune boundary, appending
  `…`.
- **Source:** `internal/web/search.go:819` in `snippetText`, with `internal/web/search.go:691` in
  `addResult`.
- **Assertion:** `TestSearchBoundsSnippets` in
  `internal/web/search_test.go:838`.
- **Rejected:** Passing a highlight through whole: Exa and you.com return paragraphs where the
  result list expects a sentence, and ten of them would push the later results out of the tool
  output cap.

---

### Apply the tool output cap

- **Rule:** Truncate a tool result at the configured cap on a rune boundary and append the marker
  `[truncated at ` plus the byte limit and the remedy naming `bash` and `curl`.
- **Source:** `internal/tools/web.go:173` in `capWebText`.
- **Assertion:** `TestWebFetchCapsOutput` in
  `internal/tools/web_test.go:167`.
- **Rejected:** Slicing at the raw byte offset; the test asserts the marker suffix and the length.

## Gaps

- `DefaultTimeout` (30s), `DefaultMaxBytes` (2<<20), and `DefaultSearchResults` (5) have no
  asserting test: `TestFetchReportsStatusAndTruncation` (`internal/web/fetch_test.go:53`) passes
  `MaxBytes: 10`; the rest go unobserved.
- The summary fragments `redirected to ` (`internal/tools/web.go:145`) and `first ` plus
  ` of a larger response` (`internal/tools/web.go:140`) have no asserting test; the nearest is
  `TestFetchReturnsTextDocumentWithFinalURLAndHeaders` (`internal/web/fetch_test.go:16`).
- Resolved 2026-10-12: the plan (`docs/superpowers/plans/2026-10-11-web-tools.md:36`) governs, so
  every backend rejects an empty or entry-less listing through `finishSearch`
  (`internal/web/search.go:560`) instead of returning an empty `Report`, and that error is settled:
  a search with nothing to find is not repeated through the fallback.
  `TestSearchReportsBackendFailures` pins the shapes.
- `webSearchText`'s `(no results)` branch (`internal/tools/web.go:153`) is unreachable while every
  backend errors on an empty listing; the `(untitled)` fallback (`:161`) and `(empty response)`
  (`:121`) still have no asserting test.
- The keyless you.com profile allows 100 queries a day and none of the backends' own limits
  (Firecrawl's per-IP refusal, TinyFish's 12,000 free requests, Exa's credits) have an asserting
  test; the live checks in `docs/superpowers/plans/2026-10-12-web-search-providers.md` cover the
  free profile only.
- The `maxEntityLength` bound (`internal/web/html.go:11`) has no asserting test: the `entities`
  case reaches only the missing-`;` and unknown-name paths.

## Review

- `searchFirecrawl` (`internal/web/search.go:418`) omits the `Authorization` header when no key is
  configured, and only a test asserts it: Firecrawl refused this machine's IP without a key, so the
  keyless tier is unverified against the live service.
- `searchTinyFish` (`internal/web/search.go:458`) applies the requested count to the first page
  because the API takes no count parameter; a model asking for 10 gets ten, but nothing pages for
  more.
- `TestSearchKeepsFailuresTheUserMustFix` (`internal/web/search_test.go:634`) asserts the crawl was
  not reached by inspecting the last request the test server saw; a fallback that fired and failed
  before opening a connection would go unnoticed.
- `decodeCharset` (`internal/web/fetch.go:234`) writes U+0081 for undefined Windows-1252 positions,
  so the claim at `internal/web/fetch.go:265` of a replacement character does not hold.
- `Document.HTML` (`internal/web/fetch.go:79`) accepts `application/xhtml+xml`, but every test
  server serves `text/html`, so that branch of the strip rule is unverified.
