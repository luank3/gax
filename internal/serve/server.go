// Package serve implements the gax remote-control HTTP + SSE + POST server
// and serves the embedded Preact web UI.
package serve

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/luank3/gax/internal/agent"
	"github.com/luank3/gax/internal/config"
	"github.com/luank3/gax/internal/core"
	"github.com/luank3/gax/internal/mcp"
	"github.com/luank3/gax/internal/platform"
	"github.com/luank3/gax/internal/provider"
	"github.com/luank3/gax/internal/provider/registry"
	"github.com/luank3/gax/internal/serve/ui"
	"github.com/luank3/gax/internal/session"
	"github.com/luank3/gax/internal/tools"
	"github.com/luank3/gax/internal/transport"
)

const (
	authCookieName = "gax_session"
	maxBodyBytes   = 1 << 20
	sseQueueCap    = 128
	DefaultListen  = "127.0.0.1:8484"
)

// Project is a registered working directory.
type Project struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Path string `json:"path"`
}

// Options configures a remote-control Server.
type Options struct {
	Listen      string
	AllowRemote bool
	Token       string
	StateDir    string
	Version     string
	Store       *config.Store
	HTTPClient  *transport.Client
	MCP         *mcp.Manager
	ResolveProv func(provID, modelID, cwd string) (provider.Client, error)
}

type sseSubscriber struct {
	ch chan []byte
}

type activeSession struct {
	mu      sync.Mutex
	id      string
	cwd     string
	sess    *agent.Session
	writer  *session.Writer
	tools   *tools.Registry
	prov    provider.Client
	model   string
	effort  string
	running bool
	sig     atomic.Int32
	cancel  context.CancelFunc
	subs    map[*sseSubscriber]struct{}
}

// Server exposes the /api/v1/ remote-control endpoints and embedded web UI.
type Server struct {
	opts     Options
	token    string
	projects []Project
	projMu   sync.Mutex
	sessions map[string]*activeSession
	sessMu   sync.Mutex
	mux      *http.ServeMux
}

// NewServer constructs a configured Server. If Token is empty, a random 32-byte hex token is generated.
func NewServer(opts Options) (*Server, error) {
	if opts.Store == nil {
		opts.Store = config.NewStore()
	}
	if opts.Listen == "" {
		opts.Listen = DefaultListen
	}
	if !opts.AllowRemote && !isLoopbackListen(opts.Listen) {
		return nil, fmt.Errorf("refusing to bind non-loopback address %q without --allow-remote", opts.Listen)
	}
	tok := strings.TrimSpace(opts.Token)
	if tok == "" {
		var b [32]byte
		if _, err := rand.Read(b[:]); err != nil {
			return nil, err
		}
		tok = hex.EncodeToString(b[:])
	}
	if opts.ResolveProv == nil {
		opts.ResolveProv = func(provID, modelID, cwd string) (provider.Client, error) {
			return registry.Resolve(provID, modelID, cwd, opts.Store, opts.HTTPClient)
		}
	}
	s := &Server{
		opts:     opts,
		token:    tok,
		sessions: make(map[string]*activeSession),
		mux:      http.NewServeMux(),
	}
	s.loadProjects()
	s.routes()
	return s, nil
}

// Token returns the active bearer token.
func (s *Server) Token() string { return s.token }

// Handler returns the HTTP handler for serving or httptest.
func (s *Server) Handler() http.Handler { return s.mux }

// Close stops all active sessions and tools.
func (s *Server) Close() {
	s.sessMu.Lock()
	list := make([]*activeSession, 0, len(s.sessions))
	for _, as := range s.sessions {
		list = append(list, as)
	}
	s.sessMu.Unlock()
	for _, as := range list {
		as.mu.Lock()
		if as.cancel != nil {
			as.cancel()
		}
		if as.tools != nil {
			as.tools.Close()
		}
		if as.writer != nil {
			_ = as.writer.Close()
		}
		as.mu.Unlock()
	}
}

