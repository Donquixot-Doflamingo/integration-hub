package telemetry

import (
	"bytes"
	"cmp"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/url"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
)

// Redacted is what every secret occurrence is replaced with.
const Redacted = "***"

// minSecretLen keeps short values (which would also match innocent text) out of the set.
const minSecretLen = 6

// SecretSet holds the secret values currently known to the process (decrypted vendor
// credentials, tenant callback secrets, DSN passwords). Logs replace any occurrence of
// them by value (DESIGN §9). It is safe for concurrent use; reads are lock-free.
type SecretSet struct {
	mu       sync.Mutex // serialises writers
	values   map[string]struct{}
	replacer atomic.Pointer[strings.Replacer]
}

// NewSecretSet returns a set holding values.
func NewSecretSet(values ...string) *SecretSet {
	s := &SecretSet{}
	s.Add(values...)
	return s
}

// Add registers values. Values shorter than 6 bytes are ignored.
func (s *SecretSet) Add(values ...string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.values == nil {
		s.values = map[string]struct{}{}
	}
	for _, v := range values {
		if len(v) >= minSecretLen {
			s.values[v] = struct{}{}
		}
	}
	s.rebuild()
}

// Remove forgets values, e.g. after a credential rotation retires the old slot.
func (s *SecretSet) Remove(values ...string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, v := range values {
		delete(s.values, v)
	}
	s.rebuild()
}

// Replace swaps the whole set in one step.
func (s *SecretSet) Replace(values ...string) {
	s.mu.Lock()
	s.values = map[string]struct{}{}
	s.mu.Unlock()
	s.Add(values...)
}

// Len reports how many values are registered.
func (s *SecretSet) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.values)
}

func (s *SecretSet) rebuild() {
	if len(s.values) == 0 {
		s.replacer.Store(nil)
		return
	}
	// Every encoded form of every value, longest first: a form that contains another (a secret
	// inside a longer secret, a JSON-escaped form over its raw one) is replaced whole.
	seen := map[string]struct{}{}
	var vs []string
	for v := range s.values {
		for _, f := range SecretForms(v) {
			if _, dup := seen[f]; !dup {
				seen[f] = struct{}{}
				vs = append(vs, f)
			}
		}
	}
	slices.SortFunc(vs, func(a, b string) int { return cmp.Or(cmp.Compare(len(b), len(a)), strings.Compare(a, b)) })
	pairs := make([]string, 0, 2*len(vs))
	for _, v := range vs {
		pairs = append(pairs, v, Redacted)
	}
	s.replacer.Store(strings.NewReplacer(pairs...))
}

// Redact replaces every registered secret in str.
func (s *SecretSet) Redact(str string) string {
	if s == nil {
		return str
	}
	if r := s.replacer.Load(); r != nil {
		return r.Replace(str)
	}
	return str
}

// SecretForms returns v followed by every encoded form of it that can reach stored or logged
// text: JSON-escaped with and without Go's HTML escaping (& < >), URL query- and path-escaped,
// and base64 (standard and URL alphabets, padded and not). Each form is also JSON-escaped,
// because URLs and headers travel inside JSON strings. A derived form identical to v is
// omitted, and so is one shorter than the minimum secret length, which would match ordinary
// text. A Basic credential "user:secret" is not derived here: callers register that token as
// its own value.
func SecretForms(v string) []string {
	out := []string{v}
	add := func(f string) {
		if len(f) >= minSecretLen && !slices.Contains(out, f) {
			out = append(out, f)
		}
	}
	q := url.QueryEscape(v)
	bases := []string{v, q, url.PathEscape(v), strings.ReplaceAll(q, "+", "%20")}
	for _, e := range []*base64.Encoding{base64.StdEncoding, base64.URLEncoding, base64.RawStdEncoding, base64.RawURLEncoding} {
		bases = append(bases, e.EncodeToString([]byte(v)))
	}
	for _, b := range bases {
		add(b)
		add(jsonEscaped(b, true))
		add(jsonEscaped(b, false))
	}
	return out
}

// jsonEscaped is the inside of the JSON string literal of s.
func jsonEscaped(s string, html bool) string {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(html)
	_ = enc.Encode(s) // a string always encodes
	b := bytes.TrimSuffix(buf.Bytes(), []byte("\n"))
	return string(b[1 : len(b)-1])
}

