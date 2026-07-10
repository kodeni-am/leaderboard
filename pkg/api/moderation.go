package api

import (
	"net/http"

	"github.com/kodeni-am/leaderboard/pkg/ingest"
	"github.com/kodeni-am/leaderboard/pkg/tenancy"
)

// Moderation endpoints: durable removal of board entries and whole players.
// The log append is the commit point — replay and rebuild reproduce the
// deletion. The immediate engine apply only provides read-your-writes; if it
// fails after a successful append we answer removal_queued and the consumer
// converges from the tombstone.

func (s *Server) handleRemoveScore(w http.ResponseWriter, r *http.Request) {
	app, _ := tenancy.AppFromContext(r.Context())
	board := r.PathValue("board")
	member := r.PathValue("member")
	lb, err := s.resolveBoard(r.Context(), app.ID, board)
	if err != nil {
		writeErr(w, http.StatusNotFound, "unknown board")
		return
	}
	if err := s.ing.Remove(r.Context(), ingest.Record{App: app.ID, Board: board, Member: member}); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := s.eng.RemoveFromAll(r.Context(), lb, member); err != nil {
		writeErr(w, http.StatusInternalServerError, "removal_queued")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleDeleteUser deletes a player entirely: one removal tombstone per board
// (each lands in the same log partition as that board's submits, preserving
// replay order), then the registration. The registry is primary data — it
// never flows through the ingest log — so its deletion needs no tombstone,
// and users.Delete releases the nickname only if this player still owns it.
func (s *Server) handleDeleteUser(w http.ResponseWriter, r *http.Request) {
	app, _ := tenancy.AppFromContext(r.Context())
	member := r.PathValue("id")
	boards, err := s.store.ListBoards(r.Context(), app.ID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	for _, lb := range boards {
		s.registry.Register(lb) // store-listed boards may not be warmed yet
		if err := s.ing.Remove(r.Context(), ingest.Record{App: app.ID, Board: lb.Board, Member: member}); err != nil {
			// Tombstones so far are durable and harmless; the client retries.
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	queued := false
	for _, lb := range boards {
		if err := s.eng.RemoveFromAll(r.Context(), lb, member); err != nil {
			queued = true // tombstone is durable; the consumer converges
		}
	}
	if err := s.users.Delete(r.Context(), app.ID, member); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if queued {
		writeErr(w, http.StatusInternalServerError, "removal_queued")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
