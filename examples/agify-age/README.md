# Example: age by name, a public API onboarded without code

Your service sends a first and last name; the Hub asks [agify.io](https://agify.io/documentation) for an age estimate and returns only the age:

```text
POST /v1/integrations/int_age_by_name/invoke   {"firstName": "har", "lastName": "sh"}
  -> Hub -> GET https://api.agify.io/?name=harsh   (vendor answers {"count":1744,"name":"harsh","age":29})
  <- 200 {"age": 29}
```

It is the onboarding case of ADR-0023 (`docs/tech/adr/`): every schema lives in the config, field by field, each field required or optional. The integration owns the caller's request schema (`firstName`, `lastName`, both required), the vendor endpoint declares the query it accepts (`name`, required), a request transformer maps one to the other, and a response transformer keeps only `age`. Samples prove both transformers before anything is served. agify is free without a key for a small daily volume, so its vendor uses the auth scheme `none` and its account has no credentials. Ticket: HUB-435.

**Everything here is an example**, not production config. The overlay is merged over `backend/config` and may only add files.

## Run it

Prerequisites: Go, curl, python3, Postgres + Redis (`docker compose -f deploy/docker-compose.yml up -d`). From the repository root:

```bash
examples/agify-age/run.sh                      # offline (default): mockvendor answers like agify
examples/agify-age/run.sh unknown-name         # a name agify has no data for
examples/agify-age/run.sh rate-limited         # the free allowance is used up
FIRST_NAME=Sarah LAST_NAME=Johnson examples/agify-age/run.sh
AGIFY_TARGET=live examples/agify-age/run.sh    # the real public API (one name of its free daily allowance)
examples/agify-age/run.sh serve                # start and keep running; call it yourself (curl below)
```

| Variable | Default | Meaning |
| --- | --- | --- |
| `AGIFY_TARGET` | `mock` | `mock` (mockvendor on this machine, nothing leaves it) or `live` (`https://api.agify.io`; `run.sh` switches the account's region) |
| `FIRST_NAME`, `LAST_NAME` | `har`, `sh` | the body sent to the Hub (with the mock target, mockvendor always answers the scenario file) |
| `HUB_PG_DSN`, `HUB_REDIS_ADDR`, `HUB_DEMO_HTTP`, `MOCKVENDOR_ADDR`, … | see `../lib/local-hub.sh` | databases and ports |

`run.sh` builds the binaries, merges this folder over `backend/config` into `.demo/agify-age/`, starts mockvendor (mock target only) and a gateway on `127.0.0.1:18080`, programs the scenario and makes the call. There is no `hubctl creds set` step: an auth `none` account has nothing to store. The live target needs direct outbound HTTPS from the gateway: the Hub's upstream client never uses an environment proxy (it would bypass the egress allowlist), so behind a proxy-only network the live call answers 503 `UPSTREAM_UNAVAILABLE`.

### The call

```bash
curl -s http://127.0.0.1:18080/v1/integrations/int_age_by_name/invoke \
  -H 'Authorization: Bearer hk_test_example_age_demo_local_only' -H 'Content-Type: application/json' \
  -d '{"firstName": "har", "lastName": "sh"}'
```

```json
{"age":29}
```

Output of the default (offline) run on the build machine, ports as the defaults:

```text
> POST /v1/integrations/int_age_by_name/invoke
> {"firstName": "har", "lastName": "sh"}
< HTTP 200 in 0.001647s
< X-Hub-Request-Id: req_01M6TA4C1RNFY06WPTJS60CX7X
< X-Hub-Served-By: primary
< {
<     "age": 29
< }
```

The other answers:

| Case | What the vendor answers | What the caller gets |
| --- | --- | --- |
| Found (`found`) | 200 `{"count":1744,"name":"harsh","age":29}` | 200 `{"age":29}` |
| Unknown name (`unknown-name`) | 200 `{"count":0,"name":"xqzvbn","age":null}` | 200 `{}` (a null field is left out: no age) |
| Free allowance used up (`rate-limited`) | 429 `{"error":"Request limit reached"}`, `X-Rate-Limit-Reset: 30` | 429 `UPSTREAM_RATE_LIMITED`, retryable, `retry_after_ms: 30000` |
| Invalid name | 422 `{"error":"Invalid 'name' parameter"}` | 422 `UPSTREAM_REJECTED` "The age could not be estimated for this name" |
| Vendor down, non-JSON 200 | 5xx, or a body that is not JSON | 503 `UPSTREAM_UNAVAILABLE`, retryable |
| Caller sends no `lastName`, an empty name or an extra field | (not called) | 400 `VALIDATION_FAILED` with the JSON pointer, e.g. `/lastName` |

The caller never sees the vendor: not its name, its `count`, the name echo or its `X-Rate-Limit-*` headers. `GET /v1/integrations/int_age_by_name` shows the operation and its request schema, nothing else.

## What is in this folder

| Path | What |
| --- | --- |
| `config/vendors/agify.yaml` | Auth `none`; regions `public` (`https://api.agify.io`) and `mock` (mockvendor); endpoint `estimate@1` `GET /` whose `request_schema.query` requires `name` (optional `country_id`), an informational `response_schema`; docs URLs and the shape checked on 2026-11-07 |
| `config/accounts/agify-public.yaml` | The account: no credentials, no `credentials_ref`; region `public`, `run.sh` switches it to `mock` |
| `config/tenants/age-demo.yaml` | The caller; local test key `hk_test_example_age_demo_local_only` (stored as its sha256) |
| `config/integrations/int_age_by_name/` | Operation `default`: `request_schema.body` with `firstName` and `lastName` required (`x-pii: name`), `validate_input: true`; `sync`, `fail_fast`, `masked` audit, no `Idempotency-Key` needed |
| `config/transformers/AGIFY-REQ` | `query.name: req.firstName + req.lastName`, samples `harsh` and `unicode` (input → expected vendor request; each must also fit the endpoint's `request_schema`) |
| `config/transformers/AGIFY-RES` | `response.age: res.body.age` and the error rules; samples `found`, `unknown_name`, `rate_limited`, `invalid_name`, `bad_gateway`, `malformed_200` |
| `mockvendor/*.json` | Scenario files (`found`, `unknown-name`, `rate-limited`), loaded by `run.sh` and the tests |
| `run.sh` | The demo (uses `../lib/local-hub.sh`) |

The mapping concatenates the names as the integration owner asked (`har` + `sh` → `harsh`). agify's docs recommend the full name with a space ("Sarah Johnson"); to send that, the request transformer would read `req.firstName + " " + req.lastName` and its samples would change accordingly.

## Tests

```bash
cd backend
go test -race -count=1 -run TestExampleAgifyConfigBuilds ./internal/edge/                 # config, samples, scenario files
go test -race -count=1 -tags integration -run TestExampleAgifyAge -v ./internal/edge/     # through the gateway
```

`TestExampleAgifyConfigBuilds` builds the overlay (every sample runs; a failing one refuses the snapshot) and checks auth `none`, the account without credentials, the endpoint schema and that every scenario file answers on `GET /`. `TestExampleAgifyAge` runs `int_age_by_name` through the gateway against mockvendor with each scenario file (every region points at mockvendor: tests never call agify.io), validates every answer against `contracts/openapi/hub-public.v1.yaml`, checks the vendor got `GET /?name=harsh` with no credential, that nothing of the vendor reaches the caller, and that caller mistakes are refused without a vendor call.
