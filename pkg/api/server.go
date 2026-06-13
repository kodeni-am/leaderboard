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

	"github.com/araasr/leaderboard/pkg/engine"
	"github.com/araasr/leaderboard/pkg/ingest"
	"github.com/araasr/leaderboard/pkg/tenancy"
	"github.com/araasr/leaderboard/pkg/trust"
	"github.com/araasr/leaderboard/pkg/window"
)

// Server wires the engine, ingestor, tenant store, and the in-memory board
// resolver into an http.Handler.
type Server struct {
	eng        engine.RankingEngine
	ing        *ingest.Ingestor
	store      tenancy.Store
	registry   *ingest.StaticRegistry
	adminToken string
	verifier   *trust.Verifier // optional HMAC anti-cheat; nil disables
}

func NewServer(eng engine.RankingEngine, ing *ingest.Ingestor, store tenancy.Store, registry *ingest.StaticRegistry, adminToken string) *Server {
	return &Server{eng: eng, ing: ing, store: store, registry: registry, adminToken: adminToken}
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

// Handler returns the configured router.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.handleHealth)
	mux.HandleFunc("POST /v1/apps", s.handleCreateApp) // admin-token protected

	auth := tenancy.Authenticate(s.store)
	authed := func(h http.HandlerFunc) http.Handler { return auth(h) }

	mux.Handle("POST /v1/boards", authed(s.handleCreateBoard))
	mux.Handle("GET /v1/boards", authed(s.handleListBoards))
	mux.Handle("POST /v1/boards/{board}/scores", authed(s.handleSubmit))
	mux.Handle("GET /v1/boards/{board}/rank", authed(s.handleRank))
	mux.Handle("GET /v1/boards/{board}/top", authed(s.handleTop))
	mux.Handle("GET /v1/boards/{board}/page", authed(s.handlePage))
	mux.Handle("GET /v1/boards/{board}/neighbors", authed(s.handleNeighbors))
	mux.Handle("POST /v1/boards/{board}/friends", authed(s.handleFriends))
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

type createAppReq struct {
	Name string `json:"name"`
}
type createAppResp struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	APIKey string `json:"api_key"` // shown once
}

func (s *Server) handleCreateApp(w http.ResponseWriter, r *http.Request) {
	if s.adminToken == "" || r.Header.Get("X-Admin-Token") != s.adminToken {
		writeErr(w, http.StatusUnauthorized, "admin token required")
		return
	}
	var req createAppReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Name == "" {
		writeErr(w, http.StatusBadRequest, "name required")
		return
	}
	app, key, err := s.store.CreateApp(r.Context(), req.Name)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, createAppResp{ID: app.ID, Name: app.Name, APIKey: key})
}

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
			writeErr(w, http.StatusUnauthorized, err.Error())
			return
		}
	}
	accepted, err := s.ing.Submit(r.Context(), ingest.Record{
		App: app.ID, Board: board, Member: req.Member, Score: req.Score,
		Time: req.Time, Segments: req.Segments, Idem: req.Idem,
	})
	if errors.Is(err, ingest.ErrUnknownBoard) {
		writeErr(w, http.StatusNotFound, "unknown board")
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !accepted {
		writeJSON(w, http.StatusOK, map[string]any{"accepted": false, "duplicate": true})
		return
	}
	// Write-behind: the score is durably logged and will be ranked shortly.
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
