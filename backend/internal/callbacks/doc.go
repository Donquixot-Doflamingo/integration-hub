// Package callbacks delivers signed outbound webhooks to tenant registrar endpoints, with its own retries and DLQ and an SSRF guard (DESIGN §12.1).
//
// The request state machine calls Enqueuer.Enqueue inside the transaction of a winning
// transition; it writes one hub.callback_deliveries row per matching subscription. A Dispatcher
// (worker role) claims due rows with FOR UPDATE SKIP LOCKED, signs the stored body
// (X-Hub-Signature), POSTs it through Guard and records the outcome with a guarded
// transition, retrying on DefaultSchedule and dead-lettering after it. Signing secrets come
// through the SigningSecrets port (creds.RegistrarSecrets); this package never imports creds.
package callbacks
