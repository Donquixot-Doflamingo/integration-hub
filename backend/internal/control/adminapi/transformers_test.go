package adminapi

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"testing/fstest"
)

func TestDiffOps(t *testing.T) {
	got := diffOps([]string{
		`/body/d/0/evtName: expected "app_closed", got "app_opened"`,
		`/headers/X-Trace: unexpected "abc"`,
		`/body/d/0/ts: expected 1, missing`,
		`/body/d: expected 2 items, got 1`,
	})
	want := []diffOp{
		{Op: "replace", Path: "/body/d/0/evtName", Before: "app_closed", After: "app_opened"},
		{Op: "add", Path: "/headers/X-Trace", After: "abc"},
		{Op: "remove", Path: "/body/d/0/ts", Before: 1.0},
		{Op: "replace", Path: "/body/d", Before: "2 items", After: 1.0},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v", got)
	}
}

func TestFixtureFilesRoundTrip(t *testing.T) {
	cases := []struct {
		name     string
		kind     string
		fixtures string
		files    []string
		refused  string // pointer of the 400, "" = accepted
	}{
		{"request", "request", `[{"name":"a","input":{"user":{"id":"u"}},"account_config":{"id":"acct"},"expected_request":{"method":"POST","path":"/x"}}]`,
			[]string{"a.input.json", "a.expected_request.json"}, ""},
		{"response ok", "response", `[{"name":"ok","vendor_response":{"status":200,"body":{}},"expected_response":{"status":"ok"}}]`,
			[]string{"ok.vendor_response.json", "ok.expected_response.json"}, ""},
		{"response error", "response", `[{"name":"err","vendor_response":{"status":500},"expected_error":{"code":"UPSTREAM_ERROR","retryable":true}}]`,
			[]string{"err.vendor_response.json", "err.expected_response.json"}, ""},
		{"none", "request", `[]`, nil, "/fixtures"},
		{"bad name", "request", `[{"name":"Bad Name","input":{},"expected_request":{}}]`, nil, "/fixtures/0/name"},
		{"duplicate", "request", `[{"name":"a","input":{},"expected_request":{}},{"name":"a","input":{},"expected_request":{}}]`, nil, "/fixtures/1/name"},
		{"request without expectation", "request", `[{"name":"a","input":{}}]`, nil, "/fixtures/0"},
		{"response with both", "response", `[{"name":"a","vendor_response":{},"expected_response":{},"expected_error":{}}]`, nil, "/fixtures/0"},
		{"inbound", "inbound", `[{"name":"a"}]`, nil, "/kind"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var fx []fixture
			if err := json.Unmarshal([]byte(c.fixtures), &fx); err != nil {
				t.Fatal(err)
			}
			files, err := fixtureFiles(c.kind, fx, "/fixtures")
			if c.refused != "" {
				e, ok := err.(*Error) //nolint:errorlint // fixtureFiles returns *Error unwrapped
				if !ok || e.Status != 400 || e.Details[0].Pointer != c.refused {
					t.Fatalf("want 400 at %s, got %v", c.refused, err)
				}
				return
			}
			if err != nil || len(files) != len(c.files) {
				t.Fatalf("%v %v", files, err)
			}
			m := fstest.MapFS{}
			for _, f := range c.files {
				if files[f] == nil {
					t.Fatalf("no %s in %v", f, files)
				}
				m["fx/"+f] = &fstest.MapFile{Data: files[f]}
			}
			back, err := readFixtures(m, "fx", c.kind)
			if err != nil || len(back) != 1 || back[0].Name != fx[0].Name {
				t.Fatalf("read back %+v %v", back, err)
			}
			if c.kind == "request" && !strings.Contains(string(back[0].Input), `"_context"`) {
				t.Fatalf("account_config did not become _context.account: %s", back[0].Input)
			}
			if c.name == "response error" && back[0].ExpectedError == nil {
				t.Fatalf("expected_error lost: %+v", back[0])
			}
		})
	}
}

func TestNormaliseAndPlaceholders(t *testing.T) {
	src := "# mapping notes\ntransformer: OTHER\nversion: 99\nkind: request\nrequest:\n  method: POST\n  path: /1/upload\n  headers:\n    X-Key: creds.api_key\n    X-Sig: creds.signing_secret\n"
	docs := "https://docs.example.test"
	out, err := normalise(src, "T5", 3, draft{Kind: "request", Capability: "events.track@v1", VendorEndpoint: "acme.upload@1"}, &docs)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"# mapping notes", "transformer: T5", "version: 3", "capability: events.track@v1", "vendor_endpoint: acme.upload@1", `docs_url: "https://docs.example.test"`} {
		if !strings.Contains(string(out), want) {
			t.Errorf("no %q in\n%s", want, out)
		}
	}
	if _, err := normalise("- a list", "T5", 1, draft{}, nil); err == nil {
		t.Fatal("a non-mapping document was accepted")
	}
	if got := placeholderCreds(src); len(got) != 2 || got["api_key"] == "" || got["signing_secret"] == "" {
		t.Fatalf("placeholders %v", got)
	}
}
