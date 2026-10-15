package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// issuePublishable mints a publishable key for the harness app via the dashboard API.
func (h *harness) issuePublishable(t *testing.T) map[string]string {
	t.Helper()
	resp, body := h.call(t, http.MethodPost, "/v1/apps/"+h.appID+"/keys", h.sess(), map[string]string{"scope": "publishable"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("issue publishable: %d %s", resp.StatusCode, body)
	}
	var k struct {
		APIKey string `json:"api_key"`
		Scope  string `json:"scope"`
	}
	json.Unmarshal(body, &k)
	if k.Scope != "publishable" {
		t.Fatalf("scope echoed: %s", body)
	}
	return map[string]string{"Authorization": "Bearer " + k.APIKey}
}

func TestPublishableKeyScope(t *testing.T) {
	h := newHarness(t)
	h.onboard(t, "scopes@example.com")
	if resp, body := h.call(t, http.MethodPost, "/v1/boards", h.key(), map[string]any{"board": "hs"}); resp.StatusCode >= 300 {
		t.Fatalf("secret key creates board: %d %s", resp.StatusCode, body)
	}
	pub := h.issuePublishable(t)

	// admin routes: refused
	if resp, body := h.call(t, http.MethodPost, "/v1/boards", pub, map[string]any{"board": "hs2"}); resp.StatusCode != http.StatusForbidden || !contains(body, "secret_key_required") {
		t.Fatalf("publishable create board: %d %s", resp.StatusCode, body)
	}
	if resp, body := h.call(t, http.MethodDelete, "/v1/boards/hs/scores/someone", pub, nil); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("publishable remove score: %d %s", resp.StatusCode, body)
	}
	// a dashboard session is not a publishable key: it passes secretOnly
	if resp, body := h.call(t, http.MethodPost, "/v1/boards", h.sess(), map[string]any{"board": "hs3"}); resp.StatusCode >= 300 {
		t.Fatalf("session creates board: %d %s", resp.StatusCode, body)
	}
	// player routes and reads: allowed (no tokens required yet)
	if resp, body := h.call(t, http.MethodPost, "/v1/boards/hs/scores", pub, map[string]any{"member": "surfer-1", "score": 10}); resp.StatusCode >= 300 {
		t.Fatalf("publishable submit: %d %s", resp.StatusCode, body)
	}
	if resp, body := h.call(t, http.MethodGet, "/v1/boards/hs/top?n=5", pub, nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("publishable read: %d %s", resp.StatusCode, body)
	}
	// unknown scope rejected
	if resp, _ := h.call(t, http.MethodPost, "/v1/apps/"+h.appID+"/keys", h.sess(), map[string]string{"scope": "admin"}); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("unknown scope: %d", resp.StatusCode)
	}
	// list shows scopes
	_, body := h.call(t, http.MethodGet, "/v1/apps/"+h.appID+"/keys", h.sess(), nil)
	if !contains(body, `"scope":"publishable"`) || !contains(body, `"scope":"secret"`) {
		t.Fatalf("list scopes: %s", body)
	}
}

func contains(b []byte, s string) bool { return strings.Contains(string(b), s) }
