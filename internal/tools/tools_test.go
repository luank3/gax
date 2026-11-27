package tools_test

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/luank3/gax/internal/config"
	"github.com/luank3/gax/internal/core"
	"github.com/luank3/gax/internal/tools"
)

func TestReadEditWrite(t *testing.T) {
	dir := t.TempDir()
	reg := tools.NewRegistry(dir, nil)

	res := reg.Execute(context.Background(), "write", `{"path":"sub/a.txt","content":"one\ntwo\nthree\n"}`, nil)
	if res.IsError {
		t.Fatalf("write: %s", res.Text)
	}

	res = reg.Execute(context.Background(), "read", `{"path":"sub/a.txt","offset":2,"limit":1}`, nil)
	if res.IsError || !strings.Contains(res.Text, "2\ttwo") {
		t.Fatalf("read: %+v", res)
	}

	res = reg.Execute(context.Background(), "edit", `{"path":"sub/a.txt","old_text":"two","new_text":"TWO"}`, nil)
	if res.IsError || !strings.Contains(res.Text, "+TWO") {
		t.Fatalf("edit: %+v", res)
	}
}

func TestReadImageAndOversizedCap(t *testing.T) {
	dir := t.TempDir()
	reg := tools.NewRegistry(dir, nil)

	img := image.NewRGBA(image.Rect(0, 0, 4, 4))
	img.Set(0, 0, color.RGBA{R: 255, A: 255})
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "ok.png"), buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	res := reg.Execute(context.Background(), "read", `{"path":"ok.png"}`, nil)
	if res.IsError || len(res.Images) != 1 || res.Images[0].MediaType != "image/png" {
		t.Fatalf("expected png attachment, got %+v", res)
	}

	big := image.NewRGBA(image.Rect(0, 0, 4097, 1))
	buf.Reset()
	if err := png.Encode(&buf, big); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "big.png"), buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	res = reg.Execute(context.Background(), "read", `{"path":"big.png"}`, nil)
	if !res.IsError || !strings.Contains(res.Text, "downscale") {
		t.Fatalf("expected oversized image refusal, got %+v", res)
	}
}

func TestBashBackgroundDetachAndTaskWaitKill(t *testing.T) {
	dir := t.TempDir()
	store := config.NewStore()
	store.SetOverride("bash.background_yield", "50ms")
	reg := tools.NewRegistry(dir, store)
	defer reg.Close()

	res := reg.Execute(context.Background(), "bash", `{"command":"sleep 30","background":true,"name":"sleeper"}`, nil)
	if res.IsError || !strings.Contains(res.Text, "sleeper") {
		t.Fatalf("expected detach, got %+v", res)
	}

	res = reg.Execute(context.Background(), "task_wait", `{"id":"sleeper","kill":true}`, nil)
	if !strings.Contains(res.Text, "task sleeper finished") {
		t.Fatalf("expected killed task output, got %+v", res)
	}
}

func TestSkillDeduplicationInActiveContext(t *testing.T) {
	reg := tools.NewRegistry(t.TempDir(), nil)
	first := reg.Execute(context.Background(), "skill", `{"name":"code-intelligence"}`, nil)
	if first.IsError || !strings.Contains(first.Text, "[skill: code-intelligence") {
		t.Fatalf("first load: %+v", first)
	}
	reg.SetActiveItems(func() []core.Item {
		return []core.Item{{Kind: core.KindToolResult, ToolName: "skill", Output: first.Text}}
	})
	second := reg.Execute(context.Background(), "skill", `{"name":"code-intelligence"}`, nil)
	if second.IsError || !strings.Contains(second.Text, "already loaded") {
		t.Fatalf("expected deduplication note, got %+v", second)
	}
}
