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

# Engine-mode load test against the compose Redis (validates read-latency-vs-size).
loadtest:
	docker compose run --rm -e REDIS_ADDR=redis:6379 app \
	  go run ./cmd/loadtest -mode engine -redis redis:6379 \
	  -sizes 1000,10000,100000,1000000 -readers 8 -writers 8 -dur 3s

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
