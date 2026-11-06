package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"os"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"
)

// target builds the HTTP request of one arrival. The body bytes are shared and never mutated.
type target struct {
	method, url string
	header      http.Header
	body        []byte
}

func (t target) request(ctx context.Context) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, t.method, t.url, bytes.NewReader(t.body))
	if err != nil {
		return nil, err
	}
	for k, v := range t.header {
		req.Header[k] = v
	}
	return req, nil
}

// sample is the outcome of one arrival.
type sample struct {
	latency time.Duration // response end minus intended send time (includes generator lag)
	service time.Duration // response end minus actual send time
	lag     time.Duration // actual send minus intended send time
	outcome string        // "200", "202", "503 UPSTREAM_UNAVAILABLE", "error conn_refused", "dropped"
	ok      bool          // 2xx
}

// generator sends arrivals at a constant rate regardless of how fast responses come back
// (open loop). Arrivals beyond maxInFlight are counted as dropped, never delayed.
type generator struct {
	client      *http.Client
	target      target
	rate        float64
	maxInFlight int
	// onResponse, if set, sees every response body of a 2xx (async: collect request ids).
	onResponse func(status int, body []byte)
}

type genResult struct {
	samples []sample
	elapsed time.Duration
}

func (g *generator) run(ctx context.Context, d time.Duration) genResult {
	n := int(math.Round(g.rate * d.Seconds()))
	samples := make([]sample, n)
	sem := make(chan struct{}, max(g.maxInFlight, 1))
	var wg sync.WaitGroup
	start := time.Now()
	sent := 0
	timer := time.NewTimer(0)
	<-timer.C
	for i := range n {
		intended := start.Add(time.Duration(float64(i) * float64(time.Second) / g.rate))
		if wait := time.Until(intended); wait > 0 {
			timer.Reset(wait)
			select {
			case <-ctx.Done():
				timer.Stop()
			case <-timer.C:
			}
		}
		if ctx.Err() != nil {
			break
		}
		sent++
		select {
		case sem <- struct{}{}:
		default:
			samples[i] = sample{outcome: "dropped"}
			continue
		}
		wg.Go(func() {
			defer func() { <-sem }()
			samples[i] = g.do(ctx, intended)
		})
	}
	wg.Wait()
	return genResult{samples: samples[:sent], elapsed: time.Since(start)}
}

func (g *generator) do(ctx context.Context, intended time.Time) sample {
	req, err := g.target.request(ctx)
	if err != nil {
		return sample{outcome: "error build"}
	}
	t0 := time.Now()
	resp, err := g.client.Do(req)
	if err != nil {
		end := time.Now()
		return sample{latency: end.Sub(intended), service: end.Sub(t0), lag: t0.Sub(intended), outcome: "error " + transportClass(err)}
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	_ = resp.Body.Close()
	end := time.Now()
	s := sample{latency: end.Sub(intended), service: end.Sub(t0), lag: t0.Sub(intended), ok: resp.StatusCode/100 == 2}
	s.outcome = outcomeOf(resp.StatusCode, body)
	if s.ok && g.onResponse != nil {
		g.onResponse(resp.StatusCode, body)
	}
	return s
}

// outcomeOf is the HTTP status plus, for errors, the canonical code of the error envelope.
func outcomeOf(status int, body []byte) string {
	if status < 400 {
		return fmt.Sprint(status)
	}
	var env struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &env) == nil && env.Error.Code != "" {
		return fmt.Sprintf("%d %s", status, env.Error.Code)
	}
	return fmt.Sprint(status)
}

// transportClass names a client-side failure without its (possibly address-bearing) text.
func transportClass(err error) string {
	var ne net.Error
	switch {
	case errors.Is(err, context.Canceled):
		return "canceled"
	case errors.Is(err, os.ErrDeadlineExceeded), errors.Is(err, context.DeadlineExceeded), errors.As(err, &ne) && ne.Timeout():
		return "client_timeout"
	case errors.Is(err, syscall.ECONNREFUSED):
		return "conn_refused"
	case errors.Is(err, syscall.ECONNRESET), errors.Is(err, io.EOF), strings.Contains(err.Error(), "connection reset"):
		return "conn_reset"
	case errors.Is(err, syscall.EADDRNOTAVAIL), errors.Is(err, syscall.EMFILE):
		return "client_resources"
	default:
		return "transport"
	}
}

// newHTTPClient keeps enough idle connections for maxInFlight concurrent arrivals.
func newHTTPClient(maxInFlight int, timeout time.Duration) *http.Client {
	tr := &http.Transport{
		Proxy:               nil, // loopback targets only; never route load through a proxy
		DialContext:         (&net.Dialer{Timeout: 2 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		MaxIdleConns:        maxInFlight,
		MaxIdleConnsPerHost: maxInFlight,
		IdleConnTimeout:     90 * time.Second,
		DisableCompression:  true,
	}
	return &http.Client{Transport: tr, Timeout: timeout}
}

// Quantiles summarises durations in milliseconds.
type Quantiles struct {
	N    int     `json:"n"`
	Mean float64 `json:"mean_ms"`
	P50  float64 `json:"p50_ms"`
	P90  float64 `json:"p90_ms"`
	P95  float64 `json:"p95_ms"`
	P99  float64 `json:"p99_ms"`
	P999 float64 `json:"p999_ms"`
	Max  float64 `json:"max_ms"`
}

// quantilesOf computes exact quantiles (nearest rank) of ds; ds is sorted in place.
func quantilesOf(ds []time.Duration) Quantiles {
	if len(ds) == 0 {
		return Quantiles{}
	}
	slices.Sort(ds)
	at := func(q float64) float64 {
		i := int(math.Ceil(q*float64(len(ds)))) - 1
		return ms(ds[min(max(i, 0), len(ds)-1)])
	}
	var sum time.Duration
	for _, d := range ds {
		sum += d
	}
	return Quantiles{
		N: len(ds), Mean: ms(sum / time.Duration(len(ds))),
		P50: at(0.50), P90: at(0.90), P95: at(0.95), P99: at(0.99), P999: at(0.999), Max: ms(ds[len(ds)-1]),
	}
}

func ms(d time.Duration) float64 { return math.Round(float64(d)/float64(time.Microsecond)) / 1000 }

// canonicalBody is an events.track@v1 request of about kb KiB (event.properties pads it).
func canonicalBody(kb int) []byte {
	props := map[string]string{}
	body := func() []byte {
		b, _ := json.Marshal(map[string]any{
			"user":  map[string]any{"id": "load-user-0001"},
			"event": map[string]any{"name": "load_test_event", "timestamp": "2026-11-07T10:00:00Z", "properties": props},
		})
		return b
	}
	for i := 0; len(body()) < kb*1024; i++ {
		props[fmt.Sprintf("p%03d", i)] = strings.Repeat("x", 48)
	}
	return body()
}

// vendorBody is what the request transformer (T1) sends for canonicalBody: the direct mode
// posts it to mockvendor so both paths move the same bytes to the vendor.
func vendorBody(kb int) []byte {
	var in struct {
		User  struct{ ID string } `json:"user"`
		Event struct {
			Name       string            `json:"name"`
			Properties map[string]string `json:"properties"`
		} `json:"event"`
	}
	_ = json.Unmarshal(canonicalBody(kb), &in)
	b, _ := json.Marshal(map[string]any{"d": []any{map[string]any{
		"identity": in.User.ID, "type": "event", "evtName": in.Event.Name, "ts": 1794045600, "evtData": in.Event.Properties,
	}}})
	return b
}
