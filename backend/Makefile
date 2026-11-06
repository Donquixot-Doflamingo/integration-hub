# Backend targets (DESIGN §19 M0, TECH_SPEC §14.2). Run from backend/.
SHELL := /bin/bash
GO ?= go
BIN := bin
CMDS := hub hubctl mockvendor hubmcp
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
GOLANGCI_LINT_VERSION := 2.15.0
COMPOSE := docker compose -f ../deploy/docker-compose.yml
export HUB_PG_DSN ?= postgres://hub:hub@localhost:5432/hub?sslmode=disable
export HUB_REDIS_ADDR ?= localhost:6379

.PHONY: all build test itest lint fmt bench run mockvendor migrate up down doctor tidy clean sqlc sqlc-check goldens monitoring gosec vuln security

all: lint test build

build: ## Build every binary into bin/
	@mkdir -p $(BIN)
	@for c in $(CMDS); do $(GO) build -trimpath -ldflags "-X main.version=$(VERSION)" -o $(BIN)/$$c ./cmd/$$c || exit 1; done
	@echo "built: $(addprefix $(BIN)/,$(CMDS))"

test: ## Unit tests with the race detector, plus timing-sensitive budget tests without it
	$(GO) test -race -count=1 ./...
	$(GO) test -count=3 -run 'Pathological|TimeBudget' ./internal/transform/

itest: up ## Integration tests (tag integration) against compose Postgres + Redis; -p 1: packages share tables
	$(GO) test -race -count=1 -p 1 -tags integration ./...

lint: ## golangci-lint (pinned) + gofmt check
	@v=$$(golangci-lint version --short 2>/dev/null || golangci-lint --version | sed -E 's/.*version ([0-9.]+).*/\1/'); \
	  [ "$$v" = "$(GOLANGCI_LINT_VERSION)" ] || echo "warning: golangci-lint $$v, pinned $(GOLANGCI_LINT_VERSION)"
	golangci-lint run ./...
	@test -z "$$(gofmt -l .)" || (gofmt -l .; echo "gofmt: files need formatting"; exit 1)

# Security scanners (HUB-234, SECURITY.md §8). Versions are pinned; bump them deliberately.
GOSEC_VERSION := 2.30.0
GOVULNCHECK_VERSION := 1.9.0

gosec: ## gosec over non-test code; fails on any medium- or high-severity finding not triaged with #nosec
	$(GO) run github.com/securego/gosec/v2/cmd/gosec@v$(GOSEC_VERSION) -quiet -severity medium -fmt text ./...

vuln: ## govulncheck: fails when reachable code calls a known-vulnerable symbol (stdlib or module)
	$(GO) run golang.org/x/vuln/cmd/govulncheck@v$(GOVULNCHECK_VERSION) ./...

security: gosec vuln ## Both security scanners (CI jobs gosec + govulncheck)

fmt: ## Format code
	golangci-lint fmt ./...

goldens: ## Run every transformer's golden fixtures (hubctl transform test)
	$(GO) run ./cmd/hubctl transform test -config-dir config -contracts-dir ../contracts

PROMTOOL ?= promtool
monitoring: ## Alert rules (promtool check + unit tests) and dashboards query only exported metrics (HUB-232)
	$(PROMTOOL) check rules ../deploy/alerts/*.rules.yaml
	cd ../deploy/alerts && $(PROMTOOL) test rules *_test.yml
	$(GO) test -count=1 -run 'TestAlertRules|TestDashboards|TestQueryChecker|TestExpandVars|TestDesignMetrics' ./cmd/hub/internal/roles/

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

SQLC_VERSION := 1.31.1
sqlc: ## Regenerate internal/control/db from migrations/ and queries
	sqlc generate

sqlc-check: ## Fail if generated sqlc code is stale (CI)
	$(GO) run github.com/sqlc-dev/sqlc/cmd/sqlc@v$(SQLC_VERSION) diff

doctor: ## Check toolchain versions (TECH_SPEC §3)
	@$(GO) version
	@golangci-lint --version | head -1
	@sqlc version 2>/dev/null || echo "sqlc not installed (brew install sqlc)"
	@docker compose version
	@docker info --format 'docker server {{.ServerVersion}}' 2>/dev/null || echo "docker daemon not running (colima start)"

tidy:
	$(GO) mod tidy

clean:
	rm -rf $(BIN)
