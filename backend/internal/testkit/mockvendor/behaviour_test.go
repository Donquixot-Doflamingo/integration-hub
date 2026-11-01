package mockvendor

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"strings"
	"syscall"
	"testing"
	"time"
)

var okBody = json.RawMessage(`{"status":"success","processed":1,"unprocessed":[]}`)

func route(b Behaviour) RouteSpec {
	return RouteSpec{Match: RouteMatch{Method: "POST", Path: "/1/upload"}, Behaviour: b}
}

func upload(t *testing.T, mv *Instance) (int, http.Header, string, time.Duration) {
	t.Helper()
	start := time.Now()
	code, h, body := call(t, mv, http.MethodPost, "/1/upload", `{"d":[]}`, nil)
	return code, h, body, time.Since(start)
}

func TestLatency(t *testing.T) {
	tests := []struct {
		name     string
		lat      Latency
		min, max time.Duration
	}{
		{"fixed", Latency{Mode: "fixed", FixedMS: 80}, 80 * time.Millisecond, 400 * time.Millisecond},
		{"random", Latency{Mode: "random", MinMS: 50, MaxMS: 120}, 50 * time.Millisecond, 500 * time.Millisecond},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mv := Start(t)
			mv.SetRoute(t, "r", route(Behaviour{Status: 200, Body: okBody, Latency: &tt.lat}))
			for range 3 {
				code, _, _, took := upload(t, mv)
				if code != 200 || took < tt.min || took > tt.max {
					t.Fatalf("status %d after %s, want 200 within [%s, %s]", code, took, tt.min, tt.max)
				}
			}
		})
	}
}

func TestStatusCodesAndHeaders(t *testing.T) {
	for _, status := range []int{200, 201, 400, 401, 403, 404, 422, 500, 502, 503, 504} {
		mv := Start(t)
		mv.SetRoute(t, "r", route(Behaviour{Status: status, Headers: map[string]string{"X-Request-Id": "mock-123"}, Body: okBody}))
		code, h, body, _ := upload(t, mv)
		if code != status || h.Get("X-Request-Id") != "mock-123" || !json.Valid([]byte(body)) {
			t.Errorf("status %d: got %d, header %q, body %q", status, code, h.Get("X-Request-Id"), body)
		}
	}
}

func TestErrorRatioConverges(t *testing.T) {
	mv := Start(t)
	seed := int64(42)
	mv.SetRoute(t, "flaky", route(Behaviour{Status: 200, Body: okBody, ErrorRatio: &ErrorRatio{Ratio: 0.3, Status: 503, Body: json.RawMessage(`{"error":"unavailable"}`), Seed: &seed}}))
	const n = 1000
	errs := 0
	for range n {
		code, _, _, _ := upload(t, mv)
		switch code {
		case 503:
			errs++
		case 200:
		default:
			t.Fatalf("unexpected status %d", code)
		}
	}
	if got := float64(errs) / n; math.Abs(got-0.3) > 0.05 {
		t.Fatalf("error ratio %.3f, want 0.30 ± 0.05", got)
	}
	recs := mv.Requests(t, nil)
	errored := 0
	for _, r := range recs {
		if r.Outcome == "errored" {
			errored++
		}
	}
	if errored != errs {
		t.Fatalf("recorded %d errored outcomes, observed %d", errored, errs)
	}
}

func TestErrorRatioSeedIsDeterministic(t *testing.T) {
	seq := func() []int {
		mv := Start(t)
		seed := int64(7)
		mv.SetRoute(t, "flaky", route(Behaviour{Status: 200, ErrorRatio: &ErrorRatio{Ratio: 0.5, Status: 500, Seed: &seed}}))
		var out []int
		for range 20 {
			code, _, _, _ := upload(t, mv)
			out = append(out, code)
		}
		return out
	}
	a, b := seq(), seq()
	for i := range a {
		if a[i] != b[i] {
			t.Fatalf("same seed gave different sequences:\n%v\n%v", a, b)
		}
	}
}

