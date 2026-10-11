package session_test

import (
	"testing"

	"github.com/luank3/gax/internal/conversation"
	"github.com/luank3/gax/internal/session"
)

func TestForkFileCopiesOnlyInheritedPrefixWithNewSelection(t *testing.T) {
	cwd := t.TempDir()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	oldProvider, oldModel, oldEffort := "pa", "ma", "hi"
	log, err := session.NewLog(cwd, session.Selection{Provider: &oldProvider, Model: &oldModel, Effort: &oldEffort})
	if err != nil {
		t.Fatal(err)
	}
	sourceID := log.ID()
	items := []conversation.Item{
		{Kind: conversation.KindTurnBoundary},
		{Kind: conversation.KindUser, Text: conversation.String("first")},
		{Kind: conversation.KindAssistant, Text: conversation.String("answer")},
		{Kind: conversation.KindTurnBoundary},
		{Kind: conversation.KindUser, Text: conversation.String("discarded")},
	}
	if err := log.Append(items); err != nil {
		t.Fatal(err)
	}
	defer log.Close()
	newProvider, newModel, label := "pb", "mb", "Model B"
	forkPath, err := session.ForkFile(log.Path(), items[:3], session.Selection{
		Provider: &newProvider, Model: &newModel, ModelLabel: &label,
	})
	if err != nil {
		t.Fatal(err)
	}
	fork, err := session.LoadAll(forkPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(fork.Items) != 3 || fork.Items[1].Text == nil || *fork.Items[1].Text != "first" {
		t.Fatalf("fork items = %+v", fork.Items)
	}
	for index, item := range fork.Items {
		if !item.Inherited {
			t.Errorf("fork item %d was not marked inherited: %+v", index, item)
		}
	}
	if fork.Meta.ID == nil || *fork.Meta.ID == *sourceID || fork.Meta.ForkedFrom == nil || *fork.Meta.ForkedFrom != *sourceID ||
		fork.Meta.Provider == nil || *fork.Meta.Provider != newProvider || fork.Meta.Model == nil || *fork.Meta.Model != newModel ||
		fork.Meta.ModelLabel == nil || *fork.Meta.ModelLabel != label || fork.Meta.Effort != nil {
		t.Fatalf("fork metadata = %+v, source id=%q", fork.Meta, *sourceID)
	}
	original, err := session.LoadAll(log.Path())
	if err != nil {
		t.Fatal(err)
	}
	if len(original.Items) != len(items) {
		t.Fatalf("fork changed source item count: %d", len(original.Items))
	}
	for index, item := range original.Items {
		if item.Inherited {
			t.Errorf("source item %d unexpectedly became inherited", index)
		}
	}
}
