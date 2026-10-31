package mockvendor

import (
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"go.uber.org/goleak"
)

func TestMain(m *testing.M) { goleak.VerifyTestMain(m) }

// call sends a request to the vendor surface and returns status, headers and body.
func call(t *testing.T, i *Instance, method, path, body string, hdr map[string]string) (int, http.Header, string) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), method, i.VendorURL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, resp.Header, string(b)
}

func intPtr(n int) *int { return &n }

func TestMatchPath(t *testing.T) {
	tests := []struct {
		glob, path string
		want       bool
	}{
		{"/1/upload", "/1/upload", true},
		{"/1/upload", "/1/upload/x", false},
		{"/1/*", "/1/upload", true},
		{"/1/*", "/1/a/b", false},
		{"/**", "/", true},
		{"/**", "/a/b/c", true},
		{"/v1/**/status", "/v1/status", true},
		{"/v1/**/status", "/v1/a/b/status", true},
		{"/v1/**/status", "/v1/a/b/state", false},
		{"/", "/", true},
		{"/a", "/b", false},
	}
	for _, tt := range tests {
		if got := matchPath(tt.glob, tt.path); got != tt.want {
			t.Errorf("matchPath(%q, %q) = %v, want %v", tt.glob, tt.path, got, tt.want)
		}
	}
}

func TestDefaultBehaviourAndOverride(t *testing.T) {
	mv := Start(t)
	if code, _, body := call(t, mv, http.MethodPost, "/anything", "{}", nil); code != 200 || strings.TrimSpace(body) != "{}" {
		t.Fatalf("default = %d %q, want 200 {}", code, body)
	}
	var b Behaviour
	if code := mv.Control(t, http.MethodPut, "/__control/default", Behaviour{Status: 404, Body: json.RawMessage(`{"error":"no such route"}`)}, &b); code != 200 {
		t.Fatalf("set default: %d", code)
	}
	if code, _, _ := call(t, mv, http.MethodGet, "/x", "", nil); code != 404 {
		t.Fatalf("after override = %d, want 404", code)
	}
	mv.Reset(t)
	if code, _, _ := call(t, mv, http.MethodGet, "/x", "", nil); code != 200 {
		t.Fatalf("after reset = %d, want default 200 restored", code)
	}
}

func TestRouteSelection(t *testing.T) {
	mv := Start(t)
	mv.SetRoute(t, "catch-all", RouteSpec{Match: RouteMatch{Method: "*", Path: "/**"}, Behaviour: Behaviour{Status: 201}})
	mv.SetRoute(t, "upload", RouteSpec{Match: RouteMatch{Method: "POST", Path: "/1/upload"}, Behaviour: Behaviour{Status: 202}})
	mv.SetRoute(t, "upload-hi", RouteSpec{Match: RouteMatch{Method: "POST", Path: "/1/*"}, Priority: 5, Behaviour: Behaviour{Status: 203}})
	mv.SetRoute(t, "header", RouteSpec{Match: RouteMatch{Method: "GET", Path: "/h", Headers: map[string]string{"X-Mode": "a"}}, Priority: 9, Behaviour: Behaviour{Status: 204}})

	tests := []struct {
		name, method, path string
		hdr                map[string]string
		want               int
	}{
		{"higher priority beats exact path", "POST", "/1/upload", nil, 203},
		{"method mismatch falls through", "GET", "/1/upload", nil, 201},
		{"header match", "GET", "/h", map[string]string{"X-Mode": "a"}, 204},
		{"header mismatch", "GET", "/h", map[string]string{"X-Mode": "b"}, 201},
	}
	for _, tt := range tests {
		if code, _, _ := call(t, mv, tt.method, tt.path, "", tt.hdr); code != tt.want {
			t.Errorf("%s: status %d, want %d", tt.name, code, tt.want)
		}
	}

	// Tie on priority: the most recently set route wins.
	mv.SetRoute(t, "tie-a", RouteSpec{Match: RouteMatch{Method: "GET", Path: "/tie"}, Priority: 1, Behaviour: Behaviour{Status: 210}})
	mv.SetRoute(t, "tie-b", RouteSpec{Match: RouteMatch{Method: "GET", Path: "/tie"}, Priority: 1, Behaviour: Behaviour{Status: 211}})
	if code, _, _ := call(t, mv, "GET", "/tie", "", nil); code != 211 {
		t.Errorf("tie: %d, want most recent 211", code)
	}
}

func TestTimesDeactivatesRoute(t *testing.T) {
	mv := Start(t)
	mv.SetRoute(t, "twice", RouteSpec{Match: RouteMatch{Method: "*", Path: "/x"}, Times: intPtr(2), Behaviour: Behaviour{Status: 500}})
	var got []int
	for range 3 {
		code, _, _ := call(t, mv, "GET", "/x", "", nil)
		got = append(got, code)
	}
	if got[0] != 500 || got[1] != 500 || got[2] != 200 {
		t.Fatalf("statuses %v, want [500 500 200]", got)
	}
	var rt Route
	mv.Control(t, http.MethodGet, "/__control/routes/twice", nil, &rt)
	if rt.Hits != 2 || rt.Remaining == nil || *rt.Remaining != 0 {
		t.Fatalf("route counters hits=%d remaining=%v", rt.Hits, rt.Remaining)
	}
}

