package web_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/luank3/gax/internal/web"
)

// youWebResults is the result section the You.com Web Search API and its MCP tool share.
const youWebResults = `{"results":{"web":[
	{"url":"https://blog.example.com/guide","title":"<b>The</b> complete guide","snippets":["First snippet.","Second snippet."]},
	{"url":"https://stackoverflow.com/q/1","title":"How to set a timeout?","description":"Use a context deadline."},
	{"url":"https://example.com/highlight","title":"Highlights entry","contents":{"highlights":["A page highlight."]}}
]},"metadata":{"query":"golang timeout"}}`

const braveResults = `{"web":{"results":[
	{"title":"<strong>Go</strong> timeouts","url":"https://blog.example.com/guide","description":"Take apart the <b>stages</b> of a timeout."},
	{"title":"Stack Overflow","url":"https://stackoverflow.com/q/1","description":"Use a context deadline."}
]}}`

const octenResults = `{"code":0,"msg":"success","request_id":"req_1","data":{"results":[
	{"title":"Influenza","url":"https://www.who.int/news-room/fact-sheets","highlight":"WHO recommends <b>annual</b> vaccination."},
	{"title":"  ","url":"  ","highlight":""}
]}}`

const exaResults = `{"requestId":"r1","results":[
	{"title":"Go timeouts","url":"https://blog.example.com/guide","highlights":["A <b>highlighted</b> passage.","A second passage."]},
	{"title":"Stack Overflow","url":"https://stackoverflow.com/q/1","text":"Plain text passage."}
]}`

const firecrawlResults = `{"success":true,"data":{"web":[
	{"title":"Go timeouts","url":"https://blog.example.com/guide","description":"Take apart the stages.","position":1}
]}}`

// crawlResults is the result page DuckDuckGo serves: an anchor with the result__a class, its
// result__snippet text, and a hit whose link goes through the click-through redirect.
const crawlResults = `<html><body>
<div class="result results_links results_links_deep web-result">
  <a rel="nofollow" class="result__a" href="https://blog.example.com/guide">The complete <b>guide</b></a>
  <a class="result__snippet" href="https://blog.example.com/guide">Take apart the <b>timeout</b> stages.</a>
</div>
<div class="result results_links results_links_deep web-result">
  <a rel="nofollow" class="result__a" href="//duckduckgo.com/l/?uddg=https%3A%2F%2Fstackoverflow.com%2Fq%2F1&amp;rut=abc">How to set a timeout?</a>
  <td class="result-snippet">Use a context deadline.</td>
</div>
<div class="no-results">No more results.</div>
</body></html>`

const tinyFishResults = `{"query":"golang timeout","results":[
	{"position":1,"site_name":"blog.example.com","title":"Go timeouts","snippet":"Take apart the stages.","url":"https://blog.example.com/guide"},
	{"position":2,"site_name":"stackoverflow.com","title":"How to set a timeout?","snippet":"Use a context deadline.","url":"https://stackoverflow.com/q/1"}
],"total_results":10,"page":0}`

// youWebCompact is youWebResults on one line: an SSE data field cannot carry a raw newline.
var youWebCompact = compactedJSON(youWebResults)

// youFreeResults is the SSE body the free MCP profile answers a tools/call with: an informational
// notification, then the reply, whose structured content and text block carry the same JSON.
var youFreeResults = sseFrame(`{"jsonrpc":"2.0","method":"notifications/message","params":{"level":"info","data":"Search successful"}}`) +
	sseFrame(fmt.Sprintf(`{"jsonrpc":"2.0","id":1,"result":{"_meta":{"you/usage":{"sku":"search"}},"content":[{"type":"text","text":%s}],"structuredContent":%s}}`,
		strconv.Quote(youWebCompact), youWebCompact))

// sseFrame renders one MCP frame as a server-sent event.
func sseFrame(payload string) string {
	return "event: message\ndata: " + payload + "\n\n"
}

