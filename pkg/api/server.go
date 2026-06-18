// Package api is the SP3 HTTP layer: a JSON API for defining boards, submitting
// scores (via the SP2 ingestor), and querying ranks (via the SP1 engine).
// Tenant auth and board resolution come from SP5 (tenancy).
package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/kodeni-am/leaderboard/pkg/accounts"
	"github.com/kodeni-am/leaderboard/pkg/engine"
	"github.com/kodeni-am/leaderboard/pkg/ingest"
	"github.com/kodeni-am/leaderboard/pkg/tenancy"
	"github.com/kodeni-am/leaderboard/pkg/trust"
	"github.com/kodeni-am/leaderboard/pkg/window"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Server wires the engine, ingestor, tenant store, and the in-memory board
// resolver into an http.Handler.
type Server struct {
	eng           engine.RankingEngine
	ing           *ingest.Ingestor
	store         tenancy.Store
	registry      *ingest.StaticRegistry
	accounts      *accounts.Service
	secureCookies bool            // set Secure on auth cookies (true behind TLS)
	signingMaster string          // master key; per-app secrets derived from it. "" disables signing.
	signingSkew   time.Duration   // allowed timestamp skew for signed submissions
	webDir        string          // if set, serve the SPA from this dir (same origin)
	corsWildcard  bool            // allow any origin (data plane; no credentials)
	corsOrigins   map[string]bool // explicit origin allowlist (credentials allowed)
}

// SetStaticDir enables serving the built dashboard SPA from dir on the same
// origin as the API (so a single container/host serves both). Unknown
// non-API paths fall back to index.html for client-side routing.
func (s *Server) SetStaticDir(dir string) { s.webDir = dir }

// SetCORS configures cross-origin access for browser game clients. spec is a
// comma-separated list of allowed origins, or "*" for any. Game clients
// authenticate with an explicit API-key header (no ambient cookie), so wildcard
// is safe for the data plane — but the spec forbids credentials with "*", so we
// only allow credentials (cookies) when an explicit allowlist is set. Empty spec
// leaves CORS off.
func (s *Server) SetCORS(spec string) {
	s.corsWildcard = false
	s.corsOrigins = nil
	for _, o := range strings.Split(spec, ",") {
		o = strings.TrimSpace(o)
		if o == "" {
			continue
		}
		if o == "*" {
			s.corsWildcard = true
			continue
		}
		if s.corsOrigins == nil {
			s.corsOrigins = map[string]bool{}
		}
		s.corsOrigins[o] = true
	}
}

// corsHeaders applies the right Access-Control-Allow-* headers for the request's
// Origin and reports whether CORS is active at all (for preflight handling).
func (s *Server) corsHeaders(w http.ResponseWriter, r *http.Request) bool {
	if !s.corsWildcard && len(s.corsOrigins) == 0 {
		return false
	}
	origin := r.Header.Get("Origin")
	h := w.Header()
	switch {
	case origin != "" && s.corsOrigins[origin]:
		// Explicit allowlist match: reflect the origin and allow credentials.
		h.Set("Access-Control-Allow-Origin", origin)
		h.Set("Access-Control-Allow-Credentials", "true")
		h.Add("Vary", "Origin")
	case s.corsWildcard:
		// Any origin, but no credentials (cookies) — API-key auth only.
		h.Set("Access-Control-Allow-Origin", "*")
	default:
		return true // CORS configured, but this origin isn't allowed (no ACAO).
	}
	return true
}

