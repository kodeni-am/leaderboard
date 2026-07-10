package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/kodeni-am/leaderboard/pkg/engine"
)

func TestRemoveScoreAndDeletePlayer(t *testing.T) {
	h := newHarness(t)
	h.onboard(t, "mod@example.com")
	ctx := context.Background()
	cleanup := func(board string) {
		for _, win := range []string{"all"} {
			_ = h.eng.Reset(ctx, engine.Board{Key: engine.BoardKey{App: h.appID, Board: board, Segment: "all", Window: win}})
		}
	}
	t.Cleanup(func() { cleanup("high"); cleanup("laps") })

	// Board + scores via API key; drain the write-behind consumer.
	if resp, body := h.call(t, http.MethodPost, "/v1/boards", h.key(), map[string]any{"board": "high"}); resp.StatusCode != http.StatusCreated {
		t.Fatalf("create board: %d %s", resp.StatusCode, body)
	}
	for _, s := range []struct {
		m string
		v float64
	}{{"alice", 300}, {"bob", 500}} {
		if resp, body := h.call(t, http.MethodPost, "/v1/boards/high/scores", h.key(), map[string]any{"member": s.m, "score": s.v}); resp.StatusCode != http.StatusAccepted {
			t.Fatalf("submit: %d %s", resp.StatusCode, body)
		}
	}
	if err := h.cons.Drain(ctx); err != nil {
		t.Fatal(err)
	}

	// --- Remove one entry (API-key auth). 204, immediately gone from top. ---
	if resp, body := h.call(t, http.MethodDelete, "/v1/boards/high/scores/alice", h.key(), nil); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("remove score: %d %s", resp.StatusCode, body)
	}
	if resp, body := h.call(t, http.MethodGet, "/v1/boards/high/top?n=10", h.key(), nil); resp.StatusCode != http.StatusOK || strings.Contains(string(body), "alice") {
		t.Fatalf("alice still in top after removal: %d %s", resp.StatusCode, body)
	}
	// Idempotent: removing an absent member is still 204.
	if resp, _ := h.call(t, http.MethodDelete, "/v1/boards/high/scores/alice", h.key(), nil); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("re-remove: %d", resp.StatusCode)
	}
	// Unknown board is 404.
	if resp, _ := h.call(t, http.MethodDelete, "/v1/boards/nope/scores/alice", h.key(), nil); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown board: %d", resp.StatusCode)
	}
	// The removal survives a rebuild: reset the board, drain from scratch...
	// (the harness Consumer keeps cursors, so replay via a fresh Rebuild is
	// covered in pkg/ingest; here we assert bob is intact instead).
	if resp, body := h.call(t, http.MethodGet, "/v1/boards/high/rank?member=bob", h.key(), nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("bob rank: %d %s", resp.StatusCode, body)
	}

	// --- Delete a registered player entirely (session auth + CSRF). ---
	var u struct {
		UserID string `json:"user_id"`
	}
	resp, body := h.call(t, http.MethodPost, "/v1/users", h.sess(), map[string]string{"nickname": "Ninja"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("register: %d %s", resp.StatusCode, body)
	}
	mustJSON(t, body, &u)
	if resp, body := h.call(t, http.MethodPost, "/v1/boards", h.key(), map[string]any{"board": "laps"}); resp.StatusCode != http.StatusCreated {
		t.Fatalf("create board 2: %d %s", resp.StatusCode, body)
	}
	for _, board := range []string{"high", "laps"} {
		if resp, body := h.call(t, http.MethodPost, "/v1/boards/"+board+"/scores", h.key(), map[string]any{"member": u.UserID, "score": 777}); resp.StatusCode != http.StatusAccepted {
			t.Fatalf("submit %s: %d %s", board, resp.StatusCode, body)
		}
	}
	if err := h.cons.Drain(ctx); err != nil {
		t.Fatal(err)
	}

	if resp, body := h.call(t, http.MethodDelete, "/v1/users/"+u.UserID, h.sess(), nil); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete player: %d %s", resp.StatusCode, body)
	}
	// Gone from every board...
	for _, board := range []string{"high", "laps"} {
		if resp, body := h.call(t, http.MethodGet, "/v1/boards/"+board+"/top?n=10", h.key(), nil); strings.Contains(string(body), u.UserID) {
			t.Fatalf("player still on %s: %d %s", board, resp.StatusCode, body)
		}
	}
	// ...registration gone, nickname re-claimable.
	if resp, _ := h.call(t, http.MethodGet, "/v1/users/"+u.UserID, h.key(), nil); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("get deleted user: %d", resp.StatusCode)
	}
	if resp, body := h.call(t, http.MethodPost, "/v1/users", h.key(), map[string]string{"nickname": "Ninja"}); resp.StatusCode != http.StatusCreated {
		t.Fatalf("nickname not released: %d %s", resp.StatusCode, body)
	}
	// Deleting an unregistered raw member is still 204 (registry no-op).
	if resp, _ := h.call(t, http.MethodDelete, "/v1/users/bob", h.key(), nil); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete raw member: %d", resp.StatusCode)
	}
}

func TestModerationAuth(t *testing.T) {
	h := newHarness(t)
	h.onboard(t, "mod-auth@example.com")

	// No auth at all -> 401.
	if resp, _ := h.call(t, http.MethodDelete, "/v1/boards/high/scores/alice", map[string]string{"Authorization": "Bearer nope"}, nil); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("bad key: %d", resp.StatusCode)
	}
	// Session without CSRF -> 403 (cookie jar carries the session).
	if resp, _ := h.call(t, http.MethodDelete, "/v1/users/whoever", map[string]string{"X-App-Id": h.appID}, nil); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("missing csrf: %d", resp.StatusCode)
	}
}

// mustJSON unmarshals or fails the test.
func mustJSON(t *testing.T, data []byte, v any) {
	t.Helper()
	if err := json.Unmarshal(data, v); err != nil {
		t.Fatalf("unmarshal %s: %v", data, err)
	}
}
