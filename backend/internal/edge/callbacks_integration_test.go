//go:build integration

package edge

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"go.opentelemetry.io/otel/trace"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/audit"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/callbacks"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/creds"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/delivery"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/queue"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/registry"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/telemetry"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/testkit/mockvendor"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/upstream"
)

const signingSecret = "fake-signing-secret-0123456789"

// receiver is a tenant callback endpoint that verifies every signature like a real receiver.
type receiver struct {
	mu     sync.Mutex
	events []map[string]any
	raw    [][]byte
	sigs   []string
	bad    int
	// hold, when set, makes every delivery wait until it is closed; entered gets one value per
	// delivery that started waiting (HUB-413: a receiver that is slow or hanging).
	hold, entered chan struct{}
}

// holdDeliveries makes the receiver hang every callback until release is closed and returns
// the channel that reports each delivery as it starts waiting.
func (rc *receiver) holdDeliveries(release chan struct{}) <-chan struct{} {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	rc.hold, rc.entered = release, make(chan struct{}, 16)
	return rc.entered
}

func (rc *receiver) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	rc.mu.Lock()
	hold, entered := rc.hold, rc.entered
	rc.mu.Unlock()
	if hold != nil {
		select {
		case entered <- struct{}{}:
		default:
		}
		<-hold
	}
	sig := r.Header.Get(callbacks.SignatureHeader)
	rc.mu.Lock()
	defer rc.mu.Unlock()
	if err := callbacks.Verify(sig, body, [][]byte{[]byte(signingSecret)}, time.Now(), 0); err != nil {
		rc.bad++
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	var ev map[string]any
	_ = json.Unmarshal(body, &ev)
	rc.events, rc.raw, rc.sigs = append(rc.events, ev), append(rc.raw, body), append(rc.sigs, sig)
	w.WriteHeader(http.StatusNoContent)
}

func (rc *receiver) get(requestID string) (map[string]any, []byte, string) {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	for i, ev := range rc.events {
		if ev["request_id"] == requestID {
			return ev, rc.raw[i], rc.sigs[i]
		}
	}
	return nil, nil, ""
}

// secretsLoader serves vendor credentials for accounts and a signing secret for registrar ids.
type secretsLoader struct{ registrar []string }

func (l secretsLoader) Load(_ context.Context, ids []string) (map[string]creds.AccountSecrets, error) {
	out := map[string]creds.AccountSecrets{}
	for _, id := range ids {
		values := fakeCreds
		if slices.Contains(l.registrar, id) {
			values = map[string]string{creds.SigningSecretKey: signingSecret}
		}
		out[id] = creds.AccountSecrets{Active: creds.Slot{Version: 1, Values: values}}
	}
	return out, nil
}

// callbackRig is the queued rig plus callbacks: enqueue in the terminal transaction, a running
// dispatcher, and the tenant's registrar endpoint pointed at a verifying receiver.
func callbackRig(t *testing.T, tune func(string, *registry.Integration, *registry.IntegrationVersion)) (*queuedRig, *receiver, *delivery.Scheduler) {
	t.Helper()
	return tracedCallbackRig(t, tune, nil)
}

// tracedCallbackRig is callbackRig with tracing: the worker, the dispatcher and an upstream
// client that forwards traceparent to every vendor of the snapshot make spans with tp (nil: no
// tracing). The caller wraps q.h with telemetry.HTTPHandler for the gateway's server span.
func tracedCallbackRig(t *testing.T, tune func(string, *registry.Integration, *registry.IntegrationVersion), tp trace.TracerProvider) (*queuedRig, *receiver, *delivery.Scheduler) {
	t.Helper()
	rc := &receiver{}
	srv := httptest.NewServer(rc)
	t.Cleanup(srv.Close)
	var sched *delivery.Scheduler
	q := newQueuedRig(t, "", tune, func(d *Deps, w *delivery.Worker) {
		snap := d.Snapshots.Snapshot()
		cp := *snap
		cp.Registrar = map[string]*registry.RegistrarEndpoint{}
		for id, ep := range snap.Registrar {
			c := *ep
			c.URL = srv.URL + "/hub/callbacks"
			cp.Registrar[id] = &c
		}
		snaps := staticSnapshots{&cp}
		d.Snapshots, w.Snapshots = snaps, snaps
		m := telemetry.NewMetrics("itest", "callbacks")
		cache, err := creds.NewCache(secretsLoader{registrar: creds.RegistrarSecretIDs(&cp)}, telemetry.NewSecretSet(), m, creds.CacheOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if err := cache.Warm(t.Context(), creds.SnapshotSecretIDs(&cp)); err != nil {
			t.Fatal(err)
		}
		enq, err := callbacks.NewEnqueuer(snaps, m, callbacks.EnqueueOptions{Validator: mustValidator(t)})
		if err != nil {
			t.Fatal(err)
		}
		w.Transitions.OnTerminal = delivery.CallbackHook(enq)
		if tp != nil {
			forward := map[string]bool{}
			for name := range cp.Vendors {
				forward[name] = true
			}
			client, err := upstream.New(m, upstream.Options{TracerProvider: tp, ForwardTraceparent: forward})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(client.Close)
			d.Upstream, w.Attempter.Upstream, w.TracerProvider = client, client, tp
		}
		disp, err := callbacks.NewDispatcher(callbacks.DispatcherDeps{Pool: w.DB, Snapshots: snaps, Secrets: creds.RegistrarSecrets{Cache: cache},
			Audit: audit.Discard{}, Metrics: m, Log: slog.New(slog.DiscardHandler), TracerProvider: tp},
			callbacks.DispatcherOptions{PollInterval: 50 * time.Millisecond, Guard: callbacks.GuardOptions{AllowLoopback: true}})
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.WithoutCancel(t.Context()))
		done := make(chan struct{})
		go func() { _ = disp.Run(ctx); close(done) }()
		t.Cleanup(func() { cancel(); <-done })
		sched = &delivery.Scheduler{DB: w.DB, Queue: &queue.Outbox{}, Transitions: w.Transitions, Log: slog.New(slog.DiscardHandler)}
	})
	return q, rc, sched
}

