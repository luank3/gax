package provider_test

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/luank3/gax/internal/core"
	"github.com/luank3/gax/internal/provider"
)

func TestBuildParametersSchemaOmitsOptionalFromRequired(t *testing.T) {
	minOne := 1
	raw := provider.BuildParametersSchema(core.ToolDef{
		Name: "bash",
		Params: []core.ToolParam{
			{Name: "command", Type: "string", Required: true},
			{Name: "timeout_seconds", Type: "integer", Minimum: &minOne},
		},
	})
	var parsed struct {
		Required []string `json:"required"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatal(err)
	}
	if len(parsed.Required) != 1 || parsed.Required[0] != "command" {
		t.Fatalf("expected only command required, got %v", parsed.Required)
	}
}

func TestExtraHeadersAndOverflowDetection(t *testing.T) {
	t.Setenv("TEST_GW_KEY", "super-secret-token")
	hdr := http.Header{}
	hdr.Set("X-Default", "old")
	secrets, warnings := provider.ApplyExtraHeaders(hdr, map[string]string{
		"X-Default": "",
		"X-Auth":    "$TEST_GW_KEY",
		"X-Session": "sess-{session_id}",
		"X-Literal": "$$escaped",
	}, "abc-123")
	if len(warnings) != 0 {
		t.Fatalf("unexpected warnings: %v", warnings)
	}
	if hdr.Get("X-Default") != "" || hdr.Get("X-Auth") != "super-secret-token" ||
		hdr.Get("X-Session") != "sess-abc-123" || hdr.Get("X-Literal") != "$escaped" {
		t.Fatalf("unexpected headers: %+v", hdr)
	}
	if len(secrets) != 1 || secrets[0] != "super-secret-token" {
		t.Fatalf("expected secret registered, got %v", secrets)
	}
	if !provider.IsOverflowError("This model's maximum context length is 8192 tokens", nil) {
		t.Fatal("expected overflow detection")
	}
	if !provider.IsOverflowError("custom proxy window full", []string{"window full"}) {
		t.Fatal("expected custom overflow phrase detection")
	}
}