// deniedKeys are attribute keys whose value is always redacted, whatever it is. Keys are
// compared lower-cased, exactly as written (no hyphen/underscore folding), so both spellings of
// the header-like ones are listed.
var deniedKeys = map[string]bool{
	"authorization": true, "proxy_authorization": true, "password": true, "passcode": true,
	"api_key": true, "secret": true, "token": true, "cookie": true, "set_cookie": true,
	"x-api-key": true, "x_api_key": true, "access_token": true, "client_secret": true, "refresh_token": true,
}

// RedactHandler is a slog.Handler that redacts registered secret values from the message
// and from every attribute, including nested groups, errors, byte slices and values added
// with Logger.With. Redaction happens when a record is handled, against the secret set at
// that moment, so secrets registered after a logger was built are still caught.
type RedactHandler struct {
	next    slog.Handler
	secrets *SecretSet
	// segments are the With/WithGroup calls in order. They are applied per record instead of
	// on next, so their values are redacted against the current secret set.
	segments []segment
}

type segment struct {
	group string // "" for a plain With
	attrs []slog.Attr
}

// NewRedactHandler wraps next.
func NewRedactHandler(next slog.Handler, secrets *SecretSet) *RedactHandler {
	return &RedactHandler{next: next, secrets: secrets}
}

// Enabled reports whether next handles level.
func (h *RedactHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.next.Enabled(ctx, level)
}

// WithAttrs returns a handler that adds attrs to every record.
func (h *RedactHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	if len(attrs) == 0 {
		return h
	}
	return h.with(segment{attrs: slices.Clone(attrs)})
}

// WithGroup returns a handler that nests later attributes under name.
func (h *RedactHandler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	return h.with(segment{group: name})
}

func (h *RedactHandler) with(s segment) *RedactHandler {
	segs := make([]segment, len(h.segments), len(h.segments)+1)
	copy(segs, h.segments)
	return &RedactHandler{next: h.next, secrets: h.secrets, segments: append(segs, s)}
}

// Handle redacts r and passes it on.
func (h *RedactHandler) Handle(ctx context.Context, r slog.Record) error {
	recAttrs := make([]slog.Attr, 0, r.NumAttrs())
	r.Attrs(func(a slog.Attr) bool {
		recAttrs = append(recAttrs, h.redactAttr(a))
		return true
	})

	// Rebuild the With/WithGroup nesting from the innermost segment outwards.
	attrs := recAttrs
	for i := len(h.segments) - 1; i >= 0; i-- {
		seg := h.segments[i]
		if seg.group != "" {
			if len(attrs) == 0 {
				continue // slog drops empty groups
			}
			attrs = []slog.Attr{{Key: seg.group, Value: slog.GroupValue(attrs...)}}
			continue
		}
		red := make([]slog.Attr, 0, len(seg.attrs)+len(attrs))
		for _, a := range seg.attrs {
			red = append(red, h.redactAttr(a))
		}
		attrs = append(red, attrs...)
	}

	out := slog.NewRecord(r.Time, r.Level, h.secrets.Redact(r.Message), r.PC)
	out.AddAttrs(attrs...)
	return h.next.Handle(ctx, out)
}

func (h *RedactHandler) redactAttr(a slog.Attr) slog.Attr {
	if deniedKeys[strings.ToLower(a.Key)] {
		return slog.String(a.Key, Redacted)
	}
	v := a.Value.Resolve() // runs LogValuer, so a Secret type logs its own masked form
	switch v.Kind() {
	case slog.KindString:
		return slog.String(a.Key, h.secrets.Redact(v.String()))
	case slog.KindGroup:
		g := v.Group()
		out := make([]slog.Attr, len(g))
		for i, ga := range g {
			out[i] = h.redactAttr(ga)
		}
		return slog.Attr{Key: a.Key, Value: slog.GroupValue(out...)}
	case slog.KindAny:
		switch x := v.Any().(type) {
		case error:
			return slog.String(a.Key, h.secrets.Redact(x.Error()))
		case []byte:
			return slog.String(a.Key, h.secrets.Redact(string(x)))
		default:
			// Stringify so nothing reaches the JSON encoder unredacted.
			return slog.String(a.Key, h.secrets.Redact(fmt.Sprint(x)))
		}
	default:
		return slog.Attr{Key: a.Key, Value: v}
	}
}
