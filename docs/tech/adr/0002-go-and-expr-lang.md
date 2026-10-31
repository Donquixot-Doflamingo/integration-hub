# ADR-0002: Go and expr-lang/expr for the backend and transformers

| | |
| --- | --- |
| Status | Accepted |
| Date | 2026-10-31 |
| Deciders | lead, user (kickoff default "Go") |
| DESIGN refs | D8, §7, §18.6, §18.7 |

## Context
The Hub is a latency-sensitive proxy (≤ 5 ms p99 overhead, DESIGN §18.6) with a declarative transformation layer whose expressions must be type-checked at publish, compiled once, pure and bounded (DESIGN §7.1). The kickoff offered Go (default) or Java.

## Options considered
1. **Go + `expr-lang/expr`** — static binaries, low and predictable latency, simple concurrency; expr compiles to bytecode, type-checks against a typed env, has no loops (only builtins like `map`/`filter`), supports `MaxNodes` and a memory budget.
2. **Go + CEL (`cel-go`)** — standardised, portable to Java; heavier API, stricter type system makes the "80 % plain paths" YAML more verbose; cost estimation is a plus.
3. **Java + CEL/JEXL** (DESIGN §18.7 fallback) — mature ecosystem, but higher memory per pod and GC tuning cost for the latency budget.

## Decision
Go 1.25.x with `expr-lang/expr`. All expressions are compiled with a typed environment derived from the capability JSON Schema at snapshot build; options: `expr.Env(typedEnv)`, `expr.AsKind`/output type checks per target, `expr.MaxNodes`, custom stdlib functions only (DESIGN §7.3), no access to arbitrary Go methods.

## Consequences
- Runtime **time** budget: expr has no native cancellation; enforcement is by construction (no loops, node limit, input/output size caps, memory budget) plus a post-hoc wall-clock check that records `transform_budget_exceeded_total{limit="time"}`. A hard preemptive time limit would require evaluating in a separate goroutine, which costs more than the budget; not done.
- If mappings ever need to run in Java (DESIGN D8), migrate to CEL; the DSL surface (paths on the left, expressions on the right) is kept engine-neutral to make that possible.
- Toolchain must be installed (HUB-012).

## Deviation from DESIGN.md
None.