// withCORS adds CORS headers and answers preflight OPTIONS requests, which the
// method-specific routes would otherwise reject. Wraps the whole router.
func (s *Server) withCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		active := s.corsHeaders(w, r)
		if r.Method == http.MethodOptions && r.Header.Get("Access-Control-Request-Method") != "" {
			if active {
				w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
				w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, X-API-Key, X-App-Id, X-CSRF-Token")
				w.Header().Set("Access-Control-Max-Age", "600")
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func NewServer(eng engine.RankingEngine, ing *ingest.Ingestor, store tenancy.Store, registry *ingest.StaticRegistry, acct *accounts.Service, secureCookies bool) *Server {
	return &Server{eng: eng, ing: ing, store: store, registry: registry, accounts: acct, secureCookies: secureCookies}
}

// SetSigningMaster configures the master key from which per-app signing secrets
// are derived (trust.DeriveAppSecret). Once set, apps that opt into
// RequireSigning must send valid sig/ts/nonce; apps that don't are unaffected.
// With no master key, signing can't be enabled for any app.
func (s *Server) SetSigningMaster(master string, skew time.Duration) {
	if skew <= 0 {
		skew = 5 * time.Minute
	}
	s.signingMaster = master
	s.signingSkew = skew
}

// signingEnabled reports whether the server can derive/verify per-app secrets.
func (s *Server) signingEnabled() bool { return s.signingMaster != "" }

// WarmRegistry loads all persisted board definitions into the in-memory
// resolver. Call once at startup.
func (s *Server) WarmRegistry(ctx context.Context) error {
	boards, err := s.store.AllBoards(ctx)
	if err != nil {
		return err
	}
	for _, lb := range boards {
		s.registry.Register(lb)
	}
	return nil
}

// Handler returns the configured router with Prometheus instrumentation.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	// reg registers a handler wrapped with metrics under the pattern's route.
	reg := func(pattern string, h http.Handler) {
		mux.Handle(pattern, instrument(routeLabel(pattern), h))
	}
	// dataPlane: API-key OR session+X-App-Id (owner-scoped). For game clients
	// and the dashboard's board/leaderboard views.
	dataPlane := func(pattern string, h http.HandlerFunc) {
		reg(pattern, s.requireApp(h))
	}
	// user: session-authed (dashboard account actions).
	user := func(pattern string, h http.HandlerFunc) {
		reg(pattern, s.requireUser(h))
	}

	reg("GET /healthz", http.HandlerFunc(s.handleHealth))
	mux.Handle("GET /metrics", promhttp.Handler()) // scrape target; not instrumented

	// Account/auth plane (humans).
	reg("POST /auth/signup", http.HandlerFunc(s.handleSignup))
	reg("POST /auth/login", http.HandlerFunc(s.handleLogin))
	reg("GET /auth/verify", http.HandlerFunc(s.handleVerify))
	reg("POST /auth/resend", http.HandlerFunc(s.handleResend))
	reg("POST /auth/forgot", http.HandlerFunc(s.handleForgot))
	reg("POST /auth/reset", http.HandlerFunc(s.handleReset))
	user("POST /auth/logout", s.handleLogout)
	user("GET /auth/me", s.handleMe)

	// App + key management (owner-scoped, session-authed).
	user("POST /v1/apps", s.handleCreateApp)
	user("GET /v1/apps", s.handleListApps)
	user("DELETE /v1/apps/{id}", s.handleDeleteApp)
	user("GET /v1/apps/{id}/keys", s.handleListKeys)
	user("POST /v1/apps/{id}/keys", s.handleIssueKey)
	user("DELETE /v1/apps/{id}/keys/{keyId}", s.handleRevokeKey)
	user("GET /v1/apps/{id}/signing", s.handleGetSigning)
	user("PUT /v1/apps/{id}/signing", s.handleSetSigning)
	user("POST /v1/apps/{id}/signing/rotate", s.handleRotateSigning)

	// Data plane (API-key or session+app-id).
	dataPlane("POST /v1/boards", s.handleCreateBoard)
	dataPlane("GET /v1/boards", s.handleListBoards)
	dataPlane("POST /v1/boards/{board}/scores", s.handleSubmit)
	dataPlane("GET /v1/boards/{board}/rank", s.handleRank)
	dataPlane("GET /v1/boards/{board}/top", s.handleTop)
	dataPlane("GET /v1/boards/{board}/page", s.handlePage)
	dataPlane("GET /v1/boards/{board}/neighbors", s.handleNeighbors)
	dataPlane("POST /v1/boards/{board}/friends", s.handleFriends)

	// Serve the dashboard SPA from the same origin (catch-all, lowest priority).
	if s.webDir != "" {
		mux.Handle("/", instrument("/", s.staticFileHandler()))
	}
	return s.withCORS(mux)
}

// staticFileHandler serves the SPA from s.webDir: real files are served
// directly, unknown non-API paths fall back to index.html (client-side
// routing), and unmatched API paths get a JSON 404 rather than HTML.
func (s *Server) staticFileHandler() http.Handler {
	fs := http.FileServer(http.Dir(s.webDir))
	index := filepath.Join(s.webDir, "index.html")
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		if strings.HasPrefix(p, "/v1/") || strings.HasPrefix(p, "/auth/") || p == "/metrics" || p == "/healthz" {
			writeErr(w, http.StatusNotFound, "not found")
			return
		}
		if p != "/" {
			if st, err := os.Stat(filepath.Join(s.webDir, filepath.Clean(p))); err == nil && !st.IsDir() {
				fs.ServeHTTP(w, r)
				return
			}
		}
		http.ServeFile(w, r, index)
	})
}

// --- helpers ---

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

// resolveBoard finds the logical board for the authenticated app, consulting
// the in-memory registry first and falling back to the store.
func (s *Server) resolveBoard(ctx context.Context, app, board string) (engine.LogicalBoard, error) {
	if lb, ok := s.registry.Resolve(app, board); ok {
		return lb, nil
	}
	lb, err := s.store.GetBoard(ctx, app, board)
	if err != nil {
		return engine.LogicalBoard{}, err
	}
	s.registry.Register(lb)
	return lb, nil
}

