# Runbook: run the ISFC local stack beside the Hub (HUB-461)

Status: planned (EP-15). Steps below follow ISFC's `SETUP.md` and the Hub's `scripts/dev-up.sh`. They have not been verified on a machine with both stacks running; HUB-461 stays open until one run is confirmed.

Both stacks use 8080, 8081, 5432 and 6379 by default (AUTH_PLAN gap G11). Move the Hub; leave ISFC on its defaults.

## 1. Start ISFC
Follow `Isfc/isfc-local/isfc/SETUP.md` (Postgres, Redis, Hydra, auth-service, user-service, api-gateway). Local Email OTP is always `123456`.

## 2. Start the Hub on other ports
```bash
HUB_GATEWAY_PORT=8280 HUB_ADMIN_PORT=8281 HUB_PG_PORT=5433 HUB_REDIS_PORT=6380 scripts/dev-up.sh
```
- `DEV_GATEWAY_PORT` and `DEV_ADMIN_PORT` still work and win if both are set.
- Compose publishes Postgres and Redis on `127.0.0.1:$HUB_PG_PORT` and `127.0.0.1:$HUB_REDIS_PORT`. Defaults (5432, 6379) are unchanged.
- If you bring your own services, set `HUB_PG_DSN` and `HUB_REDIS_ADDR` instead.

## 3. Stop
`scripts/dev-down.sh`. If you used custom ports, export the same variables first.
