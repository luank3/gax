# Provider wire spec

## Scope

Request bodies and SSE translation for the three HTTP wires: Chat Completions, Responses, and
Anthropic Messages.
Inputs: a `provider.Context` transcript, wire-specific `BodyOptions`, and the provider and model
identity used for reasoning provenance.
Outputs: `map[string]any` request bodies and `conversation.Event` streams.
Non-goal: choosing the wire, endpoint, headers, or credentials; `internal/provider/client` and
`internal/provider/runtime` own that.

## Invariants

### Fold adjacent assistant records into one assistant message
- **Rule:** Emit a single `role: "assistant"` message per run of adjacent `KindAssistant`,
  `KindToolCall`, and `KindReasoning` items.
- **Source:** `internal/provider/openai/chat_body.go:80`, `appendAssistantMessage`.
- **Assertion:** `TestBuildMessagesGroupsAssistantReasoningAndToolCalls` in `internal/provider/openai/chat_body_test.go:74`.
- **Rejected:** one message per record; the `role: "tool"` result loses its `tool_calls` parent.

---
### Gate reasoning replay on provenance
- **Rule:** Replay a `KindReasoning` item only when its recorded provider and model match the
  request, comparing canonical ids so `llama.cpp` matches `llamacpp`.
- **Source:** `internal/provider/openai/chat_body.go:210`, `provenanceMatches`.
- **Assertion:** `TestBuildMessagesGroupsAssistantReasoningAndToolCalls` in `internal/provider/openai/chat_body_test.go:74`.
- **Rejected:** comparing provider ids literally; the test records `llama.cpp`, not `llamacpp`.

---
### Name the reasoning member from configuration alone
- **Rule:** Name the plain reasoning member from `BodyOptions.ReasoningField` alone, never from the
  transcript, and leave the member out when the field is nil or empty.
- **Source:** `internal/provider/openai/chat_body.go:125`, `hasReasoningField`.
- **Assertion:** `TestBuildMessagesDoesNotInferReasoningFieldFromTranscript` in `internal/provider/openai/chat_body_test.go:178`.
- **Rejected:** reading `item.ReasoningField`; that recorded member may hold `role`.

---
### ReasoningRequired writes the member, never fabricates a message
- **Rule:** With `ReasoningRequired`, write the configured member as an empty string on otherwise
  valid assistant messages, and still drop turns with nothing else to send.
- **Source:** `internal/provider/openai/chat_body.go:140`, the `reasoningRequired` branch.
- **Assertion:** `TestBuildMessagesSendsRequiredReasoningFieldOnAssistantMessages` in `internal/provider/openai/chat_body_test.go:100`.
- **Rejected:** inventing a message for a contentless assistant record; the test expects none.

---
### Reasoning-only assistant messages keep a null content member
- **Rule:** Emit a reasoning-only assistant message with `content` set to null, never an empty
  string.
- **Source:** `internal/provider/openai/chat_body.go:130`, the `"content": nil` message literal.
- **Assertion:** `TestBuildMessagesUsesNullContentForReasoningOnlyAssistant` in `internal/provider/openai/chat_body_test.go:166`.
- **Rejected:** writing `""`; the test compares the member against nil, and an empty text part is
  not null.

---
### Responses tools are flat declarations
- **Rule:** Declare each Responses tool as one object carrying `type`, `name`, `description`, and
  `parameters` directly.
- **Source:** `internal/provider/openai/responses_body.go:83`, `BuildResponsesTools`.
- **Assertion:** `TestBuildResponsesBodyUsesFlatToolsAndEncryptedReasoning` in
  `internal/provider/openai/responses_body_test.go:11`.
- **Rejected:** nesting under `function` as Chat Completions does; the test asserts it is nil.

---
### extra_body applies last and merges objects recursively
- **Rule:** Apply `extra_body` after every protocol-owned field, merging nested objects key by key
  and letting arrays and scalars replace the generated member.
