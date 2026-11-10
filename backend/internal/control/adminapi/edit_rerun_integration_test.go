//go:build integration

package adminapi

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/creds"
)

// raceFirst makes the first n commits lose a cross-replica race (HUB-451), so Publisher.Edit
// re-runs the callback; it returns the number of commits attempted.
func raceFirst(a *adminRig, n int) *atomic.Int32 {
	var calls atomic.Int32
	a.pub.SimulateRace = func() bool { return int(calls.Add(1)) <= n }
	return &calls
}

const createAccountBody = `{"account_id":"ct-rerun","vendor_id":"clevertap","region":"in","config":{},"credentials":{"passcode":"rerun-passcode-0001"},"reason":"new workspace"}`

// An Edit callback with a credential-store side effect survives being re-run: the entry its
// first run wrote is kept (not a spurious 409), holds this request's secret once, and is removed
// when every attempt fails.
func TestCreateAccountSurvivesAnEditRerun(t *testing.T) {
	a := newAdminRig(t)
	withRotator(a)
	calls := raceFirst(a, 2)
	resp, body := a.do(RoleAdmin, http.MethodPost, "/accounts", createAccountBody)
	if resp.Code != http.StatusCreated || calls.Load() != 3 {
		t.Fatalf("create after two lost races: %d (commits %d) %s", resp.Code, calls.Load(), resp.Body)
	}
	_ = body
	sec, found, err := a.s.d.Rotator.ReadCredentials("ct-rerun")
	if err != nil || !found || sec.Active.Values["passcode"] != "rerun-passcode-0001" || sec.Next != nil {
		t.Fatalf("entry %+v found=%v err=%v: want this request's secret exactly once", sec, found, err)
	}
}

func TestCreateAccountRemovesItsCredentialsWhenEveryAttemptFails(t *testing.T) {
	a := newAdminRig(t)
	withRotator(a)
	raceFirst(a, 1000)
	resp, _ := a.do(RoleAdmin, http.MethodPost, "/accounts", createAccountBody)
	if resp.Code != http.StatusConflict {
		t.Fatalf("every attempt lost: %d %s, want 409", resp.Code, resp.Body)
	}
	if _, found, err := a.s.d.Rotator.ReadCredentials("ct-rerun"); err != nil || found {
		t.Fatalf("credentials left behind: found=%v err=%v", found, err)
	}
}

const createRegistrarBody = `{"endpoint_id":"reg_rerun","tenant_id":"orders-service","url":"https://hooks.example.com/cb","reason":"callbacks"}`

func TestCreateRegistrarSurvivesAnEditRerun(t *testing.T) {
	a := newAdminRig(t)
	withRotator(a)
	calls := raceFirst(a, 2)
	resp, body := a.do(RoleAdmin, http.MethodPost, "/registrar-endpoints", createRegistrarBody)
	if resp.Code != http.StatusCreated || calls.Load() != 3 {
		t.Fatalf("create after two lost races: %d (commits %d) %s", resp.Code, calls.Load(), resp.Body)
	}
	sec, found, err := a.s.d.Rotator.ReadCredentials("reg_rerun")
	if err != nil || !found || sec.Active.Values[creds.SigningSecretKey] != body["signing_secret"] {
		t.Fatalf("entry %+v found=%v err=%v: want the secret returned to the caller", sec, found, err)
	}
}

func TestCreateRegistrarRemovesItsSecretWhenEveryAttemptFails(t *testing.T) {
	a := newAdminRig(t)
	withRotator(a)
	raceFirst(a, 1000)
	resp, _ := a.do(RoleAdmin, http.MethodPost, "/registrar-endpoints", createRegistrarBody)
	if resp.Code != http.StatusConflict {
		t.Fatalf("every attempt lost: %d %s, want 409", resp.Code, resp.Body)
	}
	if _, found, err := a.s.d.Rotator.ReadCredentials("reg_rerun"); err != nil || found {
		t.Fatalf("secret left behind: found=%v err=%v", found, err)
	}
}

// A re-run of the rotation starts from the secrets read before this request wrote anything,
// so the previous secret becomes `next` and is not replaced by the request's own first write.
func TestRotateRegistrarSecretSurvivesAnEditRerun(t *testing.T) {
	a := newAdminRig(t)
	withRotator(a)
	before, _, _ := a.s.d.Rotator.ReadCredentials("reg_orders_cb")
	calls := raceFirst(a, 1)
	resp, body := a.do(RoleAdmin, http.MethodPost, "/registrar-endpoints/reg_orders_cb/rotate-secret", `{"overlap_ms":86400000,"reason":"scheduled rotation"}`)
	if resp.Code != http.StatusOK || calls.Load() != 2 {
		t.Fatalf("rotate after a lost race: %d (commits %d) %s", resp.Code, calls.Load(), resp.Body)
	}
	sec, _, _ := a.s.d.Rotator.ReadCredentials("reg_orders_cb")
	if sec.Active.Values[creds.SigningSecretKey] != body["signing_secret"] || sec.Active.Version != before.Active.Version+1 {
		t.Fatalf("active %+v, want the new secret at version %d", sec.Active, before.Active.Version+1)
	}
	if sec.Next == nil || sec.Next.Values[creds.SigningSecretKey] != before.Active.Values[creds.SigningSecretKey] {
		t.Fatalf("next %+v, want the secret that was active before the request", sec.Next)
	}
}

func TestRotateRegistrarSecretRestoresWhenEveryAttemptFails(t *testing.T) {
	a := newAdminRig(t)
	withRotator(a)
	before, _, _ := a.s.d.Rotator.ReadCredentials("reg_orders_cb")
	raceFirst(a, 1000)
	// rotateRegistrarSecret does not declare 409 in the contract, so the response is not validated.
	rec := httptest.NewRecorder()
	a.h.ServeHTTP(rec, a.request(RoleAdmin, http.MethodPost, "/registrar-endpoints/reg_orders_cb/rotate-secret", `{"overlap_ms":86400000,"reason":"scheduled rotation"}`))
	if rec.Code != http.StatusConflict {
		t.Fatalf("every attempt lost: %d %s", rec.Code, rec.Body)
	}
	after, _, _ := a.s.d.Rotator.ReadCredentials("reg_orders_cb")
	if after.Active.Version != before.Active.Version || after.Active.Values[creds.SigningSecretKey] != before.Active.Values[creds.SigningSecretKey] || after.Next != nil {
		t.Fatalf("secrets not restored: %+v, want %+v", after, before)
	}
}
