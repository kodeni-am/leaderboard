package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/kodeni-am/leaderboard/pkg/engine"
)

func TestUserEndpoints(t *testing.T) {
	h := newHarness(t)
	h.onboard(t, "users@example.com")

	// Register a player (API-key data plane).
	resp, body := h.call(t, http.MethodPost, "/v1/users", h.key(), map[string]string{"nickname": "Ninja"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("register: %d %s", resp.StatusCode, body)
	}
	var u struct {
		UserID   string `json:"user_id"`
		Nickname string `json:"nickname"`
	}
	json.Unmarshal(body, &u)
	if !strings.HasPrefix(u.UserID, "plr_") || u.Nickname != "Ninja" {
		t.Fatalf("register body: %s", body)
	}

	// Duplicate nickname (different case) -> 409 with a stable error code.
	resp, body = h.call(t, http.MethodPost, "/v1/users", h.key(), map[string]string{"nickname": "NINJA"})
	if resp.StatusCode != http.StatusConflict || !strings.Contains(string(body), "nickname_taken") {
		t.Fatalf("dup register: %d %s", resp.StatusCode, body)
	}

	// Invalid nickname -> 400.
	resp, body = h.call(t, http.MethodPost, "/v1/users", h.key(), map[string]string{"nickname": "  "})
	if resp.StatusCode != http.StatusBadRequest || !strings.Contains(string(body), "invalid_nickname") {
		t.Fatalf("invalid register: %d %s", resp.StatusCode, body)
	}

	// Get by id.
	resp, body = h.call(t, http.MethodGet, "/v1/users/"+u.UserID, h.key(), nil)
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "Ninja") {
		t.Fatalf("get: %d %s", resp.StatusCode, body)
	}
	resp, body = h.call(t, http.MethodGet, "/v1/users/plr_nope", h.key(), nil)
	if resp.StatusCode != http.StatusNotFound || !strings.Contains(string(body), "user_not_found") {
		t.Fatalf("get unknown: %d %s", resp.StatusCode, body)
	}

	// Reverse lookup by nickname (case-insensitive); missing param -> 400.
	resp, body = h.call(t, http.MethodGet, "/v1/users?nickname=ninja", h.key(), nil)
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), u.UserID) {
		t.Fatalf("lookup: %d %s", resp.StatusCode, body)
	}
	if resp, _ = h.call(t, http.MethodGet, "/v1/users", h.key(), nil); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("lookup without nickname: %d", resp.StatusCode)
	}

	// Rename; renaming to a taken name -> 409.
	resp, body = h.call(t, http.MethodPost, "/v1/users", h.key(), map[string]string{"nickname": "Pixel"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("register 2nd: %d %s", resp.StatusCode, body)
	}
	var u2 struct {
		UserID string `json:"user_id"`
	}
	json.Unmarshal(body, &u2)
	resp, body = h.call(t, http.MethodPatch, "/v1/users/"+u2.UserID, h.key(), map[string]string{"nickname": "ninja"})
	if resp.StatusCode != http.StatusConflict || !strings.Contains(string(body), "nickname_taken") {
		t.Fatalf("rename to taken: %d %s", resp.StatusCode, body)
	}
	resp, body = h.call(t, http.MethodPatch, "/v1/users/"+u2.UserID, h.key(), map[string]string{"nickname": "Voxel"})
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "Voxel") {
		t.Fatalf("rename: %d %s", resp.StatusCode, body)
	}
	// (Auth middleware behavior is covered by the existing data-plane tests;
	// the harness client carries a session cookie after onboard, so an
	// "unauthenticated" request here wouldn't actually be unauthenticated.)
}

func TestNicknameEnrichment(t *testing.T) {
	h := newHarness(t)
	h.onboard(t, "enrich@example.com")
	t.Cleanup(func() {
		_ = h.eng.Reset(context.Background(), engine.Board{Key: engine.BoardKey{App: h.appID, Board: "high", Segment: "all", Window: "all"}})
	})

	if resp, body := h.call(t, http.MethodPost, "/v1/boards", h.key(), map[string]any{"board": "high"}); resp.StatusCode != http.StatusCreated {
		t.Fatalf("create board: %d %s", resp.StatusCode, body)
	}

	// One registered player, one raw member.
	resp, body := h.call(t, http.MethodPost, "/v1/users", h.key(), map[string]string{"nickname": "Ninja"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("register: %d %s", resp.StatusCode, body)
	}
	var u struct {
		UserID string `json:"user_id"`
	}
	json.Unmarshal(body, &u)

	for member, score := range map[string]float64{u.UserID: 500, "raw-anon": 300} {
		if resp, body := h.call(t, http.MethodPost, "/v1/boards/high/scores", h.key(), map[string]any{"member": member, "score": score}); resp.StatusCode != http.StatusAccepted {
			t.Fatalf("submit %s: %d %s", member, resp.StatusCode, body)
		}
	}
	if err := h.cons.Drain(context.Background()); err != nil {
		t.Fatal(err)
	}

	// top: the registered member carries its nickname; the raw one omits it.
	resp, body = h.call(t, http.MethodGet, "/v1/boards/high/top?n=10", h.key(), nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("top: %d %s", resp.StatusCode, body)
	}
	var top struct {
		Entries []struct {
			Member   string `json:"member"`
			Nickname string `json:"nickname"`
		} `json:"entries"`
	}
	json.Unmarshal(body, &top)
	if len(top.Entries) != 2 || top.Entries[0].Member != u.UserID || top.Entries[0].Nickname != "Ninja" {
		t.Fatalf("top enrichment: %s", body)
	}
	if top.Entries[1].Nickname != "" || strings.Contains(jsonEntry(t, body, 1), `"nickname"`) {
		t.Fatalf("raw member should omit nickname: %s", body)
	}

	// rank (single entry) is enriched too.
	resp, body = h.call(t, http.MethodGet, "/v1/boards/high/rank?member="+u.UserID, h.key(), nil)
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), `"nickname":"Ninja"`) {
		t.Fatalf("rank enrichment: %d %s", resp.StatusCode, body)
	}

	// neighbors is enriched.
	resp, body = h.call(t, http.MethodGet, "/v1/boards/high/neighbors?member="+u.UserID+"&k=2", h.key(), nil)
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), `"nickname":"Ninja"`) {
		t.Fatalf("neighbors enrichment: %d %s", resp.StatusCode, body)
	}

	// friends is enriched.
	resp, body = h.call(t, http.MethodPost, "/v1/boards/high/friends", h.key(), map[string]any{"members": []string{u.UserID, "raw-anon"}})
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), `"nickname":"Ninja"`) {
		t.Fatalf("friends enrichment: %d %s", resp.StatusCode, body)
	}
}

// jsonEntry re-marshals entry i of an {"entries": [...]} body so tests can
// assert on the raw presence/absence of a key.
func jsonEntry(t *testing.T, body []byte, i int) string {
	t.Helper()
	var out struct {
		Entries []json.RawMessage `json:"entries"`
	}
	if err := json.Unmarshal(body, &out); err != nil || len(out.Entries) <= i {
		t.Fatalf("jsonEntry: %v %s", err, body)
	}
	return string(out.Entries[i])
}
