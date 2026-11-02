package adminapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/control"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/queue"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/registry"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/upstream"
)

// Circuit breakers (DESIGN §10.4, §16.1, HUB-209): the vendor health board and admin holds.
// The breaker state lives in Redis (hub:breaker:<account>|<vendor>.<endpoint>), shared by
// every data-plane node; a hold is written there and announced on hub:breaker, so every node
// applies it within about a second. Rolling-window counters are node-local and not shared:
// the admin reports them as 0.

func init() { registrars = append(registrars, (*Server).registerBreakers) }

func (s *Server) registerBreakers() {
	s.handle("listBreakers", RoleViewer, false, "GET /breakers", s.listBreakers)
	s.handle("getBreaker", RoleViewer, false, "GET /breakers/{account_id}/{endpoint_ref}", s.getBreaker)
	s.handle("forceOpenBreaker", RoleAdmin, true, "POST /breakers/{account_id}/{endpoint_ref}/force-open", s.forceBreaker(upstream.ForceOpen))
	s.handle("forceCloseBreaker", RoleAdmin, true, "POST /breakers/{account_id}/{endpoint_ref}/force-close", s.forceBreaker(upstream.ForceClosed))
	s.handle("clearBreakerForce", RoleAdmin, true, "POST /breakers/{account_id}/{endpoint_ref}/clear-force", s.forceBreaker(upstream.ForceNone))
}

var errNoSnapshot = errors.New("no snapshot")

// breakerRef is one (account, endpoint) breaker known to the registry.
type breakerRef struct {
	Account, Vendor, Endpoint string // Endpoint: "upload@1"
}

func (b breakerRef) key() upstream.BreakerKey {
	return upstream.BreakerKey{Account: b.Account, Endpoint: b.Vendor + "." + b.Endpoint}
}

// objectID names the breaker in the change log.
func (b breakerRef) objectID() string { return b.Account + "/" + b.Endpoint }

// resolveBreaker checks that the account exists and its vendor has the endpoint.
func resolveBreaker(snap *registry.Snapshot, account, endpoint string) (breakerRef, error) {
	if snap == nil {
		return breakerRef{}, errNoSnapshot
	}
	a := snap.Accounts[account]
	if a == nil {
		return breakerRef{}, notFound("Breaker")
	}
	v := snap.Vendors[a.Vendor]
	if v == nil {
		return breakerRef{}, notFound("Breaker")
	}
	if _, ok := v.Endpoints[endpoint]; !ok {
		return breakerRef{}, notFound("Breaker")
	}
	return breakerRef{Account: account, Vendor: a.Vendor, Endpoint: endpoint}, nil
}

// breakerTargets maps every breaker referenced by an active integration version to its
// integrations (primary and failover targets).
func breakerTargets(snap *registry.Snapshot) map[breakerRef][]string {
	out := map[breakerRef][]string{}
	for id, in := range snap.Integrations {
		v := in.Active()
		if v == nil {
			continue
		}
		for _, t := range append(slices.Clone(v.Targets), v.Failover...) {
			tf := snap.Transformers[t.Transformer.Request]
			if tf == nil {
				continue
			}
			vendor, ep, ok := strings.Cut(tf.VendorEndpoint, ".")
			if !ok {
				continue
			}
			ref := breakerRef{Account: t.Account, Vendor: vendor, Endpoint: ep}
			if !slices.Contains(out[ref], id) {
				out[ref] = append(out[ref], id)
			}
		}
	}
	for ref := range out {
		slices.Sort(out[ref])
	}
	return out
}

// breakerView is the contract Breaker.
type breakerView struct {
	AccountID           string     `json:"account_id"`
	EndpointRef         string     `json:"endpoint_ref"`
	VendorID            string     `json:"vendor_id"`
	State               string     `json:"state"`
	Forced              string     `json:"forced"`
	ForcedUntil         *time.Time `json:"forced_until"`
	FailureRatio        float64    `json:"failure_ratio"`
	WindowCalls         int        `json:"window_calls"`
	ConsecutiveTimeouts int        `json:"consecutive_timeouts"`
	OpenedAt            *time.Time `json:"opened_at"`
	OpenUntil           *time.Time `json:"open_until"`
	ReopenCount         int        `json:"reopen_count"`
	ParkedCount         int        `json:"parked_count"`
	ParkedOldestAgeMS   *int64     `json:"parked_oldest_age_ms"`
	UpdatedAt           time.Time  `json:"updated_at"`
}

