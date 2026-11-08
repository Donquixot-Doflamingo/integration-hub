//go:build integration

package delivery

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/callbacks"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/registry"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/telemetry"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/testkit/mockvendor"
)

// subscribedSnap is the pass-through snapshot with a request.completed subscription on every
// version of int_evt_brandA and the tenant's registrar endpoint it points at.
func subscribedSnap(snap *registry.Snapshot) *registry.Snapshot {
	cp := *passThroughSnap(snap)
	for _, v := range cp.Integrations["int_evt_brandA"].Versions {
		v.Webhooks = []registry.Subscription{{Event: "request.completed", Endpoint: "reg_hub449"}}
	}
	cp.Registrar = map[string]*registry.RegistrarEndpoint{
		"reg_hub449": {EndpointID: "reg_hub449", Tenant: "orders-service", URL: "https://example.com/cb", SecretRef: "localfile://reg_hub449"},
	}
	return &cp
}

// TestTerminalSucceedSurvivesAnyResultShape (HUB-449): a pass-through result can be an array, a
// string or null (204). Whatever its shape, the request ends succeeded after exactly one vendor
// call; a result the callback cannot carry is skipped, never rolls the transition back (which
// used to leave the request in flight, call the vendor again and dead-letter it as poison).
func TestTerminalSucceedSurvivesAnyResultShape(t *testing.T) {
	text := "accepted"
	cases := []struct {
		name       string
		behaviour  mockvendor.Behaviour
		forceBytes string // replaces the result handed to the hook: a payload the callback cannot carry
		wantRows   int
		wantResult string
	}{
		{"object", mockvendor.Behaviour{Status: 200, Body: json.RawMessage(`{"ok":true}`)}, "", 1, `{"ok":true}`},
		{"array", mockvendor.Behaviour{Status: 200, Body: json.RawMessage(`[{"id":1},{"id":2}]`)}, "", 1, `[{"id":1},{"id":2}]`},
		{"string", mockvendor.Behaviour{Status: 200, BodyRaw: &text, Headers: map[string]string{"Content-Type": "text/plain"}}, "", 1, `"accepted"`},
		{"empty body is null", mockvendor.Behaviour{Status: 204}, "", 1, `null`},
		{"unusable payload is skipped", mockvendor.Behaviour{Status: 200, Body: json.RawMessage(`{"ok":true}`)}, `{"cut`, 0, `{"ok":true}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newRig(t)
			r.snaps.s.Store(subscribedSnap(r.snaps.Snapshot()))
			r.route(c.behaviour)
			m := telemetry.NewMetrics("worker", "test")
			v, err := callbacks.NewSchemaValidator(os.DirFS("../../../contracts"))
			if err != nil {
				t.Fatal(err)
			}
			enq, err := callbacks.NewEnqueuer(r.snaps, m, callbacks.EnqueueOptions{Validator: v})
			if err != nil {
				t.Fatal(err)
			}
			hook := CallbackHook(enq)
			r.w.Transitions.OnTerminal = func(ctx context.Context, tx pgx.Tx, ev TerminalEvent) error {
				if c.forceBytes != "" {
					ev.Result = json.RawMessage(c.forceBytes)
				}
				return hook(ctx, tx, ev)
			}
			env := r.enqueue("int_evt_brandA", 6*time.Hour)
			t.Cleanup(func() {
				_, _ = r.db.Exec(context.Background(), `DELETE FROM hub.callback_deliveries WHERE request_id = $1`, env.RequestID)
			})
			if r.process() != 1 {
				t.Fatal("not claimed")
			}
			if s, n := r.status(env.RequestID); s != "succeeded" || n != 1 {
				t.Fatalf("status %s attempts %d, want succeeded after 1", s, n)
			}
			if reqs := r.mv.Requests(t, nil); len(reqs) != 1 {
				t.Fatalf("vendor called %d times, want once", len(reqs))
			}
			var inQueue int
			if err := r.db.QueryRow(t.Context(), `SELECT count(*) FROM hub.outbox WHERE request_id = $1`, env.RequestID).Scan(&inQueue); err != nil || inQueue != 0 {
				t.Fatalf("envelope still queued (%d, %v): the terminal transaction rolled back", inQueue, err)
			}
			var rows int
			if err := r.db.QueryRow(t.Context(), `SELECT count(*) FROM hub.callback_deliveries WHERE request_id = $1`, env.RequestID).Scan(&rows); err != nil || rows != c.wantRows {
				t.Fatalf("callback deliveries %d (%v), want %d", rows, err, c.wantRows)
			}
			var got []byte
			if err := r.db.QueryRow(t.Context(), `SELECT result FROM hub.request_results WHERE request_id = $1`, env.RequestID).Scan(&got); err != nil {
				t.Fatal(err)
			}
			var g, w any
			_ = json.Unmarshal(got, &g)
			_ = json.Unmarshal([]byte(c.wantResult), &w)
			if !jsonEqual(g, w) {
				t.Fatalf("stored result %s, want %s", got, c.wantResult)
			}
			if c.wantRows == 1 {
				var payload []byte
				if err := r.db.QueryRow(t.Context(), `SELECT payload FROM hub.callback_deliveries WHERE request_id = $1`, env.RequestID).Scan(&payload); err != nil {
					t.Fatal(err)
				}
				var doc struct {
					Data struct {
						Result any `json:"result"`
					} `json:"data"`
				}
				_ = json.Unmarshal(payload, &doc)
				if !jsonEqual(doc.Data.Result, w) {
					t.Fatalf("callback result %v, want %s", doc.Data.Result, c.wantResult)
				}
			}
		})
	}
}