func TestRouteCRUDAndValidation(t *testing.T) {
	mv := Start(t)
	rt := mv.SetRoute(t, "ct-upload", RouteSpec{Match: RouteMatch{Method: "POST", Path: "/1/upload"}, Behaviour: Behaviour{Status: 200}})
	if rt.RouteID != "ct-upload" || rt.Hits != 0 || rt.CreatedAt.IsZero() || rt.Remaining != nil {
		t.Fatalf("stored route %+v", rt)
	}
	var list struct{ Items []Route }
	mv.Control(t, http.MethodGet, "/__control/routes", nil, &list)
	if len(list.Items) != 1 {
		t.Fatalf("list %d routes", len(list.Items))
	}
	mv.DeleteRoute(t, "ct-upload")
	if code := mv.Control(t, http.MethodGet, "/__control/routes/ct-upload", nil, nil); code != 404 {
		t.Fatalf("get deleted route: %d", code)
	}
	if code := mv.Control(t, http.MethodDelete, "/__control/routes/ct-upload", nil, nil); code != 404 {
		t.Fatalf("delete missing route: %d", code)
	}

	bad := []struct {
		name    string
		spec    any
		pointer string
	}{
		{"bad method", RouteSpec{Match: RouteMatch{Method: "FETCH", Path: "/"}}, "/match/method"},
		{"relative path", RouteSpec{Match: RouteMatch{Method: "GET", Path: "x"}}, "/match/path"},
		{"latency min > max", RouteSpec{Match: RouteMatch{Method: "GET", Path: "/"}, Behaviour: Behaviour{Latency: &Latency{Mode: "random", MinMS: 9, MaxMS: 1}}}, "/behaviour/latency/min_ms"},
		{"error ratio status", RouteSpec{Match: RouteMatch{Method: "GET", Path: "/"}, Behaviour: Behaviour{ErrorRatio: &ErrorRatio{Ratio: 0.5, Status: 200}}}, "/behaviour/error_ratio/status"},
		{"rate limit both modes", RouteSpec{Match: RouteMatch{Method: "GET", Path: "/"}, Behaviour: Behaviour{RateLimit: &RateLimit{EveryNth: 2, Ratio: 0.5}}}, "/behaviour/rate_limit"},
		{"times zero", RouteSpec{Match: RouteMatch{Method: "GET", Path: "/"}, Times: intPtr(0)}, "/times"},
	}
	for _, tt := range bad {
		t.Run(tt.name, func(t *testing.T) {
			req, _ := http.NewRequestWithContext(t.Context(), http.MethodPut, mv.ControlURL+"/__control/routes/r1", strings.NewReader(mustJSON(t, tt.spec)))
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = resp.Body.Close() }()
			var env errorEnvelope
			_ = json.NewDecoder(resp.Body).Decode(&env)
			if resp.StatusCode != 400 || env.Error.Code != "VALIDATION_FAILED" {
				t.Fatalf("status %d code %q", resp.StatusCode, env.Error.Code)
			}
			found := false
			for _, d := range env.Error.Details {
				found = found || d.Pointer == tt.pointer
			}
			if !found {
				t.Fatalf("details %+v lack pointer %s", env.Error.Details, tt.pointer)
			}
		})
	}
	if code := mv.Control(t, http.MethodPut, "/__control/routes/bad%20id", RouteSpec{Match: RouteMatch{Method: "GET", Path: "/"}}, nil); code != 400 {
		t.Fatalf("invalid route id: %d", code)
	}
	if code := mv.Control(t, http.MethodGet, "/__control/nope", nil, nil); code != 404 {
		t.Fatalf("unknown control path: %d", code)
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestRecordingsAreKeptAndFiltered(t *testing.T) {
	mv := Start(t)
	mv.SetRoute(t, "ct-upload", RouteSpec{Match: RouteMatch{Method: "POST", Path: "/1/upload"}, Behaviour: Behaviour{Status: 200}})
	call(t, mv, "POST", "/1/upload?dry=1", `{"d":[{"identity":"u_1"}]}`, map[string]string{"X-CleverTap-Account-Id": "TEST-ACCOUNT"})
	call(t, mv, "POST", "/other", "not json", nil)
	call(t, mv, "GET", "/1/upload", "", nil)

	all := mv.Requests(t, nil)
	if len(all) != 3 {
		t.Fatalf("recorded %d, want 3", len(all))
	}
	first := all[0]
	if first.Method != "POST" || first.Path != "/1/upload" || first.RouteID == nil || *first.RouteID != "ct-upload" ||
		first.Query["dry"][0] != "1" || first.Headers["X-Clevertap-Account-Id"][0] != "TEST-ACCOUNT" ||
		!strings.Contains(string(first.Body), "u_1") || first.ResponseStatus == nil || *first.ResponseStatus != 200 ||
		first.Outcome != "responded" || !strings.HasPrefix(first.ID, "rec_") {
		t.Fatalf("first recording %+v", first)
	}
	if all[1].BodyRaw == nil || *all[1].BodyRaw != "not json" || string(all[1].Body) != "null" {
		t.Fatalf("non-JSON body not kept raw: %+v", all[1])
	}
	if got := mv.Requests(t, url.Values{"route_id": {"ct-upload"}}); len(got) != 1 {
		t.Fatalf("route_id filter: %d", len(got))
	}
	if got := mv.Requests(t, url.Values{"method": {"GET"}}); len(got) != 1 {
		t.Fatalf("method filter: %d", len(got))
	}
	if got := mv.Requests(t, url.Values{"path": {"/1/*"}}); len(got) != 2 {
		t.Fatalf("path filter: %d", len(got))
	}

	// Pagination: limit 2 gives a cursor to the third.
	var page struct {
		Items      []RecordedRequest
		NextCursor *string `json:"next_cursor"`
	}
	mv.Control(t, http.MethodGet, "/__control/requests?limit=2", nil, &page)
	if len(page.Items) != 2 || page.NextCursor == nil {
		t.Fatalf("page 1: %d items, cursor %v", len(page.Items), page.NextCursor)
	}
	mv.Control(t, http.MethodGet, "/__control/requests?limit=2&cursor="+*page.NextCursor, nil, &page)
	if len(page.Items) != 1 || page.NextCursor != nil || page.Items[0].Method != "GET" {
		t.Fatalf("page 2: %+v", page)
	}
	if code := mv.Control(t, http.MethodGet, "/__control/requests?limit=500", nil, nil); code != 400 {
		t.Fatalf("limit 500: %d", code)
	}

	if code := mv.Control(t, http.MethodDelete, "/__control/requests", nil, nil); code != 204 {
		t.Fatalf("clear: %d", code)
	}
	if got := mv.Requests(t, nil); len(got) != 0 {
		t.Fatalf("after clear: %d", len(got))
	}
	var h map[string]any
	mv.Control(t, http.MethodGet, "/__control/healthz", nil, &h)
	if h["status"] != "ok" || h["routes"] != float64(1) {
		t.Fatalf("health %v", h)
	}
}

func TestControlAPIAlsoServedOnVendorPort(t *testing.T) {
	mv := Start(t)
	code, _, body := call(t, mv, http.MethodGet, "/__control/healthz", "", nil)
	if code != 200 || !strings.Contains(body, `"status":"ok"`) {
		t.Fatalf("control on vendor port: %d %s", code, body)
	}
	if got := mv.Requests(t, nil); len(got) != 0 {
		t.Fatalf("control calls must not be recorded, got %d", len(got))
	}
}

func TestEchoAndRawBody(t *testing.T) {
	mv := Start(t)
	raw := "plain text"
	mv.SetRoute(t, "echo", RouteSpec{Match: RouteMatch{Method: "POST", Path: "/echo"}, Behaviour: Behaviour{Echo: true}})
	mv.SetRoute(t, "raw", RouteSpec{Match: RouteMatch{Method: "GET", Path: "/raw"}, Behaviour: Behaviour{BodyRaw: &raw, Headers: map[string]string{"Content-Type": "text/plain"}}})
	_, _, body := call(t, mv, "POST", "/echo", `{"a":1}`, nil)
	var rr RecordedRequest
	if err := json.Unmarshal([]byte(body), &rr); err != nil || rr.Path != "/echo" || string(rr.Body) != `{"a":1}` {
		t.Fatalf("echo body %s (%v)", body, err)
	}
	if code, h, body := call(t, mv, "GET", "/raw", "", nil); code != 200 || body != raw || h.Get("Content-Type") != "text/plain" {
		t.Fatalf("raw: %d %q %q", code, body, h.Get("Content-Type"))
	}
}

func TestResetSelective(t *testing.T) {
	mv := Start(t)
	mv.SetRoute(t, "r", RouteSpec{Match: RouteMatch{Method: "*", Path: "/**"}, Behaviour: Behaviour{Status: 200}})
	call(t, mv, "GET", "/a", "", nil)
	var res ResetResult
	f := false
	mv.Control(t, http.MethodPost, "/__control/reset", ResetRequest{Routes: &f}, &res)
	if res.RoutesRemoved != 0 || res.RecordingsRemoved != 1 {
		t.Fatalf("reset recordings only: %+v", res)
	}
	mv.Control(t, http.MethodPost, "/__control/reset", nil, &res)
	if res.RoutesRemoved != 1 {
		t.Fatalf("full reset: %+v", res)
	}
}
