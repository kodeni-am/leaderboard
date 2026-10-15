package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

const ptA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
const ptB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

func with(h map[string]string, token string) map[string]string {
	out := map[string]string{}
	for k, v := range h {
		out[k] = v
	}
	if token != "" {
		out["X-Player-Token"] = token
	}
	return out
}

func TestPlayerTokens(t *testing.T) {
	h := newHarness(t)
	h.onboard(t, "tokens@example.com")
	h.call(t, http.MethodPost, "/v1/boards", h.key(), map[string]any{"board": "hs"})
	pub := h.issuePublishable(t)
	submit := func(hdr map[string]string, member string) (int, string) {
		resp, body := h.call(t, http.MethodPost, "/v1/boards/hs/scores", hdr, map[string]any{"member": member, "score": 10})
		return resp.StatusCode, string(body)
	}

	// first use binds; same token passes; other token is refused
	if c, b := submit(with(pub, ptA), "surfer-1"); c >= 300 {
		t.Fatalf("bind: %d %s", c, b)
	}
	if c, b := submit(with(pub, ptA), "surfer-1"); c >= 300 {
		t.Fatalf("same token: %d %s", c, b)
	}
	if c, b := submit(with(pub, ptB), "surfer-1"); c != http.StatusForbidden || !strings.Contains(b, "player_token_mismatch") {
		t.Fatalf("other token: %d %s", c, b)
	}
	// malformed token
	if c, b := submit(with(pub, "short"), "surfer-1"); c != http.StatusBadRequest || !strings.Contains(b, "invalid_player_token") {
		t.Fatalf("malformed: %d %s", c, b)
	}
	// secret key + wrong token is still refused (the Worker passes tokens through)
	if c, b := submit(with(h.key(), ptB), "surfer-1"); c != http.StatusForbidden {
		t.Fatalf("secret+wrong token: %d %s", c, b)
	}
	// no token: allowed while the switch is off
	if c, b := submit(pub, "surfer-legacy"); c >= 300 {
		t.Fatalf("no token, switch off: %d %s", c, b)
	}

	// rename / delete / claim are guarded the same way
	resp, body := h.call(t, http.MethodPost, "/v1/users", with(pub, ptA), map[string]string{"nickname": "Wave", "member": "surfer-1"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("claim own member: %d %s", resp.StatusCode, body)
	}
	resp, body = h.call(t, http.MethodPost, "/v1/users", with(pub, ptB), map[string]string{"nickname": "Thief", "member": "surfer-1"})
	if resp.StatusCode != http.StatusForbidden || !strings.Contains(string(body), "player_token_mismatch") {
		t.Fatalf("claim someone else's member: %d %s", resp.StatusCode, body)
	}
	if resp, body = h.call(t, http.MethodPatch, "/v1/users/surfer-1", with(pub, ptB), map[string]string{"nickname": "Stolen"}); resp.StatusCode != http.StatusForbidden || !strings.Contains(string(body), "player_token_mismatch") {
		t.Fatalf("rename with other token: %d %s", resp.StatusCode, body)
	}
	if resp, body = h.call(t, http.MethodDelete, "/v1/users/surfer-1", with(pub, ptB), nil); resp.StatusCode != http.StatusForbidden || !strings.Contains(string(body), "player_token_mismatch") {
		t.Fatalf("delete with other token: %d %s", resp.StatusCode, body)
	}
	if resp, body = h.call(t, http.MethodPatch, "/v1/users/surfer-1", with(pub, ptA), map[string]string{"nickname": "Wave2"}); resp.StatusCode != http.StatusOK {
		t.Fatalf("rename with own token: %d %s", resp.StatusCode, body)
	}

	// failed create keeps the binding; retry with the same token succeeds
	h.call(t, http.MethodPost, "/v1/users", h.key(), map[string]string{"nickname": "Taken"})
	if resp, _ = h.call(t, http.MethodPost, "/v1/users", with(pub, ptB), map[string]string{"nickname": "Taken", "member": "surfer-2"}); resp.StatusCode != http.StatusConflict {
		t.Fatalf("taken nickname: %d", resp.StatusCode)
	}
	if resp, body = h.call(t, http.MethodPost, "/v1/users", with(pub, ptB), map[string]string{"nickname": "Free", "member": "surfer-2"}); resp.StatusCode != http.StatusCreated {
		t.Fatalf("retry after taken: %d %s", resp.StatusCode, body)
	}

	// minted plr_ ids bind the token too
	resp, body = h.call(t, http.MethodPost, "/v1/users", with(pub, ptA), map[string]string{"nickname": "Minted"})
	var u struct {
		UserID string `json:"user_id"`
	}
	json.Unmarshal(body, &u)
	if resp, _ = h.call(t, http.MethodPatch, "/v1/users/"+u.UserID, with(pub, ptB), map[string]string{"nickname": "Nope"}); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("minted id bound to creator's token: %d", resp.StatusCode)
	}

	// switch on: publishable without token refused, secret without token allowed
	if resp, body = h.call(t, http.MethodPut, "/v1/apps/"+h.appID+"/player-tokens", h.sess(), map[string]bool{"require_player_tokens": true}); resp.StatusCode != http.StatusOK || !strings.Contains(string(body), `"require_player_tokens":true`) {
		t.Fatalf("switch on: %d %s", resp.StatusCode, body)
	}
	if c, b := submit(pub, "surfer-3"); c != http.StatusUnauthorized || !strings.Contains(b, "player_token_required") {
		t.Fatalf("no token, switch on: %d %s", c, b)
	}
	if c, b := submit(h.key(), "surfer-3"); c >= 300 {
		t.Fatalf("secret key, no token, switch on: %d %s", c, b)
	}
	// every token-guarded user route refuses a publishable key without a token
	for name, call := range map[string]func() (*http.Response, []byte){
		"claim": func() (*http.Response, []byte) {
			return h.call(t, http.MethodPost, "/v1/users", pub, map[string]string{"nickname": "NoTok", "member": "surfer-1"})
		},
		"mint": func() (*http.Response, []byte) {
			return h.call(t, http.MethodPost, "/v1/users", pub, map[string]string{"nickname": "NoTokMint"})
		},
		"rename": func() (*http.Response, []byte) {
			return h.call(t, http.MethodPatch, "/v1/users/surfer-1", pub, map[string]string{"nickname": "NoTok2"})
		},
		"delete": func() (*http.Response, []byte) { return h.call(t, http.MethodDelete, "/v1/users/surfer-1", pub, nil) },
	} {
		if resp, body := call(); resp.StatusCode != http.StatusUnauthorized || !strings.Contains(string(body), "player_token_required") {
			t.Fatalf("%s without token, switch on: %d %s", name, resp.StatusCode, body)
		}
	}
	if resp, body = h.call(t, http.MethodGet, "/v1/apps/"+h.appID+"/player-tokens", h.sess(), nil); !strings.Contains(string(body), `"require_player_tokens":true`) {
		t.Fatalf("get switch: %d %s", resp.StatusCode, body)
	}
}

func TestPlayerTokenMalformedMint(t *testing.T) {
	h := newHarness(t)
	h.onboard(t, "malformed@example.com")
	pub := h.issuePublishable(t)
	resp, body := h.call(t, http.MethodPost, "/v1/users", with(pub, "short"), map[string]string{"nickname": "Ghost"})
	if resp.StatusCode != http.StatusBadRequest || !strings.Contains(string(body), "invalid_player_token") {
		t.Fatalf("malformed mint: %d %s", resp.StatusCode, body)
	}
	if resp, _ := h.call(t, http.MethodGet, "/v1/users?nickname=Ghost", h.key(), nil); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("no user may be created by a refused mint: %d", resp.StatusCode)
	}
}
