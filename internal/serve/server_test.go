package serve_test

import (
	"bufio"
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/luank3/gax/internal/provider"
	"github.com/luank3/gax/internal/provider/mock"
	"github.com/luank3/gax/internal/serve"
	"github.com/luank3/gax/internal/session"
)

func TestServeSecurityAndSSEStreaming(t *testing.T) {
	stateDir := t.TempDir()
	projDir := t.TempDir()

	srv, err := serve.NewServer(serve.Options{
		Listen:   "127.0.0.1:0",
		Token:    "test-secret-token",
		StateDir: stateDir,
		Version:  "v2-test",
		ResolveProv: func(_, _, cwd string) (provider.Client, error) {
			return mock.New("", cwd)
		},
	})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	defer srv.Close()

	// Non-loopback bind without --allow-remote must fail
	if _, err := serve.NewServer(serve.Options{Listen: "0.0.0.0:8484"}); err == nil {
		t.Fatal("expected non-loopback bind without AllowRemote to fail")
	}

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	// 1. Unauthenticated & URL token rejected
	resp, _ := http.Get(ts.URL + "/api/v1/projects")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", resp.StatusCode)
	}
	resp, _ = http.Get(ts.URL + "/api/v1/projects?token=test-secret-token")
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for query token, got %d", resp.StatusCode)
	}

	// 2. Origin mismatch rejected on POST
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/v1/projects", strings.NewReader(`{"path":"`+projDir+`"}`))
	req.Header.Set("Authorization", "Bearer test-secret-token")
	req.Header.Set("Origin", "https://evil.example")
	resp, _ = http.DefaultClient.Do(req)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403 for foreign origin, got %d", resp.StatusCode)
	}

	// 3. Register project & open session
	post := func(path string, body any) *http.Response {
		b, _ := json.Marshal(body)
		r, _ := http.NewRequest(http.MethodPost, ts.URL+path, bytes.NewReader(b))
		r.Header.Set("Authorization", "Bearer test-secret-token")
		r.Header.Set("Content-Type", "application/json")
		res, err := http.DefaultClient.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		return res
	}
	resp = post("/api/v1/projects", map[string]string{"path": projDir})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("add project: %d", resp.StatusCode)
	}
	var proj serve.Project
	_ = json.NewDecoder(resp.Body).Decode(&proj)
	resp.Body.Close()

	resp = post("/api/v1/sessions", map[string]string{"project_id": proj.ID})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("open session: %d", resp.StatusCode)
	}
	var sessOut struct{ ID string }
	_ = json.NewDecoder(resp.Body).Decode(&sessOut)
	resp.Body.Close()

	// 4. Subscribe SSE & send prompt
	sseReq, _ := http.NewRequest(http.MethodGet, ts.URL+"/api/v1/sessions/"+sessOut.ID+"/events", nil)
	sseReq.Header.Set("Authorization", "Bearer test-secret-token")
	sseResp, err := http.DefaultClient.Do(sseReq)
	if err != nil {
		t.Fatal(err)
	}
	defer sseResp.Body.Close()

	pResp := post("/api/v1/sessions/"+sessOut.ID+"/prompt", map[string]string{"text": "hello web"})
	if pResp.StatusCode != http.StatusAccepted {
		t.Fatalf("prompt: %d", pResp.StatusCode)
	}
	pResp.Body.Close()

	sc := bufio.NewScanner(sseResp.Body)
	sawComplete := false
	for sc.Scan() {
		line := sc.Text()
		if strings.Contains(line, `"outcome":"complete"`) {
			sawComplete = true
			break
		}
	}
	if !sawComplete {
		t.Fatal("did not observe turn_finished complete on SSE stream")
	}

	// 5. CLI session store sees the same persisted session
	list, err := session.List(stateDir, projDir)
	if err != nil || len(list) != 1 || list[0].ID != sessOut.ID {
		t.Fatalf("expected shared CLI session store entry, got %+v err=%v", list, err)
	}
}
