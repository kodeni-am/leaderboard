// Package api is the SP3 HTTP layer: a JSON API for defining boards, submitting
// scores (via the SP2 ingestor), and querying ranks (via the SP1 engine).
// Tenant auth and board resolution come from SP5 (tenancy).
package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
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
	verifier      *trust.Verifier // optional HMAC anti-cheat; nil disables
}

func NewServer(eng engine.RankingEngine, ing *ingest.Ingestor, store tenancy.Store, registry *ingest.StaticRegistry, acct *accounts.Service, secureCookies bool) *Server {
	return &Server{eng: eng, ing: ing, store: store, registry: registry, accounts: acct, secureCookies: secureCookies}
}

// SetVerifier enables HMAC signature verification on score submissions. When
// set, submits must carry a valid sig/ts/nonce.
func (s *Server) SetVerifier(v *trust.Verifier) { s.verifier = v }

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

	// App management (owner-scoped, session-authed).
	user("POST /v1/apps", s.handleCreateApp)
	user("GET /v1/apps", s.handleListApps)

	// Data plane (API-key or session+app-id).
	dataPlane("POST /v1/boards", s.handleCreateBoard)
	dataPlane("GET /v1/boards", s.handleListBoards)
	dataPlane("POST /v1/boards/{board}/scores", s.handleSubmit)
	dataPlane("GET /v1/boards/{board}/rank", s.handleRank)
	dataPlane("GET /v1/boards/{board}/top", s.handleTop)
	dataPlane("GET /v1/boards/{board}/page", s.handlePage)
	dataPlane("GET /v1/boards/{board}/neighbors", s.handleNeighbors)
	dataPlane("POST /v1/boards/{board}/friends", s.handleFriends)
	return mux
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
}

func (s *Server) handleCreateBoard(w http.ResponseWriter, r *http.Request) {
	app, _ := tenancy.AppFromContext(r.Context())
	var req createBoardReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Board == "" {
		writeErr(w, http.StatusBadRequest, "board required")
		return
	}
	cfg := engine.BoardConfig{
		SortOrder:    engine.SortOrder(req.SortOrder),
		UpdatePolicy: engine.UpdatePolicy(req.UpdatePolicy),
		TieBreak:     engine.TieBreak(req.TieBreak),
		ScoreBits:    req.ScoreBits,
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
	if s.verifier != nil {
		if err := s.verifier.Verify(req.Sig, req.TS, time.Now(), app.ID, board, req.Member, req.Score, req.Nonce); err != nil {
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
	entry, err := s.eng.GetRank(r.Context(), b, member)
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
