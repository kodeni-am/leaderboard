import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

// Same-origin dev: proxy the API routes to leaderboardd so session cookies work
// without CORS. In production the built SPA is served on the same origin as the
// API (e.g. behind Caddy).
export default defineConfig({
  plugins: [react()],
  server: {
    port: 5173,
    proxy: {
      "/auth": "http://localhost:8080",
      "/v1": "http://localhost:8080",
      "/healthz": "http://localhost:8080",
    },
  },
});