// compactedJSON flattens a JSON fixture onto one line.
func compactedJSON(value string) string {
	var compacted bytes.Buffer
	if err := json.Compact(&compacted, []byte(value)); err != nil {
		panic(err)
	}
	return compacted.String()
}

// searchRecorder captures the request one backend made, for the assertions about parameters and
// credentials.
type searchRecorder struct {
	mu      sync.Mutex
	request *http.Request
	body    []byte
}

func (recorder *searchRecorder) record(r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	recorder.request = r.Clone(context.Background())
	recorder.body = body
}

func (recorder *searchRecorder) snapshot() (*http.Request, []byte) {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	return recorder.request, recorder.body
}

func (recorder *searchRecorder) path() string {
	request, _ := recorder.snapshot()
	if request == nil {
		return ""
	}
	return request.URL.Path
}

func (recorder *searchRecorder) header(name string) string {
	request, _ := recorder.snapshot()
	if request == nil {
		return ""
	}
	return request.Header.Get(name)
}

func (recorder *searchRecorder) queryParameter(name string) string {
	request, _ := recorder.snapshot()
	if request == nil {
		return ""
	}
	return request.URL.Query().Get(name)
}

func (recorder *searchRecorder) jsonBody(t *testing.T) map[string]any {
	t.Helper()
	_, body := recorder.snapshot()
	var parsed map[string]any
	if err := json.Unmarshal(body, &parsed); err != nil {
		t.Fatalf("request body %q is not valid JSON: %v", body, err)
	}
	return parsed
}

// searchTestClient points every backend at one test server, so a test sees which backend answered.
func searchTestClient(server *httptest.Server) *web.Client {
	return &web.Client{
		HTTP:               server.Client(),
		BraveEndpoint:      server.URL + "/brave",
		YouEndpoint:        server.URL + "/you",
		YouMCPEndpoint:     server.URL + "/you-mcp",
		OctenEndpoint:      server.URL + "/octen",
		ExaEndpoint:        server.URL + "/exa",
		FirecrawlEndpoint:  server.URL + "/firecrawl",
		TinyFishEndpoint:   server.URL + "/tinyfish",
		HTMLSearchEndpoint: server.URL + "/crawl",
	}
}

// newSearchTestServer serves one canned listing per backend and records the request that asked for
// it.
func newSearchTestServer(t *testing.T) (*web.Client, *searchRecorder) {
	t.Helper()
	recorder := &searchRecorder{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		recorder.record(r)
		switch r.URL.Path {
		case "/you-mcp":
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, youFreeResults)
		case "/brave":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, braveResults)
		case "/you":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, youWebResults)
		case "/octen":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, octenResults)
		case "/exa":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, exaResults)
		case "/firecrawl":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, firecrawlResults)
		case "/tinyfish":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, tinyFishResults)
		case "/crawl":
			w.Header().Set("Content-Type", "text/html")
			_, _ = io.WriteString(w, crawlResults)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	return searchTestClient(server), recorder
}

// withKey adds one backend's credential, the way the config store fills the set.
func withKey(keys web.Keys, provider, key string) web.Keys {
	switch provider {
	case web.BackendBrave:
		keys.Brave = key
	case web.BackendYou:
		keys.You = key
	case web.BackendOcten:
		keys.Octen = key
	case web.BackendExa:
		keys.Exa = key
	case web.BackendFirecrawl:
		keys.Firecrawl = key
	case web.BackendTinyFish:
		keys.TinyFish = key
	}
	return keys
}

// keysFor builds the credential set with one backend keyed.
func keysFor(provider, key string) web.Keys {
	return withKey(web.Keys{}, provider, key)
}

