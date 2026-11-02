//go:build integration

package adminapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/redis/go-redis/v9"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/control"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/registry"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/testkit/contract"
)

// adminRig is a full admin server on the shared dev database: an empty snapshot archive (the
// publisher starts from backend/config), Redis pings, a dev issuer.
type adminRig struct {
	s    *Server
	h    http.Handler
	iss  *DevIssuer
	pub  *control.Publisher
	spec *contract.Spec
	t    *testing.T
}

func newAdminRig(t *testing.T) *adminRig {
	t.Helper()
	pool := testPool(t)
	// The archive is global: every rig starts from the dev config tree.
	if _, err := pool.Exec(t.Context(), `TRUNCATE hub.config_snapshots`); err != nil {
		t.Fatal(err)
	}
	rdb := redis.NewClient(&redis.Options{Addr: os.Getenv("HUB_REDIS_ADDR")})
	t.Cleanup(func() { _ = rdb.Close() })
	archive := control.NewArchive(pool, control.DirBlobStore{Root: t.TempDir()})
	pub := &control.Publisher{Pool: pool, Archive: archive, Guard: control.RetireGuard(pool),
		Bootstrap: registry.Sources{Config: os.DirFS("../../../config"), Contracts: os.DirFS("../../../../contracts")},
		Notify:    func(ctx context.Context, v int64) { rdb.Publish(ctx, control.SnapshotChannel, v) }}
	snapshot := func() *registry.Snapshot {
		_, snap, err := pub.Current(context.Background())
		if err != nil {
			return nil
		}
		return snap
	}
	iss, err := NewDevIssuer("http://admin.test")
	if err != nil {
		t.Fatal(err)
	}
	s := New(Deps{Pool: pool, Redis: rdb, Verifier: iss.Verifier(), Publisher: pub, Archive: archive, Snapshot: snapshot})
	return &adminRig{s: s, h: s.Handler(), iss: iss, pub: pub, spec: contract.Load(t, "hub-admin.v1.yaml"), t: t}
}

// do sends a contract-validated request as role and decodes the JSON answer.
func (a *adminRig) do(role Role, method, path, body string) (*httptest.ResponseRecorder, map[string]any) {
	a.t.Helper()
	var rd *strings.Reader
	if body != "" {
		rd = strings.NewReader(body)
	} else {
		rd = strings.NewReader("")
	}
	req := httptest.NewRequestWithContext(a.t.Context(), method, Base+path, rd)
	req.Header.Set("Authorization", "Bearer "+token(a.t, a.iss, role, "orders-service"))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := a.spec.Serve(a.t, a.h, req)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec, out
}

// mutationCase is one valid call of a mutating operation: role, method + path, body. Resource
// test files add theirs in init(); TestEveryMutationWritesOneChange requires one per route.
type mutationCase struct {
	role         Role
	method, path string
	body         string
	// setup prepares state the call needs (may be nil).
	setup func(a *adminRig)
}

var reasonRE = regexp.MustCompile(`"reason":"[^"]*"`)

var mutationCases = map[string]mutationCase{
	"pauseIntegration":  {role: RoleEditor, method: http.MethodPost, path: "/integrations/int_evt_brandA/pause", body: `{"reason":"vendor incident"}`},
	"resumeIntegration": {role: RoleEditor, method: http.MethodPost, path: "/integrations/int_evt_brandA/resume", body: `{"reason":"vendor recovered"}`},
}

