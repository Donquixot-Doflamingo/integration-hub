//go:build integration

package edge

import (
	"testing"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/audit"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/registry"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/sessions"
)

// TestSessionStepTwoStaysOnOpeningVersion is HUB-183 (DESIGN §6.3 row 6) for an account and
// transformer swap, in sync and wait mode: step 2 of a session opened on v7 is served by v7's
// account and transformers after v8 is published; a session opened after the publish uses v8.
func TestSessionStepTwoStaysOnOpeningVersion(t *testing.T) {
	for _, mode := range []registry.Mode{registry.ModeSync, registry.ModeWait} {
		t.Run(string(mode), func(t *testing.T) {
			s := newSessionRig(t, mode)

			// Step 1 on v7 opens a session.
			s.vendorAnswers(t, "txn-v7")
			rec, body := s.step1(t)
			sid := rec.Header().Get("X-Hub-Session")
			if rec.Code != 200 || !sessions.IDPattern.MatchString(sid) || body["vendor_ref"] != "txn-v7" {
				t.Fatalf("step 1: %d session %q %s", rec.Code, sid, rec.Body)
			}
			wantVendor(t, s.lastVendorCall(t), "TA@1", "ct-brandA-in", "none", "none")
			if v, acct, txn := s.row(t, sid); v != 7 || acct != "ct-brandA-in" || txn != "txn-v7" {
				t.Fatalf("session row v%d %s %s", v, acct, txn)
			}
			opened := s.sink.eventuallyMeta(t, audit.ResponseTransformed)
			meta, _ := opened[len(opened)-1]["session"].(map[string]any)
			if meta["id"] != sid || meta["event"] != "opened" || meta["version"] != 7 || meta["account"] != "ct-brandA-in" {
				t.Fatalf("open not audited: %v", opened)
			}
			if _, leaked := meta["data"]; leaked {
				t.Fatal("session data in the audit meta")
			}

			// Publish v8 (new account and transformers).
			s.publish(t, 8)
			s.sink.reset()

			// Step 2 of the v7 session: v7's account and transformers, with the session's data.
			s.vendorAnswers(t, "txn-step2")
			rec, _ = s.step2(t, sid)
			if rec.Code != 200 || rec.Header().Get("X-Hub-Integration-Version") != "7" || rec.Header().Get("X-Hub-Session") != "" {
				t.Fatalf("step 2: %d version %q session %q %s", rec.Code, rec.Header().Get("X-Hub-Integration-Version"), rec.Header().Get("X-Hub-Session"), rec.Body)
			}
			wantVendor(t, s.lastVendorCall(t), "TA@1", "ct-brandA-in", "txn-v7", "RA@1")
			used := s.sink.eventuallyMeta(t, audit.IntegrationResolved)
			um, _ := used[0]["session"].(map[string]any)
			if um["id"] != sid || um["event"] != "used" || used[0]["version"] != 7 || used[0]["account"] != "ct-brandA-in" {
				t.Fatalf("use not audited: %v", used)
			}

			// A new session after the publish opens on v8, and its step 2 stays there.
			s.vendorAnswers(t, "txn-v8")
			rec, _ = s.step1(t)
			sid8 := rec.Header().Get("X-Hub-Session")
			if rec.Code != 200 || sid8 == "" || sid8 == sid || rec.Header().Get("X-Hub-Integration-Version") != "8" {
				t.Fatalf("new step 1: %d %q %s", rec.Code, sid8, rec.Body)
			}
			wantVendor(t, s.lastVendorCall(t), "TB@1", "ct-brandB-us", "none", "none")
			if v, acct, _ := s.row(t, sid8); v != 8 || acct != "ct-brandB-us" {
				t.Fatalf("v8 session row v%d %s", v, acct)
			}
			s.vendorAnswers(t, "txn-step2")
			if rec, _ = s.step2(t, sid8); rec.Code != 200 {
				t.Fatalf("v8 step 2: %d %s", rec.Code, rec.Body)
			}
			wantVendor(t, s.lastVendorCall(t), "TB@1", "ct-brandB-us", "txn-v8", "RB@1")

			// The old session is still pinned to v7 after the second session opened.
			if rec, _ = s.step2(t, sid); rec.Code != 200 || rec.Header().Get("X-Hub-Integration-Version") != "7" {
				t.Fatalf("v7 step 2 again: %d", rec.Code)
			}
			if n, err := sessions.CountOpen(t.Context(), s.db, "ct-brandA-in"); err != nil || n < 1 {
				t.Fatalf("CountOpen %d %v", n, err)
			}
		})
	}
}

func (s *sessionRig) row(t *testing.T, id string) (version int, account, txn string) {
	t.Helper()
	if err := s.db.QueryRow(t.Context(), `SELECT version, account_id, coalesce(vendor_txn_id, '') FROM hub.vendor_sessions WHERE session_id = $1`, id).
		Scan(&version, &account, &txn); err != nil {
		t.Fatalf("session row %s: %v", id, err)
	}
	return version, account, txn
}

// eventMeta returns the meta of every recorded event of type typ.
func (s *recordingSink) eventMeta(typ string) []map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []map[string]any
	for _, b := range s.batches {
		for _, e := range b.Events {
			if e.Type == typ {
				out = append(out, e.Meta)
			}
		}
	}
	return out
}

func (s *recordingSink) reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.batches = nil
}

// eventuallyMeta waits for the audit batch of an attempt that may finish after the response.
func (s *recordingSink) eventuallyMeta(t *testing.T, typ string) []map[string]any {
	t.Helper()
	var got []map[string]any
	eventually(t, typ+" audited", func() bool { got = s.eventMeta(typ); return len(got) > 0 })
	return got
}
