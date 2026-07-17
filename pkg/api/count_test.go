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
	// Data plane requires auth.
	if resp, _ := h.call(t, http.MethodPost, "/auth/logout", map[string]string{"X-CSRF-Token": h.csrf}, nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("logout: %d", resp.StatusCode)
	}
	if resp, _ := h.call(t, http.MethodGet, "/v1/boards/high/count", nil, nil); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated: %d", resp.StatusCode)
	}
}
