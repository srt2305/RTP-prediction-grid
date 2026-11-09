.PHONY: build serve demo live grid audit series web lint test test-full up down reset

build:
	go -C backend build -o ../bin/engine .

# Engine plus HTTP and WebSocket on :8080.
serve:
	go -C backend run . serve

# Next.js dev server on :3000, talks to the engine above.
web:
	cd frontend && npm run dev

demo:
	go -C backend run . demo

live:
	go -C backend run . live

grid:
	go -C backend run . grid

lint:
	@test -z "$$(gofmt -l backend)" || (gofmt -l backend && exit 1)
	go -C backend vet ./...
	cd backend && golangci-lint run

# Unit only. Integration tests skip themselves without a running stack.
test:
	go -C backend test -short -race ./...

# Everything: Monte Carlo plus integration against Postgres and Redis.
test-full: up
	go -C backend test -race -count=1 ./...

up:
	docker compose up -d --wait

down:
	docker compose down

reset:
	docker compose down -v
	docker compose up -d --wait
