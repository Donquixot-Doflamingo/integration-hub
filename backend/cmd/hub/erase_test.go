package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestEraseSubjectUsage(t *testing.T) {
	const key = "mask-key-0123456789abcdef0123456789"
	env := func(vars map[string]string) func(string) string { return func(k string) string { return vars[k] } }
	full := map[string]string{"HUB_PG_DSN": "postgres://hub:pw-canary@127.0.0.1:1/hub", "HUB_AUDIT_MASK_KEY": key}
	okArgs := []string{"-tenant", "lending", "-actor", "dpo@example.com", "-reason", "erasure ticket PRIV-1"}
	tests := []struct {
		name  string
		args  []string
		env   map[string]string
		stdin string
		want  string
	}{
		{"no subject on stdin", okArgs, full, "", "no subject identifier on stdin"},
		{"subject as an argument is refused", append(okArgs, "ABCDE1234F"), full, "ABCDE1234F\n", "subject is read from stdin"},
		{"no dsn", okArgs, map[string]string{"HUB_AUDIT_MASK_KEY": key}, "ABCDE1234F\n", "HUB_PG_DSN is required"},
		{"no mask key", okArgs, map[string]string{"HUB_PG_DSN": "postgres://x"}, "ABCDE1234F\n", "HUB_AUDIT_MASK_KEY is required"},
		{"bad since", append(okArgs, "-since", "yesterday"), full, "ABCDE1234F\n", "since: want RFC 3339"},
		{"short reason", []string{"-tenant", "lending", "-actor", "dpo", "-reason", "gdpr"}, full, "ABCDE1234F\n", "reason must be at least 10"},
		{"no tenant", []string{"-actor", "dpo", "-reason", "erasure ticket PRIV-1"}, full, "ABCDE1234F\n", "tenant is required"},
		{"short subject", okArgs, full, "AB\n", "subject must be at least 3"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := eraseSubjectCmd(t.Context(), tt.args, env(tt.env), strings.NewReader(tt.stdin), &stdout, &stderr)
			if code != eraseUsage {
				t.Fatalf("exit %d, want %d; stderr %s", code, eraseUsage, stderr.String())
			}
			if !strings.Contains(stderr.String(), tt.want) {
				t.Fatalf("stderr %q does not contain %q", stderr.String(), tt.want)
			}
			for _, leak := range []string{"ABCDE1234F", "pw-canary", key} {
				if strings.Contains(stderr.String()+stdout.String(), leak) {
					t.Fatalf("output leaks %q", leak)
				}
			}
		})
	}
}
