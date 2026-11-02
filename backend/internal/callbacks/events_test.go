package callbacks

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/core"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/registry"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/telemetry"
)

// contractsFS is the repository's contracts/ directory (schemas are never copied).
func contractsFS(t testing.TB) fs.FS {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if fi, err := os.Stat(filepath.Join(dir, "contracts", "schemas", "events")); err == nil && fi.IsDir() {
			return os.DirFS(filepath.Join(dir, "contracts"))
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("contracts/ not found")
		}
		dir = parent
	}
}

func validator(t testing.TB) *SchemaValidator {
	t.Helper()
	v, err := NewSchemaValidator(contractsFS(t))
	if err != nil {
		t.Fatal(err)
	}
	return v
}

var t0 = time.Date(2026, 10, 31, 9, 15, 2, 123e6, time.UTC)

func baseEvent(data EventData) *Event {
	return &Event{
		TenantID: "orders-service", IntegrationID: "int_evt_brandB", IntegrationVersion: 1,
		RequestID: core.NewRequestID(), IdempotencyKey: "order-8821-created", Data: data,
	}
}

func sampleEvents() map[string]*Event {
	next := t0.Add(30 * time.Second)
	return map[string]*Event{
		"completed": baseEvent(&Completed{
			Result: json.RawMessage(`{"status":"ok","accepted":1,"rejected":[]}`), Attempts: 2, CompletedAt: t0,
			Timings: &Timings{TotalMS: 1840, HubOverheadMS: 3, VendorMS: 212, QueueWaitMS: 1625},
		}),
		"completed without timings or key": func() *Event {
			ev := baseEvent(&Completed{Result: json.RawMessage(` {"ok":true} `), Attempts: 1, CompletedAt: t0})
			ev.IdempotencyKey = ""
			return ev
		}(),
		"failed": baseEvent(&Failed{Attempts: 8, FailedAt: t0, Error: core.CanonicalError{
			Code: core.CodeUpstreamRejected, Message: "event name must not contain spaces",
			Upstream: &core.Upstream{Status: core.Ptr(400), Code: core.Ptr("E_INVALID_EVENT")},
		}}),
		"failed attempts exhausted": baseEvent(&Failed{Attempts: 9, FailedAt: t0, Error: core.CanonicalError{
			Code: core.CodeUpstreamUnavailable, Message: "upstream unavailable", Retryable: true,
			Reason: core.Ptr("attempts_exhausted"), Circuit: core.Ptr("open"), RetryAfterMS: core.Ptr(int64(30000)),
		}}),
		"expired":                baseEvent(&Expired{Attempts: 0, ExpiresAt: t0, LastState: "parked"}),
		"delivery.status parked": baseEvent(&DeliveryStatus{State: "parked", PreviousState: "in_flight", Sequence: 2, Attempt: 1, Reason: "circuit_open"}),
		"delivery.status retry": baseEvent(&DeliveryStatus{
			State: "retry_scheduled", PreviousState: "in_flight", Sequence: 3, Attempt: 1, NextAttemptAt: &next,
			Reason: "retryable_error", LastError: &core.CanonicalError{Code: core.CodeUpstreamTimeout, Message: "timed out", Retryable: true},
		}),
		"delivery.status accepted": baseEvent(&DeliveryStatus{State: "accepted", Sequence: 1, Attempt: 0}),
	}
}