func TestSearchSelectsTheBackend(t *testing.T) {
	client, recorder := newSearchTestServer(t)
	tests := []struct {
		name     string
		options  web.SearchOptions
		backend  string
		wantPath string
	}{
		{name: "you with a key", options: web.SearchOptions{Keys: keysFor(web.BackendYou, "you-secret")}, backend: web.BackendYou, wantPath: "/you"},
		{
			name: "you.com leads the automatic order",
			options: web.SearchOptions{
				Keys: withKey(keysFor(web.BackendBrave, "brave-secret"), web.BackendYou, "you-secret"),
			},
			backend:  web.BackendYou,
			wantPath: "/you",
		},
		{
			name:     "a key for another backend outranks the keyless you.com profile",
			options:  web.SearchOptions{Keys: keysFor(web.BackendBrave, "brave-secret")},
			backend:  web.BackendBrave,
			wantPath: "/brave",
		},
		{name: "octen", options: web.SearchOptions{Keys: keysFor(web.BackendOcten, "octen-secret")}, backend: web.BackendOcten, wantPath: "/octen"},
		{name: "exa", options: web.SearchOptions{Keys: keysFor(web.BackendExa, "exa-secret")}, backend: web.BackendExa, wantPath: "/exa"},
		{name: "firecrawl", options: web.SearchOptions{Keys: keysFor(web.BackendFirecrawl, "fc-secret")}, backend: web.BackendFirecrawl, wantPath: "/firecrawl"},
		{name: "tinyfish", options: web.SearchOptions{Keys: keysFor(web.BackendTinyFish, "tiny-secret")}, backend: web.BackendTinyFish, wantPath: "/tinyfish"},
		{
			name:     "no key falls back to the free you.com search",
			options:  web.SearchOptions{},
			backend:  web.BackendYou,
			wantPath: "/you-mcp",
		},
		{
			name: "an explicit provider wins over the configured keys",
			options: web.SearchOptions{
				Provider: web.BackendTinyFish,
				Keys:     withKey(keysFor(web.BackendBrave, "brave-secret"), web.BackendTinyFish, "tiny-secret"),
			},
			backend:  web.BackendTinyFish,
			wantPath: "/tinyfish",
		},
		{
			name:     "auto is the same as an empty provider",
			options:  web.SearchOptions{Provider: "auto", Keys: keysFor(web.BackendOcten, "octen-secret")},
			backend:  web.BackendOcten,
			wantPath: "/octen",
		},
		{
			name:     "the crawl can be selected outright",
			options:  web.SearchOptions{Provider: web.BackendDuckDuckGo},
			backend:  web.BackendDuckDuckGo,
			wantPath: "/crawl",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			report, err := client.Search(context.Background(), "golang timeout", test.options)
			if err != nil {
				t.Fatal(err)
			}
			if report.Backend != test.backend || len(report.Results) == 0 {
				t.Fatalf("report = %+v, want backend %q", report, test.backend)
			}
			if got := recorder.path(); got != test.wantPath {
				t.Fatalf("backend was reached at %q, want %q", got, test.wantPath)
			}
		})
	}
}

func TestSearchParsesBraveResults(t *testing.T) {
	client, recorder := newSearchTestServer(t)
	report, err := client.Search(context.Background(), "golang timeout", web.SearchOptions{
		Keys: web.Keys{Brave: "brave-secret"}, Count: 99,
	})
	if err != nil {
		t.Fatal(err)
	}
	if report.Backend != web.BackendBrave || len(report.Results) != 2 {
		t.Fatalf("brave report = %+v", report)
	}
	if report.Results[0].Title != "Go timeouts" || report.Results[0].Snippet != "Take apart the stages of a timeout." {
		t.Fatalf("brave result markup was not stripped: %+v", report.Results[0])
	}
	if got := recorder.queryParameter("count"); got != "10" {
		t.Fatalf("brave count = %q, want the clamped maximum", got)
	}
	if got := recorder.queryParameter("q"); got != "golang timeout" {
		t.Fatalf("brave query = %q", got)
	}
	if got := recorder.header("X-Subscription-Token"); got != "brave-secret" {
		t.Fatalf("brave subscription header = %q", got)
	}
	if got := recorder.header("Accept"); got != "application/json" {
		t.Fatalf("brave Accept = %q", got)
	}
}

