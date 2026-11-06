# ADR-0012: Subject erasure job (PII deletion by subject identifier)

| | |
| --- | --- |
| Status | Accepted |
| Date | 2026-11-06 |
| Deciders | backend-engineer (for lead review) |
| DESIGN refs | §13.2, §13.3, §13.4, §14, §16.1, §16.5; SECURITY §4 |

## Context
DESIGN §16.5 says "deletion job purges by subject identifier" and §13.3 says hashes, sizes, versions, attempts and the final record are always kept. The design does not say how the job is started, how a subject's rows are found, what happens to requests still being delivered, or what replaces a purged body. Facts that shape the answer:

- A subject's identifier can be stored in many forms: raw (`full` payloads, and the outbox and DLQ envelopes, kept results and callback payloads, which keep the canonical body in clear whatever the policy), JSON-escaped, as a keyed HMAC (`masked` payloads, per tenant, from `HUB_AUDIT_MASK_KEY`), and encrypted (`sealed` payloads, per-tenant AES-GCM).
- There is no subject index: nothing maps a subject to its requests.
- `contracts/openapi/hub-admin.v1.yaml` (1.x) has no erasure operation, and `ObjectType` has no `subject` value.
- Queued envelopes are still needed for delivery, and an in-flight request keeps writing audit rows.

## Options considered
1. **Admin API operation**: not in the contract, so it cannot be shipped (AGENTS §4). Requested as a contract change instead (HUB-247).
2. **`hubctl` subcommand**: hubctl only talks to the admin API (token, contracted endpoints). It cannot reach Postgres or the mask and seal keys.
3. **`hub erase-subject` maintenance command** (like `hub migrate`): runs with the same environment as the roles (`HUB_PG_DSN`, `HUB_AUDIT_MASK_KEY`, seal key file) and writes `change_log` directly.

How rows are found: (a) a subject index written on the hot path (a new table, a write per request, a §18.5 review); (b) **scan the tenant's rows in a time window** for every stored form, opening sealed payloads one by one.

In-flight requests: (i) purge them too, which breaks delivery or needs a new terminal transition and callback; (ii) **defer them** and report them, so the operator runs the job again once they are terminal.

What a purged body becomes: (x) delete the rows, which breaks §13.3; (y) **set the body to NULL and keep the row**.

## Decision
Option 3 with (b), (ii) and (y):

- `hub erase-subject -tenant T -actor WHO -reason TEXT [-since] [-until] [-dry-run]`. The identifier comes **from stdin only** (process listings and shell history never see it) and is registered with the redacting logger. Output is JSON. Exit codes: 0 done, 3 done with deferred requests, 1 failed (the transaction rolled back), 2 usage.
- **Matching**: `audit.Masker.SubjectForms` gives the raw value, its JSON-escaped forms, the identifier HMAC and, for emails, local-part HMAC@domain. These are the only x-pii masks derived from the whole value. Kinds masked to the last four characters or `***` keep nothing that identifies the subject, so they add no form. Each owner package searches its own tables, scoped to the tenant: `audit` (request_events clear payloads in Postgres, sealed payloads opened with the sealer, request_results), `queue` (outbox and dead-letter envelopes, last vendor request/response), `callbacks` (payloads), `sessions` (data, vendor txn id). `control.Erasure` takes the union of the request ids.
- **Scope of a purge**: every stored body of every matched request goes, not only the bodies that contain the identifier. A credit report or a KYC response is about the subject even when it does not repeat the identifier.
- **Purge** (one transaction, with the `change_log` row): `request_events.payload = NULL`, `payload_state = 'omitted'` (the existing "hash and size only" state, so the timeline shows sha256 and bytes) and `meta.pii_erased = <change_id>`. Kept result and error become NULL. A dead letter's `envelope.canonical` becomes null and its last vendor request/response are dropped, and `queue.Outbox.Replay` now refuses an erased dead letter. Delivered and dead-lettered callbacks get an empty payload. Vendor sessions holding a form are deleted (forced expiry). Event rows, sha256, bytes, types, times, other meta, the `requests` row, `request_attempts`, dead-letter and callback metadata all stay.
- **Deferral**: a request still in the outbox, or with a callback still to deliver, is not touched and is listed in `deferred_request_ids`. Dead letters are locked `FOR UPDATE` before the outbox check, so a concurrent replay waits and then finds nothing to replay.
- **Fail closed**: if the tenant has sealed payloads in the window and no seal key is configured, the job fails (`audit.ErrNoSealKey`). It never reports a partial search as done. `HUB_AUDIT_MASK_KEY` is required.
- **change_log**: one row per run (`action = eraseSubjectPayloads`, `object_type = request`, `object_id` = the request id, `bulk`, or `none`, reason required, ≥ 10 characters). `after` holds tenant, counts, request ids (first 1000), deferred ids and `subject_ref`. `subject_ref` is an HMAC with its own domain label, never equal to a stored form. A dry run writes nothing.

## Consequences
- Cost: one sequential scan of the tenant's events in the window (all partitions by default), plus one AES-GCM open per sealed event. This is fine for an occasional, operator-run job at phase-1 volume. HUB-245 tracks a hashed subject index so the job reads only the subject's rows.
- An identifier stored in a form the job does not compute is not found. Examples: a value normalised differently by a transformer (case, spaces), or a mask kind whose output is not derived from the whole value. Masked last-four forms are left, by design.
- Not covered by this job: idempotency replay records in Redis (24 h TTL, keys hashed). Releasing them would make a retry with the same key run again and could double-charge `moves_money_or_quota` integrations. HUB-246 tracks this. Identifiers used as `Idempotency-Key` or ordering keys are metadata, not bodies, and stay; callers must not put PII in them (SECURITY §4). Object-storage spill of truncated payloads (HUB-098) does not exist yet. When it lands, the erasure must delete the referenced objects.
- Whoever has the gateway's environment can run the job. There is no admin RBAC until the contracted operation (HUB-247) exists. Every run is in `change_log` with actor and reason.
- `getRequestStatus` of an erased request returns `result: null` / `error: null`. `getDeadLetter` returns `canonical: null`. Both are allowed by the contracts.
