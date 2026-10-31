// Package hubclient is the Go SDK for Hub callers. It returns a 202 as a distinct Queued result, never as success (DESIGN §4.5).
//
// It imports nothing from internal/: it is public and must build on its own.
package hubclient
