package transform

import (
	"strings"
	"testing"
	"testing/fstest"
	"time"
)

func fixtures(files map[string]string) fstest.MapFS {
	m := fstest.MapFS{}
	for k, v := range files {
		m[k] = &fstest.MapFile{Data: []byte(v)}
	}
	return m
}

const t1InputJSON = `{"user": {"id": "u_1"}, "event": {"name": "order_placed", "timestamp": "2026-10-31T09:15:00Z", "properties": {"amount": 499}},
 "_context": {"creds": {"account_id": "fixture-account-id", "passcode": "fixture-passcode"}}}`

const t1ExpectedJSON = `{"method": "POST", "path": "/1/upload",
 "headers": {"X-CleverTap-Account-Id": "fixture-account-id", "X-CleverTap-Passcode": "fixture-passcode", "Content-Type": "application/json; charset=utf-8"},
 "body": {"d": [{"identity": "u_1", "type": "event", "evtName": "order_placed", "ts": 1793438100, "evtData": {"amount": 499}}]}}`

func TestGoldensPassAndDiff(t *testing.T) {
	p := compileDev(t, "T1/7.yaml")
	wrong := strings.Replace(strings.Replace(t1ExpectedJSON, `"ts": 1793438100`, `"ts": 1`, 1), `"type": "event", `, ``, 1)
	res := RunGoldens(p, fixtures(map[string]string{
		"ok.input.json": t1InputJSON, "ok.expected_request.json": t1ExpectedJSON,
		"bad.input.json": t1InputJSON, "bad.expected_request.json": wrong,
	}))
	if len(res) != 2 || res[0].Case != "bad" || res[1].Case != "ok" {
		t.Fatalf("results %+v", res)
	}
	if !res[1].Passed() {
		t.Fatalf("ok case failed: %+v", res[1])
	}
	got := strings.Join(res[0].Diffs, "\n")
	for _, want := range []string{"/body/d/0/ts: expected 1, got 1793438100", `/body/d/0/type: unexpected "event"`} {
		if !strings.Contains(got, want) {
			t.Errorf("diffs missing %q:\n%s", want, got)
		}
	}
}

func TestGoldensResponseCases(t *testing.T) {
	p := devResponse(t, "R1/2.yaml")
	res := RunGoldens(p, fixtures(map[string]string{
		"ok.vendor_response.json":      `{"status": 200, "headers": {"X-Request-Id": "vr-1"}, "body": {"status": "success", "processed": 1, "unprocessed": []}}`,
		"ok.expected_response.json":    `{"response": {"status": "ok", "accepted": 1, "rejected": [], "vendor_ref": "vr-1", "raw": {"status": "success", "processed": 1, "unprocessed": []}}}`,
		"rl.vendor_response.json":      `{"status": 429, "headers": {"Retry-After": "3"}, "body": null}`,
		"rl.expected_response.json":    `{"error": {"code": "UPSTREAM_RATE_LIMITED", "retryable": true, "retry_after_ms": 3000}}`,
		"wrong.vendor_response.json":   `{"status": 500, "body": null}`,
		"wrong.expected_response.json": `{"error": {"code": "UPSTREAM_REJECTED", "retryable": false}}`,
	}))
	if len(res) != 3 || !res[0].Passed() || !res[1].Passed() || res[2].Passed() {
		t.Fatalf("results %+v", res)
	}
	if !strings.Contains(strings.Join(res[2].Diffs, "\n"), `/error/code: expected "UPSTREAM_REJECTED", got "UPSTREAM_UNAVAILABLE"`) {
		t.Fatalf("diffs %v", res[2].Diffs)
	}
}

func TestGoldensRequireFixtures(t *testing.T) {
	res := RunGoldens(compileDev(t, "T1/7.yaml"), fixtures(nil))
	if len(res) != 1 || res[0].Passed() || !strings.Contains(res[0].Err, "no golden fixtures") {
		t.Fatalf("results %+v", res)
	}
}

func TestGoldensReportTransformerErrorsWithDetail(t *testing.T) {
	p := compileResponse(t, respHead+"response:\n  status: '\"ok\"'\n  accepted: int(res.body.processed)\n")
	res := RunGoldens(p, fixtures(map[string]string{
		"x.vendor_response.json": `{"status": 200, "body": {"processed": "lots"}}`, "x.expected_response.json": `{"response": {"status": "ok"}}`,
	}))
	if res[0].Passed() || !strings.Contains(res[0].Err, "response.accepted") || !strings.Contains(res[0].Err, "lots") {
		t.Fatalf("err %q", res[0].Err)
	}
}

func TestDiffNumbersCompareByValue(t *testing.T) {
	if d := Diff(map[string]any{"a": 1.0, "b": []any{1.0}}, map[string]any{"a": 1, "b": []any{int64(1)}}); len(d) != 0 {
		t.Fatalf("diffs %v", d)
	}
	if d := Diff([]any{1.0, 2.0}, []any{1.0}); len(d) != 1 || d[0] != "/: expected 2 items, got 1" {
		t.Fatalf("diffs %v", d)
	}
}

// TestGoldensRetryTimeBudgetOnce (HUB-238): a case that overruns the wall-clock budget once
// (GC pause, loaded CI machine) passes on its re-run; a transformer that is really too slow
// overruns twice and still fails, so publishing a slow transformer stays impossible.
func TestGoldensRetryTimeBudgetOnce(t *testing.T) {
	files := map[string]string{"ok.input.json": t1InputJSON, "ok.expected_request.json": t1ExpectedJSON}
	tests := []struct {
		name     string
		recover  bool // the overrun is transient: the budget is restored after the first one
		overruns int
		wantPass bool
	}{
		{name: "transient overrun passes on the re-run", recover: true, overruns: 1, wantPass: true},
		{name: "persistent overrun still fails", recover: false, overruns: 2, wantPass: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := compileDev(t, "T1/7.yaml")
			p.limits.TimeBudget = time.Nanosecond
			overruns := 0
			goldenTimeBudgetHook = func() {
				overruns++
				if tt.recover {
					p.limits.TimeBudget = time.Second
				}
			}
			t.Cleanup(func() { goldenTimeBudgetHook = nil })
			res := RunGoldens(p, fixtures(files))
			if len(res) != 1 || res[0].Passed() != tt.wantPass {
				t.Fatalf("result %+v, want passed=%v", res, tt.wantPass)
			}
			if overruns != tt.overruns {
				t.Fatalf("overruns = %d, want %d (one re-run, never more)", overruns, tt.overruns)
			}
			if !tt.wantPass && !strings.Contains(res[0].Err, "time budget exceeded") {
				t.Fatalf("err %q", res[0].Err)
			}
		})
	}
}