// TestPayloadsValidateAgainstContract is the HUB-120 "payload validates against
// contracts/schemas/events/" criterion, for every event type and the optional members.
func TestPayloadsValidateAgainstContract(t *testing.T) {
	v := validator(t)
	for name, ev := range sampleEvents() {
		t.Run(name, func(t *testing.T) {
			body, err := ev.Marshal(t0)
			if err != nil {
				t.Fatal(err)
			}
			if err := v.Validate(ev.Data.Type(), body); err != nil {
				t.Fatalf("%v\n%s", err, body)
			}
			var got map[string]any
			if err := json.Unmarshal(body, &got); err != nil {
				t.Fatal(err)
			}
			if got["request_id"] != ev.RequestID || got["type"] != string(ev.Data.Type()) || got["spec_version"] != "1" {
				t.Fatalf("envelope = %s", body)
			}
			if _, ok := got["idempotency_key"]; !ok {
				t.Fatal("idempotency_key must always be present (null when absent)")
			}
			if !eventIDPattern.MatchString(ev.EventID) || !ev.OccurredAt.Equal(t0) {
				t.Fatalf("event id %q / occurred_at %v not filled", ev.EventID, ev.OccurredAt)
			}
			for _, banned := range []string{"clevertap", "ct-brand", `"vendor"`, `"account"`, `"version"`, `"integration_version"`} {
				if strings.Contains(strings.ToLower(string(body)), banned) {
					t.Fatalf("payload leaks %q: %s", banned, body)
				}
			}
		})
	}
}

func TestSchemaValidatorRejectsBrokenBodies(t *testing.T) {
	v := validator(t)
	ev := sampleEvents()["failed"]
	body, err := ev.Marshal(t0)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	_ = json.Unmarshal(body, &doc)
	for name, mutate := range map[string]func(map[string]any){
		"no request_id":      func(d map[string]any) { delete(d, "request_id") },
		"no idempotency_key": func(d map[string]any) { delete(d, "idempotency_key") },
		"bad event id":       func(d map[string]any) { d["event_id"] = "evt_1" },
		"wrong status":       func(d map[string]any) { d["data"].(map[string]any)["status"] = "succeeded" },
		"unknown error code": func(d map[string]any) { d["data"].(map[string]any)["error"].(map[string]any)["code"] = "VENDOR_DOWN" },
		"bad occurred_at":    func(d map[string]any) { d["occurred_at"] = "yesterday" },
		"wrong spec version": func(d map[string]any) { d["spec_version"] = "2" },
	} {
		t.Run(name, func(t *testing.T) {
			var c map[string]any
			_ = json.Unmarshal(body, &c)
			mutate(c)
			b, _ := json.Marshal(c)
			if err := v.Validate(EventRequestFailed, b); err == nil {
				t.Fatalf("schema accepted %s", b)
			}
		})
	}
	if err := v.Validate(EventType("request.unknown"), body); err == nil {
		t.Fatal("unknown type accepted")
	}
}

func TestMarshalRejectsInvalidEvents(t *testing.T) {
	for name, ev := range map[string]*Event{
		"no data":        {TenantID: "orders-service", IntegrationID: "int_x", IntegrationVersion: 1, RequestID: core.NewRequestID()},
		"bad request id": func() *Event { e := baseEvent(&Expired{ExpiresAt: t0}); e.RequestID = "req_x"; return e }(),
		"no version":     func() *Event { e := baseEvent(&Expired{ExpiresAt: t0}); e.IntegrationVersion = 0; return e }(),
		"bad event id":   func() *Event { e := baseEvent(&Expired{ExpiresAt: t0}); e.EventID = "evt_nope"; return e }(),
		"long key": func() *Event {
			e := baseEvent(&Expired{ExpiresAt: t0})
			e.IdempotencyKey = strings.Repeat("k", 256)
			return e
		}(),
		"result not object":   baseEvent(&Completed{Result: json.RawMessage(`[1]`), Attempts: 1, CompletedAt: t0}),
		"result invalid":      baseEvent(&Completed{Result: json.RawMessage(`{`), Attempts: 1, CompletedAt: t0}),
		"completed 0 attempt": baseEvent(&Completed{Result: json.RawMessage(`{}`), CompletedAt: t0}),
		"failed no code":      baseEvent(&Failed{Attempts: 1, FailedAt: t0, Error: core.CanonicalError{Message: "x"}}),
		"expired other code":  baseEvent(&Expired{ExpiresAt: t0, Error: core.CanonicalError{Code: core.CodeInternalError, Message: "x"}}),
		"status no sequence":  baseEvent(&DeliveryStatus{State: "parked"}),
	} {
		if _, err := ev.Marshal(t0); err == nil {
			t.Errorf("%s: Marshal accepted an invalid event", name)
		}
	}
}

