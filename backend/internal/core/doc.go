// Package core holds the shared value types of the Hub: request IDs, the envelope, canonical error codes (DESIGN §4.4), request states (DESIGN §10.9), delivery modes and the vendor request/response shapes.
//
// It is a leaf package: it imports only the standard library, so every other package can depend on it without cycles (ADR-0001).
package core
