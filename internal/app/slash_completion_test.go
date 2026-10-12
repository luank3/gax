package app

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"

	"github.com/luank3/gax/internal/config"
	"github.com/luank3/gax/internal/provider/registry"
	"github.com/luank3/gax/internal/provider/runtime"
	"github.com/luank3/gax/internal/transport"
)

func TestSlashCompleterCompletesArguments(t *testing.T) {
	completer := slashCompleter(func(name string) []string {
		if name == "provider" {
			return []string{"mock", "openai"}
		}
		return nil
	})
	start, end, matched := completer.Match("/provider mo", 12)
	if !matched || start != 1 || end != 12 {
		t.Fatalf("Match = (%d, %d, %t)", start, end, matched)
	}
	if completed, ok := completer.Complete("provider mo"); !ok || completed != "provider mock " {
		t.Fatalf("Complete = %q, %t", completed, ok)
	}
	if candidates, ok := completer.Candidates("provider "); !ok || candidates != "  mock openai" {
		t.Fatalf("Candidates = %q, %t", candidates, ok)
	}
	// An argument word of a command that enumerates nothing stays with the completers that follow.
	if _, _, matched := completer.Match("/compact @src", 13); matched {
		t.Fatal("compact argument word matched the command completer")
	}
}

func TestSlashArgumentChoicesForPresets(t *testing.T) {
	store := config.New(config.DefaultSettings())
	store.SetEnvironment(func(string) (string, bool) { return "", false })
	if err := store.LoadConfig([]byte(`{"presets":{"review":{"provider":"mock"},"alpha":{"provider":"mock"},` +
		`"zeta":{"provider":"mock"},"code review":{"provider":"mock"}}}`)); err != nil {
		t.Fatal(err)
	}
	// Alphabetical, and without a name outside the preset-name grammar, which cannot complete as one
	// word.
	want := []string{"alpha", "review", "zeta"}
	if got := slashArgumentChoices("preset", nil, nil, nil, store); !reflect.DeepEqual(got, want) {
		t.Fatalf("preset choices = %v, want %v", got, want)
	}
	if got := slashArgumentChoices("new", nil, nil, nil, store); !reflect.DeepEqual(got, want) {
		t.Fatalf("new choices = %v, want %v", got, want)
	}
	if got := slashArgumentChoices("tasks", nil, nil, nil, store); got != nil {
		t.Fatalf("tasks choices = %v, want none", got)
	}
}

func TestSlashArgumentChoicesForSelection(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" {
			t.Errorf("unexpected request path %q", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"data":[{"id":"zeta-model"},{"id":"alpha-model"},{"id":"esc\u001b[31m"}]}`)
	}))
	defer server.Close()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(t.TempDir(), "state"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(t.TempDir(), "cache"))
	t.Setenv("GAX_OPENAI_BASE_URL", server.URL)
	t.Setenv("GAX_OPENAI_API_KEY", "test-key")
	t.Setenv("GAX_CATALOG_URL", "")
	store := config.New(config.DefaultSettings())
	store.SetEnvironment(os.LookupEnv)
	providers := registry.New(store)
	provider, err := runtime.NewResolver(store, providers).Resolve("openai-compatible")
	if err != nil {
		t.Fatal(err)
	}
	defer provider.Close()
	model := "alpha-model"

	ids := slashArgumentChoices("provider", providers, provider, &model, store)
	if len(ids) == 0 || !sort.StringsAreSorted(ids) {
		t.Fatalf("provider choices = %v", ids)
	}
	if slashArgumentChoices("provider", nil, provider, &model, store) != nil {
		t.Fatal("provider choices without a registry were offered")
	}
	if got := slashArgumentChoices("model", providers, provider, &model, store); got != nil {
		t.Fatalf("model choices before a listing = %v", got)
	}
	if _, err := provider.ListModels(context.Background(), transport.DefaultClient, nil); err != nil {
		t.Fatal(err)
	}
	listed := slashArgumentChoices("model", providers, provider, &model, store)
	// The version-aware order applies, and a control byte in an id never reaches the prompt.
	if !reflect.DeepEqual(listed, []string{"alpha-model", "esc[31m", "zeta-model"}) {
		t.Fatalf("model choices = %v", listed)
	}
	levels := slashArgumentChoices("effort", providers, provider, &model, store)
	if len(levels) < 2 || levels[len(levels)-1] != "default" {
		t.Fatalf("effort choices = %v", levels)
	}
	if got := slashArgumentChoices("effort", providers, nil, &model, store); got != nil {
		t.Fatalf("effort choices without a provider = %v", got)
	}
}
