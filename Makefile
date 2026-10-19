.PHONY: help up down logs ps migrate-up migrate-down migrate-status \
        test test-unit test-race test-integration test-all \
        fmt vet lint check build run token clean

COMPOSE := docker compose
GO      := go

help:
	@echo "JungleGaming Wagering Backend"
	@echo ""
	@echo "Environment:"
	@echo "  make up               start postgres, keycloak, localstack, migrations and the api"
	@echo "  make down             stop everything (keeps volumes)"
	@echo "  make clean            stop everything and drop volumes"
	@echo "  make logs             follow the api logs"
	@echo "  make ps               list running services"
	@echo ""
	@echo "Database:"
	@echo "  make migrate-up       apply pending migrations"
	@echo "  make migrate-down     revert the last migration"
	@echo "  make migrate-status   show which migrations are applied"
	@echo ""
	@echo "Tests:"
	@echo "  make test             unit tests"
	@echo "  make test-race        unit tests with the race detector (runs in a linux container)"
	@echo "  make test-integration integration tests against real containers"
	@echo "  make test-all         unit + race + integration"
	@echo ""
	@echo "Code quality:"
	@echo "  make fmt              format with gofmt"
	@echo "  make vet              run go vet"
	@echo "  make check            fmt check + vet + unit tests"
	@echo ""
	@echo "Utilities:"
	@echo "  make token            print an access token for provider-a"

up:
	$(COMPOSE) up --build -d
	@echo "api:      http://localhost:8080"
	@echo "keycloak: http://localhost:8081 (admin/admin)"

down:
	$(COMPOSE) down

clean:
	$(COMPOSE) down -v

logs:
	$(COMPOSE) logs -f api

ps:
	$(COMPOSE) ps

migrate-up:
	$(COMPOSE) run --rm migrate /app/bin/migrate up

migrate-down:
	$(COMPOSE) run --rm migrate /app/bin/migrate down

migrate-status:
	$(COMPOSE) run --rm migrate /app/bin/migrate status

test test-unit:
	$(GO) test ./...

test-race:
	docker run --rm -v "$(CURDIR):/app" -w /app golang:1.27 go test -race ./...

test-integration:
	TESTCONTAINERS_RYUK_DISABLED=true $(GO) test -tags integration ./tests/integration/ -v -timeout 600s

test-all: test-unit test-race test-integration

fmt:
	$(GO) fmt ./...

vet:
	$(GO) vet ./...
	$(GO) vet -tags integration ./tests/...

lint:
	staticcheck ./...

check: vet test-unit
	@test -z "$$(gofmt -l . | grep -v '^$$')" || (echo "files need gofmt:"; gofmt -l .; exit 1)
	@echo "all checks passed"

build:
	$(GO) build -o bin/api ./cmd/api
	$(GO) build -o bin/migrate ./cmd/migrate

run:
	$(GO) run ./cmd/api

token:
	@curl -s -X POST http://localhost:8081/realms/junglegaming/protocol/openid-connect/token \
		-H 'Content-Type: application/x-www-form-urlencoded' \
		-d 'grant_type=client_credentials&client_id=provider-a&client_secret=provider-a-secret' \
		| sed -n 's/.*"access_token":"\([^"]*\)".*/\1/p'
