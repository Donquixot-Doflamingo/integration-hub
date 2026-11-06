package main

import (
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

var payoutBody = map[string]any{"user": map[string]any{"id": "u_77"}, "event": map[string]any{"name": "payout_8821"}}

func moneyIntegration(m map[string]any) {
	m["moves_money_or_quota"] = true
	m["idempotency_key_required"] = true
}

// TestConfirmGuard proves contract rule 2 / HUB-304: an integration that moves money or quota is
// never invoked without confirm=true and an idempotency_key; the refusal makes no Hub invoke call
// and echoes a summary of the held-back call.
func TestConfirmGuard(t *testing.T) {
	tests := []struct {
		name     string
		money    bool
		args     map[string]any
		wantOut  string // outcome
		wantCode string
		invokes  int
	}{
		{name: "no confirm, no key", money: true, args: map[string]any{}, wantOut: "refused", wantCode: codeConfirmationRequired},
		{name: "key but no confirm", money: true, args: map[string]any{"idempotency_key": "payout-8821"}, wantOut: "refused", wantCode: codeConfirmationRequired},
		{name: "confirm=false explicitly", money: true, args: map[string]any{"confirm": false, "idempotency_key": "payout-8821"}, wantOut: "refused", wantCode: codeConfirmationRequired},
		{name: "confirm but no key", money: true, args: map[string]any{"confirm": true}, wantOut: "refused", wantCode: codeIdempotencyKeyRequired},
		{name: "confirm and key", money: true, args: map[string]any{"confirm": true, "idempotency_key": "payout-8821"}, wantOut: "completed", invokes: 1},
		{name: "not money: no confirm needed", money: false, args: map[string]any{}, wantOut: "completed", invokes: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hub := newFakeHub(t)
			if tt.money {
				hub.on("getIntegration", hub.pub("getIntegration", 200, "", moneyIntegration))
			} else {
				hub.on("getIntegration", hub.pub("getIntegration", 200, ""))
			}
			hub.on("invokeIntegration", hub.pub("invokeIntegration", 200, "events.track@v1"))
			cs := connect(t, testApp(t, hub.srv.URL, false, nil))
			args := map[string]any{"integration_id": "int_evt_brandA", "body": payoutBody}
			for k, v := range tt.args {
				args[k] = v
			}
			out := callTool(t, cs, toolInvoke, args)
			if out.field(t, "outcome") != tt.wantOut {
				t.Fatalf("outcome %v, want %s: %s", out.field(t, "outcome"), tt.wantOut, out.json)
			}
			if n := len(hub.callsTo("invokeIntegration")); n != tt.invokes {
				t.Fatalf("%d invoke calls, want %d", n, tt.invokes)
			}
			if len(hub.callsTo("getIntegration")) != 1 {
				t.Errorf("flag not checked via getIntegration")
			}
			if tt.wantCode == "" {
				if tt.invokes == 1 && tt.money && hub.callsTo("invokeIntegration")[0].Header.Get("Idempotency-Key") != "payout-8821" {
					t.Error("idempotency key not sent")
				}
				return
			}
			if !out.res.IsError || out.field(t, "error", "code") != tt.wantCode || out.field(t, "http_status") != nil || out.field(t, "request_id") != nil {
				t.Fatalf("refusal: %s", out.json)
			}
			text := out.res.Content[0].(*mcp.TextContent).Text
			for _, want := range []string{"REFUSED", "the Hub was not called", "integration_id=int_evt_brandA", "payout_8821"} {
				if !strings.Contains(text, want) {
					t.Errorf("summary lacks %q: %s", want, text)
				}
			}
		})
	}
}

// TestConfirmGuardFailsClosed: when the flag cannot be read, nothing is invoked.
func TestConfirmGuardFailsClosed(t *testing.T) {
	hub := newFakeHub(t)
	hub.on("getIntegration", hub.pub("getIntegration", 500, "INTERNAL_ERROR"))
	cs := connect(t, testApp(t, hub.srv.URL, false, nil))
	out := callTool(t, cs, toolInvoke, map[string]any{"integration_id": "int_evt_brandA", "body": payoutBody, "confirm": true, "idempotency_key": "k"})
	if out.field(t, "outcome") != "error" || len(hub.callsTo("invokeIntegration")) != 0 {
		t.Fatalf("got %s with %d invokes", out.json, len(hub.callsTo("invokeIntegration")))
	}
}
