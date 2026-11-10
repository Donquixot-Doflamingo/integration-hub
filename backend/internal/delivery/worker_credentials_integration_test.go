//go:build integration

package delivery

import (
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/creds"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/telemetry"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/testkit/mockvendor"
)

// TestWorkerCredentialMissDoesNotCountAnAttempt is HUB-453: a credential cache miss sends
// nothing to the vendor, so the worker puts the envelope back without counting an attempt (the
// 8-step schedule would dead-letter healthy requests during a credential outage of about 2 h),
// counts it in worker_credentials_deferred_total, and delivers it as a first attempt once the
// credentials are back.
func TestWorkerCredentialMissDoesNotCountAnAttempt(t *testing.T) {
	r := newRig(t)
	r.route(mockvendor.Behaviour{Status: 200, Body: okBody})
	reg := prometheus.NewRegistry()
	r.w.RegisterMetrics(reg)
	cold, err := creds.NewCache(staticLoader(testCreds), telemetry.NewSecretSet(), telemetry.NewMetrics("worker", "cold"), creds.CacheOptions{})
	if err != nil {
		t.Fatal(err)
	}
	warm := r.w.Attempter.Creds
	r.w.Attempter.Creds = cold // never warmed: every Get is a miss
	e := r.enqueue("int_evt_brandA", 6*time.Hour)

	// 12 rounds, past the whole 8-step schedule (the last steps are an hour).
	for round := range 12 {
		if n := r.process(); n != 1 {
			t.Fatalf("round %d: claimed %d, want 1", round, n)
		}
		status, attempt, crashes, _ := r.outboxRow(e.RequestID)
		if status != "accepted" || attempt != 0 || crashes != 0 {
			t.Fatalf("round %d: %s attempt %d crashes %d, want accepted with no attempt counted", round, status, attempt, crashes)
		}
		r.advance(10 * time.Second)
	}
	if n := len(r.mv.Requests(t, nil)); n != 0 {
		t.Fatalf("the vendor got %d calls during the credential outage", n)
	}
	if got := testutil.ToFloat64(r.w.metrics.credsDeferred.WithLabelValues("ct-brandA-in")); got != 12 {
		t.Fatalf("worker_credentials_deferred_total = %v, want 12", got)
	}

	r.w.Attempter.Creds = warm
	if n := r.process(); n != 1 {
		t.Fatalf("claimed %d after recovery, want 1", n)
	}
	if s, attempts := r.status(e.RequestID); s != "succeeded" || attempts != 1 {
		t.Fatalf("ended %s after %d attempts, want succeeded on its first", s, attempts)
	}
}
