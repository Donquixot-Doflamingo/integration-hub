# Lint and format expectations

## Formatting
- `gofmt` (simplify on) and `goimports` with the local prefix `github.com/Donquixot-Doflamingo/integration-hub`, giving three import groups: stdlib, third-party, this module.
- Editors run them on save; CI fails on any diff (`gofmt -l .` must print nothing).

## golangci-lint
Use golangci-lint v2 (`version: "2"` config). Pin the binary version in the Makefile/CI so results are reproducible. Sketch of `backend/.golangci.yml` (check field names against the pinned version's docs before committing):

```yaml
version: "2"
run:
  timeout: 5m
  build-tags: [integration]
linters:
  default: standard          # errcheck, govet, ineffassign, staticcheck, unused
  enable:
    - bodyclose               # http response bodies closed
    - contextcheck            # ctx passed through, not replaced
    - errorlint               # errors.Is/As, %w
    - errname                 # ErrX vars, XError types
    - exhaustive              # switch over enums (Code, status)
    - forbidigo               # bans below
    - gocritic
    - gosec
    - misspell
    - nilerr
    - noctx                   # http requests built with context
    - revive
    - sloglint                # consistent slog usage
    - unconvert
    - unparam
    - depguard                # import rules from go-architecture
  settings:
    forbidigo:
      forbid:
        - pattern: '^fmt\.Print'
          msg: use slog
        - pattern: '^log\.(Print|Fatal|Panic)'
          msg: use slog; exit only in main
        - pattern: '^time\.Sleep$'
          msg: use timers with ctx, or testing/synctest in tests
    sloglint:
      kv-only: false
      attr-only: true          # slog.String(...) style keeps keys typed
      context: all             # always the *Context variants so trace ids attach
      key-naming-case: snake
    depguard:
      rules:
        transform-pure:
          files: ["**/internal/transform/**"]
          deny:
            - pkg: net/http
              desc: transformers are pure (DESIGN 7.1)
            - pkg: github.com/jackc/pgx/v5
              desc: no DB in transform
            - pkg: github.com/redis/go-redis/v9
              desc: no Redis in transform
  exclusions:
    rules:
      - path: _test\.go
        linters: [forbidigo, gosec, unparam]
formatters:
  enable: [gofmt, goimports]
  settings:
    goimports:
      local-prefixes: [github.com/Donquixot-Doflamingo/integration-hub]
```

Notes:
- `//nolint:<linter> // reason` only with a reason, on the specific line. Never file-wide.
- Adding a new linter that produces many findings goes in its own `chore(lint)` commit.