// physicalBoard builds the engine.Board for a read, applying segment/window
// query params (defaulting to "all").
func physicalBoard(lb engine.LogicalBoard, r *http.Request) engine.Board {
	seg := r.URL.Query().Get("segment")
	if seg == "" {
		seg = "all"
	}
	// window may be a literal id or a cadence keyword (daily/weekly/monthly).
	win := window.Resolve(r.URL.Query().Get("window"), time.Now().UTC())
	return engine.Board{
		Key:    engine.BoardKey{App: lb.App, Board: lb.Board, Segment: seg, Window: win},
		Config: lb.Config,
	}
}

func intParam(r *http.Request, name string, def int) int {
	if v := r.URL.Query().Get(name); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// --- app management ---

// App management handlers live in auth.go (session-authed, owner-scoped).

// --- board definitions ---

type windowReq struct {
	Kind     string `json:"kind"`
	CustomID string `json:"custom_id,omitempty"`
}
type createBoardReq struct {
	Board        string      `json:"board"`
	SortOrder    string      `json:"sort_order,omitempty"`
	UpdatePolicy string      `json:"update_policy,omitempty"`
	TieBreak     string      `json:"tie_break,omitempty"`
	ScoreBits    uint        `json:"score_bits,omitempty"`
	Windows      []windowReq `json:"windows,omitempty"`
	// Opt-in approximate-rank tier (for very large boards). Requires
	// ApproxMax > ApproxMin; ApproxBuckets defaults to 1024.
	ApproxRank    bool    `json:"approx_rank,omitempty"`
	ApproxMin     float64 `json:"approx_min,omitempty"`
	ApproxMax     float64 `json:"approx_max,omitempty"`
	ApproxBuckets int     `json:"approx_buckets,omitempty"`
}

func (s *Server) handleCreateBoard(w http.ResponseWriter, r *http.Request) {
	app, _ := tenancy.AppFromContext(r.Context())
	var req createBoardReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Board == "" {
		writeErr(w, http.StatusBadRequest, "board required")
		return
	}
	cfg := engine.BoardConfig{
		SortOrder:     engine.SortOrder(req.SortOrder),
		UpdatePolicy:  engine.UpdatePolicy(req.UpdatePolicy),
		TieBreak:      engine.TieBreak(req.TieBreak),
		ScoreBits:     req.ScoreBits,
		ApproxRank:    req.ApproxRank,
		ApproxMin:     req.ApproxMin,
		ApproxMax:     req.ApproxMax,
		ApproxBuckets: req.ApproxBuckets,
	}
	if err := cfg.Validate(); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	windows := make([]engine.WindowSpec, 0, len(req.Windows))
	for _, wr := range req.Windows {
		windows = append(windows, engine.WindowSpec{Kind: engine.WindowKind(wr.Kind), CustomID: wr.CustomID})
	}
	if len(windows) == 0 {
		windows = []engine.WindowSpec{{Kind: engine.WindowAllTime}}
	}
	lb := engine.LogicalBoard{App: app.ID, Board: req.Board, Config: cfg, Windows: windows}
	// Validate the physical key shape with a sample key.
	if err := (engine.BoardKey{App: lb.App, Board: lb.Board}).Validate(); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.store.UpsertBoard(r.Context(), lb); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.registry.Register(lb)
	writeJSON(w, http.StatusCreated, lb)
}

func (s *Server) handleListBoards(w http.ResponseWriter, r *http.Request) {
	app, _ := tenancy.AppFromContext(r.Context())
	boards, err := s.store.ListBoards(r.Context(), app.ID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"boards": boards})
}

// --- score submission ---

type submitReq struct {
	Member   string    `json:"member"`
	Score    float64   `json:"score"`
	Time     time.Time `json:"time,omitempty"`
	Segments []string  `json:"segments,omitempty"`
	Idem     string    `json:"idem,omitempty"`
	// Anti-cheat (used only when the server has a verifier configured).
	Sig   string `json:"sig,omitempty"`
	TS    int64  `json:"ts,omitempty"`
	Nonce string `json:"nonce,omitempty"`
}

