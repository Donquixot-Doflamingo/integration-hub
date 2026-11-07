package transform

import (
	"errors"
	"strings"
	"testing"
)

// sessionResp is a response transformer that keeps the vendor's request id and the canonical
// user between the steps of a multi-step operation (HUB-175).
const sessionResp = respHead + `response:
  status: '"ok"'
  accepted: 1
  rejected: '[]'
session:
  vendor_txn_id: header(res, "X-Request-Id")
  user: req.user.id
  region: account.region
  missing: res.body.nothing
`

func compileSession(t *testing.T, src string) (*Program, error) {
	t.Helper()
	return Compile(specFromYAML(t, src), Options{})
}

func TestSessionSectionCompileErrors(t *testing.T) {
	tests := []struct {
		name, src, want string
	}{
		{"creds in session", respHead + "response:\n  status: '\"ok\"'\nsession:\n  key: creds.passcode\n", "session.key: creds.* is never allowed in session"},
		{"hdr in session", respHead + "response:\n  status: '\"ok\"'\nsession:\n  k: hdr.idempotency_key\n", "session.k: hdr is not available in session mapping"},
		{"session reads session", respHead + "response:\n  status: '\"ok\"'\nsession:\n  k: session.txn\n", "session.k: session is not available in session mapping"},
		{"not a mapping", respHead + "response:\n  status: '\"ok\"'\nsession: res.body\n", "session: must be a non-empty mapping of session fields"},
		{"empty mapping", respHead + "response:\n  status: '\"ok\"'\nsession: {}\n", "session: must be a non-empty mapping of session fields"},
		{"syntax", respHead + "response:\n  status: '\"ok\"'\nsession:\n  k: res.status ==\n", "session.k: syntax:"},
		{"in a request transformer", reqHead + "  body:\n    id: req.user.id\nsession:\n  k: req.user.id\n", "session: not allowed in a request transformer"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := compileSession(t, tt.src)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error %v\nwant %q", err, tt.want)
			}
		})
	}
}

func TestMapResponseSession(t *testing.T) {
	p, err := compileSession(t, sessionResp)
	if err != nil {
		t.Fatal(err)
	}
	if !p.HasSession() {
		t.Fatal("HasSession false")
	}
	in := Input{Req: map[string]any{"user": map[string]any{"id": "u_1"}}, Account: map[string]any{"region": "in"}}
	out, session, ce, err := p.MapResponseSession(vendor(200, map[string]any{}, "X-Request-Id", "vr-9"), in)
	if err != nil || ce != nil || out == nil {
		t.Fatalf("out %v ce %v err %v", out, ce, err)
	}
	want := map[string]any{"vendor_txn_id": "vr-9", "user": "u_1", "region": "in"}
	if d := Diff(want, session); len(d) > 0 {
		t.Fatalf("session %v: %v", session, d)
	}
	// MapResponse keeps its old shape and drops the session.
	if out2, ce2, err2 := p.MapResponse(vendor(200, map[string]any{}, "X-Request-Id", "vr-9"), in); err2 != nil || ce2 != nil || out2 == nil {
		t.Fatalf("MapResponse %v %v %v", out2, ce2, err2)
	}
	// An error response never opens a session.
	_, session, ce, err = p.MapResponseSession(vendor(503, nil, "X-Request-Id", "vr-9"), in)
	if err != nil || ce == nil || session != nil {
		t.Fatalf("error case: session %v ce %v err %v", session, ce, err)
	}
	// Everything nil: nothing to keep.
	_, session, _, err = p.MapResponseSession(vendor(200, map[string]any{}), Input{})
	if err != nil || session != nil {
		t.Fatalf("empty session %v err %v", session, err)
	}
	// A transformer without a session: section maps none.
	r1 := devResponse(t, "R1/2.yaml")
	if r1.HasSession() {
		t.Fatal("R1 has no session section")
	}
	_, session, _, _ = r1.MapResponseSession(vendor(200, map[string]any{"processed": 1.0}), in)
	if session != nil {
		t.Fatalf("R1 session %v", session)
	}
}

func TestSessionOutputBudget(t *testing.T) {
	p, err := compileSession(t, respHead+"response:\n  status: '\"ok\"'\n  accepted: 1\n  rejected: '[]'\nsession:\n  blob: res.text\n")
	if err != nil {
		t.Fatal(err)
	}
	_, _, _, err = p.MapResponseSession(vendor(200, strings.Repeat("x", MaxSessionBytes+1)), Input{})
	var ee *EvalError
	if !errors.As(err, &ee) || ee.Path != "session" || !strings.Contains(err.Error(), "session data is larger") {
		t.Fatalf("err %v", err)
	}
}

func TestGoldensWithSession(t *testing.T) {
	p, err := compileSession(t, sessionResp)
	if err != nil {
		t.Fatal(err)
	}
	vr := `{"status": 200, "headers": {"X-Request-Id": "vr-1"}, "body": {}, "_context": {"req": {"user": {"id": "u_1"}}, "account": {"region": "in"}}}`
	res := RunGoldens(p, fixtures(map[string]string{
		"ok.vendor_response.json":         vr,
		"ok.expected_response.json":       `{"response": {"status": "ok", "accepted": 1, "rejected": []}, "session": {"vendor_txn_id": "vr-1", "user": "u_1", "region": "in"}}`,
		"wrong.vendor_response.json":      vr,
		"wrong.expected_response.json":    `{"response": {"status": "ok", "accepted": 1, "rejected": []}, "session": {"vendor_txn_id": "vr-2", "user": "u_1", "region": "in"}}`,
		"unpinned.vendor_response.json":   vr,
		"unpinned.expected_response.json": `{"response": {"status": "ok", "accepted": 1, "rejected": []}}`,
		"err.vendor_response.json":        `{"status": 500, "headers": {"X-Request-Id": "vr-1"}, "body": null}`,
		"err.expected_response.json":      `{"error": {"code": "UPSTREAM_UNAVAILABLE", "retryable": true}}`,
	}))
	byCase := map[string]GoldenResult{}
	for _, r := range res {
		byCase[r.Case] = r
	}
	if !byCase["ok"].Passed() || !byCase["err"].Passed() {
		t.Fatalf("results %+v", res)
	}
	if d := strings.Join(byCase["wrong"].Diffs, "\n"); !strings.Contains(d, `/session/vendor_txn_id: expected "vr-2", got "vr-1"`) {
		t.Fatalf("wrong diffs %q", d)
	}
	if d := strings.Join(byCase["unpinned"].Diffs, "\n"); !strings.Contains(d, "/session: unexpected") {
		t.Fatalf("unpinned diffs %q", d)
	}
}
