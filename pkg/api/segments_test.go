package api

import (
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/kodeni-am/leaderboard/pkg/engine"
)

func TestListSegments(t *testing.T) {
	h := newHarness(t)
	h.onboard(t, "segs@example.com")
	ctx := context.Background()
	t.Cleanup(func() {
		for _, seg := range []string{"all", "region=eu"} {
			_ = h.eng.Reset(ctx, engine.Board{Key: engine.BoardKey{App: h.appID, Board: "high", Segment: seg, Window: "all"}})
		}
	})

	if resp, body := h.call(t, http.MethodPost, "/v1/boards", h.key(), map[string]any{"board": "high"}); resp.StatusCode != http.StatusCreated {
		t.Fatalf("create board: %d %s", resp.StatusCode, body)
	}

	// Fresh board: an empty JSON array, not null.
	resp, body := h.call(t, http.MethodGet, "/v1/boards/high/segments", h.key(), nil)
	if resp.StatusCode != http.StatusOK || strings.TrimSpace(string(body)) != `{"segments":[]}` {
		t.Fatalf("fresh board: %d %s", resp.StatusCode, body)
	}

	if resp, body := h.call(t, http.MethodPost, "/v1/boards/high/scores", h.key(), map[string]any{"member": "alice", "score": 100, "segments": []string{"all", "region=eu"}}); resp.StatusCode != http.StatusAccepted {
		t.Fatalf("submit: %d %s", resp.StatusCode, body)
	}
	if err := h.cons.Drain(ctx); err != nil {
		t.Fatal(err)
	}

	resp, body = h.call(t, http.MethodGet, "/v1/boards/high/segments", h.key(), nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("segments: %d %s", resp.StatusCode, body)
	}
	var out struct {
		Segments []string `json:"segments"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("unmarshal %s: %v", body, err)
	}
	if !reflect.DeepEqual(out.Segments, []string{"all", "region=eu"}) {
		t.Fatalf("got %v, want [all region=eu]", out.Segments)
	}

	if resp, _ := h.call(t, http.MethodGet, "/v1/boards/nope/segments", h.key(), nil); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown board: %d", resp.StatusCode)
	}
}
