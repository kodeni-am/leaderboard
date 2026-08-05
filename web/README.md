# OpenLeaderboard — Web (landing + dashboard)

React + Vite single-page app: the public **landing page**, the **docs**, the
account screens, and the authenticated **dashboard** (apps & API keys, board
management, leaderboard viewer, test-submit).

## Design system — "cold instrument"

The portal is built like measuring equipment, not a game menu. All tokens live
at the top of `src/index.css`; the rules that keep it coherent:

- **Surfaces.** Matte blue-black (`--ink`) with panel faces one step up. Depth
  is a 1px lit top edge — no gradients, glows, background grids, or scanlines.
- **Colour is reserved.** `--lamp` (amber) means live, rank 1, shown-once, or
  pending; `--alarm` (red) means destructive or failed. Everything else is a
  step of the cold grey ramp. Row-level destructive actions use
  `.btn-danger-quiet`, which only turns red on hover, so the alarm colour never
  becomes wallpaper.
- **Type.** Archivo (widened) for headings, Instrument Sans for prose, IBM Plex
  Mono for anything that is data, an id, or a label. Numerals in columns are
  tabular; large standalone figures are proportional.
- **The panel** (`.panel` + `.panel-head`) is the one container: an engraved
  head strip naming the instrument and reporting its state, over a body.
- **The position rule** (`PositionRule` in `src/components.tsx`) is the
  signature element — where one member stands inside the whole ordered set. An
  approximate rank renders as a band rather than a tick, so the mark never
  claims more precision than the API's `exact` flag carries. It appears on the
  landing hero, in the docs' exact-vs-approximate section, and in the
  dashboard's rank lookup.

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