func TestSearchParsesYouResults(t *testing.T) {
	client, recorder := newSearchTestServer(t)
	report, err := client.Search(context.Background(), "golang timeout", web.SearchOptions{
		Keys: web.Keys{You: "you-secret"}, Count: 5,
	})
	if err != nil {
		t.Fatal(err)
	}
	if report.Backend != web.BackendYou {
		t.Fatalf("backend = %q", report.Backend)
	}
	want := []web.Result{
		{Title: "The complete guide", URL: "https://blog.example.com/guide", Snippet: "First snippet."},
		{Title: "How to set a timeout?", URL: "https://stackoverflow.com/q/1", Snippet: "Use a context deadline."},
		{Title: "Highlights entry", URL: "https://example.com/highlight", Snippet: "A page highlight."},
	}
	if len(report.Results) != len(want) {
		t.Fatalf("results = %+v", report.Results)
	}
	for index, expected := range want {
		if report.Results[index] != expected {
			t.Fatalf("result %d = %+v, want %+v", index, report.Results[index], expected)
		}
	}
	if got := recorder.header("X-API-Key"); got != "you-secret" {
		t.Fatalf("you key header = %q", got)
	}
	body := recorder.jsonBody(t)
	if body["query"] != "golang timeout" || body["count"] != float64(5) {
		t.Fatalf("you request body = %#v", body)
	}
}

func TestSearchParsesYouFreeMCPResults(t *testing.T) {
	client, recorder := newSearchTestServer(t)
	report, err := client.Search(context.Background(), "golang timeout", web.SearchOptions{Count: 2})
	if err != nil {
		t.Fatal(err)
	}
	if report.Backend != web.BackendYou || len(report.Results) != 2 {
		t.Fatalf("free search report = %+v", report)
	}
	if report.Results[0].Title != "The complete guide" {
		t.Fatalf("free search result = %+v", report.Results[0])
	}
	if got := recorder.header("Accept"); !strings.Contains(got, "text/event-stream") {
		t.Fatalf("free search Accept = %q", got)
	}
	body := recorder.jsonBody(t)
	if body["method"] != "tools/call" {
		t.Fatalf("free search did a %v call: %#v", body["method"], body)
	}
	params, _ := body["params"].(map[string]any)
	if params["name"] != "you-search" {
		t.Fatalf("free search called %v", params["name"])
	}
	arguments, _ := params["arguments"].(map[string]any)
	if arguments["query"] != "golang timeout" || arguments["count"] != float64(2) {
		t.Fatalf("free search arguments = %#v", arguments)
	}
}