func breakerViewOf(ref breakerRef, snap upstream.BreakerSnapshot, parked queue.ParkedGroup, now, fallback time.Time) breakerView {
	v := breakerView{AccountID: ref.Account, EndpointRef: ref.Endpoint, VendorID: ref.Vendor, State: snap.State.String(), Forced: "none",
		ReopenCount: snap.Reopens, ParkedCount: parked.Count, UpdatedAt: fallback.UTC()}
	if snap.Version > 0 {
		v.UpdatedAt = time.UnixMilli(snap.Version).UTC()
	}
	if snap.Held(now) {
		v.Forced = snap.Forced.String()
		if !snap.ForcedUntil.IsZero() {
			u := snap.ForcedUntil.UTC()
			v.ForcedUntil = &u
		}
	}
	if snap.State == upstream.Open {
		opened := v.UpdatedAt
		v.OpenedAt = &opened
		if !snap.OpenUntil.IsZero() {
			u := snap.OpenUntil.UTC()
			v.OpenUntil = &u
		}
	}
	if parked.Count > 0 {
		age := max(now.Sub(parked.Oldest).Milliseconds(), 0)
		v.ParkedOldestAgeMS = &age
	}
	return v
}

// parkedBy groups the parked backlog by breaker key (account + vendor endpoint).
func (s *Server) parkedBy(ctx context.Context) (map[upstream.BreakerKey]queue.ParkedGroup, error) {
	if s.d.Pool == nil {
		return nil, errNoDB
	}
	groups, err := (&queue.Outbox{}).Parked(ctx, s.d.Pool)
	if err != nil {
		return nil, err
	}
	out := map[upstream.BreakerKey]queue.ParkedGroup{}
	for _, g := range groups {
		out[upstream.BreakerKey{Account: g.Account, Endpoint: g.Endpoint}] = g
	}
	return out, nil
}

func (s *Server) listBreakers(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	limit, cursor, err := page(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	q := r.URL.Query()
	snap := s.servingSnapshot()
	if snap == nil {
		s.fail(w, r, errNoSnapshot)
		return
	}
	shared, err := upstream.SharedStates(ctx, s.d.Redis)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	parked, err := s.parkedBy(ctx)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	// Every breaker an active integration uses, plus every one with a shared record.
	refs := map[breakerRef]bool{}
	for ref := range breakerTargets(snap) {
		refs[ref] = true
	}
	for k := range shared {
		vendor, ep, ok := strings.Cut(k.Endpoint, ".")
		if ok {
			refs[breakerRef{Account: k.Account, Vendor: vendor, Endpoint: ep}] = true
		}
	}
	now := s.d.Now()
	items := make([]breakerView, 0, len(refs))
	for ref := range refs {
		v := breakerViewOf(ref, shared[ref.key()], parked[ref.key()], now, snap.BuiltAt)
		if (q.Get("vendor_id") != "" && v.VendorID != q.Get("vendor_id")) || (q.Get("account_id") != "" && v.AccountID != q.Get("account_id")) ||
			(q.Get("state") != "" && v.State != q.Get("state")) {
			continue
		}
		items = append(items, v)
	}
	slices.SortFunc(items, func(a, b breakerView) int {
		return strings.Compare(a.AccountID+"\x00"+a.EndpointRef, b.AccountID+"\x00"+b.EndpointRef)
	})
	if len(cursor) == 2 {
		after := cursor[0] + "\x00" + cursor[1]
		items = slices.DeleteFunc(items, func(v breakerView) bool { return v.AccountID+"\x00"+v.EndpointRef <= after })
	} else if cursor != nil {
		s.fail(w, r, badRequest("", "cursor is not a cursor of this list"))
		return
	}
	var next *string
	if len(items) > limit {
		items = items[:limit]
		next = nextCursor(items[limit-1].AccountID, items[limit-1].EndpointRef)
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "next_cursor": next})
}

// breakerConfig is the contract BreakerConfig of a vendor (defaults filled as the data plane
// fills them).
func breakerConfig(v *registry.Vendor) map[string]any {
	p := upstream.PolicyFrom(v.Defaults.Breaker)
	return map[string]any{"window_ms": p.Window.Milliseconds(), "min_calls": p.MinCalls, "failure_ratio": p.FailureRatio,
		"consecutive_timeouts": max(p.ConsecutiveTimeouts, 1), "open_ms": p.Open.Milliseconds(), "open_max_ms": p.OpenMax.Milliseconds(),
		"half_open_probes": p.HalfOpenProbes}
}

// transition is one recorded breaker transition (admin holds, from the change log; automatic
// transitions are node events, in metrics and logs).
type transition struct {
	At    time.Time `json:"at"`
	From  string    `json:"from"`
	To    string    `json:"to"`
	Cause string    `json:"cause"`
	Actor *string   `json:"actor"`
}