func TestSignAndVerify(t *testing.T) {
	body := []byte(`{"event_id":"evt_01J9Z5C3D4E5F6G7H8J9K0M1NP"}`)
	active, next, other := []byte("active-secret-0123456789"), []byte("next-secret-0123456789"), []byte("unrelated-secret-01234")
	now := time.Unix(1793438102, 0)

	// Known vector: hex(HMAC-SHA256("active-secret-0123456789", "1793438102." + body)).
	sig, err := Sign([][]byte{active}, now, body)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(sig, "t=1793438102,v1=") || len(sig) != len("t=1793438102,v1=")+64 {
		t.Fatalf("signature format %q", sig)
	}
	if want := "t=1793438102,v1=" + hexMAC(active, "1793438102", body); sig != want {
		t.Fatalf("Sign = %q, want %q", sig, want)
	}
	both, err := Sign([][]byte{active, next}, now, body)
	if err != nil || strings.Count(both, ",v1=") != 2 {
		t.Fatalf("rotation signature %q, %v", both, err)
	}
	if _, err := Sign(nil, now, body); !errors.Is(err, ErrNoSecret) {
		t.Fatalf("Sign without secrets: %v", err)
	}

	for _, tc := range []struct {
		name    string
		header  string
		body    []byte
		secrets [][]byte
		now     time.Time
		want    error
	}{
		{"valid", sig, body, [][]byte{active}, now, nil},
		{"valid within tolerance", sig, body, [][]byte{active}, now.Add(4*time.Minute + 59*time.Second), nil},
		{"receiver still on old secret during overlap", both, body, [][]byte{active}, now, nil},
		{"receiver already on new secret during overlap", both, body, [][]byte{next}, now, nil},
		{"receiver accepts either", sig, body, [][]byte{other, active}, now, nil},
		{"stale timestamp", sig, body, [][]byte{active}, now.Add(5*time.Minute + time.Second), ErrSignatureExpired},
		{"future timestamp", sig, body, [][]byte{active}, now.Add(-6 * time.Minute), ErrSignatureExpired},
		{"tampered body", sig, []byte(`{"event_id":"evt_X"}`), [][]byte{active}, now, ErrSignatureMismatch},
		{"wrong secret", sig, body, [][]byte{other}, now, ErrSignatureMismatch},
		{"timestamp swapped", strings.Replace(sig, "t=1793438102", "t=1793438103", 1), body, [][]byte{active}, now, ErrSignatureMismatch},
		{"unknown scheme ignored", sig + ",v2=abc", body, [][]byte{active}, now, nil},
		{"no t", strings.TrimPrefix(sig, "t=1793438102,"), body, [][]byte{active}, now, ErrSignatureMalformed},
		{"no v1", "t=1793438102", body, [][]byte{active}, now, ErrSignatureMalformed},
		{"short v1", "t=1793438102,v1=abcd", body, [][]byte{active}, now, ErrSignatureMalformed},
		{"bad t", "t=abc,v1=" + strings.Repeat("0", 64), body, [][]byte{active}, now, ErrSignatureMalformed},
		{"empty", "", body, [][]byte{active}, now, ErrSignatureMalformed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := Verify(tc.header, tc.body, tc.secrets, tc.now, 0); !errors.Is(err, tc.want) {
				t.Fatalf("Verify = %v, want %v", err, tc.want)
			}
		})
	}
}

func hexMAC(secret []byte, t string, body []byte) string {
	return hex.EncodeToString(mac(secret, t, body))
}

func TestIDs(t *testing.T) {
	if id := NewDeliveryID(); !deliveryIDPattern.MatchString(id) {
		t.Fatalf("delivery id %q", id)
	}
	if id := NewEventID(); !eventIDPattern.MatchString(id) {
		t.Fatalf("event id %q", id)
	}
}

// ---- subscription matching (no database) ----

type staticSnapshots struct{ s *registry.Snapshot }

func (s staticSnapshots) Snapshot() *registry.Snapshot { return s.s }

