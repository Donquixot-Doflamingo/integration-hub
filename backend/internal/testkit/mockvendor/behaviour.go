package mockvendor

import (
	"context"
	"encoding/json"
	"math/rand/v2"
	"net"
	"net/http"
	"strconv"
	"time"
)

// decide makes every random choice for one request (called with Server.mu held). count is the
// number of requests the route has matched so far, including this one.
func decide(p plan, count int, rng *rand.Rand) plan {
	b := p.beh
	if l := b.Latency; l != nil {
		switch l.Mode {
		case "fixed":
			p.latency = time.Duration(l.FixedMS) * time.Millisecond
		case "random":
			p.latency = time.Duration(l.MinMS+rng.IntN(l.MaxMS-l.MinMS+1)) * time.Millisecond
		}
	}
	if rl := b.RateLimit; rl != nil && count > rl.Burst {
		n := count - rl.Burst
		p.rateLimited = (rl.EveryNth > 0 && n%rl.EveryNth == 0) || (rl.Ratio > 0 && rng.Float64() < rl.Ratio)
	}
	if e := b.ErrorRatio; e != nil && !p.rateLimited {
		p.errored = rng.Float64() < e.Ratio
	}
	return p
}

const htmlErrorPage = "<!DOCTYPE html><html><head><title>502 Bad Gateway</title></head><body><h1>502 Bad Gateway</h1></body></html>"

// respond answers a request according to p in the contract's order: down → latency →
// rate_limit → error_ratio → response shaped by slow_drip and malformed. It records the outcome.
func (s *Server) respond(w http.ResponseWriter, r *http.Request, e *entry, p plan) {
	b := p.beh
	ctx := r.Context()

	if d := b.Down; d != nil {
		switch d.Mode {
		case "hang":
			select {
			case <-ctx.Done():
			case <-s.stop:
			}
			e.finish(0, "hung")
		default: // reset; a request that reaches a refusing mock (race with the listener close) is reset too
			resetConn(w)
			e.finish(0, "reset")
		}
		return
	}

	if p.latency > 0 && !wait(ctx, s.stop, p.latency) {
		e.finish(0, "hung") // client gave up while we were delaying
		return
	}

	if p.rateLimited {
		rl := b.RateLimit
		w.Header().Set("Retry-After", strconv.Itoa((rl.RetryAfterMS+999)/1000))
		body := rl.Body
		if len(body) == 0 {
			body = json.RawMessage(`{"error":"rate_limited"}`)
		}
		writeBody(w, http.StatusTooManyRequests, nil, "application/json", body)
		e.finish(http.StatusTooManyRequests, "rate_limited")
		return
	}

	if p.errored {
		er := b.ErrorRatio
		writeBody(w, er.Status, er.Headers, "application/json", er.Body)
		e.finish(er.Status, "errored")
		return
	}

	status := b.Status
	if status == 0 {
		status = http.StatusOK
	}
	contentType := "application/json"
	var payload []byte
	switch {
	case b.Echo:
		payload, _ = json.Marshal(e.snapshot())
	case b.BodyRaw != nil:
		payload, contentType = []byte(*b.BodyRaw), ""
	default:
		payload = b.Body
	}
	if m := b.Malformed; m != nil {
		switch m.Kind {
		case "truncated_json":
			if len(payload) < 2 {
				payload = []byte("{")
			}
			payload = payload[:len(payload)/2]
		case "invalid_json":
			payload = []byte(`{"status": "success", processed: }`)
		case "wrong_content_type":
			contentType = "text/plain; charset=utf-8"
		case "empty_body":
			payload = nil
		case "html_error_page":
			payload, contentType = []byte(htmlErrorPage), "text/html; charset=utf-8"
		}
		// The route's own Content-Type header must not undo the corruption.
		hdr := make(map[string]string, len(b.Headers))
		for k, v := range b.Headers {
			if http.CanonicalHeaderKey(k) != "Content-Type" {
				hdr[k] = v
			}
		}
		b.Headers = hdr
	}

	if d := b.SlowDrip; d != nil {
		if !drip(ctx, s.stop, w, status, b.Headers, contentType, payload, d) {
			e.finish(status, "hung")
			return
		}
		e.finish(status, "responded")
		return
	}
	writeBody(w, status, b.Headers, contentType, payload)
	e.finish(status, "responded")
}

// writeBody writes headers (route headers override the default content type) and payload.
func writeBody(w http.ResponseWriter, status int, headers map[string]string, contentType string, payload []byte) {
	if contentType != "" {
		w.Header().Set("Content-Type", contentType)
	}
	for k, v := range headers {
		w.Header().Set(k, v)
	}
	w.WriteHeader(status)
	if len(payload) > 0 {
		_, _ = w.Write(payload)
	}
}

// drip sends headers at once, then payload in chunks every interval. It reports false when
// the client went away or the mock is closing.
func drip(ctx context.Context, stop <-chan struct{}, w http.ResponseWriter, status int, headers map[string]string, contentType string, payload []byte, d *SlowDrip) bool {
	w.Header().Set("X-Content-Type-Options", "nosniff") // stop the server buffering to sniff a type
	writeBody(w, status, headers, contentType, nil)
	rc := http.NewResponseController(w)
	_ = rc.Flush()
	interval := time.Duration(d.IntervalMS) * time.Millisecond
	for i := 0; i < len(payload); i += d.ChunkBytes {
		if i > 0 && !wait(ctx, stop, interval) {
			return false
		}
		if _, err := w.Write(payload[i:min(i+d.ChunkBytes, len(payload))]); err != nil {
			return false
		}
		if err := rc.Flush(); err != nil {
			return false
		}
	}
	return true
}

// wait sleeps for d, returning false early if ctx is done or stop is closed.
func wait(ctx context.Context, stop <-chan struct{}, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	case <-stop:
		return false
	}
}

// resetConn closes the TCP connection without a response (RST where possible).
func resetConn(w http.ResponseWriter) {
	conn, _, err := http.NewResponseController(w).Hijack()
	if err != nil {
		return
	}
	if tcp, ok := conn.(*net.TCPConn); ok {
		_ = tcp.SetLinger(0)
	}
	_ = conn.Close()
}