func (s *Server) transitions(ctx context.Context, ref breakerRef) ([]transition, error) {
	rows, err := s.d.Pool.Query(ctx, `SELECT at, actor, action, before, after FROM hub.change_log
WHERE object_type = 'breaker' AND object_id = $1 ORDER BY at DESC LIMIT 50`, ref.objectID())
	if err != nil {
		return nil, err
	}
	out, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (transition, error) {
		var t transition
		var actor, action string
		var before, after map[string]any
		if err := r.Scan(&t.At, &actor, &action, &before, &after); err != nil {
			return t, err
		}
		t.At, t.Actor = t.At.UTC(), &actor
		t.From, _ = before["state"].(string)
		t.To, _ = after["state"].(string)
		t.Cause = "forced"
		if action == "clearBreakerForce" {
			t.Cause = "force_cleared"
		}
		return t, nil
	})
	if err != nil {
		return nil, err
	}
	slices.Reverse(out) // oldest first
	return out, nil
}

func (s *Server) getBreaker(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	snap := s.servingSnapshot()
	ref, err := resolveBreaker(snap, r.PathValue("account_id"), r.PathValue("endpoint_ref"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	shared, _, err := upstream.ReadShared(ctx, s.d.Redis, ref.key())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	parked, err := s.parkedBy(ctx)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	trans, err := s.transitions(ctx, ref)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	integrations := breakerTargets(snap)[ref]
	if integrations == nil {
		integrations = []string{}
	}
	writeJSON(w, http.StatusOK, struct {
		breakerView
		Config       map[string]any  `json:"config"`
		Transitions  []transition    `json:"transitions"`
		LastProbe    json.RawMessage `json:"last_probe"`
		Integrations []string        `json:"integrations"`
	}{breakerViewOf(ref, shared, parked[ref.key()], s.d.Now(), snap.BuiltAt), breakerConfig(snap.Vendors[ref.Vendor]), trans,
		json.RawMessage("null"), integrations})
}

// forceBreaker is force-open, force-close (force) and clear-force (ForceNone). The change log
// row is written in a transaction that commits only after the hold is in Redis, so a failed
// Redis write logs nothing.
func (s *Server) forceBreaker(force upstream.Force) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		var body struct {
			DurationMS *int64 `json:"duration_ms"`
			Reason     string `json:"reason"`
		}
		if err := decode(w, r, &body); err != nil {
			s.fail(w, r, err)
			return
		}
		if force == upstream.ForceNone && body.DurationMS != nil {
			s.fail(w, r, badRequest("/duration_ms", "clear-force takes no duration"))
			return
		}
		reason, err := reasonOf(r, body.Reason)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		if d := body.DurationMS; d != nil && (*d < 1000 || *d > 86_400_000) {
			s.fail(w, r, badRequest("/duration_ms", "duration_ms must be 1000..86400000"))
			return
		}
		snap := s.servingSnapshot()
		ref, err := resolveBreaker(snap, r.PathValue("account_id"), r.PathValue("endpoint_ref"))
		if err != nil {
			s.fail(w, r, err)
			return
		}
		if s.d.Pool == nil {
			s.fail(w, r, errNoDB)
			return
		}
		now := s.d.Now()
		var until time.Time
		if body.DurationMS != nil {
			until = now.Add(time.Duration(*body.DurationMS) * time.Millisecond)
		}
		if force == upstream.ForceNone {
			cur, _, err := upstream.ReadShared(ctx, s.d.Redis, ref.key())
			if err != nil {
				s.fail(w, r, err)
				return
			}
			if !cur.Held(now) {
				s.fail(w, r, &Error{Status: http.StatusConflict, Code: CodeConflict, Message: "The breaker is not forced"})
				return
			}
		}
		action := map[upstream.Force]string{upstream.ForceOpen: "forceOpenBreaker", upstream.ForceClosed: "forceCloseBreaker",
			upstream.ForceNone: "clearBreakerForce"}[force]
		var changeID string
		var next upstream.BreakerSnapshot
		err = pgx.BeginFunc(ctx, s.d.Pool, func(tx pgx.Tx) error {
			prev, n, err := upstream.ForceShared(ctx, s.d.Redis, ref.key(), force, until, now)
			if err != nil {
				return err
			}
			next = n
			after := map[string]any{"state": n.State.String(), "forced": force.String(), "forced_until": nil}
			if !until.IsZero() {
				after["forced_until"] = until.UTC()
			}
			before := map[string]any{"state": prev.State.String(), "forced": "none"}
			if prev.Held(now) {
				before["forced"] = prev.Forced.String()
			}
			changeID, err = control.RecordChange(ctx, tx, control.Change{Actor: principal(r).Actor(), Action: action,
				ObjectType: "breaker", ObjectID: ref.objectID(), Reason: reason, Before: before, After: after})
			return err
		})
		if err != nil {
			s.fail(w, r, err)
			return
		}
		parked, err := s.parkedBy(ctx)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"change_id": changeID, "breaker": breakerViewOf(ref, next, parked[ref.key()], s.d.Now(), snap.BuiltAt)})
	}
}
