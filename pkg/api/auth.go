package api

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/kodeni-am/leaderboard/pkg/accounts"
	"github.com/kodeni-am/leaderboard/pkg/tenancy"
)

const (
	sessionCookie = "lb_session"
	csrfCookie    = "lb_csrf"
)

type ctxKey int

const userCtxKey ctxKey = 1

func userFromContext(ctx context.Context) (accounts.User, bool) {
	u, ok := ctx.Value(userCtxKey).(accounts.User)
	return u, ok
}

func bearerToken(r *http.Request) string {
	if h := r.Header.Get("Authorization"); h != "" {
		if after, ok := strings.CutPrefix(h, "Bearer "); ok {
			return strings.TrimSpace(after)
		}
	}
	return strings.TrimSpace(r.Header.Get("X-API-Key"))
}

func isMutation(method string) bool {
	switch method {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	}
	return false
}

func randToken() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// setAuthCookies sets the HttpOnly session cookie and the (JS-readable) CSRF
// cookie, returning the CSRF token for the response body.
func (s *Server) setAuthCookies(w http.ResponseWriter, sessionToken string, ttl time.Duration) string {
	csrf := randToken()
	maxAge := int(ttl.Seconds())
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: sessionToken, Path: "/",
		HttpOnly: true, Secure: s.secureCookies, SameSite: http.SameSiteLaxMode, MaxAge: maxAge,
	})
	http.SetCookie(w, &http.Cookie{
		Name: csrfCookie, Value: csrf, Path: "/",
		HttpOnly: false, Secure: s.secureCookies, SameSite: http.SameSiteLaxMode, MaxAge: maxAge,
	})
	return csrf
}

func (s *Server) clearAuthCookies(w http.ResponseWriter) {
	for _, name := range []string{sessionCookie, csrfCookie} {
		http.SetCookie(w, &http.Cookie{Name: name, Value: "", Path: "/", MaxAge: -1, HttpOnly: name == sessionCookie})
	}
}

// csrfOK verifies the double-submit CSRF token (header must match cookie).
func csrfOK(r *http.Request) bool {
	c, err := r.Cookie(csrfCookie)
	if err != nil || c.Value == "" {
		return false
	}
	h := r.Header.Get("X-CSRF-Token")
	return h != "" && subtle.ConstantTimeCompare([]byte(h), []byte(c.Value)) == 1
}

func (s *Server) userFromCookie(r *http.Request) (accounts.User, bool) {
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		return accounts.User{}, false
	}
	u, err := s.accounts.UserFromSession(r.Context(), c.Value)
	if err != nil {
		return accounts.User{}, false
	}
	return u, true
}

// requireUser authenticates a dashboard user via session cookie (+ CSRF on
// mutations) and puts the user in context.
func (s *Server) requireUser(next http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, ok := s.userFromCookie(r)
		if !ok {
			writeErr(w, http.StatusUnauthorized, "not authenticated")
			return
		}
		if isMutation(r.Method) && !csrfOK(r) {
			writeErr(w, http.StatusForbidden, "invalid csrf token")
			return
		}
		next(w, r.WithContext(context.WithValue(r.Context(), userCtxKey, u)))
	})
}

// requireApp authenticates a data-plane request via EITHER an API key (game
// client) OR a session cookie + X-App-Id header for an app the user owns. Both
// set the tenant in context so the handlers are identical.
func (s *Server) requireApp(next http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if key := bearerToken(r); key != "" {
			app, err := s.store.AppByKey(r.Context(), key)
			if err != nil {
				writeErr(w, http.StatusUnauthorized, "invalid api key")
				return
			}
			next(w, r.WithContext(tenancy.WithApp(r.Context(), app)))
			return
		}
		if u, ok := s.userFromCookie(r); ok {
			if isMutation(r.Method) && !csrfOK(r) {
				writeErr(w, http.StatusForbidden, "invalid csrf token")
				return
			}
			appID := r.Header.Get("X-App-Id")
			if appID == "" {
				writeErr(w, http.StatusBadRequest, "X-App-Id header required for session auth")
				return
			}
			app, err := s.store.GetApp(r.Context(), appID)
			if err != nil || app.OwnerUserID != u.ID {
				writeErr(w, http.StatusForbidden, "app not found or not owned")
				return
			}
			next(w, r.WithContext(tenancy.WithApp(r.Context(), app)))
			return
		}
		writeErr(w, http.StatusUnauthorized, "authentication required")
	})
}

// ---- auth handlers ----

type credsReq struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

func (s *Server) handleSignup(w http.ResponseWriter, r *http.Request) {
	var req credsReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "email and password required")
		return
	}
	u, err := s.accounts.Signup(r.Context(), req.Email, req.Password)
	switch {
	case errors.Is(err, accounts.ErrEmailTaken):
		writeErr(w, http.StatusConflict, "email already registered")
		return
	case errors.Is(err, accounts.ErrInvalidEmail):
		writeErr(w, http.StatusBadRequest, "invalid email address")
		return
	case errors.Is(err, accounts.ErrWeakPassword):
		writeErr(w, http.StatusBadRequest, "password must be at least 8 characters")
		return
	case err != nil:
		writeErr(w, http.StatusInternalServerError, "could not create account")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"id": u.ID, "email": u.Email, "email_verified": false,
		"message": "Account created — check your email to verify it.",
	})
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req credsReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "email and password required")
		return
	}
	token, u, err := s.accounts.Login(r.Context(), req.Email, req.Password)
	if errors.Is(err, accounts.ErrEmailNotVerified) {
		writeErr(w, http.StatusForbidden, "email not verified")
		return
	}
	if err != nil {
		writeErr(w, http.StatusUnauthorized, "invalid email or password")
		return
	}
	csrf := s.setAuthCookies(w, token, s.accounts.SessionTTL())
	writeJSON(w, http.StatusOK, map[string]any{"id": u.ID, "email": u.Email, "csrf_token": csrf})
}

