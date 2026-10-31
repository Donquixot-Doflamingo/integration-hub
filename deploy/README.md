# deploy/

Phase 2 will add:
- `docker-compose.yml`: Postgres 16 + Redis 7 for local dev and integration tests (ticket in EP-01).
- `alerts/`: alert rules as code (DESIGN §16.3, `docs/tech/OBSERVABILITY.md`).
- `dashboards/`: tenant, vendor/account and platform dashboards as JSON.
- `k8s/`: manifests (later).
