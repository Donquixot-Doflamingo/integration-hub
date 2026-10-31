package mockvendor

import (
	"encoding/json"
	"math/rand/v2"
	"net/http"
)

// decide makes the random choices for one request. count is the number of requests the
// route has matched so far, including this one. Fault behaviours arrive in HUB-029.
func decide(p plan, _ int, _ *rand.Rand) plan { return p }

// respond answers a request according to p and records the outcome.
func (s *Server) respond(w http.ResponseWriter, r *http.Request, e *entry, p plan) {
	b := p.beh
	status := b.Status
	if status == 0 {
		status = http.StatusOK
	}
	for k, v := range b.Headers {
		w.Header().Set(k, v)
	}
	switch {
	case b.Echo:
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(e.snapshot())
	case b.BodyRaw != nil:
		w.WriteHeader(status)
		_, _ = w.Write([]byte(*b.BodyRaw))
	default:
		if w.Header().Get("Content-Type") == "" {
			w.Header().Set("Content-Type", "application/json")
		}
		w.WriteHeader(status)
		if len(b.Body) > 0 {
			_, _ = w.Write(b.Body)
		}
	}
	e.finish(status, "responded")
	_ = r
}
