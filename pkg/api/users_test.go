package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
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
	if resp.StatusCode != http.StatusConflict {
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