- **Source:** `internal/provider/openai/chat_body.go:289`, the `mergeObject` call in `BuildBody`.
- **Assertion:** `TestBuildBodyComposesChatRequest` in `internal/provider/openai/chat_body_test.go:12`.
- **Rejected:** replacing the whole member; the test keeps `keep` while `include_usage` flips.

---
### Unsigned Anthropic thinking blocks degrade to text
- **Rule:** Replay a `thinking` block as a text block when its `signature` is missing or empty, and
  drop the block when `thinking` is empty too.
- **Source:** `internal/provider/anthropic/body.go:98`, `reasoningBlock`.
- **Assertion:** `TestBuildBodyGroupsMessagesAndThinkingOptions` in
  `internal/provider/anthropic/body_test.go:11`.
- **Rejected:** forwarding the unsigned block; the test expects `cot` as a text block.

---
### Anthropic input tokens include cache reads and writes
- **Rule:** Report Anthropic input tokens as `input_tokens` plus `cache_read_input_tokens` plus
  `cache_creation_input_tokens`.
- **Source:** `internal/provider/anthropic/events.go:219`, `captureUsage`.
- **Assertion:** `TestEventsMergeUsageAndRejectIncompleteStopReason` in
  `internal/provider/anthropic/events_test.go:32`.
- **Rejected:** reporting `input_tokens` unchanged; the test feeds 100, 40, 10 and asserts 150.

---
### Emit one terminal event per stream
- **Rule:** Emit exactly one terminal event, and emit `stream ended before completion` when the
  stream ends before any terminal state.
- **Source:** `internal/provider/openai/responses_events.go:93`, `Finalize`.
- **Assertion:** `TestResponsesEventsSeparatesReasoningPartsAndFinalizesOnce` in
  `internal/provider/openai/responses_events_test.go:36`.
- **Rejected:** emitting unconditionally from `Finalize`; the test calls it twice, expecting one.

## Gaps

- Rule 7's rejection branches are pinned by `TestBuildBodyRejectsMalformedExtraBody`
  (`internal/provider/openai/chat_body_test.go`): `decode extra_body: %w` and
  `extra_body must be a JSON object` at `internal/provider/openai/chat_body.go:284`; the merge path is covered only
  indirectly by `TestPrepareAppliesModelRoutingAndProviderExtraBody` in
  `internal/provider/client/client_test.go:465`.
- Anthropic budget mode has no asserting test: `applyThinking` at
  `internal/provider/anthropic/body.go:271` clamps a budget at or above `MaxTokens` to
  `MaxTokens-1` and skips thinking when `MaxTokens < 2`;
  `internal/provider/anthropic/body_test.go:11` covers adaptive only.
- `toolUseBlock` at `internal/provider/anthropic/body.go:123` rewrites absent or non-object
  `Arguments` to `{}`; no test in `internal/provider/anthropic/body_test.go` sends non-object
  arguments.
- The chat tool-call id fallback `call_%d` at `internal/provider/openai/chat_events.go:299` has no
  asserting test; `TestStreamRetriesTransientStatusThenParsesSSE` in
  `internal/provider/openai/chat_stream_test.go:14` streams no `tool_calls`.
- `BuildResponsesInputItems` at `internal/provider/openai/responses_body.go:58` replays stored
  reasoning after `json.Valid` only, without checking that the decoded `type` is `reasoning`.

## Review

- Rule 2 exists twice with separate canonicalizers: `canonicalProviderID` at
  `internal/provider/openai/chat_body.go:217` and `providerIdentity` at
  `internal/provider/anthropic/body.go:177`; a new alias must land in both.
- Rule 10's chat counterpart at `internal/provider/openai/chat_events.go:90` differs: after a finish
  reason it routes through `emitTerminal`, which can emit an `EventError` embedding `LengthHint`.
- `attachCacheControl`'s `case []map[string]any` at `internal/provider/openai/chat_body.go:325` is
  reached only when a content-parts user message is the newest cacheable one; no cited test does.
- Rule 9 replaces rather than accumulates, so a `message_delta` repeating `input_tokens` would
  overwrite the summed value at `internal/provider/anthropic/events.go:227`.