func TestRateLimit(t *testing.T) {
	t.Run("every 2nd with Retry-After rounded up", func(t *testing.T) {
		mv := Start(t)
		mv.SetRoute(t, "rl", route(Behaviour{Status: 200, RateLimit: &RateLimit{EveryNth: 2, RetryAfterMS: 1500}}))
		var codes []int
		var retryAfter string
		for range 4 {
			code, h, _, _ := upload(t, mv)
			codes = append(codes, code)
			if code == 429 {
				retryAfter = h.Get("Retry-After")
			}
		}
		if codes[0] != 200 || codes[1] != 429 || codes[2] != 200 || codes[3] != 429 {
			t.Fatalf("codes %v, want [200 429 200 429]", codes)
		}
		if retryAfter != "2" {
			t.Fatalf("Retry-After %q, want 2 (1500 ms rounded up)", retryAfter)
		}
	})
	t.Run("burst then every request", func(t *testing.T) {
		mv := Start(t)
		mv.SetRoute(t, "rl", route(Behaviour{Status: 200, RateLimit: &RateLimit{EveryNth: 1, Burst: 3, RetryAfterMS: 0}}))
		var codes []int
		for range 5 {
			code, _, _, _ := upload(t, mv)
			codes = append(codes, code)
		}
		if codes[2] != 200 || codes[3] != 429 || codes[4] != 429 {
			t.Fatalf("codes %v, want 3 × 200 then 429s", codes)
		}
		if got := mv.Requests(t, nil)[4].Outcome; got != "rate_limited" {
			t.Fatalf("outcome %q", got)
		}
	})
}

func TestMalformed(t *testing.T) {
	body := json.RawMessage(`{"status":"success","processed":1}`)
	tests := []struct {
		kind      string
		wantCT    string
		checkBody func(string) bool
	}{
		{"truncated_json", "application/json", func(b string) bool { return b != "" && !json.Valid([]byte(b)) && strings.HasPrefix(string(body), b) }},
		{"invalid_json", "application/json", func(b string) bool { return b != "" && !json.Valid([]byte(b)) }},
		{"wrong_content_type", "text/plain; charset=utf-8", func(b string) bool { return json.Valid([]byte(b)) }},
		{"empty_body", "application/json", func(b string) bool { return b == "" }},
		{"html_error_page", "text/html; charset=utf-8", func(b string) bool { return strings.Contains(b, "<html>") }},
	}
	for _, tt := range tests {
		t.Run(tt.kind, func(t *testing.T) {
			mv := Start(t)
			mv.SetRoute(t, "m", route(Behaviour{Status: 200, Body: body, Headers: map[string]string{"Content-Type": "application/json"}, Malformed: &Malformed{Kind: tt.kind}}))
			code, h, got, _ := upload(t, mv)
			if code != 200 || h.Get("Content-Type") != tt.wantCT || !tt.checkBody(got) {
				t.Fatalf("status %d, content-type %q, body %q", code, h.Get("Content-Type"), got)
			}
		})
	}
}

func TestSlowDrip(t *testing.T) {
	mv := Start(t)
	payload := strings.Repeat("x", 64)
	mv.SetRoute(t, "drip", route(Behaviour{Status: 200, BodyRaw: &payload, SlowDrip: &SlowDrip{ChunkBytes: 16, IntervalMS: 60}}))

	req, _ := http.NewRequestWithContext(t.Context(), http.MethodPost, mv.VendorURL+"/1/upload", nil)
	start := time.Now()
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	headersAfter := time.Since(start)
	r := bufio.NewReader(resp.Body)
	first := make([]byte, 16)
	if _, err := io.ReadFull(r, first); err != nil {
		t.Fatal(err)
	}
	firstAfter := time.Since(start)
	rest, _ := io.ReadAll(r)
	total := time.Since(start)
	if headersAfter > 50*time.Millisecond || firstAfter > 50*time.Millisecond {
		t.Errorf("headers after %s, first chunk after %s, want both immediately", headersAfter, firstAfter)
	}
	if total < 180*time.Millisecond { // 4 chunks → 3 intervals of 60 ms
		t.Errorf("whole body in %s, want >= 180ms", total)
	}
	if string(first)+string(rest) != payload {
		t.Errorf("body corrupted: %q", string(first)+string(rest))
	}
}