// testSnapshot: int_evt_brandB v1 (owner orders-service) subscribes request.completed to two
// endpoints (one listed twice), request.failed to one, and one endpoint of another tenant.
func testSnapshot(url string) *registry.Snapshot {
	reg := func(id, tenant string) *registry.RegistrarEndpoint {
		return &registry.RegistrarEndpoint{EndpointID: id, Tenant: tenant, URL: url, SecretRef: "localfile://" + id}
	}
	return &registry.Snapshot{
		Integrations: map[string]*registry.Integration{
			"int_evt_brandB": {
				IntegrationFile: registry.IntegrationFile{Integration: "int_evt_brandB", OwnerTenant: "orders-service", ActiveVersion: 1},
				Versions: map[int]*registry.IntegrationVersion{1: {
					Integration: "int_evt_brandB", Version: 1,
					Webhooks: []registry.Subscription{
						{Event: "request.completed", Endpoint: "reg_orders_cb"},
						{Event: "request.failed", Endpoint: "reg_orders_cb"},
						{Event: "request.completed", Endpoint: "reg_orders_audit"},
						{Event: "request.completed", Endpoint: "reg_orders_cb"},
						{Event: "request.completed", Endpoint: "reg_other_tenant"},
						{Event: "request.completed", Endpoint: "reg_missing"},
						{Event: "request.completed", Endpoint: "reg_off"},
					},
				}},
			},
		},
		Registrar: map[string]*registry.RegistrarEndpoint{
			"reg_orders_cb":    reg("reg_orders_cb", "orders-service"),
			"reg_orders_audit": reg("reg_orders_audit", "orders-service"),
			"reg_other_tenant": reg("reg_other_tenant", "payments-service"),
			"reg_off":          {EndpointID: "reg_off", Tenant: "orders-service", URL: url, SecretRef: "localfile://reg_off", Disabled: true},
		},
	}
}

func TestEnqueueTargets(t *testing.T) {
	e, err := NewEnqueuer(staticSnapshots{testSnapshot("https://example.com/cb")}, telemetry.NewMetrics("test", "dev"), EnqueueOptions{})
	if err != nil {
		t.Fatal(err)
	}
	snap := testSnapshot("https://example.com/cb")
	ev := sampleEvents()["completed"]
	got := e.targets(t.Context(), snap, ev)
	if len(got) != 2 || got[0] != (target{"reg_orders_cb", 0}) || got[1] != (target{"reg_orders_audit", 2}) {
		t.Fatalf("targets = %+v", got)
	}
	if n := testutil.ToFloat64(e.skipped.WithLabelValues(skipTenantMismatch)); n != 1 {
		t.Fatalf("tenant_mismatch skipped = %v", n)
	}
	if n := testutil.ToFloat64(e.skipped.WithLabelValues(skipUnknownEndpoint)); n != 1 {
		t.Fatalf("unknown_endpoint skipped = %v", n)
	}
	if n := testutil.ToFloat64(e.skipped.WithLabelValues(skipDisabled)); n != 1 {
		t.Fatalf("endpoint_disabled skipped = %v", n)
	}

	// No subscription for the type: nothing to write, the transaction is not touched (nil tx).
	n, err := e.Enqueue(t.Context(), nil, sampleEvents()["expired"])
	if err != nil || n != 0 {
		t.Fatalf("Enqueue(no subscription) = %d, %v", n, err)
	}
	unknown := sampleEvents()["completed"]
	unknown.IntegrationVersion = 9
	if n, err := e.Enqueue(t.Context(), nil, unknown); err != nil || n != 0 {
		t.Fatalf("Enqueue(unknown version) = %d, %v", n, err)
	}
	if n := testutil.ToFloat64(e.skipped.WithLabelValues(skipUnknownVersion)); n != 1 {
		t.Fatalf("unknown_version skipped = %v", n)
	}

	empty, err := NewEnqueuer(staticSnapshots{}, telemetry.NewMetrics("test", "dev"), EnqueueOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := empty.Enqueue(t.Context(), nil, sampleEvents()["completed"]); !errors.Is(err, ErrNoSnapshot) {
		t.Fatalf("Enqueue without snapshot = %v", err)
	}
}