func TestSearchReadsYouFreeMCPFailures(t *testing.T) {
	tests := []struct {
		name    string
		status  int
		body    string
		want    string
		results bool
	}{
		{
			name:   "refused call",
			status: http.StatusOK,
			body:   `data: {"jsonrpc":"2.0","id":1,"error":{"code":-32602,"message":"Invalid arguments"}}`,
			want:   "refused the call: Invalid arguments (-32602)",
		},
		{
			name:   "no reply",
			status: http.StatusOK,
			body:   "event: message\ndata: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/message\",\"params\":{\"level\":\"info\"}}\n",
			want:   "returned no reply",
		},
		{
			name:   "empty body",
			status: http.StatusOK,
			body:   "",
			want:   "empty response",
		},
		{
			name:   "listing without results",
			status: http.StatusOK,
			body:   `data: {"jsonrpc":"2.0","id":1,"result":{"structuredContent":{"results":{"web":[]}}}}`,
			want:   "returned no results for this query",
		},
		{
			name:   "invalid json",
			status: http.StatusOK,
			body:   "data: not json\n",
			want:   "not valid JSON",
		},
		{
			name:   "rate limited",
			status: http.StatusTooManyRequests,
			body:   "slow down",
			want:   "returned HTTP 429",
		},
		{
			name:    "replies as a json body",
			status:  http.StatusOK,
			body:    fmt.Sprintf(`{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":%s}]}}`, strconv.Quote(youWebCompact)),
			results: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				w.WriteHeader(test.status)
				_, _ = io.WriteString(w, test.body)
			}))
			defer server.Close()
			report, err := searchTestClient(server).Search(context.Background(), "q", web.SearchOptions{})
			if test.results {
				if err != nil || len(report.Results) != 3 {
					t.Fatalf("free search reply = %+v (%v)", report, err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("free search error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestSearchParsesOtherBackends(t *testing.T) {
	client, recorder := newSearchTestServer(t)
	tests := []struct {
		name     string
		options  web.SearchOptions
		backend  string
		wantPath string
		want     []web.Result
		check    func(t *testing.T)
	}{
		{
			name:     "octen",
			options:  web.SearchOptions{Keys: web.Keys{Octen: "octen-secret"}},
			backend:  web.BackendOcten,
			wantPath: "/octen",
			want: []web.Result{
				{Title: "Influenza", URL: "https://www.who.int/news-room/fact-sheets", Snippet: "WHO recommends annual vaccination."},
			},
			check: func(t *testing.T) {
				if got := recorder.header("Authorization"); got != "Bearer octen-secret" {
					t.Fatalf("octen Authorization = %q", got)
				}
			},
		},
		{
			name:     "exa",
			options:  web.SearchOptions{Keys: web.Keys{Exa: "exa-secret"}},
			backend:  web.BackendExa,
			wantPath: "/exa",
			want: []web.Result{
				{Title: "Go timeouts", URL: "https://blog.example.com/guide", Snippet: "A highlighted passage."},
				{Title: "Stack Overflow", URL: "https://stackoverflow.com/q/1", Snippet: "Plain text passage."},
			},
			check: func(t *testing.T) {
				if got := recorder.header("x-api-key"); got != "exa-secret" {
					t.Fatalf("exa key header = %q", got)
				}
				body := recorder.jsonBody(t)
				if body["numResults"] != float64(5) {
					t.Fatalf("exa request body = %#v", body)
				}
				if contents, ok := body["contents"].(map[string]any); !ok || contents["highlights"] != true {
					t.Fatalf("exa contents = %#v", body["contents"])
				}
			},
		},
		{
			name:     "firecrawl with a key",
			options:  web.SearchOptions{Keys: web.Keys{Firecrawl: "fc-secret"}},
			backend:  web.BackendFirecrawl,
			wantPath: "/firecrawl",
			want: []web.Result{
				{Title: "Go timeouts", URL: "https://blog.example.com/guide", Snippet: "Take apart the stages."},
			},
			check: func(t *testing.T) {
				if got := recorder.header("Authorization"); got != "Bearer fc-secret" {
					t.Fatalf("firecrawl Authorization = %q", got)
				}
			},
		},
		{
			name:     "firecrawl without a key",
			options:  web.SearchOptions{Provider: web.BackendFirecrawl},
			backend:  web.BackendFirecrawl,
			wantPath: "/firecrawl",
			want: []web.Result{
				{Title: "Go timeouts", URL: "https://blog.example.com/guide", Snippet: "Take apart the stages."},
			},
			check: func(t *testing.T) {
				if got := recorder.header("Authorization"); got != "" {
					t.Fatalf("keyless firecrawl sent %q", got)
				}
			},
		},
		{
			name:     "tinyfish",
			options:  web.SearchOptions{Keys: web.Keys{TinyFish: "tiny-secret"}},
			backend:  web.BackendTinyFish,
			wantPath: "/tinyfish",
			want: []web.Result{
				{Title: "Go timeouts", URL: "https://blog.example.com/guide", Snippet: "Take apart the stages."},
				{Title: "How to set a timeout?", URL: "https://stackoverflow.com/q/1", Snippet: "Use a context deadline."},
			},
			check: func(t *testing.T) {
				if got := recorder.header("X-API-Key"); got != "tiny-secret" {
					t.Fatalf("tinyfish key header = %q", got)
				}
				if got := recorder.queryParameter("query"); got != "golang timeout" {
					t.Fatalf("tinyfish query = %q", got)
				}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			report, err := client.Search(context.Background(), "golang timeout", test.options)
			if err != nil {
				t.Fatal(err)
			}
			if report.Backend != test.backend || recorder.path() != test.wantPath {
				t.Fatalf("report = %+v from %q, want backend %q at %q", report, recorder.path(), test.backend, test.wantPath)
			}
			if len(report.Results) != len(test.want) {
				t.Fatalf("results = %+v, want %+v", report.Results, test.want)
			}
			for index, expected := range test.want {
				if report.Results[index] != expected {
					t.Fatalf("result %d = %+v, want %+v", index, report.Results[index], expected)
				}
			}
			test.check(t)
		})
	}
}

func TestSearchLimitsResultsAndReportsFailures(t *testing.T) {
	client, _ := newSearchTestServer(t)
	report, err := client.Search(context.Background(), "golang timeout", web.SearchOptions{
		Keys: web.Keys{Brave: "brave-secret"}, Count: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Results) != 1 || report.Results[0].URL != "https://blog.example.com/guide" {
		t.Fatalf("limited search = %+v", report.Results)
	}
	if _, err := client.Search(context.Background(), "   ", web.SearchOptions{}); err == nil ||
		!strings.Contains(err.Error(), "empty search query") {
		t.Fatalf("blank query error = %v", err)
	}
}

func TestSearchFallsBackToDuckDuckGo(t *testing.T) {
	tests := []struct {
		name    string
		options web.SearchOptions
	}{
		{
			name:    "the keyless default profile fails",
			options: web.SearchOptions{},
		},
		{
			name:    "a selected backend fails",
			options: web.SearchOptions{Provider: web.BackendExa, Keys: web.Keys{Exa: "exa-secret"}},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client, recorder := newFailingSearchTestServer(t, http.StatusBadGateway, `{"error":"upstream is down"}`)
			report, err := client.Search(context.Background(), "golang timeout", test.options)
			if err != nil {
				t.Fatal(err)
			}
			if report.Backend != web.BackendDuckDuckGo || len(report.Results) != 2 {
				t.Fatalf("fallback report = %+v", report)
			}
			want := []web.Result{
				{Title: "The complete guide", URL: "https://blog.example.com/guide", Snippet: "Take apart the timeout stages."},
				{Title: "How to set a timeout?", URL: "https://stackoverflow.com/q/1", Snippet: "Use a context deadline."},
			}
			for index, expected := range want {
				if report.Results[index] != expected {
					t.Fatalf("fallback result %d = %+v, want %+v", index, report.Results[index], expected)
				}
			}
			if got := recorder.queryParameter("q"); got != "golang timeout" {
				t.Fatalf("crawl query = %q", got)
			}
			if got := recorder.header("User-Agent"); !strings.HasPrefix(got, "Mozilla/5.0") {
				t.Fatalf("crawl User-Agent = %q", got)
			}
			if got := recorder.header("Accept"); !strings.Contains(got, "text/html") {
				t.Fatalf("crawl Accept = %q", got)
			}
		})
	}
}

func TestSearchKeepsFailuresTheUserMustFix(t *testing.T) {
	tests := []struct {
		name    string
		options web.SearchOptions
		status  int
		body    string
		want    string
	}{
		{
			name:    "a rejected key",
			options: web.SearchOptions{Keys: web.Keys{Brave: "brave-secret"}},
			status:  http.StatusUnauthorized,
			body:    `{"error":{"code":"SUBSCRIPTION_TOKEN_INVALID"}}`,
			want:    "check BRAVE_API_KEY",
		},
		{
			name:    "an empty listing",
			options: web.SearchOptions{Keys: web.Keys{Brave: "brave-secret"}},
			status:  http.StatusOK,
			body:    `{"web":{"results":[]}}`,
			want:    "returned no results for this query",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client, recorder := newFailingSearchTestServer(t, test.status, test.body)
			_, err := client.Search(context.Background(), "golang timeout", test.options)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want substring %q", err, test.want)
			}
			if strings.Contains(err.Error(), "fallback") {
				t.Fatalf("error mentions the fallback: %v", err)
			}
			if got := recorder.path(); got == "/crawl" {
				t.Fatal("the crawl was reached for a failure the user must fix")
			}
		})
	}
}

func TestSearchReportsBothFailures(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadGateway)
		_, _ = io.WriteString(w, `{"error":"upstream is down"}`)
	}))
	defer server.Close()
	response, err := searchTestClient(server).Search(context.Background(), "golang timeout", web.SearchOptions{Keys: web.Keys{Exa: "exa-secret"}})
	if err == nil || len(response.Results) != 0 {
		t.Fatalf("double failure search = %+v (%v)", response, err)
	}
	want := "the Exa API returned HTTP 502: upstream is down; the DuckDuckGo fallback failed too: DuckDuckGo returned HTTP 502"
	if err.Error() != want {
		t.Fatalf("double failure error = %q, want %q", err, want)
	}
}

