# Web Search Providers Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the DuckDuckGo HTML crawl behind `web_search` with real search APIs: You.com (the
default, keyless through its free MCP profile), Brave (existing), Octen, Exa, Firecrawl, and
TinyFish — one credential setting each and a `web.search_provider` override. The crawl came back in
the follow-up below, as the fallback for a backend that cannot answer.

**Architecture:** `internal/web/search.go` keeps one function per backend behind a single `Search`
entry point, which resolves the backend from `SearchOptions.Provider` or from the first configured
key. `internal/web/fetch.go` grows the endpoint fields a test replaces and one `postJSON` helper.
`internal/tools/web.go` reads `web.search_provider` and the `web.<backend>_api_key` settings into a
`web.Keys` set, and `internal/config/settings.go` registers them.

**Tech Stack:** Go standard library only (`net/http`, `net/url`, `encoding/json`, `strconv`,
`bytes`, `unicode/utf8`).

**Spec:** replacing the DuckDuckGo *default* is the point of this change: its HTML endpoint serves
a no-JavaScript result page only to a browser-shaped client, throttles or challenges everything
else, and breaks whenever the markup changes, so it cannot be the path a fresh install depends on.
Each replacement backend was read from its own documentation before the parser was written, and the
You.com free MCP call was verified live (`initialize` is not required; the endpoint answers
`tools/call` directly). The crawl survives as the last resort for a backend that cannot answer,
where a throttled page costs nothing that was not already lost.

## Constraints

- Standard library only; `go.mod` keeps no `require` block.
- One credential setting per backend, named `web.<backend>_api_key` and reading that backend's own
  environment variable (`BRAVE_API_KEY`, `YDC_API_KEY`, `OCTEN_API_KEY`, `EXA_API_KEY`,
  `FIRECRAWL_API_KEY`, `TINYFISH_API_KEY`); `web.search_provider` picks one explicitly.
- No key at all still searches: you.com's `?profile=free` MCP profile needs none, so the tool keeps
  its keyless default instead of failing like every other backend does.
- A backend that cannot answer never leaves the model without results while the keyless crawl can
  still be reached, but a failure the user has to act on (a missing or rejected key, an empty
  listing, a provider name the settings reject) and a cancelled turn are reported instead of
  papered over.
- A missing key, a rejected key, a rate limit, a payment requirement, and an empty listing are each
  reported as their own diagnostic, naming the variable that fixes it and never echoing its value.
- A refusal that a backend reports inside a `200` body (Octen's business code) is an error, not an
  empty listing.
- Snippets are bounded to one line of at most 400 bytes, because a backend highlight is a paragraph
  where the tool renders a sentence.

## Tasks

### 1. Backend plumbing

- [x] Replace the two search endpoints with one field per backend plus the MCP endpoint.
- [x] Factor the request path into `send`, and add `postJSON` for the JSON-body backends.
- [x] Drop the DuckDuckGo parser, its browser identity constants, and the token accessors that only
      it used.

### 2. Backends

- [x] Write table-driven tests for each backend's request shape, credential header, result parsing,
      and failure mapping.
- [x] Add `searchBrave`, `searchYou` (REST with a key, free MCP without), `searchOcten`,
      `searchExa`, `searchFirecrawl`, and `searchTinyFish` behind `Search`.
- [x] Add the shared helpers: `requiredKey`, `statusFailure`, `serviceMessage`, `finishSearch`,
      `addResult`/`snippetText`, and the JSON-RPC envelope reader.
- [x] Verify `internal/web` tests.

### 3. Selection and settings

- [x] Register `web.search_provider` and the six credential settings.
- [x] Read them into `web.SearchOptions`, and update the tool description.
- [x] Keep the credential settings and the backend list in step with a test.
- [x] Verify `internal/tools` and `internal/config` tests.

### 4. Documentation

- [x] Update the README tool paragraph and the credentials list, the changelog, and the AGENTS.md
      layout table.
- [x] Record the removal in the web tools plan.
- [x] Run `make lint test-race`.

### 5. Follow-up: You.com first, DuckDuckGo as the fallback

- [x] Reorder automatic selection so you.com leads it: its key when set, its free profile otherwise.
- [x] Restore the DuckDuckGo crawl — identity headers, strict 200, the `result__a`/`result__snippet`
      parser, and the `uddg` unwrapping — and let an availability failure retry through it.
- [x] Mark the failures a fallback would bury (missing or rejected key, no credit, empty listing,
      unknown provider name) and skip the fallback for them, and for a cancelled turn.
- [x] Report both reasons when the fallback fails too.
- [x] Let `web.search_provider` name `duckduckgo` outright, and keep the setting's choices and the
      backend list in step with a test.
- [x] Verify `internal/web`, `internal/tools`, and `internal/config` tests.

## Verification

- [x] Run `make lint`.
- [x] Run `make test-race`.
- [x] Live check: `web.DefaultClient.Search` with no configured key returns real results through
      the free MCP profile (`backend=you`, three hits for `golang context timeout`).
- [ ] Live check against a keyed backend (Brave, Exa, Firecrawl, TinyFish): not run, no key on this
      machine.
- [x] Live check of the fallback: with the free profile pointed at an unreachable endpoint, the
      search answered through the live crawl (`backend=duckduckgo`, three hits for
      `golang context timeout`).

## Record

- Automatic selection leads with you.com — its key when set, its free profile when nothing is
  keyed — then takes the first of brave, octen, exa, firecrawl, and tinyfish with a key. A key for
  another backend still outranks the keyless you.com profile, since the loop looks for a key first
  and only then falls back to the profile.
- A failed backend spends its own timeout budget before the fallback starts (`web.timeout`, 30s by
  default), so a search whose backend times out can take twice that. Only the selected backend's
  timeout is spent when it answers, which is the normal case.
- The DuckDuckGo crawl is the fallback rather than a candidate: it answers when the selected backend
  fails for availability (rate limit, server error, unusable response, an exhausted free profile),
  and it stays selectable through `web.search_provider` for a user who wants nothing else. A
  failure the user must act on is marked `settledError` and returned as it is, so a wrong key is
  never hidden behind crawl results, and a cancelled turn does not fire a second request.
- The free MCP endpoint is stateless: a single `tools/call` POST answers with
  `text/event-stream`, an informational `notifications/message` frame first and the reply second,
  so the reader skips frames without a `result` or `error` and prefers `structuredContent` over the
  same JSON in a text block.
- Firecrawl is sent no `Authorization` header when no key is configured (its keyless tier exists,
  though it refuses suspicious IPs with 403), and its `sources: ["web"]` keeps the reply to
  `data.web`. Octen and TinyFish both report failures in the body, which is appended to the status
  error.
- Exa is asked for `contents.highlights` rather than full text: highlights are the cheapest
  per-result text it returns, and the model can `web_fetch` a page it wants to read.
- TinyFish takes no result-count parameter (its page is ten results), so the count is applied to
  the first page client-side.
- The you.com REST API and its MCP tool return the same `results.web` shape, so one parser serves
  both.
