package audit

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/registry"
)

// MaxInlinePayload is the largest payload stored in request_events; the rest is truncated
// (DESIGN §13.1; spilling the remainder to object storage is HUB-098).
const MaxInlinePayload = 64 << 10

// TrailOptions describe one request's audit context.
type TrailOptions struct {
	RequestID string
	Tenant    string
	Policy    registry.PayloadPolicy // "" = masked (DESIGN §13.2 default)
	PII       []registry.PIIPath     // x-pii paths of the operation's request schema
	Canonical map[string]any         // validated canonical body (masking values)
	Masker    *Masker
	// Secrets are replaced by "***" in every stored payload, whatever the policy.
	Secrets []string
	Now     func() time.Time
}

// Trail collects the §13.1 events of one request with sequence numbers, hashes and the payload
// policy applied. It is used by one goroutine (the request's).
type Trail struct {
	o       TrailOptions
	mask    *masking
	events  []Event
	secrets [][]byte
}

// NewTrail starts the trail of one request.
func NewTrail(o TrailOptions) *Trail {
	if o.Policy == "" {
		o.Policy = registry.PayloadMasked
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Masker == nil {
		o.Masker = NewMasker(nil)
	}
	t := &Trail{o: o}
	for _, s := range o.Secrets {
		if s != "" {
			t.secrets = append(t.secrets, []byte(s))
		}
	}
	if o.Policy == registry.PayloadMasked {
		t.mask = newMasking(o.Masker, o.Tenant, o.PII, o.Canonical)
	}
	return t
}

// AddSecrets adds values that must never be stored (credentials learned mid-request). Payloads
// recorded before the call are unaffected: add secrets before the stage that may carry them.
func (t *Trail) AddSecrets(values ...string) {
	for _, s := range values {
		if s != "" {
			t.secrets = append(t.secrets, []byte(s))
		}
	}
}

// Policy is the payload policy the trail applies (recorded on the requests row).
func (t *Trail) Policy() registry.PayloadPolicy { return t.o.Policy }

// Canonical stages are masked by x-pii pointer; vendor stages by the values found there.
var canonicalStages = map[string]bool{RequestReceived: true, RequestValidated: true}

// Add records one event. payload is the exact bytes seen at that stage (nil when the event has
// none); its sha256 and size are kept under every policy (DESIGN §13.3).
func (t *Trail) Add(typ string, attempt int, payload []byte, meta map[string]any) {
	e := Event{At: t.o.Now(), RequestID: t.o.RequestID, Seq: len(t.events) + 1, Type: typ, AttemptNo: attempt, Meta: meta, PayloadState: PayloadNone}
	if payload != nil {
		sum := sha256.Sum256(payload)
		e.SHA256, e.Bytes = "sha256:"+hex.EncodeToString(sum[:]), len(payload)
		e.Payload, e.PayloadState = t.store(typ, payload)
	}
	t.events = append(t.events, e)
}

// AddJSON records an event whose payload is v encoded as JSON.
func (t *Trail) AddJSON(typ string, attempt int, v any, meta map[string]any) {
	b, err := json.Marshal(v)
	if err != nil {
		b = nil
	}
	t.Add(typ, attempt, b, meta)
}

func (t *Trail) store(typ string, payload []byte) ([]byte, PayloadState) {
	var out []byte
	state := PayloadFull
	switch t.o.Policy {
	case registry.PayloadFull:
		out = bytes.Clone(payload)
	case registry.PayloadMasked:
		out, state = t.masked(typ, payload), PayloadMasked
	default:
		// metadata_only, and sealed until field-level encryption lands (HUB-208): never store
		// a body that the policy does not allow in clear.
		return nil, PayloadOmitted
	}
	for _, s := range t.secrets {
		out = bytes.ReplaceAll(out, s, []byte(Redacted))
	}
	if len(out) > MaxInlinePayload {
		return out[:MaxInlinePayload], PayloadTruncated
	}
	return out, state
}

func (t *Trail) masked(typ string, payload []byte) []byte {
	var doc any
	if err := json.Unmarshal(payload, &doc); err != nil {
		return []byte(t.mask.text(string(payload)))
	}
	if canonicalStages[typ] {
		doc = t.mask.canonical(doc)
	} else {
		doc = t.mask.byValue(doc)
	}
	b, _ := json.Marshal(doc)
	return b
}

// Events returns the events recorded so far.
func (t *Trail) Events() []Event { return t.events }

// Batch returns the trail's events with the final record (policy filled in) and attempts.
func (t *Trail) Batch(rec Record, attempts ...Attempt) Batch {
	rec.PayloadPolicy = string(t.o.Policy)
	return Batch{Events: t.events, Records: []Record{rec}, Attempts: attempts}
}
