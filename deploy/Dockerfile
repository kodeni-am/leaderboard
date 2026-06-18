# syntax=docker/dockerfile:1
# Multi-stage build: compile the dashboard SPA and the Go server, then ship a
# single image that serves both on one origin (WEB_DIR=/web).

# 1. Build the React dashboard.
FROM node:20-alpine AS web
WORKDIR /web
COPY web/package.json web/package-lock.json* ./
RUN npm ci --no-audit --no-fund || npm install --no-audit --no-fund
COPY web/ ./
RUN npm run build

# 2. Build the Go server.
FROM golang:1.23 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOFLAGS=-buildvcs=false go build -o /out/leaderboardd ./cmd/leaderboardd

# 3. Final image.
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/leaderboardd /leaderboardd
COPY --from=web /web/dist /web
ENV WEB_DIR=/web
EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/leaderboardd"]
