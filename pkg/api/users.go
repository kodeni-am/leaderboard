package api

import (
	"encoding/json"
	"errors"
	"net/http"

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
// Trust model: the data plane is API-key authenticated, so ANY client holding
// the key can claim a nickname for ANY raw member id — impersonation of
// anonymous rows is possible by design. This is the same trust level as
// unsigned score submits. TODO(trust): when an app opts into RequireSigning
// (HMAC submit enforcement in handleSubmit, pkg/api/server.go), claims for an
// explicit member must join it — require a valid signature proving control of
// that member id.
func (s *Server) handleCreateUser(w http.ResponseWriter, r *http.Request) {
	app, _ := tenancy.AppFromContext(r.Context())
	var req userReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "nickname required")
		return
	}
	u, err := s.users.Create(r.Context(), app.ID, req.Nickname, req.Member)
	if err != nil {
		writeUserErr(w, err)
		return
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
	u, err := s.users.Rename(r.Context(), app.ID, r.PathValue("id"), req.Nickname)
	if err != nil {
		writeUserErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, u)
}
