package conversation

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestTurnDoesNotRecordReasoningMemberNameAndItemCodecPreservesLegacyField(t *testing.T) {
	var turn Turn
	turn.Consume(Event{Kind: EventReasoningDelta, Text: "thought"})
	turn.Consume(Event{Kind: EventDone})
	items := turn.TakeItems()
	if len(items) != 1 || items[0].Kind != KindReasoning || items[0].ReasoningField != "" {
		t.Fatalf("turn should store reasoning text without a response member name: %#v", items)
	}

	legacyItem := Item{Kind: KindReasoning, ReasoningText: String("thought"), ReasoningField: "reasoning"}
	encoded, err := json.Marshal(legacyItem)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `"reasoning_field":"reasoning"`) {
		t.Fatalf("reasoning field was not persisted: %s", encoded)
	}
	decoded, err := DecodeItem([]byte(`{"kind":"reasoning","reasoning_text":"thought","reasoning_field":"reasoning"}`))
	if err != nil {
		t.Fatal(err)
	}
	if decoded.ReasoningField != "reasoning" {
		t.Fatalf("reasoning field was not restored: %+v", decoded)
	}
}
