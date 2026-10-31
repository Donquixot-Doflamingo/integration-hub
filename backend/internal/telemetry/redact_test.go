package telemetry

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
)

const canary = "fixture-canary-9f2c41"

type secretStruct struct{ Token string }

func newTestLogger(buf *bytes.Buffer, s *SecretSet) *slog.Logger {
	return NewLogger(buf, slog.LevelDebug, s, "test")
}

func TestRedactHandlerNeverEmitsSecrets(t *testing.T) {
	tests := []struct {
		name string
		log  func(t *testing.T, l *slog.Logger)
	}{
		{"message", func(t *testing.T, l *slog.Logger) { l.InfoContext(t.Context(), "token "+canary) }},
		{"string attr", func(t *testing.T, l *slog.Logger) {
			l.InfoContext(t.Context(), "x", slog.String("hdr", "Bearer "+canary))
		}},
		{"group", func(t *testing.T, l *slog.Logger) {
			l.InfoContext(t.Context(), "x", slog.Group("g", slog.String("v", canary)))
		}},
		{"nested group", func(t *testing.T, l *slog.Logger) {
			l.InfoContext(t.Context(), "x", slog.Group("a", slog.Group("b", slog.String("c", "pre-"+canary+"-post"))))
		}},
		{"error", func(t *testing.T, l *slog.Logger) {
			l.InfoContext(t.Context(), "x", slog.Any("error", fmt.Errorf("auth %s: %w", canary, io.EOF)))
		}},
		{"bytes", func(t *testing.T, l *slog.Logger) { l.InfoContext(t.Context(), "x", slog.Any("b", []byte(canary))) }},
		{"struct", func(t *testing.T, l *slog.Logger) {
			l.InfoContext(t.Context(), "x", slog.Any("s", secretStruct{Token: canary}))
		}},
		{"with attrs", func(t *testing.T, l *slog.Logger) { l.With(slog.String("k", canary)).InfoContext(t.Context(), "x") }},
		{"with group then attrs", func(t *testing.T, l *slog.Logger) {
			l.WithGroup("g").With(slog.String("k", canary)).InfoContext(t.Context(), "x", slog.String("v", canary))
		}},
		{"denied key", func(t *testing.T, l *slog.Logger) {
			l.InfoContext(t.Context(), "x", slog.String("Authorization", "anything-not-registered")) //nolint:sloglint // case-insensitive denied-key match is under test
		}},
		{"many secrets in one string", func(t *testing.T, l *slog.Logger) {
			l.InfoContext(t.Context(), canary+" and "+canary+"-two")
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			tt.log(t, newTestLogger(&buf, NewSecretSet(canary, canary+"-two")))
			out := buf.String()
			if strings.Contains(out, canary) || strings.Contains(out, "anything-not-registered") {
				t.Fatalf("secret leaked: %s", out)
			}
			if !strings.Contains(out, Redacted) {
				t.Fatalf("no redaction marker in %s", out)
			}
		})
	}
}

func TestSecretAddedAfterLoggerBuiltIsStillRedacted(t *testing.T) {
	var buf bytes.Buffer
	s := NewSecretSet()
	l := newTestLogger(&buf, s).With(slog.String("cred", canary)) // attached before the secret is known
	s.Add(canary)
	l.InfoContext(t.Context(), "after rotation")
	if strings.Contains(buf.String(), canary) {
		t.Fatalf("secret attached via With before Add leaked: %s", buf.String())
	}
}

func TestRemoveStopsRedacting(t *testing.T) {
	s := NewSecretSet(canary)
	s.Remove(canary)
	if got := s.Redact(canary); got != canary {
		t.Fatalf("Redact after Remove = %q", got)
	}
	s.Replace("another-secret-1", "short")
	if s.Len() != 1 {
		t.Fatalf("Len = %d, want 1 (short value ignored)", s.Len())
	}
}

func TestLongestSecretWins(t *testing.T) {
	s := NewSecretSet("abcdef", "abcdefghij")
	if got := s.Redact("xx abcdefghij yy"); got != "xx *** yy" {
		t.Fatalf("got %q", got)
	}
}

func TestGroupStructureIsPreserved(t *testing.T) {
	var buf bytes.Buffer
	l := slog.New(NewRedactHandler(slog.NewJSONHandler(&buf, nil), NewSecretSet()))
	l.With(slog.String("top", "1")).WithGroup("g").With(slog.String("inner", "2")).InfoContext(t.Context(), "m", slog.Int("n", 3))
	var got map[string]any
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	g, ok := got["g"].(map[string]any)
	if got["top"] != "1" || !ok || g["inner"] != "2" || g["n"] != float64(3) {
		t.Fatalf("unexpected structure: %s", buf.String())
	}
}

func TestRequestFieldsAreAdded(t *testing.T) {
	var buf bytes.Buffer
	ctx := WithRequestFields(t.Context(), RequestFields{RequestID: "req_1", Tenant: "orders-service", IntegrationID: "int_evt_brandA", IntegrationVersion: 7, Attempt: 2})
	newTestLogger(&buf, nil).InfoContext(ctx, "m")
	for _, want := range []string{`"request_id":"req_1"`, `"tenant":"orders-service"`, `"integration_version":7`, `"attempt":2`, `"role":"test"`} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("missing %s in %s", want, buf.String())
		}
	}
}

func TestConcurrentUpdatesAndLogging(t *testing.T) {
	s := NewSecretSet()
	l := slog.New(NewRedactHandler(slog.DiscardHandler, s))
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Go(func() {
			for j := range 200 {
				v := fmt.Sprintf("secret-%d-%d", i, j)
				s.Add(v)
				l.InfoContext(t.Context(), "m", slog.String("v", v), slog.Any("err", errors.New(v)))
				s.Remove(v)
			}
		})
	}
	wg.Wait()
	if s.Len() != 0 {
		t.Fatalf("Len = %d after removing everything", s.Len())
	}
}

func BenchmarkRedactHandler(b *testing.B) {
	secrets := make([]string, 50)
	for i := range secrets {
		secrets[i] = fmt.Sprintf("vendor-secret-value-%03d", i)
	}
	cases := map[string]slog.Handler{
		"json_only":     slog.NewJSONHandler(io.Discard, nil),                                             //nolint:sloglint // measures real JSON encoding cost
		"redact_50_sec": NewRedactHandler(slog.NewJSONHandler(io.Discard, nil), NewSecretSet(secrets...)), //nolint:sloglint // as above
	}
	for name, h := range cases {
		b.Run(name, func(b *testing.B) {
			l := slog.New(h).With(slog.String("role", "gateway"))
			b.ReportAllocs()
			for b.Loop() {
				l.InfoContext(b.Context(), "vendor responded", slog.String("request_id", "req_01J9Z3M1A5B7C9D1E3F5G7H9JN"),
					slog.Int("status", 200), slog.String("host", "in1.api.example.com"), slog.Any("error", io.EOF))
			}
		})
	}
}
