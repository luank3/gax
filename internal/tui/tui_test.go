package tui_test

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/luank3/gax/internal/agent"
	"github.com/luank3/gax/internal/config"
	"github.com/luank3/gax/internal/core"
	"github.com/luank3/gax/internal/provider/mock"
	"github.com/luank3/gax/internal/tools"
	"github.com/luank3/gax/internal/tui"
)

func TestTUISlashCommandsAndMockTurn(t *testing.T) {
	dir := t.TempDir()
	prov, err := mock.New("", dir)
	if err != nil {
		t.Fatal(err)
	}
	reg := tools.NewRegistry(dir, nil)
	defer reg.Close()

	in := strings.NewReader("/help\n/session\nhello tui\n/undo\n/tasks\n/preset-save fav teal\n/preset fav\n/new\n")
	var out bytes.Buffer
	code := tui.Run(context.Background(), tui.Options{
		CWD:      dir,
		Version:  "v2-test",
		Store:    config.NewStore(),
		Session:  agent.NewSession(core.SessionHeader{Version: 1, ID: "tui-1", CWD: dir}),
		Provider: prov,
		Tools:    reg,
		Model:    "mock-1",
		Stdin:    in,
		Stdout:   &out,
		Stderr:   &out,
	})
	if code != 0 {
		t.Fatalf("expected exit 0, got %d", code)
	}
	text := out.String()
	for _, want := range []string{"gax v2-test · mock", "/provider", "session tui-1", "hello tui", "ctx 15", "rolled back to 0", "no background tasks", "saved preset \"fav\"", "started a new conversation"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q in TUI output:\n%s", want, text)
		}
	}
}
