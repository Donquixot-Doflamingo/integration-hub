# ADR-0013: Verify alert and dashboard queries against the exported metric catalogue

| | |
| --- | --- |
| Status | Accepted |
| Date | 2026-11-06 |
| Deciders | backend-engineer (for lead review) |
| Tickets | HUB-232, HUB-247 |
| DESIGN refs | §16.2, §16.3; TESTING_STRATEGY §11 |

## Context
Alert rules (`deploy/alerts/`) and Grafana dashboards (`deploy/dashboards/`) are PromQL strings. A renamed metric or label in Go silently empties a panel or turns an alert into one that never fires; `promtool check rules` validates syntax only. The OBSERVABILITY §11.1 plan itself grouped `parked_oldest_age_s` by a `vendor` label the metric does not have. HUB-232 asks for a dashboard "import check", but Grafana is not available in CI or in the dev container, and an import succeeds with any query string.

## Options considered
1. **Grafana import check in CI** (Grafana service container, POST each dashboard). Proves the JSON model loads, not that a query returns data. Needs a pinned Grafana image and a job we cannot run locally.
2. **Regex scan for metric names.** No new dependency; fragile with PromQL functions, `by` clauses, label matchers and Grafana variables.
3. **Parse with the Prometheus PromQL parser and check against the registry (chosen).** A Go test builds every metric owner the roles wire on one registry (`catalogRegistry`), reads names and label names from the descriptors, and walks each parsed expression: selector names and matchers, `by` labels against the labels the aggregated input carries, and `on (...)` labels on both sides. Dashboards are linted in the same test (stable `hub-*` uid, `id: null`, unique panel ids, datasource only through `${datasource}`, every `$variable` defined, `label_values()` variable queries checked too).

## Decision
Option 3, in `backend/cmd/hub/internal/roles/deploy_queries_test.go`, using `github.com/prometheus/prometheus/promql/parser` at v0.304.2 (the module of the promtool 3.4.2 release CI uses, so both read the same PromQL dialect). The parser is a test-only import: no production binary links it. Classic histograms are queried through `_bucket`/`_sum`/`_count` only (a bare histogram name is rejected), because the default Prometheus scrape does not ingest native histograms. CI runs the test plus `promtool check rules` and `promtool test rules` in the `monitoring` job (`make monitoring`), with promtool from the pinned release tarball and its SHA-256.

## Consequences
- Positive: renaming a metric or label, or dropping a label from a metric, fails CI with the exact alert or panel. Rules and dashboards cannot reference planned-but-unbuilt metrics.
- Costs: `go.mod` gains `prometheus/prometheus` (plus `grafana/regexp`, `dennwc/varint`; about 70 `go.sum` lines). Histograms and summaries are listed by hand in the test (`histogramNames`, `summaryNames`); a stale entry fails the test, a missing one fails as "not exported" on first use.
- Labels added by the scrape config (`job`, `instance`, `pod`, `namespace`, `role`) are accepted on every metric.
- Grafana import is not proven; HUB-247 adds that job once a Grafana image is pinned.

## Deviation from DESIGN.md
None. TECH_SPEC §13 lists the new test dependency.
