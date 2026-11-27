package web_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/luank3/gax/internal/web"
)

func TestFetchAndHTMLToText(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, "<html><head><style>body{}</style></head><body><h1>Title</h1><p>Hello &amp; world</p></body></html>")
	}))
	defer srv.Close()

	c := web.NewClient()
	doc, err := c.Fetch(context.Background(), srv.URL, web.Limits{})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if !doc.HTML() {
		t.Fatal("expected HTML content type")
	}
	txt := web.HTMLToText(string(doc.Body))
	if txt != "Title\nHello & world" {
		t.Fatalf("unexpected HTMLToText: %q", txt)
	}
}

func TestSearchAllBackendsAndFallback(t *testing.T) {
	ddg := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `<a class="result__a" href="https://duckduckgo.com/l/?uddg=https%3A%2F%2Fexample.com%2Fddg">DDG Hit</a>`)
	}))
	defer ddg.Close()

	jsonSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/fail":
			w.WriteHeader(http.StatusTooManyRequests)
		case "/brave":
			_, _ = io.WriteString(w, `{"web":{"results":[{"title":"B","url":"https://b.test","description":"bd"}]}}`)
		case "/firecrawl":
			_, _ = io.WriteString(w, `{"data":{"web":[{"title":"F","url":"https://f.test","description":"fd"}]}}`)
		case "/you-mcp":
			_, _ = io.WriteString(w, `{"results":[{"title":"Y","url":"https://y.test","description":"yd"}]}`)
		default:
			_, _ = io.WriteString(w, `{"results":[{"title":"G","url":"https://g.test","snippet":"gd","text":"gd","description":"gd"}]}`)
		}
	}))
	defer jsonSrv.Close()

	c := web.NewClient()
	c.HTMLSearchEndpoint = ddg.URL
	c.Endpoints[web.BackendYou+"_mcp"] = jsonSrv.URL + "/you-mcp"
	c.Endpoints[web.BackendBrave] = jsonSrv.URL + "/brave"
	c.Endpoints[web.BackendOcten] = jsonSrv.URL + "/octen"
	c.Endpoints[web.BackendExa] = jsonSrv.URL + "/exa"
	c.Endpoints[web.BackendFirecrawl] = jsonSrv.URL + "/firecrawl"
	c.Endpoints[web.BackendTinyFish] = jsonSrv.URL + "/tinyfish"
	c.Endpoints[web.BackendKeenable] = jsonSrv.URL + "/keenable"
	c.Endpoints[web.BackendKeenable+"_p"] = jsonSrv.URL + "/keenable-pub"

	keys := web.Keys{Brave: "k", Octen: "k", Exa: "k", Firecrawl: "k", TinyFish: "k", Keenable: "k"}
	for _, b := range web.SearchBackends {
		rep, err := c.Search(context.Background(), "golang", web.SearchOptions{Provider: b, Keys: keys})
		if err != nil {
			t.Fatalf("backend %s failed: %v", b, err)
		}
		if rep.Backend != b || len(rep.Results) != 1 {
			t.Fatalf("backend %s unexpected report: %+v", b, rep)
		}
	}

	// Rate-limited backend falls back to DuckDuckGo.
	c.Endpoints[web.BackendYou+"_mcp"] = jsonSrv.URL + "/fail"
	rep, err := c.Search(context.Background(), "golang", web.SearchOptions{Provider: web.BackendYou})
	if err != nil || rep.Backend != web.BackendDuckDuckGo || !strings.Contains(rep.Results[0].URL, "example.com/ddg") {
		t.Fatalf("expected DuckDuckGo fallback, got rep=%+v err=%v", rep, err)
	}
}
