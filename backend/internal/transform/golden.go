package transform

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/core"
)

// Golden fixtures (DESIGN §7.6): next to each transformer, in fixtures/,
//
//	<case>.input.json            request: canonical body, plus optional "_context"
//	<case>.expected_request.json request: {method, path, query, headers, body}
//	<case>.vendor_response.json  response: {status, headers, body}, plus optional "_context"
//	<case>.expected_response.json response: {"response": ..., "session": {...}} or {"error": {code, retryable, retry_after_ms, message}}
//
// "_context" may set hdr, account, creds, session, req (the canonical request, for a response
// transformer's session: section), now (RFC 3339) and uuids (list). Fixture creds must be
// obviously fake ("fixture-..."). now defaults to GoldenNow. "session" is the vendor session data
// a response transformer maps on success; a transformer with a session: section must pin it in
// every success case (an unpinned session is reported as unexpected).

// GoldenNow is the default pinned clock of golden runs.
var GoldenNow = time.Date(2026, 10, 31, 9, 15, 0, 0, time.UTC)

// GoldenResult is the outcome of one fixture case.
type GoldenResult struct {
	Transformer string
	Case        string
	Diffs       []string // "/body/d/0/ts: expected 1, got 2"
	Err         string   // a transformer or fixture error; Detail() included (fixture data)
}

// Passed reports whether the case matched.
func (r GoldenResult) Passed() bool { return len(r.Diffs) == 0 && r.Err == "" }

type goldenContext struct {
	Hdr     map[string]any    `json:"hdr"`
	Account map[string]any    `json:"account"`
	Creds   map[string]string `json:"creds"`
	Session map[string]any    `json:"session"`
	Req     map[string]any    `json:"req"`
	Now     string            `json:"now"`
	UUIDs   []string          `json:"uuids"`
}

func (gc goldenContext) input(req map[string]any) (Input, error) {
	in := Input{Req: req, Hdr: gc.Hdr, Account: gc.Account, Creds: gc.Creds, Session: gc.Session, Now: GoldenNow}
	if gc.Now != "" {
		t, err := time.Parse(time.RFC3339Nano, gc.Now)
		if err != nil {
			return Input{}, fmt.Errorf("_context.now: %w", err)
		}
		in.Now = t
	}
	n := 0
	in.UUID = func() string {
		if n < len(gc.UUIDs) {
			n++
			return gc.UUIDs[n-1]
		}
		n++
		return fmt.Sprintf("00000000-0000-4000-8000-%012d", n)
	}
	return in, nil
}

// goldenTimeBudgetRetries is how many times RunGoldens re-runs a case that failed only on the
// time budget (HUB-238). goldenTimeBudgetHook, when set (tests), is called on every overrun.
var (
	goldenTimeBudgetRetries = 1
	goldenTimeBudgetHook    func()
)

// RunGoldens runs every fixture case of p from fixtures (the transformer's fixtures/ directory).
// A transformer with no cases fails: every transformer ships with goldens (AGENTS §3.4).
func RunGoldens(p *Program, fixtures fs.FS) []GoldenResult {
	suffix := ".input.json"
	if p.kind == "response" {
		suffix = ".vendor_response.json"
	}
	entries, err := fs.ReadDir(fixtures, ".")
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return []GoldenResult{{Transformer: p.ref, Err: "read fixtures: " + err.Error()}}
	}
	var cases []string
	for _, e := range entries {
		if name, ok := strings.CutSuffix(e.Name(), suffix); ok && !e.IsDir() {
			cases = append(cases, name)
		}
	}
	slices.Sort(cases)
	if len(cases) == 0 {
		return []GoldenResult{{Transformer: p.ref, Err: "no golden fixtures (fixtures/<case>" + suffix + ")"}}
	}
	out := make([]GoldenResult, 0, len(cases))
	for _, c := range cases {
		r := GoldenResult{Transformer: p.ref, Case: c}
		run := func() ([]string, error) {
			if p.kind == "request" {
				return p.goldenRequest(fixtures, c)
			}
			return p.goldenResponse(fixtures, c)
		}
		var err error
		// The time budget is wall-clock, so a GC pause or a loaded machine (CI under -race) can
		// overrun it once. Re-run a case that failed on time alone, as the request path does
		// (ErrTimeBudget); a transformer that is really too slow overruns again and still fails.
		for attempt := 0; ; attempt++ {
			r.Diffs, err = run()
			if !errors.Is(err, ErrTimeBudget) {
				break
			}
			if goldenTimeBudgetHook != nil {
				goldenTimeBudgetHook()
			}
			if attempt == goldenTimeBudgetRetries {
				break
			}
		}
		if err != nil {
			r.Err = err.Error()
			var ee *EvalError
			if errors.As(err, &ee) && ee.Detail() != "" {
				r.Err += " (" + ee.Detail() + ")"
			}
		}
		out = append(out, r)
	}
	return out
}