func (s *Server) handleSubmit(w http.ResponseWriter, r *http.Request) {
	app, _ := tenancy.AppFromContext(r.Context())
	board := r.PathValue("board")
	var req submitReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Member == "" {
		writeErr(w, http.StatusBadRequest, "member and score required")
		return
	}
	if app.RequireSigning {
		if !s.signingEnabled() {
			// App demands signing but the server has no master key to verify with.
			// Fail closed rather than silently accepting unsigned writes.
			submitsTotal.WithLabelValues("rejected").Inc()
			writeErr(w, http.StatusServiceUnavailable, "signed submissions required but signing is not configured on this server")
			return
		}
		secret := trust.DeriveAppSecret(s.signingMaster, app.ID, app.SigningKeyVersion)
		v := trust.NewVerifier(secret, s.signingSkew)
		if err := v.Verify(req.Sig, req.TS, time.Now(), app.ID, board, req.Member, req.Score, req.Nonce); err != nil {
			submitsTotal.WithLabelValues("rejected").Inc()
			writeErr(w, http.StatusUnauthorized, err.Error())
			return
		}
	}
	accepted, err := s.ing.Submit(r.Context(), ingest.Record{
		App: app.ID, Board: board, Member: req.Member, Score: req.Score,
		Time: req.Time, Segments: req.Segments, Idem: req.Idem,
	})
	if errors.Is(err, ingest.ErrUnknownBoard) {
		submitsTotal.WithLabelValues("unknown_board").Inc()
		writeErr(w, http.StatusNotFound, "unknown board")
		return
	}
	if err != nil {
		submitsTotal.WithLabelValues("error").Inc()
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !accepted {
		submitsTotal.WithLabelValues("duplicate").Inc()
		writeJSON(w, http.StatusOK, map[string]any{"accepted": false, "duplicate": true})
		return
	}
	// Write-behind: the score is durably logged and will be ranked shortly.
	submitsTotal.WithLabelValues("accepted").Inc()
	writeJSON(w, http.StatusAccepted, map[string]any{"accepted": true})
}

// --- queries ---

func (s *Server) handleRank(w http.ResponseWriter, r *http.Request) {
	b, ok := s.readBoard(w, r)
	if !ok {
		return
	}
	member := r.URL.Query().Get("member")
	if member == "" {
		writeErr(w, http.StatusBadRequest, "member required")
		return
	}
	// ?approx=true uses the histogram tier (O(buckets), Exact=false) when the
	// board enables it; otherwise the exact O(log N) rank.
	var entry engine.RankEntry
	var err error
	if r.URL.Query().Get("approx") == "true" {
		entry, err = s.eng.GetApproxRank(r.Context(), b, member)
		if errors.Is(err, engine.ErrApproxDisabled) {
			writeErr(w, http.StatusBadRequest, "approximate rank not enabled for this board")
			return
		}
	} else {
		entry, err = s.eng.GetRank(r.Context(), b, member)
	}
	if errors.Is(err, engine.ErrMemberNotFound) {
		writeErr(w, http.StatusNotFound, "member not found")
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, entry)
}

func (s *Server) handleTop(w http.ResponseWriter, r *http.Request) {
	b, ok := s.readBoard(w, r)
	if !ok {
		return
	}
	entries, err := s.eng.TopN(r.Context(), b, intParam(r, "n", 10))
	s.writeEntries(w, entries, err)
}

func (s *Server) handlePage(w http.ResponseWriter, r *http.Request) {
	b, ok := s.readBoard(w, r)
	if !ok {
		return
	}
	entries, err := s.eng.Page(r.Context(), b, intParam(r, "offset", 0), intParam(r, "limit", 20))
	s.writeEntries(w, entries, err)
}

func (s *Server) handleNeighbors(w http.ResponseWriter, r *http.Request) {
	b, ok := s.readBoard(w, r)
	if !ok {
		return
	}
	member := r.URL.Query().Get("member")
	if member == "" {
		writeErr(w, http.StatusBadRequest, "member required")
		return
	}
	entries, err := s.eng.Neighbors(r.Context(), b, member, intParam(r, "k", 5))
	if errors.Is(err, engine.ErrMemberNotFound) {
		writeErr(w, http.StatusNotFound, "member not found")
		return
	}
	s.writeEntries(w, entries, err)
}

type friendsReq struct {
	Members []string `json:"members"`
}

func (s *Server) handleFriends(w http.ResponseWriter, r *http.Request) {
	b, ok := s.readBoard(w, r)
	if !ok {
		return
	}
	var req friendsReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "members required")
		return
	}
	entries, err := s.eng.FriendRank(r.Context(), b, req.Members)
	s.writeEntries(w, entries, err)
}

// readBoard resolves the board for the authed app and builds the physical board
// from segment/window params, writing an error response on failure.
func (s *Server) readBoard(w http.ResponseWriter, r *http.Request) (engine.Board, bool) {
	app, _ := tenancy.AppFromContext(r.Context())
	board := r.PathValue("board")
	lb, err := s.resolveBoard(r.Context(), app.ID, board)
	if err != nil {
		writeErr(w, http.StatusNotFound, "unknown board")
		return engine.Board{}, false
	}
	return physicalBoard(lb, r), true
}

func (s *Server) writeEntries(w http.ResponseWriter, entries []engine.RankEntry, err error) {
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"entries": entries})
}
