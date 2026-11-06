# deploy/

| Path | What | Ticket |
| --- | --- | --- |
| `docker-compose.yml` | Postgres 16 + Redis 7 for local dev and integration tests | HUB-012 |
| `smoke/` | M0 smoke test (`smoke/m0.sh`, CI job `smoke-m0`) | HUB-032 |
| `alerts/` | Prometheus alert rules (DESIGN §16.3) and their promtool unit tests | HUB-232 |
| `dashboards/` | Grafana dashboards: tenant, vendor/account, platform | HUB-232 |
| `k8s/` | manifests (later) | — |

## Alerts (`alerts/`)
- `hub.rules.yaml`: one rule per DESIGN §16.3 alert, in groups `hub-resilience`, `hub-delivery`, `hub-errors`, `hub-platform`. Labels: `severity` = `page` | `warn`. Annotations: `summary`, `description`, `runbook_url` (`docs/runbooks/<AlertName>.md`, HUB-237). Expressions and caveats: `docs/tech/OBSERVABILITY.md` §11.1, ADR-0014.
- `*_test.yml`: `promtool test rules` unit tests, a firing and a non-firing case for every alert.
- Load it as a Prometheus rule file (`rule_files: [hub.rules.yaml]`). Rules and dashboards read classic histogram buckets (`*_bucket`): if the Prometheus scrape uses native histograms, set `always_scrape_classic_histograms: true` on the Hub job.
- Rules rely on the scrape label `instance` (one per Hub process) and nothing else from the scrape config.

## Dashboards (`dashboards/`)
- `tenant.json` (uid `hub-tenant`, variables `tenant`, `integration`), `vendor.json` (`hub-vendor`: `vendor`, `account`, `endpoint`), `platform.json` (`hub-platform`). Panel list and the panels left out until their metric exists: OBSERVABILITY §10.
- Import: Grafana → Dashboards → New → Import → upload the file, then pick the Prometheus data source in the `datasource` variable. No data source uid is hard-coded; `id` is `null` so Grafana assigns one.
- Edit in Grafana, export with "Export for sharing externally" off, keep `uid`, set `id` to `null`, and review the JSON diff.

## Checks (`make monitoring` in `backend/`, CI job `monitoring`)
```bash
cd backend
make monitoring                       # needs promtool 3.4.2 on PATH (or PROMTOOL=/path/to/promtool)
```
It runs `promtool check rules`, `promtool test rules`, and Go tests in `backend/cmd/hub/internal/roles` that:
- parse every rule and dashboard query with the PromQL parser and fail on any metric or label name the backend does not export (the catalogue is built from the same metric owners the roles register), including `by (...)` and `on (...)` labels and `label_values()` variable queries;
- lint the dashboards (valid JSON, stable `hub-*` uid, unique panel ids, datasource only through `${datasource}`, every `$variable` defined);
- check that every DESIGN §16.3 alert has exactly one rule, its annotations and runbook link, and a firing and a non-firing promtool case.

Grafana itself is not started, so a dashboard import is not proven in CI (HUB-243). Install promtool from the release tarball (`go install` of `cmd/promtool` is refused because the Prometheus module has `replace` directives):
```bash
curl -sSfLO https://github.com/prometheus/prometheus/releases/download/v3.4.2/prometheus-3.4.2.linux-amd64.tar.gz
echo "630177c6ad011193987904f09ffafec29d531abfeb5e43fa3714e376e5f28ddc  prometheus-3.4.2.linux-amd64.tar.gz" | sha256sum -c -
tar -xzf prometheus-3.4.2.linux-amd64.tar.gz --strip-components=1 prometheus-3.4.2.linux-amd64/promtool
```
