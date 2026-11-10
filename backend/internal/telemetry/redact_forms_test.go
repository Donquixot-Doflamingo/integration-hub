package telemetry

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"slices"
	"strings"
	"testing"
)

// HUB-448: a secret with JSON- or URL-special characters is masked in every encoded form it can
// appear in inside log text (an error carrying a JSON body or a request URL).
func TestRedactHandlerNeverEmitsEncodedSecrets(t *testing.T) {
	for _, secret := range []string{`pa&ss<w>"rd\1`, `päss wörd`, `a+b/c=d`, `tok%en`, `plainSecret42`} {
		forms := independentForms(secret)
		jsonBody, _ := json.Marshal(map[string]string{"k": secret})
		for _, form := range forms {
			logs := map[string]func(l *slog.Logger){
				"message": func(l *slog.Logger) { l.InfoContext(t.Context(), "failed "+form) },
				"attr": func(l *slog.Logger) {
					l.InfoContext(t.Context(), "x", slog.String("url", "https://v/p?k="+form+"&z=1"))
				},
				"group": func(l *slog.Logger) {
					l.InfoContext(t.Context(), "x", slog.Group("a", slog.Group("b", slog.String("c", form))))
				},
				"error": func(l *slog.Logger) {
					l.InfoContext(t.Context(), "x", slog.Any("error", fmt.Errorf("vendor said %s: %w", jsonBody, io.EOF)))
				},
			}
			for name, fn := range logs {
				var buf bytes.Buffer
				fn(newTestLogger(&buf, NewSecretSet(secret)))
				out := buf.String()
				for _, f := range forms {
					// The log line is itself JSON, so compare against what the handler writes for f too.
					enc, _ := json.Marshal(f)
					if strings.Contains(out, f) || strings.Contains(out, string(enc[1:len(enc)-1])) {
						t.Errorf("%s: form %q of %q leaked: %s", name, f, secret, out)
					}
				}
			}
		}
	}
}

// independentForms builds the encoded forms with the standard library only, so the tests do not
// trust SecretForms to say what must be masked.
func independentForms(secret string) []string {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	_ = enc.Encode(secret)
	withHTML := strings.TrimSuffix(buf.String(), "\n")
	buf.Reset()
	enc.SetEscapeHTML(false)
	_ = enc.Encode(secret)
	noHTML := strings.TrimSuffix(buf.String(), "\n")
	return []string{secret, withHTML[1 : len(withHTML)-1], noHTML[1 : len(noHTML)-1],
		url.QueryEscape(secret), url.PathEscape(secret),
		base64.StdEncoding.EncodeToString([]byte(secret)), base64.URLEncoding.EncodeToString([]byte(secret))}
}

func TestEncodedFormsLeaveOrdinaryTextAlone(t *testing.T) {
	s := NewSecretSet(`pa&ss<w>"rd\1`)
	const text = "plain text, a=b&c=d, <html> and %20 stay"
	if got := s.Redact(text); got != text {
		t.Fatalf("ordinary text changed: %q", got)
	}
	if forms := SecretForms("plainSecret42"); slices.Contains(forms[1:], "plainSecret42") {
		t.Fatalf("a derived form equals the raw secret: %v", forms)
	}
	s.Remove(`pa&ss<w>"rd\1`)
	if got := s.Redact(`pa&ss`); got != `pa&ss` {
		t.Fatalf("Remove left forms behind: %q", got)
	}
}

func TestDeniedKeysCoverCommonCredentialFields(t *testing.T) {
	for _, k := range []string{"X-Api-Key", "x-api-key", "Access_Token", "client_secret", "Refresh_Token"} {
		var buf bytes.Buffer
		newTestLogger(&buf, nil).InfoContext(t.Context(), "x", slog.String(k, "not-registered-value")) //nolint:sloglint // key spelling is under test
		if strings.Contains(buf.String(), "not-registered-value") {
			t.Errorf("key %q not denied: %s", k, buf.String())
		}
	}
}