func TestSearchDoesNotFallBackAfterCancellation(t *testing.T) {
	client, recorder := newFailingSearchTestServer(t, http.StatusBadGateway, `{}`)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := client.Search(ctx, "golang timeout", web.SearchOptions{})
	if err == nil {
		t.Fatal("a cancelled search returned no error")
	}
	if strings.Contains(err.Error(), "fallback") {
		t.Fatalf("a cancelled search tried the fallback: %v", err)
	}
	if got := recorder.path(); got == "/crawl" {
		t.Fatal("a cancelled search reached the crawl")
	}
}

// newFailingSearchTestServer answers every backend with one status and body, and serves the crawl
// page on the DuckDuckGo path, so a fallback is visible in the recorder and in the result.
func newFailingSearchTestServer(t *testing.T, status int, body string) (*web.Client, *searchRecorder) {
	t.Helper()
	recorder := &searchRecorder{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		recorder.record(r)
		if r.URL.Path == "/crawl" {
			w.Header().Set("Content-Type", "text/html")
			_, _ = io.WriteString(w, crawlResults)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(server.Close)
	return searchTestClient(server), recorder
}

func TestSearchReportsBackendFailures(t *testing.T) {
	tests := []struct {
		name     string
		provider string
		key      string
		status   int
		body     string
		want     string
	}{
		{
			name: "brave rejects the key", provider: web.BackendBrave, key: "secret",
			status: http.StatusUnauthorized, body: `{"error":{"code":"SUBSCRIPTION_TOKEN_INVALID"}}`,
			want: "the Brave Search API rejected the key (HTTP 401) — check BRAVE_API_KEY",
		},
		{
			name: "brave validates the request", provider: web.BackendBrave, key: "secret",
			status: http.StatusUnprocessableEntity, body: `{"error":{"detail":"Unable to validate request parameter(s)"},"type":"ErrorResponse"}`,
			want: "the Brave Search API returned HTTP 422: Unable to validate request parameter(s)",
		},
		{
			name: "brave rate limit", provider: web.BackendBrave, key: "secret",
			status: http.StatusTooManyRequests, body: `{}`,
			want: "the Brave Search API rate limit was reached (HTTP 429)",
		},
		{
			name: "you rejects the key", provider: web.BackendYou, key: "secret",
			status: http.StatusUnauthorized, body: `{"message":"Unauthorized"}`,
			want: "the You.com API rejected the key (HTTP 401) — check YDC_API_KEY",
		},
		{
			name: "octen refuses the query", provider: web.BackendOcten, key: "secret",
			status: http.StatusForbidden, body: `{"code":403,"msg":"Insufficient balance in account"}`,
			want: "the Octen API rejected the key (HTTP 403) — check OCTEN_API_KEY: Insufficient balance in account",
		},
		{
			name: "octen reports a business error with HTTP 200", provider: web.BackendOcten, key: "secret",
			status: http.StatusOK, body: `{"code":4001,"msg":"Exceeding the rate limit"}`,
			want: "the Octen API refused the query: Exceeding the rate limit (4001)",
		},
		{
			name: "exa wants payment", provider: web.BackendExa, key: "secret",
			status: http.StatusPaymentRequired, body: `{"error":"Payment required to access this resource"}`,
			want: "the Exa API has no credit left (HTTP 402) — check the account behind EXA_API_KEY: Payment required",
		},
		{
			name: "firecrawl refuses a keyless search", provider: web.BackendFirecrawl, key: "",
			status: http.StatusForbidden, body: `{"success":false,"error":"your IP address looks suspicious, so Firecrawl can't be used without an API key"}`,
			want: "the Firecrawl API rejected the key (HTTP 403) — check FIRECRAWL_API_KEY: your IP address looks suspicious",
		},
		{
			name: "tinyfish wants a key", provider: web.BackendTinyFish, key: "secret",
			status: http.StatusUnauthorized, body: `{"error":{"code":"MISSING_API_KEY","message":"X-API-Key header is required"}}`,
			want: "the TinyFish API rejected the key (HTTP 401) — check TINYFISH_API_KEY: X-API-Key header is required",
		},
		{
			name: "unexpected status", provider: web.BackendExa, key: "secret",
			status: http.StatusBadGateway, body: `{}`,
			want: "the Exa API returned HTTP 502",
		},
		{
			name: "invalid json", provider: web.BackendBrave, key: "secret",
			status: http.StatusOK, body: "not json",
			want: "the Brave Search API response is not valid JSON",
		},
		{
			name: "empty listing", provider: web.BackendTinyFish, key: "secret",
			status: http.StatusOK, body: `{"results":[]}`,
			want: "the TinyFish API returned no results for this query",
		},
		{
			name: "listing without usable entries", provider: web.BackendExa, key: "secret",
			status: http.StatusOK, body: `{"results":[{"title":"","url":""},{"title":"  ","url":"  "}]}`,
			want: "the Exa API returned no results for this query",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(test.status)
				_, _ = io.WriteString(w, test.body)
			}))
			defer server.Close()
			options := web.SearchOptions{Keys: keysFor(test.provider, test.key)}
			if test.key == "" {
				options.Provider = test.provider
			}
			_, err := searchTestClient(server).Search(context.Background(), "q", options)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("failure error = %v, want substring %q", err, test.want)
			}
			if strings.Contains(err.Error(), "secret") {
				t.Fatalf("failure error leaked the key: %v", err)
			}
		})
	}
}

