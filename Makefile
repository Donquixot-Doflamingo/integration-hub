# Root Makefile: delegates to backend/ and frontend/ (TECH_SPEC §14.2).
NODE22 := /opt/homebrew/opt/node@22/bin
export PATH := $(NODE22):$(PATH)

.PHONY: doctor up down check backend-check frontend-check

doctor:
	$(MAKE) -C backend doctor
	@node --version

up:
	$(MAKE) -C backend up

down:
	$(MAKE) -C backend down

backend-check:
	$(MAKE) -C backend lint test

frontend-check:
	cd frontend && npm run check && npm run lint && npm test

check: backend-check frontend-check
