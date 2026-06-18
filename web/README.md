# OpenLeaderboard — Web (landing + dashboard)

React + Vite single-page app: the public **landing page** and the authenticated
**dashboard** (accounts, apps & API keys, board management, leaderboard viewer,
test-submit). Aesthetic: "esports telemetry" — dark, electric-lime accent, HUD
display type, mono numerals.

## Develop

The app is **same-origin** with the API to keep session cookies working: Vite
dev-proxies `/auth`, `/v1`, and `/healthz` to `leaderboardd` on `:8080`.

```bash
# 1. Start the backend (Redis + server + Mailpit) from the repo root:
docker compose up -d --build leaderboardd

# 2. Start the dev server:
cd web
npm install
npm run dev          # http://localhost:5173
```

Sign up, then grab the verification link from **Mailpit** at
`http://localhost:8025`, verify, and log in.

## Build

```bash
npm run build        # type-checks (tsc --noEmit) then builds to web/dist
```

## Production

Serve `web/dist` as static files on the **same origin** as the API (e.g. a
Caddy `file_server` + `reverse_proxy /auth /v1` to `leaderboardd`) so the
session cookie is first-party. No CORS, no separate auth domain.