func TestDownReset(t *testing.T) {
	mv := Start(t)
	mv.SetRoute(t, "down", RouteSpec{Match: RouteMatch{Method: "*", Path: "/**"}, Behaviour: Behaviour{Down: &Down{Mode: "reset"}}})
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodPost, mv.VendorURL+"/1/upload", strings.NewReader("{}"))
	resp, err := http.DefaultClient.Do(req)
	if err == nil {
		_ = resp.Body.Close()
		t.Fatalf("got HTTP %d, want a transport error", resp.StatusCode)
	}
	// The client sees the reset before the server finishes recording it: give it a moment.
	var got []RecordedRequest
	for deadline := time.Now().Add(time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if got = mv.Requests(t, nil); len(got) > 0 && got[0].Outcome == "reset" {
			break
		}
	}
	if len(got) == 0 || got[0].Outcome != "reset" || got[0].ResponseStatus != nil {
		t.Fatalf("recording %+v", got)
	}
}

func TestDownRefuseIsConnectionRefusedAndRecovers(t *testing.T) {
	mv := Start(t)
	mv.SetRoute(t, "down", RouteSpec{Match: RouteMatch{Method: "*", Path: "/**"}, Behaviour: Behaviour{Down: &Down{Mode: "refuse"}}})

	req, _ := http.NewRequestWithContext(t.Context(), http.MethodPost, mv.VendorURL+"/1/upload", strings.NewReader("{}"))
	resp, err := (&http.Client{Transport: &http.Transport{}}).Do(req) // fresh transport: no pooled connection
	if err == nil {
		_ = resp.Body.Close()
		t.Fatalf("got HTTP %d, want connection refused", resp.StatusCode)
	}
	if !errors.Is(err, syscall.ECONNREFUSED) {
		t.Fatalf("error %v, want ECONNREFUSED", err)
	}

	// The dedicated control listener is still up: remove the route and the vendor port reopens.
	mv.DeleteRoute(t, "down")
	if code, _, _, _ := upload(t, mv); code != 200 {
		t.Fatalf("after recovery: %d", code)
	}
}

func TestDownHangHoldsUntilClientGivesUp(t *testing.T) {
	mv := Start(t)
	mv.SetRoute(t, "hang", route(Behaviour{Down: &Down{Mode: "hang"}}))
	ctx, cancel := context.WithTimeout(t.Context(), 150*time.Millisecond)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, mv.VendorURL+"/1/upload", strings.NewReader("{}"))
	start := time.Now()
	resp, err := http.DefaultClient.Do(req)
	if err == nil {
		_ = resp.Body.Close()
		t.Fatalf("got HTTP %d, want a timeout", resp.StatusCode)
	}
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) < 150*time.Millisecond {
		t.Fatalf("err %v after %s", err, time.Since(start))
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		if recs := mv.Requests(t, nil); len(recs) == 1 && recs[0].Outcome == "hung" {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("outcome never became hung: %+v", mv.Requests(t, nil))
		}
		timer := time.NewTimer(10 * time.Millisecond)
		<-timer.C
	}
}

func TestEvaluationOrderDownBeatsEverything(t *testing.T) {
	mv := Start(t)
	mv.SetRoute(t, "r", route(Behaviour{Status: 200, Down: &Down{Mode: "reset"}, RateLimit: &RateLimit{EveryNth: 1, RetryAfterMS: 1000}, ErrorRatio: &ErrorRatio{Ratio: 1, Status: 500}}))
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodPost, mv.VendorURL+"/1/upload", nil)
	if resp, err := http.DefaultClient.Do(req); err == nil {
		_ = resp.Body.Close()
		t.Fatalf("down must win, got HTTP %d", resp.StatusCode)
	}
	mv.SetRoute(t, "r", route(Behaviour{Status: 200, RateLimit: &RateLimit{EveryNth: 1, RetryAfterMS: 1000}, ErrorRatio: &ErrorRatio{Ratio: 1, Status: 500}}))
	if code, _, _, _ := upload(t, mv); code != 429 {
		t.Fatalf("rate_limit must win over error_ratio, got %d", code)
	}
}
