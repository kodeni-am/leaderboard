# Accounts + Dashboard — Design Spec

**Date:** 2026-06-18
**Status:** Approved — implementation in progress (branch `feat/accounts-dashboard`)

## Goal
Add a human-facing **dashboard** with a real **login/accounts system** on top of
the existing leaderboard API, plus a public **landing page**. Email+password auth
(OAuth-ready), with email verification + password reset.

## Two auth planes (keep separate)
- **Session auth (new):** humans in the dashboard. Redis-backed opaque token in
  an HttpOnly·Secure·SameSite=Lax cookie; CSRF (double-submit) on cookie-authed
  mutations.
- **API-key auth (exists, unchanged):** game SDKs on the data plane.

A logged-in **user owns many apps**; each app keeps its API key for the game.

## Backend

### `pkg/email`
- `Sender` interface; `ConsoleSender` (dev — logs the link), `SMTPSender` (prod,
  env-configured). Dev compose also runs MailHog.

### `pkg/accounts`
- `User{ ID, Email, PasswordHash(bcrypt), EmailVerified, CreatedAt }`.
- `UserStore`, `SessionStore`, `TokenStore` interfaces — Redis + in-memory impls.
  - Sessions: `session:<tok> -> userID` (TTL) + `user:sessions:<uid>` set for
    revoke-all (used on password reset).
  - Tokens (verify/reset): `tok:<purpose>:<tok> -> userID` (TTL), one-time
    (GETDEL on consume).
- `Service`: `Signup`, `Verify`, `ResendVerification`, `Login` (requires verified
  email), `Logout`, `RequestReset`, `ResetPassword`, `UserFromSession`.
- OAuth-ready: password auth behind the service; providers can be added later.

### tenancy changes
- `App` gains `OwnerUserID`. `ListByOwner(userID)`. `CreateApp` records owner.

### API (`pkg/api`)
- New routes: `POST /auth/signup`, `POST /auth/login`, `POST /auth/logout`,
  `GET /auth/verify`, `POST /auth/resend`, `POST /auth/forgot`,
  `POST /auth/reset`, `GET /auth/me`.
- `POST /v1/apps` + `GET /v1/apps` become **session-authed, owner-scoped**
  (replaces admin-token creation).
- **Unified `/v1` data-plane auth:** accept either (a) `Authorization: Bearer
  <apiKey>` (game client) or (b) session cookie + `X-App-Id` header where the
  user owns that app. Both set the same app context, so existing handlers are
  unchanged. CSRF required for session-authed mutations.
- Session + CSRF cookies set on login; cleared on logout.

## Frontend (`web/`, React + Vite)
- **Landing** (public): pitch, design story, quickstart, 3 SDKs, GitHub.
- **Auth**: signup / login / verify / forgot / reset.
- **Dashboard** (session-gated): apps & API keys; board create/configure;
  leaderboard viewer (top-N, search rank, neighbors, friends); test-submit.
- **Aesthetic:** esports-telemetry — refined dark, electric accent, monospace
  numerals, data-dense.
- **Same-origin:** Vite dev-proxies `/auth` + `/v1` to `:8080`; prod serves the
  built SPA on the same origin (Caddy/Go static) so cookies work without CORS.

## Security checklist
bcrypt hashing; session token = 32 random bytes; HttpOnly·Secure·SameSite=Lax;
CSRF double-submit on cookie mutations; revoke sessions on password reset;
one-time email tokens with TTL; generic responses on forgot/login to avoid user
enumeration; never expose the API key to JS except once at create/regenerate.

## Build order
1. Backend accounts + email + wiring (+ tests, live verify with console sender + MailHog).
2. Migrate existing e2e tests/SDK/loadtest to signup→create-app.
3. Frontend.
