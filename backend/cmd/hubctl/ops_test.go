package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDryRunPrintsTheVendorRequestRedacted(t *testing.T) {
	var sent map[string]any
	url := fakeAdmin(t, map[string]any{
		"GET /admin/v1/integrations/int_evt_brandA": map[string]any{"integration_id": "int_evt_brandA", "active": map[string]any{"version": 2, "spec": map[string]any{
			"targets": []any{
				map[string]any{"account_id": "ct-brandB-us", "weight": 10, "transformer": map[string]any{"request": "T1@7", "response": "R1@2"}},
				map[string]any{"account_id": "ct-brandA-in", "weight": 90, "transformer": map[string]any{"request": "T1@7", "response": "R2@1"}},
			}}}},
		"POST /admin/v1/transformers/dry-run": func(r *http.Request) any {
			_ = json.NewDecoder(r.Body).Decode(&sent)
			return map[string]any{"ok": true, "kind": "request", "compile_errors": []any{}, "vendor_request": map[string]any{
				"method": "POST", "url": "https://in1.api.clevertap.example/1/upload",
				"headers": map[string]string{"X-CleverTap-Account-Id": "***", "X-CleverTap-Passcode": "leaked-by-a-bug", "Content-Type": "application/json"},
				"body":    map[string]any{"d": []any{map[string]any{"identity": "u_1"}}}}}
		},
	})
	t.Setenv("HUB_ADMIN_TOKEN", "tok")
	body := filepath.Join(t.TempDir(), "body.json")
	_ = os.WriteFile(body, []byte(`{"user":{"id":"u_1"},"event":{"name":"order_placed"}}`), 0o600)
	var out, errb bytes.Buffer
	if code := run([]string{"dry-run", "-admin-url", url, "int_evt_brandA", body}, &out, &errb); code != 0 {
		t.Fatalf("dry-run %d: %s", code, errb.String())
	}
	got := out.String()
	if sent["account_id"] != "ct-brandA-in" || sent["ref"] != "T1@7" || sent["input"].(map[string]any)["user"] == nil {
		t.Fatalf("dry-run request %v", sent)
	}
	if !strings.Contains(got, "POST https://in1.api.clevertap.example/1/upload") || !strings.Contains(got, "X-CleverTap-Passcode: ***") ||
		strings.Contains(got, "leaked-by-a-bug") || !strings.Contains(got, `"identity": "u_1"`) {
		t.Fatalf("dry-run output:\n%s", got)
	}
}

func TestReplayCountsThenNeedsYes(t *testing.T) {
	var calls []map[string]any
	url := fakeAdmin(t, map[string]any{
		"POST /admin/v1/dlq/replay": func(r *http.Request) any {
			var in map[string]any
			_ = json.NewDecoder(r.Body).Decode(&in)
			calls = append(calls, in)
			if in["dry_run"] == true {
				return map[string]any{"change_id": nil, "matched": 42, "affected": 0, "truncated": false, "dry_run": true}
			}
			return map[string]any{"change_id": "chg_01J9Z4B2C3D4E5F6G7H8J9K0NM", "matched": 42, "affected": 42, "truncated": false, "dry_run": false}
		},
	})
	t.Setenv("HUB_ADMIN_TOKEN", "tok")
	base := []string{"replay", "-admin-url", url, "-integration", "int_evt_brandA", "-error-code", "UPSTREAM_REJECTED", "-reason", "T1@8 fixes names"}
	var out, errb bytes.Buffer
	if code := run(base, &out, &errb); code != 2 || !strings.Contains(out.String(), "42 dead letter(s) match") || len(calls) != 1 {
		t.Fatalf("without -yes: %d %q %q %d calls", code, out.String(), errb.String(), len(calls))
	}
	out.Reset()
	if code := run(append(base, "-dry-run"), &out, &errb); code != 0 || len(calls) != 2 {
		t.Fatalf("-dry-run: %d %d calls", code, len(calls))
	}
	out.Reset()
	if code := run(append(base, "-yes"), &out, &errb); code != 0 || !strings.Contains(out.String(), "42 dead letter(s) replayed") || len(calls) != 4 {
		t.Fatalf("-yes: %d %q %d calls", code, out.String(), len(calls))
	}
	f := calls[3]["filter"].(map[string]any)
	if f["integration_id"] != "int_evt_brandA" || f["reason"] != "UPSTREAM_REJECTED" || f["replayed"] != false || calls[3]["reason"] != "T1@8 fixes names" {
		t.Fatalf("replay request %v", calls[3])
	}
	if code := run([]string{"replay", "-admin-url", url, "-reason", "no filter"}, &out, &errb); code != 2 {
		t.Fatalf("replay without a filter: %d", code)
	}
}
