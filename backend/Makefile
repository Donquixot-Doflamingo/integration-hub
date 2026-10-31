# Backend targets (DESIGN §19 M0, TECH_SPEC §14.2). Run from backend/.
SHELL := /bin/bash
GO ?= go
BIN := bin
CMDS := hub hubctl mockvendor hubmcp
GOLANGCI_LINT_VERSION := 2.15.0
COMPOSE := docker compose -f ../deploy/docker-compose.yml
export HUB_PG_DSN ?= postgres://hub:hub@localhost:5432/hub?sslmode=disable
export HUB_REDIS_ADDR ?= localhost:6379

.PHONY: all build test itest lint fmt bench run mockvendor migrate up down doctor tidy clean

all: lint test build

build: ## Build every binary into bin/
	@mkdir -p $(BIN)
	@for c in $(CMDS); do $(GO) build -trimpath -o $(BIN)/$$c ./cmd/$$c || exit 1; done
	@echo "built: $(addprefix $(BIN)/,$(CMDS))"

test: ## Unit tests with the race detector
	$(GO) test -race -count=1 ./...

itest: up ## Integration tests (tag integration) against compose Postgres + Redis
	$(GO) test -race -count=1 -tags integration ./...

lint: ## golangci-lint (pinned) + gofmt check
	@v=$$(golangci-lint version --short 2>/dev/null || golangci-lint --version | sed -E 's/.*version ([0-9.]+).*/\1/'); \
	  [ "$$v" = "$(GOLANGCI_LINT_VERSION)" ] || echo "warning: golangci-lint $$v, pinned $(GOLANGCI_LINT_VERSION)"
	golangci-lint run ./...
	@test -z "$$(gofmt -l .)" || (gofmt -l .; echo "gofmt: files need formatting"; exit 1)

fmt: ## Format code
	golangci-lint fmt ./...

bench: ## Benchmarks with allocations
	$(GO) test -run '^$$' -bench . -benchmem ./...

run: build ## Run the gateway locally
	$(BIN)/hub gateway

mockvendor: build ## Run mockvendor locally on :8099
	$(BIN)/mockvendor

up: ## Start Postgres + Redis
	$(COMPOSE) up -d --wait

down: ## Stop Postgres + Redis
	$(COMPOSE) down

migrate: ## Apply embedded goose migrations to HUB_PG_DSN
	$(GO) run ./cmd/hub migrate

doctor: ## Check toolchain versions (TECH_SPEC §3)
	@$(GO) version
	@golangci-lint --version | head -1
	@docker compose version
	@docker info --format 'docker server {{.ServerVersion}}' 2>/dev/null || echo "docker daemon not running (colima start)"

tidy:
	$(GO) mod tidy

clean:
	rm -rf $(BIN)
