package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/kodeni-am/leaderboard/pkg/players"
	"github.com/kodeni-am/leaderboard/pkg/tenancy"
	"github.com/kodeni-am/leaderboard/pkg/users"
)

// Player registry endpoints (data plane). Registration is optional: raw
// member strings still work everywhere; registered players additionally get
// their nickname attached to read results.

type userReq struct {
	Nickname string `json:"nickname"`
	// Member, when set on registration, claims that existing (anonymous)
	// board member id instead of minting a plr_ one — the nickname attaches
	// to the member's existing rows in place. Ignored on rename.
	Member string `json:"member,omitempty"`
}

// writeUserErr maps users store errors onto stable API error codes.
func writeUserErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, users.ErrInvalidNickname):
		writeErr(w, http.StatusBadRequest, "invalid_nickname")
	case errors.Is(err, users.ErrNicknameTaken):
		writeErr(w, http.StatusConflict, "nickname_taken")
	case errors.Is(err, users.ErrInvalidMember):
		writeErr(w, http.StatusBadRequest, "invalid_member")
	case errors.Is(err, users.ErrMemberTaken):
		writeErr(w, http.StatusConflict, "member_taken")
	case errors.Is(err, users.ErrNotFound):
		writeErr(w, http.StatusNotFound, "user_not_found")
	case errors.Is(err, users.ErrRenameContention):
		writeErr(w, http.StatusServiceUnavailable, "rename_contention")
	default:
		writeErr(w, http.StatusInternalServerError, err.Error())
	}
}

// handleCreateUser registers a player. With req.Member set it claims that
// existing member id in place (user_id echoes it) instead of minting one.
//
// Trust model: claims and minted ids are bound to the caller's player token
// (pkg/players); with a publishable key and `RequirePlayerTokens` on, a token
// is mandatory.
func (s *Server) handleCreateUser(w http.ResponseWriter, r *http.Request) {
	app, _ := tenancy.AppFromContext(r.Context())
	var req userReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "nickname required")
		return
	}
	member := strings.TrimSpace(req.Member)
	token := strings.TrimSpace(r.Header.Get("X-Player-Token"))
	if member != "" {
		// claiming an existing member: prove it is yours (binds on first use)
		if !s.authorizePlayer(w, r, app, member) {
			return
		}
	} else if token != "" && !players.ValidToken(token) {
		// minting: reject a malformed token before creating anything
		writeErr(w, http.StatusBadRequest, "invalid_player_token")
		return
	} else if token == "" && keyScopeFrom(r.Context()) != tenancy.ScopeSecret && app.RequirePlayerTokens {
		writeErr(w, http.StatusUnauthorized, "player_token_required")
		return
	}
	u, err := s.users.Create(r.Context(), app.ID, req.Nickname, req.Member)
	if err != nil {
		writeUserErr(w, err)
		return
	}
	if member == "" && token != "" {
		// a minted plr_ id is brand new: bind the creator's token to it
		if err := s.players.Authorize(r.Context(), app.ID, u.ID, token); err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	writeJSON(w, http.StatusCreated, u)
}

func (s *Server) handleGetUser(w http.ResponseWriter, r *http.Request) {
	app, _ := tenancy.AppFromContext(r.Context())
	u, err := s.users.Get(r.Context(), app.ID, r.PathValue("id"))
	if err != nil {
		writeUserErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, u)
}

func (s *Server) handleLookupUser(w http.ResponseWriter, r *http.Request) {
	app, _ := tenancy.AppFromContext(r.Context())
	nick := r.URL.Query().Get("nickname")
	if nick == "" {
		writeErr(w, http.StatusBadRequest, "nickname required")
		return
	}
	u, err := s.users.GetByNickname(r.Context(), app.ID, nick)
	if err != nil {
		writeUserErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, u)
}

func (s *Server) handleRenameUser(w http.ResponseWriter, r *http.Request) {
	app, _ := tenancy.AppFromContext(r.Context())
	var req userReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "nickname required")
		return
	}
	if !s.authorizePlayer(w, r, app, r.PathValue("id")) {
		return
	}
	u, err := s.users.Rename(r.Context(), app.ID, r.PathValue("id"), req.Nickname)
	if err != nil {
		writeUserErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, u)
}