func (s *Server) handleVerify(w http.ResponseWriter, r *http.Request) {
	// Relative redirect keeps us on whatever origin the link was opened from
	// (the SPA in prod, the dev proxy in dev).
	if err := s.accounts.Verify(r.Context(), r.URL.Query().Get("token")); err != nil {
		http.Redirect(w, r, "/login?verified=0", http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/login?verified=1", http.StatusSeeOther)
}

func (s *Server) handleResend(w http.ResponseWriter, r *http.Request) {
	var req credsReq
	_ = json.NewDecoder(r.Body).Decode(&req)
	_ = s.accounts.ResendVerification(r.Context(), req.Email)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true}) // generic (no enumeration)
}

func (s *Server) handleForgot(w http.ResponseWriter, r *http.Request) {
	var req credsReq
	_ = json.NewDecoder(r.Body).Decode(&req)
	if err := s.accounts.RequestReset(r.Context(), req.Email); err != nil {
		writeErr(w, http.StatusInternalServerError, "could not process request")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true}) // generic (no enumeration)
}

type resetReq struct {
	Token    string `json:"token"`
	Password string `json:"password"`
}

func (s *Server) handleReset(w http.ResponseWriter, r *http.Request) {
	var req resetReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "token and password required")
		return
	}
	err := s.accounts.ResetPassword(r.Context(), req.Token, req.Password)
	switch {
	case errors.Is(err, accounts.ErrBadToken):
		writeErr(w, http.StatusBadRequest, "invalid or expired token")
		return
	case errors.Is(err, accounts.ErrWeakPassword):
		writeErr(w, http.StatusBadRequest, "password must be at least 8 characters")
		return
	case err != nil:
		writeErr(w, http.StatusInternalServerError, "could not reset password")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil {
		_ = s.accounts.Logout(r.Context(), c.Value)
	}
	s.clearAuthCookies(w)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	u, _ := userFromContext(r.Context())
	writeJSON(w, http.StatusOK, map[string]any{"id": u.ID, "email": u.Email, "email_verified": u.EmailVerified})
}

// ---- app management (owner-scoped) ----

type createAppReq struct {
	Name string `json:"name"`
}

func (s *Server) handleCreateApp(w http.ResponseWriter, r *http.Request) {
	u, _ := userFromContext(r.Context())
	var req createAppReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || strings.TrimSpace(req.Name) == "" {
		writeErr(w, http.StatusBadRequest, "name required")
		return
	}
	app, key, err := s.store.CreateApp(r.Context(), u.ID, req.Name)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	// The API key is shown exactly once, here.
	writeJSON(w, http.StatusCreated, map[string]any{"id": app.ID, "name": app.Name, "api_key": key})
}

func (s *Server) handleListApps(w http.ResponseWriter, r *http.Request) {
	u, _ := userFromContext(r.Context())
	apps, err := s.store.ListApps(r.Context(), u.ID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if apps == nil {
		apps = []tenancy.App{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"apps": apps})
}

// ownedApp resolves the {id} path param to an app the session user owns, or
// writes a 404 and returns ok=false.
func (s *Server) ownedApp(w http.ResponseWriter, r *http.Request) (tenancy.App, bool) {
	u, _ := userFromContext(r.Context())
	app, err := s.store.GetApp(r.Context(), r.PathValue("id"))
	if err != nil || app.OwnerUserID != u.ID {
		writeErr(w, http.StatusNotFound, "app not found")
		return tenancy.App{}, false
	}
	return app, true
}

func (s *Server) handleListKeys(w http.ResponseWriter, r *http.Request) {
	app, ok := s.ownedApp(w, r)
	if !ok {
		return
	}
	keys, err := s.store.ListKeys(r.Context(), app.ID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if keys == nil {
		keys = []tenancy.APIKey{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"keys": keys})
}

func (s *Server) handleIssueKey(w http.ResponseWriter, r *http.Request) {
	app, ok := s.ownedApp(w, r)
	if !ok {
		return
	}
	plain, key, err := s.store.IssueKey(r.Context(), app.ID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	// Plaintext is shown exactly once.
	writeJSON(w, http.StatusCreated, map[string]any{"id": key.ID, "prefix": key.Prefix, "api_key": plain})
}

func (s *Server) handleRevokeKey(w http.ResponseWriter, r *http.Request) {
	app, ok := s.ownedApp(w, r)
	if !ok {
		return
	}
	err := s.store.RevokeKey(r.Context(), app.ID, r.PathValue("keyId"))
	if errors.Is(err, tenancy.ErrKeyNotFound) {
		writeErr(w, http.StatusNotFound, "key not found")
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleDeleteApp(w http.ResponseWriter, r *http.Request) {
	app, ok := s.ownedApp(w, r)
	if !ok {
		return
	}
	if err := s.store.DeleteApp(r.Context(), app.ID); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
