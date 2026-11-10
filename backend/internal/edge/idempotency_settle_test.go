package edge

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/idempotency"
)

// fakeIdem is an in-memory Idempotency that records how each claim was settled.
type fakeIdem struct {
	mu          sync.Mutex
	state       map[string]string // key -> "p" in progress | "t" completed, not stored | "c" completed
	owner       map[string]string
	completeErr error
	marks       int
	releases    int
}

func newFakeIdem(completeErr error) *fakeIdem {
	return &fakeIdem{state: map[string]string{}, owner: map[string]string{}, completeErr: completeErr}
}

func (f *fakeIdem) Begin(_ context.Context, k idempotency.Key, _, rid string) (idempotency.Outcome, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch f.state[k.Key] {
	case "p":
		return idempotency.Outcome{State: idempotency.InProgress, RequestID: f.owner[k.Key]}, nil
	case "t":
		return idempotency.Outcome{State: idempotency.CompletedNotStored, RequestID: f.owner[k.Key]}, nil
	}
	f.state[k.Key], f.owner[k.Key] = "p", rid
	return idempotency.Outcome{State: idempotency.Fresh}, nil
}

func (f *fakeIdem) Complete(_ context.Context, k idempotency.Key, _ string, _ idempotency.Response) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.completeErr != nil {
		return f.completeErr
	}
	f.state[k.Key] = "c"
	return nil
}

func (f *fakeIdem) MarkCompleted(_ context.Context, k idempotency.Key, _ string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.marks++
	f.state[k.Key] = "t"
	return nil
}

func (f *fakeIdem) Release(_ context.Context, k idempotency.Key, _ string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.releases++
	delete(f.state, k.Key)
	return nil
}

// A request that succeeded must never free its Idempotency-Key, even when the replay copy cannot
// be stored: a retry would call the vendor again (double charge). Failed requests still release.
func TestSettleNeverReleasesAfterASuccessfulCall(t *testing.T) {
	down := &idempotency.UnavailableError{Op: "complete", Err: errors.New("i/o timeout")}
	tests := []struct {
		name        string
		completeErr error
		first       step
		wantStatus  int
		wantMarks   int
		wantRelease int
		retry       bool // send the same key again
		retryStatus int
		retryCalls  int // vendor calls in total after the retry
	}{
		{name: "stored", first: ok200(), wantStatus: 200},
		{name: "too large: key kept, retry answers conflict", completeErr: idempotency.ErrTooLarge, first: ok200(),
			wantStatus: 200, wantMarks: 1, retry: true, retryStatus: 409, retryCalls: 1},
		{name: "store down at settle: key kept", completeErr: down, first: ok200(),
			wantStatus: 200, wantMarks: 1, retry: true, retryStatus: 409, retryCalls: 1},
		{name: "lease lost: nothing to keep or release", completeErr: idempotency.ErrNotOwner, first: ok200(), wantStatus: 200},
		{name: "vendor rejected: key released, retry runs fresh", first: step{status: 400, body: map[string]any{"status": "fail", "error": "bad"}},
			wantStatus: 422, wantRelease: 1, retry: true, retryStatus: 200, retryCalls: 2},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			up := &fakeUpstream{steps: []step{tc.first, ok200()}}
			idem := newFakeIdem(tc.completeErr)
			h, _ := invokeServer(t, devSnapshot(t), up, func(d *Deps) { d.Idempotency = idem })
			const target = "/v1/integrations/int_evt_brandA/invoke"
			rec := post(t, h, devKey, target, trackBody, "Idempotency-Key", "k-1")
			if rec.Code != tc.wantStatus {
				t.Fatalf("first: %d %s", rec.Code, rec.Body)
			}
			if idem.marks != tc.wantMarks || idem.releases != tc.wantRelease {
				t.Fatalf("marks %d releases %d, want %d and %d", idem.marks, idem.releases, tc.wantMarks, tc.wantRelease)
			}
			if !tc.retry {
				return
			}
			rec = post(t, h, devKey, target, trackBody, "Idempotency-Key", "k-1")
			if rec.Code != tc.retryStatus {
				t.Fatalf("retry: %d %s", rec.Code, rec.Body)
			}
			if tc.retryStatus == 409 {
				e := errorCode(t, rec.Body.String())
				if e["code"] != "IDEMPOTENCY_CONFLICT" || e["reason"] != "completed_not_stored" || e["retryable"] != false {
					t.Fatalf("retry error %v", e)
				}
			}
			if up.calls() != tc.retryCalls {
				t.Fatalf("vendor called %d times, want %d", up.calls(), tc.retryCalls)
			}
		})
	}
}