func isLoopbackListen(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func (s *Server) routes() {
	s.mux.HandleFunc("POST /api/v1/auth", s.withOriginCheck(s.handleAuth))
	s.mux.HandleFunc("GET /api/v1/projects", s.withAuth(s.handleListProjects))
	s.mux.HandleFunc("POST /api/v1/projects", s.withOriginCheck(s.withAuth(s.handleAddProject)))
	s.mux.HandleFunc("GET /api/v1/projects/{id}/sessions", s.withAuth(s.handleListSessions))
	s.mux.HandleFunc("POST /api/v1/sessions", s.withOriginCheck(s.withAuth(s.handleOpenSession)))
	s.mux.HandleFunc("GET /api/v1/sessions/{id}/events", s.withAuth(s.handleSessionEvents))
	s.mux.HandleFunc("POST /api/v1/sessions/{id}/prompt", s.withOriginCheck(s.withAuth(s.handlePrompt)))
	s.mux.HandleFunc("POST /api/v1/sessions/{id}/interrupt", s.withOriginCheck(s.withAuth(s.handleInterrupt)))
	s.mux.HandleFunc("POST /api/v1/sessions/{id}/undo", s.withOriginCheck(s.withAuth(s.handleUndo)))

	if static := ui.FS(); static != nil {
		s.mux.Handle("/", http.FileServer(http.FS(static)))
	} else {
		s.mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			_, _ = w.Write([]byte("gax serve: web UI assets not built (build with `make web && make build`)\n"))
		})
	}
}

func (s *Server) withOriginCheck(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if origin := r.Header.Get("Origin"); origin != "" {
			if !strings.HasSuffix(origin, "://"+r.Host) {
				http.Error(w, "forbidden origin", http.StatusForbidden)
				return
			}
		}
		next(w, r)
	}
}

func (s *Server) withAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Never accept tokens in URL query parameters.
		if r.URL.Query().Has("token") {
			http.Error(w, "token in URL is forbidden", http.StatusBadRequest)
			return
		}
		var candidate string
		if auth := r.Header.Get("Authorization"); strings.HasPrefix(auth, "Bearer ") {
			candidate = strings.TrimPrefix(auth, "Bearer ")
		} else if c, err := r.Cookie(authCookieName); err == nil {
			candidate = c.Value
		}
		if subtle.ConstantTimeCompare([]byte(candidate), []byte(s.token)) != 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next(w, r)
	}
}

