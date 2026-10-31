package upstream

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"syscall"
)

// Kind classifies a transport failure, i.e. a call that produced no vendor HTTP response.
// A vendor that answered (any status) is not a transport error: Do returns the response.
// Classifier mapping (TECH_SPEC §10): Timeout → UPSTREAM_TIMEOUT; Connect, Reset, Protocol
// and EgressDenied → UPSTREAM_UNAVAILABLE; Canceled → the caller went away (no vendor outcome).
type Kind string

const (
	KindTimeout      Kind = "timeout"       // connect timeout, endpoint timeout or caller deadline
	KindConnect      Kind = "connect"       // refused, DNS failure, TLS handshake/verification failure
	KindReset        Kind = "reset"         // connection reset or closed before a full response
	KindEgressDenied Kind = "egress_denied" // host not declared in the vendor's regions; never dialed
	KindCanceled     Kind = "canceled"      // the caller's context was canceled (not a deadline)
	KindProtocol     Kind = "protocol"      // anything else: malformed HTTP, bad request build
)

// Sentinels for errors.Is; a *TransportError matches the one of its Kind.
var (
	ErrTimeout      = errors.New("upstream: timeout")
	ErrConnect      = errors.New("upstream: connect failed")
	ErrReset        = errors.New("upstream: connection reset")
	ErrEgressDenied = errors.New("upstream: egress denied")
	ErrCanceled     = errors.New("upstream: canceled")
	ErrProtocol     = errors.New("upstream: protocol error")
)

var kindSentinel = map[Kind]error{
	KindTimeout: ErrTimeout, KindConnect: ErrConnect, KindReset: ErrReset,
	KindEgressDenied: ErrEgressDenied, KindCanceled: ErrCanceled, KindProtocol: ErrProtocol,
}

// TransportError is the typed failure of one attempt. Its message never contains the request
// URL's query string or any header value (query credentials must not reach logs).
type TransportError struct {
	Kind Kind
	// Phase is where it failed: "egress", "connect", "tls", "request" or "response".
	Phase string
	// CallerDeadline is set on a timeout when the caller's deadline (X-Request-Deadline), not
	// the endpoint timeout, ended the attempt.
	CallerDeadline bool
	Err            error
}

func (e *TransportError) Error() string {
	msg := fmt.Sprintf("upstream %s (%s)", e.Kind, e.Phase)
	if e.CallerDeadline {
		msg += " at caller deadline"
	}
	if e.Err != nil {
		msg += ": " + e.Err.Error()
	}
	return msg
}

func (e *TransportError) Unwrap() error { return e.Err }

// Is matches the Kind's sentinel, so errors.Is(err, upstream.ErrTimeout) works.
func (e *TransportError) Is(target error) bool { return kindSentinel[e.Kind] == target }

// dialError marks a failure inside our dial functions (TCP connect or TLS handshake).
type dialError struct {
	phase   string // "connect" | "tls"
	timeout bool   // the connect timeout fired
	err     error
}

func (e *dialError) Error() string { return e.phase + ": " + e.err.Error() }
func (e *dialError) Unwrap() error { return e.err }

// classify turns a client.Do or body-read error into a *TransportError. attemptCtx carries the
// effective deadline; parent is the caller's context.
func classify(err error, phase string, attemptCtx, parent context.Context, callerCapped bool) *TransportError {
	var te *TransportError
	if errors.As(err, &te) {
		return te
	}
	inner := err
	var ue *url.Error
	if errors.As(err, &ue) {
		inner = ue.Err // drop the URL: its query may carry credentials
	}
	out := &TransportError{Kind: KindProtocol, Phase: phase, Err: inner}
	var de *dialError
	var dnsErr *net.DNSError
	var netErr net.Error
	switch {
	case errors.Is(parent.Err(), context.Canceled):
		out.Kind = KindCanceled
	case errors.As(err, &de):
		out.Phase = de.phase
		out.Kind = KindConnect
		if de.timeout {
			out.Kind = KindTimeout
		}
	case errors.Is(err, context.DeadlineExceeded) || errors.Is(attemptCtx.Err(), context.DeadlineExceeded):
		out.Kind = KindTimeout
		out.CallerDeadline = callerCapped
	case errors.As(err, &netErr) && netErr.Timeout():
		out.Kind = KindTimeout
		out.CallerDeadline = callerCapped
	case errors.Is(err, syscall.ECONNREFUSED), errors.As(err, &dnsErr):
		out.Kind = KindConnect
	case isTLSFailure(err):
		out.Kind, out.Phase = KindConnect, "tls"
	case errors.Is(err, syscall.ECONNRESET), errors.Is(err, syscall.EPIPE), errors.Is(err, syscall.ECONNABORTED),
		errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF), errors.Is(err, net.ErrClosed):
		out.Kind = KindReset
	}
	return out
}

func isTLSFailure(err error) bool {
	var (
		unknownCA x509.UnknownAuthorityError
		hostErr   x509.HostnameError
		invalid   x509.CertificateInvalidError
		verifyErr *tls.CertificateVerificationError
		recordErr tls.RecordHeaderError
		alert     tls.AlertError
	)
	return errors.As(err, &unknownCA) || errors.As(err, &hostErr) || errors.As(err, &invalid) ||
		errors.As(err, &verifyErr) || errors.As(err, &recordErr) || errors.As(err, &alert)
}
