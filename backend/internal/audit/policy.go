package audit

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strconv"
	"strings"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/registry"
)

// Masker masks x-pii values for the `masked` policy (DESIGN §13.2,
// contracts/schemas/capabilities/README.md). Identifiers are HMAC-SHA256'd with a per-tenant key
// derived from the master key, so the same subject masks to the same value within a tenant (and
// the deletion job can find it) but cannot be joined across tenants.
type Masker struct{ master []byte }

// NewMasker returns a masker for master (≥ 32 random bytes from the secret store).
func NewMasker(master []byte) *Masker { return &Masker{master: bytes.Clone(master)} }

func (m *Masker) tenantKey(tenant string) []byte {
	mac := hmac.New(sha256.New, m.master)
	mac.Write([]byte("audit-mask:" + tenant))
	return mac.Sum(nil)
}

func (m *Masker) hmac(tenant, v string) string {
	mac := hmac.New(sha256.New, m.tenantKey(tenant))
	mac.Write([]byte(v))
	return "hmac:" + hex.EncodeToString(mac.Sum(nil))[:32]
}

func last4(v string) string {
	if len(v) <= 4 {
		return Redacted
	}
	return Redacted + v[len(v)-4:]
}

// Redacted replaces secrets and masked values.
const Redacted = "***"

// maskString masks one string value of the given x-pii kind.
func (m *Masker) maskString(tenant, kind, v string) string {
	switch kind {
	case "identifier", "ip_address", "device_id":
		return m.hmac(tenant, v)
	case "email":
		local, domain, ok := strings.Cut(v, "@")
		if !ok {
			return m.hmac(tenant, v)
		}
		return m.hmac(tenant, local) + "@" + domain
	case "phone", "government_id", "financial":
		return last4(v)
	default: // name, address, free_text, unknown kinds
		return Redacted
	}
}

// summary replaces a whole free_text value: its hash and size, no content.
func summary(v any) map[string]any {
	b, _ := json.Marshal(v)
	sum := sha256.Sum256(b)
	return map[string]any{"masked": "free_text", "sha256": "sha256:" + hex.EncodeToString(sum[:]), "bytes": len(b)}
}

// masking is the masking plan of one request: x-pii pointers into the canonical body, plus every
// tagged string value found there, so the same values are masked in later stages (vendor
// request and response) where the field names differ.
type masking struct {
	m      *Masker
	tenant string
	paths  []registry.PIIPath
	values map[string]string // original → masked
}

func newMasking(m *Masker, tenant string, paths []registry.PIIPath, canonical map[string]any) *masking {
	mk := &masking{m: m, tenant: tenant, paths: paths, values: map[string]string{}}
	for _, p := range paths {
		for _, v := range lookup(canonical, splitPointer(p.Pointer)) {
			mk.collect(p.Kind, v)
		}
	}
	return mk
}

func (mk *masking) collect(kind string, v any) {
	switch x := v.(type) {
	case string:
		if len(x) >= 3 { // shorter values would mask unrelated text
			mk.values[x] = mk.m.maskString(mk.tenant, kind, x)
		}
	case map[string]any:
		for _, c := range x {
			mk.collect(kind, c)
		}
	case []any:
		for _, c := range x {
			mk.collect(kind, c)
		}
	case float64:
		if kind != "free_text" {
			s := strconv.FormatFloat(x, 'f', -1, 64)
			if len(s) >= 6 { // long numbers only: account numbers, phone numbers
				mk.values[s] = mk.m.maskString(mk.tenant, kind, s)
			}
		}
	}
}

// canonical masks a canonical body by pointer (request.received, request.validated).
func (mk *masking) canonical(doc any) any {
	for _, p := range mk.paths {
		doc = replaceAt(doc, splitPointer(p.Pointer), func(v any) any {
			if p.Kind == "free_text" {
				return summary(v)
			}
			return mk.byKind(p.Kind, v)
		})
	}
	return mk.byValue(doc)
}

func (mk *masking) byKind(kind string, v any) any {
	switch x := v.(type) {
	case string:
		return mk.m.maskString(mk.tenant, kind, x)
	case map[string]any, []any:
		return summary(v)
	case float64:
		return mk.m.maskString(mk.tenant, kind, strconv.FormatFloat(x, 'f', -1, 64))
	}
	return v // nil, bool: nothing personal to mask
}

// byValue masks every collected value wherever it appears (vendor-side stages).
func (mk *masking) byValue(doc any) any {
	switch x := doc.(type) {
	case string:
		return mk.text(x)
	case float64:
		if r, ok := mk.values[strconv.FormatFloat(x, 'f', -1, 64)]; ok {
			return r
		}
		return x
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, v := range x {
			out[k] = mk.byValue(v)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, v := range x {
			out[i] = mk.byValue(v)
		}
		return out
	}
	return doc
}

func (mk *masking) text(s string) string {
	if r, ok := mk.values[s]; ok {
		return r
	}
	for orig, r := range mk.values {
		if strings.Contains(s, orig) {
			s = strings.ReplaceAll(s, orig, r)
		}
	}
	return s
}

func splitPointer(p string) []string {
	if p == "" || p == "/" {
		return nil
	}
	parts := strings.Split(strings.TrimPrefix(p, "/"), "/")
	for i, s := range parts {
		parts[i] = strings.NewReplacer("~1", "/", "~0", "~").Replace(s)
	}
	return parts
}

// lookup returns every value at the pointer ("*" matches each array item).
func lookup(doc any, path []string) []any {
	if len(path) == 0 {
		return []any{doc}
	}
	switch x := doc.(type) {
	case map[string]any:
		if v, ok := x[path[0]]; ok {
			return lookup(v, path[1:])
		}
	case []any:
		var out []any
		for i, v := range x {
			if path[0] == "*" || path[0] == strconv.Itoa(i) {
				out = append(out, lookup(v, path[1:])...)
			}
		}
		return out
	}
	return nil
}

// replaceAt returns doc with f applied at the pointer; it copies only the maps and slices on the
// way, so the caller's canonical body is never modified.
func replaceAt(doc any, path []string, f func(any) any) any {
	if len(path) == 0 {
		return f(doc)
	}
	switch x := doc.(type) {
	case map[string]any:
		v, ok := x[path[0]]
		if !ok {
			return doc
		}
		out := make(map[string]any, len(x))
		for k, c := range x {
			out[k] = c
		}
		out[path[0]] = replaceAt(v, path[1:], f)
		return out
	case []any:
		out := make([]any, len(x))
		for i, c := range x {
			if path[0] == "*" || path[0] == strconv.Itoa(i) {
				c = replaceAt(c, path[1:], f)
			}
			out[i] = c
		}
		return out
	}
	return doc
}