func (s *Server) handleAuth(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Token string `json:"token"`
	}
	if err := decodeLimitedJSON(r, &body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if subtle.ConstantTimeCompare([]byte(body.Token), []byte(s.token)) != 1 {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     authCookieName,
		Value:    s.token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
	})
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleListProjects(w http.ResponseWriter, _ *http.Request) {
	s.projMu.Lock()
	out := append([]Project(nil), s.projects...)
	s.projMu.Unlock()
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleAddProject(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Path string `json:"path"`
	}
	if err := decodeLimitedJSON(r, &body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	abs, err := filepath.Abs(strings.TrimSpace(body.Path))
	if err != nil {
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}
	info, err := os.Stat(abs)
	if err != nil || !info.IsDir() {
		http.Error(w, "path is not an existing directory", http.StatusBadRequest)
		return
	}
	if real, err := filepath.EvalSymlinks(abs); err == nil {
		abs = real
	}
	sum := sha256.Sum256([]byte(abs))
	proj := Project{
		ID:   hex.EncodeToString(sum[:6]),
		Name: filepath.Base(abs),
		Path: abs,
	}
	s.projMu.Lock()
	for _, existing := range s.projects {
		if existing.ID == proj.ID {
			s.projMu.Unlock()
			writeJSON(w, http.StatusOK, existing)
			return
		}
	}
	s.projects = append(s.projects, proj)
	s.saveProjectsLocked()
	s.projMu.Unlock()
	writeJSON(w, http.StatusCreated, proj)
}

func (s *Server) findProject(id string) (Project, bool) {
	s.projMu.Lock()
	defer s.projMu.Unlock()
	for _, p := range s.projects {
		if p.ID == id {
			return p, true
		}
	}
	return Project{}, false
}

func (s *Server) handleListSessions(w http.ResponseWriter, r *http.Request) {
	proj, ok := s.findProject(r.PathValue("id"))
	if !ok {
		http.Error(w, "unknown project", http.StatusNotFound)
		return
	}
	list, err := session.List(s.opts.StateDir, proj.Path)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	type item struct {
		ID       string `json:"id"`
		Preview  string `json:"preview"`
		Provider string `json:"provider"`
		Model    string `json:"model"`
	}
	out := make([]item, 0, len(list))
	for _, sum := range list {
		out = append(out, item{ID: sum.ID, Preview: sum.Preview, Provider: sum.Provider, Model: sum.Model})
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleOpenSession(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ProjectID string `json:"project_id"`
		ResumeID  string `json:"resume_id"`
		ForkFrom  string `json:"fork_from"`
	}
	if err := decodeLimitedJSON(r, &body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	proj, ok := s.findProject(body.ProjectID)
	if !ok {
		http.Error(w, "unknown project", http.StatusNotFound)
		return
	}

	if body.ResumeID != "" {
		s.sessMu.Lock()
		if existing, ok := s.sessions[body.ResumeID]; ok {
			s.sessMu.Unlock()
			writeJSON(w, http.StatusOK, map[string]string{"id": existing.id})
			return
		}
		s.sessMu.Unlock()
	}

	provID := s.opts.Store.String("provider")
	if provID == "" || provID == "auto" {
		provID = "mock"
	}
	modelID := s.opts.Store.String("model")
	effort := s.opts.Store.String("effort")

	var writer *session.Writer
	var sess *agent.Session
	var err error

	switch {
	case body.ForkFrom != "":
		srcPath, ferr := session.FindByID(s.opts.StateDir, proj.Path, body.ForkFrom)
		if ferr != nil {
			http.Error(w, ferr.Error(), http.StatusNotFound)
			return
		}
		writer, err = session.Fork(srcPath, s.opts.StateDir, proj.Path, s.opts.Version, 0)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		loaded, lerr := session.Load(writer.Path())
		if lerr != nil {
			http.Error(w, lerr.Error(), http.StatusInternalServerError)
			return
		}
		sess = agent.NewSession(loaded.Header)
		sess.Items = append(sess.Items, loaded.LiveItems...)
	case body.ResumeID != "":
		path, ferr := session.FindByID(s.opts.StateDir, proj.Path, body.ResumeID)
		if ferr != nil {
			http.Error(w, ferr.Error(), http.StatusNotFound)
			return
		}
		loaded, lerr := session.Load(path)
		if lerr != nil {
			http.Error(w, lerr.Error(), http.StatusInternalServerError)
			return
		}
		writer, err = session.OpenAppend(path)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		sess = agent.NewSession(loaded.Header)
		sess.Items = append(sess.Items, loaded.LiveItems...)
		if loaded.Selection.Provider != "" {
			provID = loaded.Selection.Provider
		}
		if loaded.Selection.Model != "" {
			modelID = loaded.Selection.Model
		}
		if loaded.Selection.Effort != "" {
			effort = loaded.Selection.Effort
		}
	default:
		hdr := core.SessionHeader{
			Version: 1, GaxVersion: s.opts.Version, ID: session.NewUUID(),
			CWD: proj.Path, Provider: provID, Model: modelID, Effort: effort,
		}
		writer, err = session.Create(s.opts.StateDir, proj.Path, hdr)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		sess = agent.NewSession(hdr)
	}

	prov, err := s.opts.ResolveProv(provID, modelID, proj.Path)
	if err != nil {
		_ = writer.Close()
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	sess.SetOnAppend(func(it core.Item) {
		_ = writer.AppendItem(it)
	})
	reg := tools.NewRegistry(proj.Path, s.opts.Store)
	reg.SetMCP(s.opts.MCP)

	as := &activeSession{
		id:     sess.Header.ID,
		cwd:    proj.Path,
		sess:   sess,
		writer: writer,
		tools:  reg,
		prov:   prov,
		model:  modelID,
		effort: effort,
		subs:   make(map[*sseSubscriber]struct{}),
	}
	s.sessMu.Lock()
	s.sessions[as.id] = as
	s.sessMu.Unlock()
	writeJSON(w, http.StatusCreated, map[string]string{"id": as.id})
}

func (s *Server) getSession(id string) (*activeSession, bool) {
	s.sessMu.Lock()
	defer s.sessMu.Unlock()
	as, ok := s.sessions[id]
	return as, ok
}

func (s *Server) handleSessionEvents(w http.ResponseWriter, r *http.Request) {
	as, ok := s.getSession(r.PathValue("id"))
	if !ok {
		http.Error(w, "session not open", http.StatusNotFound)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	sub := &sseSubscriber{ch: make(chan []byte, sseQueueCap)}
	as.mu.Lock()
	totals := as.sess.Totals()
	snap, _ := json.Marshal(map[string]any{
		"items":    as.sess.Items,
		"running":  as.running,
		"provider": as.prov.ID(),
		"model":    as.model,
		"totals":   totals,
	})
	as.subs[sub] = struct{}{}
	as.mu.Unlock()

	defer func() {
		as.mu.Lock()
		delete(as.subs, sub)
		as.mu.Unlock()
	}()

	_, _ = fmt.Fprintf(w, "event: snapshot\ndata: %s\n\n", snap)
	flusher.Flush()

	for {
		select {
		case <-r.Context().Done():
			return
		case payload := <-sub.ch:
			_, _ = fmt.Fprintf(w, "event: agent\ndata: %s\n\n", payload)
			flusher.Flush()
		}
	}
}

func (s *Server) handlePrompt(w http.ResponseWriter, r *http.Request) {
	as, ok := s.getSession(r.PathValue("id"))
	if !ok {
		http.Error(w, "session not open", http.StatusNotFound)
		return
	}
	var body struct {
		Text string `json:"text"`
	}
	if err := decodeLimitedJSON(r, &body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	text := strings.TrimSpace(body.Text)
	if text == "" {
		http.Error(w, "empty prompt", http.StatusBadRequest)
		return
	}

	as.mu.Lock()
	if as.running {
		as.mu.Unlock()
		http.Error(w, "session is already running", http.StatusConflict)
		return
	}
	as.running = true
	as.sig.Store(int32(agent.SignalNone))
	runCtx, cancel := context.WithCancel(context.Background())
	as.cancel = cancel
	if len(as.sess.Items) > 0 {
		as.sess.Append(core.Item{Kind: core.KindTurnBoundary})
	}
	as.sess.Append(core.Item{Kind: core.KindUser, Source: "web", Text: text})
	as.mu.Unlock()

	go s.runSessionTurn(runCtx, as)
	writeJSON(w, http.StatusAccepted, map[string]bool{"accepted": true})
}

func (s *Server) handleInterrupt(w http.ResponseWriter, r *http.Request) {
	as, ok := s.getSession(r.PathValue("id"))
	if !ok {
		http.Error(w, "session not open", http.StatusNotFound)
		return
	}
	var body struct {
		Mode string `json:"mode"`
	}
	_ = decodeLimitedJSON(r, &body)
	as.mu.Lock()
	if body.Mode == "abort" {
		as.sig.Store(int32(agent.SignalAbort))
		if as.cancel != nil {
			as.cancel()
		}
	} else {
		as.sig.Store(int32(agent.SignalPause))
	}
	as.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleUndo(w http.ResponseWriter, r *http.Request) {
	as, ok := s.getSession(r.PathValue("id"))
	if !ok {
		http.Error(w, "session not open", http.StatusNotFound)
		return
	}
	as.mu.Lock()
	if as.running {
		as.mu.Unlock()
		http.Error(w, "cannot undo while running", http.StatusConflict)
		return
	}
	total := session.CountUserTurns(as.sess.Items)
	keep := max(0, total-1)
	as.sess.Items = session.ApplyUndo(as.sess.Items, keep)
	if as.writer != nil {
		_ = as.writer.AppendUndo(keep)
	}
	itemsCopy := append([]core.Item(nil), as.sess.Items...)
	totals := as.sess.Totals()
	as.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"items": itemsCopy, "totals": totals})
}

func (s *Server) runSessionTurn(ctx context.Context, as *activeSession) {
	defer func() {
		as.mu.Lock()
		as.running = false
		if as.cancel != nil {
			as.cancel()
			as.cancel = nil
		}
		as.mu.Unlock()
	}()

	shellName := ""
	if sp, err := platform.ResolveShell(s.opts.Store.String("bash.shell")); err == nil {
		shellName = sp.Name
	}
	sysPrompt := agent.BuildSystemPrompt(agent.PromptOptions{
		CWD: as.cwd, Model: as.model, ShellName: shellName, Store: s.opts.Store, MCPIndex: s.opts.MCP.PromptIndex(),
	})

	agent.RunLoop(ctx, agent.LoopOptions{
		Session:          as.sess,
		Provider:         as.prov,
		Tools:            as.tools,
		SystemPrompt:     sysPrompt,
		Model:            as.model,
		Effort:           as.effort,
		SessionID:        as.id,
		MaxTurns:         int(s.opts.Store.Int("max_turns")),
		CompactAuto:      s.opts.Store.Bool("compact.auto"),
		CompactThreshold: s.opts.Store.Int("compact.threshold"),
		StreamOptions:    registry.ResolveStreamOptions(as.prov.ID(), as.model, s.opts.Store, nil),
		CheckSignal:      func() agent.Signal { return agent.Signal(as.sig.Load()) },
		Sink: core.EventFunc(func(ev core.Event) {
			b := encodeEventJSON(ev)
			if len(b) == 0 {
				return
			}
			as.mu.Lock()
			for sub := range as.subs {
				select {
				case sub.ch <- b:
				default:
					// Bounded queue: drop when subscriber falls behind.
				}
			}
			as.mu.Unlock()
		}),
	})
}

func encodeEventJSON(ev core.Event) []byte {
	var m map[string]any
	switch e := ev.(type) {
	case core.TurnStarted:
		m = map[string]any{"type": "turn_started", "turn": e.Turn}
	case core.TextDelta:
		m = map[string]any{"type": "text_delta", "text": e.Text}
	case core.ToolCallStarted:
		m = map[string]any{"type": "tool_started", "name": e.Name}
	case core.ToolCallFinished:
		m = map[string]any{"type": "tool_finished", "name": e.Name, "is_error": e.IsError}
	case core.TurnUsage:
		m = map[string]any{"type": "turn_usage", "input": e.Usage.Input, "output": e.Usage.Output, "cost": e.Usage.CostTotal}
	case core.TurnFinished:
		m = map[string]any{"type": "turn_finished", "outcome": e.Outcome, "error": e.Error}
	default:
		return nil
	}
	b, _ := json.Marshal(m)
	return b
}

func (s *Server) projectsFile() string {
	if s.opts.StateDir == "" {
		return ""
	}
	return filepath.Join(s.opts.StateDir, "projects.json")
}

func (s *Server) loadProjects() {
	p := s.projectsFile()
	if p == "" {
		return
	}
	if b, err := os.ReadFile(p); err == nil {
		_ = json.Unmarshal(b, &s.projects)
	}
}

func (s *Server) saveProjectsLocked() {
	p := s.projectsFile()
	if p == "" {
		return
	}
	_ = os.MkdirAll(filepath.Dir(p), 0o700)
	if b, err := json.MarshalIndent(s.projects, "", "  "); err == nil {
		_ = os.WriteFile(p, b, 0o600)
	}
}

func decodeLimitedJSON(r *http.Request, dst any) error {
	defer r.Body.Close()
	return json.NewDecoder(http.MaxBytesReader(nil, r.Body, maxBodyBytes)).Decode(dst)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// Keep fs import used when ui.FS is checked in tests.
var _ fs.FS
