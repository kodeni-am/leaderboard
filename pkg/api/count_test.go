package api

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/kodeni-am/leaderboard/pkg/engine"
)

// countOf GETs a count endpoint with API-key auth and returns the count field.
func countOf(t *testing.T, h *harness, path string) int64 {
	t.Helper()
	resp, body := h.call(t, http.MethodGet, path, h.key(), nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s: %d %s", path, resp.StatusCode, body)
	}
	var out struct {
		Count int64 `json:"count"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("unmarshal %s: %v", body, err)
	}
	return out.Count
}

func TestBoardCount(t *testing.T) {
	h := newHarness(t)
	h.onboard(t, "count@example.com")
	ctx := context.Background()
	t.Cleanup(func() {
		for _, seg := range []string{"all", "region=eu"} {
			_ = h.eng.Reset(ctx, engine.Board{Key: engine.BoardKey{App: h.appID, Board: "high", Segment: seg, Window: "all"}})
		}
	})

	if resp, body := h.call(t, http.MethodPost, "/v1/boards", h.key(), map[string]any{"board": "high"}); resp.StatusCode != http.StatusCreated {
		t.Fatalf("create board: %d %s", resp.StatusCode, body)
	}

	// A fresh board counts zero.
	if n := countOf(t, h, "/v1/boards/high/count"); n != 0 {
		t.Fatalf("fresh board: %d, want 0", n)
	}

	// alice lands in both "all" and "region=eu"; bob only in "all".
	if resp, body := h.call(t, http.MethodPost, "/v1/boards/high/scores", h.key(), map[string]any{"member": "alice", "score": 100, "segments": []string{"all", "region=eu"}}); resp.StatusCode != http.StatusAccepted {
		t.Fatalf("submit alice: %d %s", resp.StatusCode, body)
	}
	if resp, body := h.call(t, http.MethodPost, "/v1/boards/high/scores", h.key(), map[string]any{"member": "bob", "score": 50}); resp.StatusCode != http.StatusAccepted {
		t.Fatalf("submit bob: %d %s", resp.StatusCode, body)
	}
	if err := h.cons.Drain(ctx); err != nil {
		t.Fatal(err)
	}

	if n := countOf(t, h, "/v1/boards/high/count"); n != 2 {
		t.Fatalf("unfiltered: %d, want 2", n)
	}
	// The segment filter counts only that slice.
	if n := countOf(t, h, "/v1/boards/high/count?segment=region=eu"); n != 1 {
		t.Fatalf("segment=region=eu: %d, want 1", n)
	}
	// A window nothing was submitted to is empty, not an error.
	if n := countOf(t, h, "/v1/boards/high/count?window=daily"); n != 0 {
		t.Fatalf("window=daily: %d, want 0", n)
	}
	// Unknown board -> 404, like the other board reads.
	if resp, _ := h.call(t, http.MethodGet, "/v1/boards/nope/count", h.key(), nil); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown board: %d", resp.StatusCode)
	}
	// Data plane requires auth: a cookie-less client sends no session at all.
	r, err := http.Get(h.ts.URL + "/v1/boards/high/count")
	if err != nil {
		t.Fatal(err)
	}
	r.Body.Close()
	if r.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated: %d, want 401", r.StatusCode)
	}
}

func TestAppStats(t *testing.T) {
	h := newHarness(t)
	h.onboard(t, "stats@example.com")

	// Owner-plane GET: the cookie jar carries the session, no CSRF needed.
	players := func() int64 {
		t.Helper()
		resp, body := h.call(t, http.MethodGet, "/v1/apps/"+h.appID+"/stats", nil, nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("stats: %d %s", resp.StatusCode, body)
		}
		var out struct {
			Players int64 `json:"players"`
		}
		if err := json.Unmarshal(body, &out); err != nil {
			t.Fatalf("unmarshal %s: %v", body, err)
		}
		return out.Players
	}

	if n := players(); n != 0 {
		t.Fatalf("new app: %d players, want 0", n)
	}

	resp, body := h.call(t, http.MethodPost, "/v1/users", h.key(), map[string]string{"nickname": "Ninja"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("register Ninja: %d %s", resp.StatusCode, body)
	}
	var u struct {
		UserID string `json:"user_id"`
	}
	if err := json.Unmarshal(body, &u); err != nil {
		t.Fatalf("unmarshal %s: %v", body, err)
	}
	if resp, body := h.call(t, http.MethodPost, "/v1/users", h.key(), map[string]string{"nickname": "Rook"}); resp.StatusCode != http.StatusCreated {
		t.Fatalf("register Rook: %d %s", resp.StatusCode, body)
	}
	if n := players(); n != 2 {
		t.Fatalf("after 2 registrations: %d, want 2", n)
	}

	// Deleting a player drops the count.
	if resp, body := h.call(t, http.MethodDelete, "/v1/users/"+u.UserID, h.sess(), nil); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete player: %d %s", resp.StatusCode, body)
	}
	if n := players(); n != 1 {
		t.Fatalf("after delete: %d, want 1", n)
	}

	// Owner plane: no session -> 401, even though the app exists.
	r, err := http.Get(h.ts.URL + "/v1/apps/" + h.appID + "/stats")
	if err != nil {
		t.Fatal(err)
	}
	r.Body.Close()
	if r.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated: %d, want 401", r.StatusCode)
	}

	// Unknown app -> 404, matching the other owner-plane app routes.
	if resp, _ := h.call(t, http.MethodGet, "/v1/apps/app_nope/stats", nil, nil); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown app: %d, want 404", resp.StatusCode)
	}
}