func TestSearchRequiresAKeyAndAKnownProvider(t *testing.T) {
	client, _ := newSearchTestServer(t)
	_, err := client.Search(context.Background(), "q", web.SearchOptions{Provider: web.BackendOcten})
	if err == nil || !strings.Contains(err.Error(), "the Octen backend needs an API key — set OCTEN_API_KEY") {
		t.Fatalf("keyless octen error = %v", err)
	}
	_, err = client.Search(context.Background(), "q", web.SearchOptions{Provider: "gopher"})
	want := `unknown web search provider "gopher" (choose you, brave, octen, exa, firecrawl, tinyfish, duckduckgo)`
	if err == nil || err.Error() != want {
		t.Fatalf("unknown provider error = %v, want %q", err, want)
	}
}

func TestSearchBoundsSnippets(t *testing.T) {
	paragraph := strings.Repeat("naïve prose ", 80) // 960 bytes, with multibyte runes
	body := fmt.Sprintf(`{"results":{"web":[{"title":"Long","url":"https://example.com/long","description":%s}]}}`,
		strconv.Quote(paragraph))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, body)
	}))
	defer server.Close()
	report, err := searchTestClient(server).Search(context.Background(), "q", web.SearchOptions{Keys: web.Keys{You: "secret"}})
	if err != nil {
		t.Fatal(err)
	}
	snippet := report.Results[0].Snippet
	if len(snippet) > web.MaxSnippetChars+len("…") || !strings.HasSuffix(snippet, "…") {
		t.Fatalf("snippet was not bounded: %d bytes, %q…", len(snippet), snippet[:40])
	}
	if !strings.HasPrefix(paragraph, strings.TrimSuffix(snippet, "…")) {
		t.Fatalf("bounded snippet cut into a rune: %q", snippet[len(snippet)-10:])
	}
}
