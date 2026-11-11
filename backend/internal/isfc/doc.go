// Package isfc is the Hub's client of the ISFC auth-service (AUTH_PLAN §3.1, §6.2, §8): the
// calls the tenant reconciler and the caller-key sync make, and the in-memory map of ISFC
// caller keys the gateway checks on the request path.
//
// Authentication follows the ISFC S2S rule: the client fetches its own API key with its client
// credentials (GET /api/v1/client/api-key, basic auth), sends it as x-isfc-client-id and
// x-isfc-api-key, and refreshes it once on 401 or 403. Nothing here logs a secret, a key or a
// response body; API keys in a client configuration are hashed (registry.HashKey) the moment they
// are decoded, so the plaintext never leaves this package. Tests never call a real ISFC: they use
// package mockisfc.
package isfc