// TestEveryMutationWritesOneChange is HUB-201: every mutating route writes exactly one
// change_log row, carrying the request's id, actor and roles, and returns its change_id; a
// missing reason is 400 and writes nothing.
func TestEveryMutationWritesOneChange(t *testing.T) {
	a := newAdminRig(t)
	var ops []string
	for _, rt := range a.s.Routes() {
		if rt.Mutating {
			ops = append(ops, rt.Op)
		}
	}
	slices.Sort(ops)
	for _, op := range ops {
		t.Run(op, func(t *testing.T) {
			a.t = t
			c, ok := mutationCases[op]
			if !ok {
				t.Fatalf("no mutation case for %s: add one to mutationCases", op)
			}
			if c.setup != nil {
				c.setup(a)
			}
			if blank := reasonRE.ReplaceAllString(c.body, `"reason":"  "`); blank != c.body {
				req := httptest.NewRequestWithContext(t.Context(), c.method, Base+c.path, strings.NewReader(blank))
				req.Header.Set("Authorization", "Bearer "+token(t, a.iss, c.role))
				req.Header.Set("Content-Type", "application/json")
				refused := a.spec.ServeInvalid(t, a.h, req)
				if refused.Code != http.StatusBadRequest {
					t.Fatalf("with a blank reason: %d %s", refused.Code, refused.Body)
				}
				if n := changesOf(t, a, refused.Header().Get("X-Hub-Request-Id")); n != 0 {
					t.Fatalf("a refused mutation wrote %d change(s)", n)
				}
			}
			resp, body := a.do(c.role, c.method, c.path, c.body)
			if resp.Code >= 300 {
				t.Fatalf("%s: %d %s", op, resp.Code, resp.Body)
			}
			id := resp.Header().Get("X-Hub-Request-Id")
			var n int
			var action, actor string
			var roles []string
			if err := a.s.d.Pool.QueryRow(t.Context(), `SELECT count(*) OVER (), action, actor, actor_roles FROM hub.change_log WHERE request_id = $1`, id).
				Scan(&n, &action, &actor, &roles); err != nil {
				t.Fatalf("change row of %s: %v", id, err)
			}
			if n != 1 || action != op || actor != string(c.role)+"@example.com" || len(roles) != 1 || roles[0] != string(c.role) {
				t.Fatalf("change rows %d action %s actor %s roles %v", n, action, actor, roles)
			}
			if body["change_id"] == nil {
				t.Fatalf("no change_id in %v", body)
			}
		})
	}
}

func changesOf(t *testing.T, a *adminRig, requestID string) int {
	t.Helper()
	var n int
	_ = a.s.d.Pool.QueryRow(t.Context(), `SELECT count(*) FROM hub.change_log WHERE request_id = $1`, requestID).Scan(&n)
	return n
}

// TestPauseAndResume: pause flips the snapshot's kill switch with its reason, resume clears it,
// pausing twice still logs.
func TestPauseAndResume(t *testing.T) {
	a := newAdminRig(t)
	resp, body := a.do(RoleEditor, http.MethodPost, "/integrations/int_evt_brandA/pause", `{"reason":"true"}`)
	in, _ := body["integration"].(map[string]any)
	if resp.Code != 200 || in["paused"] != true || in["paused_reason"] != "true" {
		t.Fatalf("pause: %d %v", resp.Code, body)
	}
	_, snap, _ := a.pub.Current(t.Context())
	if !snap.Integrations["int_evt_brandA"].Paused || snap.Integrations["int_evt_brandA"].PausedReason != "true" {
		t.Fatal("the published snapshot is not paused")
	}
	if resp, _ := a.do(RoleEditor, http.MethodPost, "/integrations/int_evt_brandA/pause", `{"reason":"again"}`); resp.Code != 200 {
		t.Fatalf("pause twice: %d", resp.Code)
	}
	resp, body = a.do(RoleEditor, http.MethodPost, "/integrations/int_evt_brandA/resume", `{"reason":"fixed"}`)
	in, _ = body["integration"].(map[string]any)
	if resp.Code != 200 || in["paused"] != false || in["paused_reason"] != nil {
		t.Fatalf("resume: %d %v", resp.Code, body)
	}
	if resp, body := a.do(RoleEditor, http.MethodPost, "/integrations/int_nope/pause", `{"reason":"no such integration"}`); resp.Code != 404 {
		t.Fatalf("unknown integration: %d %v", resp.Code, body)
	}
}
