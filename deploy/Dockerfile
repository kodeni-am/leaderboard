# syntax=docker/dockerfile:1
# Multi-stage build for the leaderboardd server.
FROM golang:1.23 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOFLAGS=-buildvcs=false go build -o /out/leaderboardd ./cmd/leaderboardd

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/leaderboardd /leaderboardd
EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/leaderboardd"]
