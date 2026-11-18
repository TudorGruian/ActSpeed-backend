COMPOSE := docker compose -f deploy/docker-compose.yml

.PHONY: up down run test build fmt

up:        ## start Postgres and Mosquitto
	$(COMPOSE) up -d

down:      ## stop them (data is kept)
	$(COMPOSE) down

run:       ## run the backend
	go run ./cmd/actspeed

test:
	go vet ./...
	go test ./...

build:
	go build -o bin/ ./cmd/...

fmt:
	gofmt -w .

.PHONY: dashboard
dashboard:
	npm --prefix dashboard install
	npm --prefix dashboard run build
