# Root Makefile: delegates to backend/ and frontend/ (TECH_SPEC §14.2), and validates contracts/.
NODE22 := /opt/homebrew/opt/node@22/bin
export PATH := $(NODE22):$(PATH)

.PHONY: doctor up down dev-up dev-down test-scripts check backend-check frontend-check contracts-check contracts-tools contracts-version-check

doctor:
	$(MAKE) -C backend doctor
	@node --version

up:
	$(MAKE) -C backend up

down:
	$(MAKE) -C backend down

# Whole local stack for manual testing (HUB-286): Postgres + Redis, migrations, seed, mockvendor,
# the hub roles and the console against the real admin API. See README "Run the whole stack locally".
dev-up:
	scripts/dev-up.sh

dev-down:
	scripts/dev-down.sh

# Hermetic shell tests for the dev tooling (HUB-456): own listeners and temp dirs, never the dev stack.
test-scripts:
	@for t in scripts/tests/*_test.sh; do echo "== $$t"; bash $$t || exit 1; done

backend-check:
	$(MAKE) -C backend lint test

frontend-check:
	cd frontend && npm run check && npm run lint && npm test

check: backend-check frontend-check

# ---- contracts (HUB-015, HUB-014). Usage: contracts/README.md "Validate locally".
# CONTRACTS_DIR lets you point the checks at a copy (e.g. to prove they fail on a broken file).
CONTRACTS_DIR ?= contracts
CONTRACTS_TOOLS := $(CONTRACTS_DIR)/tools
export REDOCLY_TELEMETRY := off
export REDOCLY_SUPPRESS_UPDATE_NOTICE := true

# Pinned toolchain (contracts/tools/package-lock.json); reinstalled only when the lockfile changes.
contracts-tools: $(CONTRACTS_TOOLS)/node_modules/.package-lock.json
$(CONTRACTS_TOOLS)/node_modules/.package-lock.json: $(CONTRACTS_TOOLS)/package-lock.json
	cd $(CONTRACTS_TOOLS) && npm ci --no-audit --no-fund

# Redocly lint of the three OpenAPI documents, then check.mjs ($refs, ajv 2020-12 schemas and
# examples, contracts/AGENTS.md rules, D12 vendor-leak check, MCP maps_to). Both always run, so
# one pass shows every problem; the target fails if either fails.
contracts-check: contracts-tools
	@cd $(CONTRACTS_TOOLS) && rc=0; \
	echo "== redocly lint (contracts/tools/redocly.yaml)"; \
	./node_modules/.bin/redocly lint --config redocly.yaml \
		../openapi/hub-public.v1.yaml ../openapi/hub-admin.v1.yaml ../openapi/mockvendor.v1.yaml || rc=1; \
	echo "== check.mjs"; \
	node check.mjs || rc=1; \
	exit $$rc

# Every contract document whose content changed since BASE has a higher version and a
# contracts/CHANGELOG.md entry. Usage: make contracts-version-check BASE=origin/main
BASE ?= origin/main
contracts-version-check: contracts-tools
	cd $(CONTRACTS_TOOLS) && node version-check.mjs --base $(BASE)