func mustValidator(t *testing.T) *callbacks.SchemaValidator {
	t.Helper()
	v, err := callbacks.NewSchemaValidator(os.DirFS("../../../contracts"))
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// TestAsyncCallbackEndToEnd is HUB-121 and the signature half of HUB-123: 202, then a signed
// request.completed callback with the canonical result; a tampered body or a timestamp older
// than 5 minutes fails verification.
func TestAsyncCallbackEndToEnd(t *testing.T) {
	q, rc, _ := callbackRig(t, nil)
	q.route(t, mockvendor.Behaviour{Status: 200, Body: okVendor})
	rec, body := q.invoke(t, "int_evt_brandB", trackBody, "Idempotency-Key", "cb-"+time.Now().Format("150405.000000"))
	if rec.Code != 202 {
		t.Fatalf("%d %v", rec.Code, body)
	}
	id := body["request_id"].(string)
	if n, err := q.worker.ProcessOnce(t.Context()); err != nil || n != 1 {
		t.Fatalf("worker %d %v", n, err)
	}
	eventually(t, "the callback", func() bool { ev, _, _ := rc.get(id); return ev != nil })
	ev, raw, sig := rc.get(id)
	data, _ := ev["data"].(map[string]any)
	result, _ := data["result"].(map[string]any)
	if ev["type"] != "request.completed" || result["status"] != "ok" || ev["idempotency_key"] == nil || ev["tenant_id"] != "orders-service" {
		t.Fatalf("callback %v", ev)
	}
	secret := [][]byte{[]byte(signingSecret)}
	tampered := []byte(strings.Replace(string(raw), `"ok"`, `"partial"`, 1))
	if err := callbacks.Verify(sig, tampered, secret, time.Now(), 0); err == nil {
		t.Fatal("a tampered body verified")
	}
	if err := callbacks.Verify(sig, raw, secret, time.Now().Add(6*time.Minute), 0); err == nil {
		t.Fatal("a timestamp older than 5 minutes verified")
	}
	if rc.bad != 0 {
		t.Fatalf("%d callbacks failed verification", rc.bad)
	}
}

// TestExpiryCallback is HUB-123: a request that waits past its TTL becomes expired and its
// subscriber receives a signed request.expired callback.
func TestExpiryCallback(t *testing.T) {
	q, rc, sched := callbackRig(t, func(id string, _ *registry.Integration, v *registry.IntegrationVersion) {
		if id == "int_evt_brandB" { // the dev config subscribes to completed and failed only
			v.Webhooks = append(slices.Clone(v.Webhooks), registry.Subscription{Event: "request.expired", Endpoint: "reg_orders_cb"})
		}
	})
	q.route(t, mockvendor.Behaviour{Status: 200, Body: okVendor})
	_, body := q.invoke(t, "int_evt_brandB", trackBody)
	id := body["request_id"].(string)
	if _, err := q.db.Exec(t.Context(), `UPDATE hub.outbox SET expires_at = now() - interval '1 second', next_attempt_at = now() + interval '1 hour' WHERE request_id = $1`, id); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	sched.Tick, sched.Standby = 50*time.Millisecond, 100*time.Millisecond
	go sched.Run(ctx)
	eventually(t, "the expiry callback", func() bool { ev, _, _ := rc.get(id); return ev != nil })
	ev, _, _ := rc.get(id)
	if ev["type"] != "request.expired" {
		t.Fatalf("callback %v", ev)
	}
	if _, st := q.status(t, devKey, id); st["status"] != "expired" || st["error"].(map[string]any)["code"] != "EXPIRED" {
		t.Fatalf("status %v", st)
	}
}
