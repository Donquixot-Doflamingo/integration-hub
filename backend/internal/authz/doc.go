// Package authz is the Hub's client of the OPA decision API for the admin plane (AUTH_PLAN §7.1
// step 5, F-24). It posts the same input the ISFC sidecar middleware posts to
// POST {OPA}/v1/data/<client id without '-'>/decision, so the Rego of integration-hub (which is
// tested against that shape) decides unchanged. It does not import go-commons: that module is not
// published, uses ISFC's error codes and does not verify signatures (gap G10).
//
// The client fails closed: a deny is Decision{Allow: false}; a timeout, a transport error, a
// non-200 answer or a body without a decision is ErrUnavailable. Callers map the two to 403 and
// 503 authz_unavailable.
package authz