func readJSON(fsys fs.FS, name string) (map[string]any, error) {
	b, err := fs.ReadFile(fsys, name)
	if err != nil {
		return nil, err
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	return m, nil
}

// splitContext removes "_context" from a fixture document.
func splitContext(doc map[string]any) (goldenContext, error) {
	var gc goldenContext
	raw, ok := doc["_context"]
	if !ok {
		return gc, nil
	}
	delete(doc, "_context")
	b, _ := json.Marshal(raw)
	if err := json.Unmarshal(b, &gc); err != nil {
		return gc, fmt.Errorf("_context: %w", err)
	}
	return gc, nil
}

func (p *Program) goldenRequest(fixtures fs.FS, c string) ([]string, error) {
	input, err := readJSON(fixtures, c+".input.json")
	if err != nil {
		return nil, err
	}
	want, err := readJSON(fixtures, c+".expected_request.json")
	if err != nil {
		return nil, err
	}
	gc, err := splitContext(input)
	if err != nil {
		return nil, err
	}
	in, err := gc.input(input)
	if err != nil {
		return nil, err
	}
	req, err := p.BuildRequest(in)
	if err != nil {
		return nil, err
	}
	got := map[string]any{"method": req.Method, "path": req.Path}
	if len(req.Query) > 0 {
		got["query"] = stringMap(req.Query)
	}
	if len(req.Headers) > 0 {
		got["headers"] = stringMap(req.Headers)
	}
	if req.Body != nil {
		got["body"] = req.Body
	}
	return Diff(want, got), nil
}

func (p *Program) goldenResponse(fixtures fs.FS, c string) ([]string, error) {
	vr, err := readJSON(fixtures, c+".vendor_response.json")
	if err != nil {
		return nil, err
	}
	want, err := readJSON(fixtures, c+".expected_response.json")
	if err != nil {
		return nil, err
	}
	gc, err := splitContext(vr)
	if err != nil {
		return nil, err
	}
	in, err := gc.input(gc.Req)
	if err != nil {
		return nil, err
	}
	resp := core.VendorResponse{Headers: map[string][]string{}, Body: vr["body"]}
	status, ok := vr["status"].(float64)
	if !ok {
		return nil, errors.New(c + ".vendor_response.json: status is required")
	}
	resp.Status = int(status)
	if hs, ok := vr["headers"].(map[string]any); ok {
		for k, v := range hs {
			resp.Headers[k] = []string{fmt.Sprint(v)}
		}
	}
	out, session, ce, err := p.MapResponseSession(resp, in)
	if err != nil {
		return nil, err
	}
	got := map[string]any{}
	if ce != nil {
		e := map[string]any{"code": string(ce.Code), "retryable": ce.Retryable}
		if ce.RetryAfterMS != nil {
			e["retry_after_ms"] = *ce.RetryAfterMS
		}
		// Only compare the message when the fixture pins one.
		if we, ok := want["error"].(map[string]any); ok {
			if _, pinned := we["message"]; pinned {
				e["message"] = ce.Message
			}
		}
		got["error"] = e
	} else {
		got["response"] = out
		if session != nil {
			got["session"] = session
		}
	}
	return Diff(want, got), nil
}

func stringMap(m map[string]string) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// Diff compares two JSON trees and lists differences as "pointer: expected X, got Y". Numbers
// compare by value (1 == 1.0).
func Diff(want, got any) []string {
	var diffs []string
	diffAt("", want, got, &diffs)
	return diffs
}

func diffAt(ptr string, want, got any, diffs *[]string) {
	at := ptr
	if at == "" {
		at = "/"
	}
	wm, wIsMap := want.(map[string]any)
	gm, gIsMap := got.(map[string]any)
	if wIsMap && gIsMap {
		keys := make([]string, 0, len(wm)+len(gm))
		for k := range wm {
			keys = append(keys, k)
		}
		for k := range gm {
			if _, ok := wm[k]; !ok {
				keys = append(keys, k)
			}
		}
		slices.Sort(keys)
		for _, k := range keys {
			wv, wok := wm[k]
			gv, gok := gm[k]
			child := ptr + "/" + strings.NewReplacer("~", "~0", "/", "~1").Replace(k)
			switch {
			case !gok:
				*diffs = append(*diffs, child+": expected "+show(wv)+", missing")
			case !wok:
				*diffs = append(*diffs, child+": unexpected "+show(gv))
			default:
				diffAt(child, wv, gv, diffs)
			}
		}
		return
	}
	wl, wIsList := asList(want)
	gl, gIsList := asList(got)
	if wIsList && gIsList {
		if len(wl) != len(gl) {
			*diffs = append(*diffs, fmt.Sprintf("%s: expected %d items, got %d", at, len(wl), len(gl)))
			return
		}
		for i := range wl {
			diffAt(ptr+"/"+strconv.Itoa(i), wl[i], gl[i], diffs)
		}
		return
	}
	if !scalarEqual(want, got) {
		*diffs = append(*diffs, at+": expected "+show(want)+", got "+show(got))
	}
}

func asList(v any) ([]any, bool) {
	switch x := v.(type) {
	case []any:
		return x, true
	case []string:
		out := make([]any, len(x))
		for i, s := range x {
			out[i] = s
		}
		return out, true
	}
	return nil, false
}

func scalarEqual(a, b any) bool {
	af, aNum := number(a)
	bf, bNum := number(b)
	if aNum || bNum {
		return aNum && bNum && af == bf
	}
	return a == b
}

func number(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case int:
		return float64(x), true
	case int64:
		return float64(x), true
	}
	return 0, false
}

func show(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprint(v)
	}
	if len(b) > 120 {
		return string(b[:117]) + "..."
	}
	return string(b)
}
