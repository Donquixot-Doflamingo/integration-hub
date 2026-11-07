package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func write(t *testing.T, root, rel, content string) {
	t.Helper()
	p := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// configWithT1 copies the dev vendor + T1 transformer into a temp config with one fixture.
func configWithT1(t *testing.T, expectedTS string) string {
	t.Helper()
	dir := t.TempDir()
	for _, rel := range []string{"vendors/clevertap.yaml", "transformers/T1/7.yaml"} {
		b, err := os.ReadFile(filepath.Join("../../config", rel))
		if err != nil {
			t.Fatal(err)
		}
		write(t, dir, rel, string(b))
	}
	write(t, dir, "transformers/T1/fixtures/minimal.input.json", `{"user": {"id": "u_1"}, "event": {"name": "e", "timestamp": "2026-10-31T09:15:00Z"}, "_context": {"creds": {"account_id": "fixture-a", "passcode": "fixture-p"}}}`)
	write(t, dir, "transformers/T1/fixtures/minimal.expected_request.json", `{"method": "POST", "path": "/1/upload", "headers": {"X-CleverTap-Account-Id": "fixture-a", "X-CleverTap-Passcode": "fixture-p", "Content-Type": "application/json; charset=utf-8"},
 "body": {"d": [{"identity": "u_1", "type": "event", "evtName": "e", "ts": `+expectedTS+`}]}}`)
	return dir
}

func TestTransformTestExitCodes(t *testing.T) {
	tests := []struct {
		name string
		ts   string
		code int
		out  string
	}{
		{"all pass", "1793438100", 0, "1 passed, 0 failed (1 transformers)"},
		{"golden fails", "42", 1, "/body/d/0/ts: expected 42, got 1793438100"},
		// ADR-0023: the produced request must also fit the endpoint's request_schema.
		{"endpoint schema fails", "breaks-endpoint", 1, "endpoint_schema /query/name: missing property 'name'"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out, errOut bytes.Buffer
			dir := configWithT1(t, tt.ts)
			if tt.ts == "breaks-endpoint" {
				dir = configWithT1(t, "1793438100")
				v, err := os.ReadFile(filepath.Join(dir, "vendors/clevertap.yaml"))
				if err != nil {
					t.Fatal(err)
				}
				write(t, dir, "vendors/clevertap.yaml", strings.Replace(string(v), "    request_schema:\n", "    request_schema:\n      query: { type: object, required: [name] }\n", 1))
			}
			code := run([]string{"transform", "test", "-config-dir", dir}, &out, &errOut)
			if code != tt.code || !strings.Contains(out.String(), tt.out) {
				t.Fatalf("exit %d, want %d\nstdout: %s\nstderr: %s", code, tt.code, out.String(), errOut.String())
			}
		})
	}
}

func TestUsageAndFilters(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := run(nil, &out, &errOut); code != 2 || !strings.Contains(errOut.String(), "usage: hubctl") {
		t.Fatalf("no args: %d %s", code, errOut.String())
	}
	dir := configWithT1(t, "1793438100")
	if code := run([]string{"transform", "test", "-config-dir", dir, "R9"}, &out, &errOut); code != 2 {
		t.Fatalf("unknown filter exit %d", code)
	}
	out.Reset()
	if code := run([]string{"transform", "test", "-config-dir", dir, "-v", "T1@7"}, &out, &errOut); code != 0 || !strings.Contains(out.String(), "PASS T1@7 minimal") {
		t.Fatalf("filter T1@7: %d %s", code, out.String())
	}
}
