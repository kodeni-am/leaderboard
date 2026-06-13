.PHONY: tidy test test-engine build run down fmt vet lint cover

# Run any go command in the toolchain container, with Redis available.
GO = docker compose run --rm app go

tidy:
	$(GO) mod tidy

test:
	docker compose run --rm app go test ./... -count=1

test-engine:
	docker compose run --rm app go test ./pkg/engine/... -count=1 -v

cover:
	docker compose run --rm app go test ./... -count=1 -cover

vet:
	$(GO) vet ./...

fmt:
	$(GO) fmt ./...

build:
	docker compose build leaderboardd

run:
	docker compose up --build leaderboardd

down:
	docker compose down -v
