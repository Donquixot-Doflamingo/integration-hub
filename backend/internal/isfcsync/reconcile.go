package isfcsync

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"strings"
	"sync"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/control"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/core"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/isfc"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/registry"
)

// Actor is the change-log actor of every change the sync writes.
const Actor = "isfc-sync"

// Triggers (the label of hub_isfc_sync_runs_total).
const (
	TriggerEvent  = "event"
	TriggerTick   = "tick"
	TriggerManual = "manual"
)

// Outcomes (hub-admin TenantSyncOutcome).
const (
	OutcomeOK      = "ok"
	OutcomePartial = "partial"
	OutcomeFailed  = "failed"
)

// ErrBusy: another reconcile is running (the admin API answers 409).
var ErrBusy = errors.New("isfcsync: a sync is already running")

// ErrNotLinked: syncTenant for a tenant that has no ISFC link (409).
var ErrNotLinked = errors.New("isfcsync: tenant has no ISFC link")

// Source reads ISFC (*isfc.Client).
type Source interface {
	ListTenants(ctx context.Context) ([]isfc.Tenant, error)
	GetTenant(ctx context.Context, id string) (isfc.Tenant, error)
	ClientMetadata(ctx context.Context, clientID string) (isfc.ClientInfo, error)
	ClientConfiguration(ctx context.Context, clientID string) (isfc.ClientConfiguration, error)
}

// Editor changes the config tree (*control.Publisher).
type Editor interface {
	Current(ctx context.Context) (registry.Sources, *registry.Snapshot, error)
	Edit(ctx context.Context, fn func(t control.Tree, snap *registry.Snapshot) (control.Change, error)) (control.Result, error)
}

// Config configures a Reconciler.
type Config struct {
	// ConsoleClientID is the Hub console client whose tenants list is the set of linked tenants.
	ConsoleClientID string
	// BackendClientID is the Hub backend client that issues caller keys (HUB-483).
	BackendClientID string
	// PlatformTenantID is the ISFC UUID of the platform tenant; sync skips it.
	PlatformTenantID string
	// DryRun logs and records what would change and changes nothing (HUB_ISFC_SYNC_DRY_RUN).
	DryRun bool
}

// RunOptions say what a run is for.
type RunOptions struct {
	// Actor is who the change log names: the operator of a manual run. Empty: "isfc-sync", the
	// actor of event and scheduled runs.
	Actor string
	// Reason is the operator's reason of a manual run; it goes into the change log.
	Reason string
	// Tenant restricts the run to one Hub tenant id (syncTenant). It must be linked to ISFC.
	Tenant string
}

// Result is the outcome of one run (hub-admin TenantSyncResult and TenantSyncChange).
type Result struct {
	RunID      string
	ChangeID   string
	Outcome    string
	Error      string
	DryRun     bool
	Started    time.Time
	Finished   time.Time
	Created    int
	Updated    int
	Disabled   int
	Enabled    int
	Unchanged  int
	HubTenants []string // Hub ids touched, sorted
	Snapshot   *registry.Snapshot
}

// Reconciler runs the sync. Use one per process; Run is safe to call from several goroutines
// (a second concurrent call gets ErrBusy).
type Reconciler struct {
	Source  Source
	Editor  Editor
	Store   Store
	Config  Config
	Metrics *Metrics // may be nil
	Log     *slog.Logger
	Now     func() time.Time
	// Keys, when set, syncs the caller keys after the tenants (HUB-483).
	Keys KeySyncer

	mu sync.Mutex
}

// KeySyncer syncs the ISFC caller keys; the tenant snapshot it gets is the one after the
// tenant changes.
type KeySyncer interface {
	SyncKeys(ctx context.Context, snap *registry.Snapshot) error
}

func (r *Reconciler) now() time.Time {
	if r.Now != nil {
		return r.Now().UTC()
	}
	return time.Now().UTC()
}

func (r *Reconciler) log() *slog.Logger {
	if r.Log != nil {
		return r.Log
	}
	return slog.New(slog.DiscardHandler)
}

// Run reconciles once. ISFC being unreachable is not an error: the result says failed, the last
// good state is kept, and nothing is disabled. An error is returned only for ErrBusy,
// ErrNotLinked, an unknown tenant and a Hub-side failure that no outcome describes.
func (r *Reconciler) Run(ctx context.Context, trigger string, opt RunOptions) (Result, error) {
	if !r.mu.TryLock() {
		return Result{}, ErrBusy
	}
	defer r.mu.Unlock()
	unlock, ok, err := r.Store.TryRunLock(ctx)
	if err != nil {
		return Result{}, fmt.Errorf("isfc sync: run lock: %w", err)
	}
	if !ok {
		return Result{}, ErrBusy
	}
	defer unlock()

	res := Result{RunID: "isr_" + core.NewULID(r.now()), Started: r.now(), DryRun: r.Config.DryRun}
	only := ""
	if opt.Tenant != "" {
		_, snap, err := r.Editor.Current(ctx)
		if err != nil {
			return Result{}, fmt.Errorf("isfc sync: current snapshot: %w", err)
		}
		t, ok := snap.Tenants[opt.Tenant]
		if !ok {
			return Result{}, fmt.Errorf("%w: tenant %q", control.ErrNotFound, opt.Tenant)
		}
		if t.ISFC == nil {
			return Result{}, fmt.Errorf("%w: %q", ErrNotLinked, opt.Tenant)
		}
		only = strings.ToLower(t.ISFC.TenantID)
	}

	state, err := r.fetch(ctx, only)
	if err != nil {
		return r.finish(ctx, trigger, opt, res, OutcomeFailed, err)
	}
	popt := PlanOptions{PlatformTenantID: r.Config.PlatformTenantID, Only: only}

	if r.Config.DryRun {
		_, snap, err := r.Editor.Current(ctx)
		if err != nil {
			return r.finish(ctx, trigger, opt, res, OutcomeFailed, fmt.Errorf("current snapshot: %w", err))
		}
		plan := BuildPlan(snap, state, r.now(), popt)
		res.fill(plan)
		for _, a := range plan.Actions {
			r.log().InfoContext(ctx, "isfc.sync dry run: would change", slog.String("run", res.RunID), slog.String("tenant", a.HubID),
				slog.String("kind", a.Kind), slog.String("fields", strings.Join(a.Fields, ",")))
		}
		return r.finish(ctx, trigger, opt, res, OutcomeOK, nil)
	}

	var final Plan
	edit, err := r.Editor.Edit(ctx, func(tr control.Tree, snap *registry.Snapshot) (control.Change, error) {
		plan := BuildPlan(snap, state, r.now(), popt) // recomputed on every re-run: another replica may have written
		if len(plan.Actions) == 0 {
			final = plan
			return control.Change{}, errNothing
		}
		for _, a := range plan.Actions {
			if err := writeTenant(tr, a); err != nil {
				return control.Change{}, err
			}
		}
		final = plan
		return r.change(res, trigger, opt, plan), nil
	})
	switch {
	case errors.Is(err, errNothing):
		res.fill(final)
		if res.Snapshot == nil {
			_, res.Snapshot, _ = r.Editor.Current(ctx)
		}
	case err != nil:
		return r.finish(ctx, trigger, opt, res, OutcomeFailed, fmt.Errorf("apply: %w", err))
	default:
		res.fill(final)
		res.ChangeID, res.Snapshot = edit.ChangeID, edit.Snapshot
	}

	outcome := OutcomeOK
	if r.Keys != nil && res.Snapshot != nil && only == "" {
		if err := r.Keys.SyncKeys(ctx, res.Snapshot); err != nil {
			r.log().WarnContext(ctx, "isfc.sync caller keys not synced", slog.String("run", res.RunID), slog.String("error", err.Error()))
			return r.finish(ctx, trigger, opt, res, OutcomePartial, err)
		}
	}
	return r.finish(ctx, trigger, opt, res, outcome, nil)
}

var errNothing = errors.New("isfcsync: nothing to change")

func (res *Result) fill(p Plan) {
	res.Created, res.Updated, res.Disabled, res.Enabled = p.Counts()
	res.Unchanged = p.Unchanged
	res.HubTenants = res.HubTenants[:0]
	for _, a := range p.Actions {
		res.HubTenants = append(res.HubTenants, a.HubID)
	}
	slices.Sort(res.HubTenants)
}

// change is the change-log row of a run that edits config: one row per run, actor isfc-sync.
func (r *Reconciler) change(res Result, trigger string, opt RunOptions, p Plan) control.Change {
	created, updated, disabled, enabled := p.Counts()
	ids := make([]string, 0, len(p.Actions))
	for _, a := range p.Actions {
		ids = append(ids, a.HubID)
	}
	slices.Sort(ids)
	oid := "isfc-sync"
	if len(ids) == 1 {
		oid = ids[0]
	}
	return control.Change{Actor: actor(opt), Action: action(opt), ObjectType: "tenant", ObjectID: oid, Reason: reason(res.RunID, trigger, opt),
		After: map[string]any{"run_id": res.RunID, "trigger": trigger, "created": created, "updated": updated, "disabled": disabled, "enabled": enabled, "tenants": ids}}
}

// action names the change-log row like the admin operation: syncTenant for one tenant.
func action(opt RunOptions) string {
	if opt.Tenant != "" {
		return "syncTenant"
	}
	return "syncTenants"
}

func objectID(opt RunOptions) string {
	if opt.Tenant != "" {
		return opt.Tenant
	}
	return "isfc-sync"
}

func actor(opt RunOptions) string {
	if opt.Actor != "" {
		return opt.Actor
	}
	return Actor
}

func reason(runID, trigger string, opt RunOptions) string {
	s := fmt.Sprintf("ISFC sync %s (%s)", runID, trigger)
	if opt.Reason != "" {
		s += ": " + opt.Reason
	}
	return s
}

// fetch reads ISFC's state: the tenants linked to the console client and the ISFC tenants. A
// single-tenant run reads just that tenant (404 counts as gone).
func (r *Reconciler) fetch(ctx context.Context, only string) (State, error) {
	meta, err := r.Source.ClientMetadata(ctx, r.Config.ConsoleClientID)
	if err != nil {
		return State{}, fmt.Errorf("console client metadata: %w", err)
	}
	st := State{Linked: map[string]bool{}, Tenants: map[string]isfc.Tenant{}}
	for _, id := range meta.Tenants {
		st.Linked[strings.ToLower(id)] = true
	}
	if only != "" {
		t, err := r.Source.GetTenant(ctx, only)
		switch {
		case err == nil:
			st.Tenants[only] = t
		case !isfc.IsNotFound(err):
			return State{}, fmt.Errorf("tenant %s: %w", only, err)
		}
		return st, nil
	}
	all, err := r.Source.ListTenants(ctx)
	if err != nil {
		return State{}, fmt.Errorf("list tenants: %w", err)
	}
	for _, t := range all {
		t.ID = strings.ToLower(t.ID)
		st.Tenants[t.ID] = t
	}
	return st, nil
}

// finish completes the run: manual runs without a config change still write one change-log row,
// the run is recorded (when it did something, failed, or was asked for) and metrics move.
func (r *Reconciler) finish(ctx context.Context, trigger string, opt RunOptions, res Result, outcome string, cause error) (Result, error) {
	res.Outcome, res.Finished = outcome, r.now()
	if cause != nil {
		res.Error = shortErr(cause)
		r.log().WarnContext(ctx, "isfc.sync run did not complete", slog.String("run", res.RunID), slog.String("trigger", trigger),
			slog.String("result", outcome), slog.String("error", res.Error))
	}
	if trigger == TriggerManual && res.ChangeID == "" && !res.DryRun && outcome != OutcomeFailed {
		id, err := r.Store.RecordChange(ctx, control.Change{Actor: actor(opt), Action: action(opt), ObjectType: "tenant", ObjectID: objectID(opt),
			Reason: reason(res.RunID, trigger, opt),
			After:  map[string]any{"run_id": res.RunID, "trigger": trigger, "created": res.Created, "updated": res.Updated, "disabled": res.Disabled, "unchanged": res.Unchanged}})
		if err != nil {
			r.log().WarnContext(ctx, "isfc.sync change log not written", slog.String("error", err.Error()))
		}
		res.ChangeID = id
	}
	if trigger == TriggerManual && res.ChangeID == "" && outcome == OutcomeFailed {
		// The contract promises a change id on every answer; a failed manual run is logged too.
		if id, err := r.Store.RecordChange(ctx, control.Change{Actor: actor(opt), Action: action(opt), ObjectType: "tenant", ObjectID: objectID(opt),
			Reason: reason(res.RunID, trigger, opt) + " (failed)", After: map[string]any{"run_id": res.RunID, "result": outcome}}); err == nil {
			res.ChangeID = id
		}
	}
	if r.Metrics != nil {
		r.Metrics.Observe(trigger, outcome)
	}
	changed := res.Created+res.Updated+res.Disabled+res.Enabled > 0
	if changed || outcome != OutcomeOK || trigger != TriggerTick { // quiet ticks leave no row
		rec := RunRecord{ID: res.RunID, Trigger: trigger, DryRun: res.DryRun, Started: res.Started, Finished: res.Finished, Result: outcome,
			Error: res.Error, ChangeID: res.ChangeID,
			Changes: map[string]any{"created": res.Created, "updated": res.Updated, "disabled": res.Disabled, "enabled": res.Enabled,
				"unchanged": res.Unchanged, "tenants": res.HubTenants}}
		if err := r.Store.RecordRun(ctx, rec); err != nil {
			r.log().WarnContext(ctx, "isfc.sync run not recorded", slog.String("run", res.RunID), slog.String("error", err.Error()))
		}
	}
	r.log().InfoContext(ctx, "isfc.sync", slog.String("run", res.RunID), slog.String("trigger", trigger), slog.String("result", outcome),
		slog.Bool("dry_run", res.DryRun), slog.Int("created", res.Created), slog.Int("updated", res.Updated),
		slog.Int("disabled", res.Disabled), slog.Int("enabled", res.Enabled), slog.Int("unchanged", res.Unchanged))
	return res, nil
}

func shortErr(err error) string {
	s := err.Error()
	if len(s) > 300 {
		s = s[:300]
	}
	return s
}

// ---- writing tenant files ----

// writeTenant puts the tenant file of a planned action: a new file for a create, otherwise the
// existing file with only isfc and disabled replaced (comments and every other key stay).
func writeTenant(tr control.Tree, a Action) error {
	path := "tenants/" + a.HubID + ".yaml"
	doc, exists := tr.Get(path)
	if a.Created {
		if exists {
			return fmt.Errorf("%w: %s exists", control.ErrConflict, path)
		}
		out, err := yaml.Marshal(a.Tenant)
		if err != nil {
			return err
		}
		tr.Put(path, append([]byte("# Created by the ISFC sync (actor isfc-sync). The isfc block is a read-only copy; setup is finished in the console.\n"), out...))
		return nil
	}
	if !exists {
		return fmt.Errorf("%w: %s", control.ErrNotFound, path)
	}
	kv := map[string]any{"isfc": a.Tenant.ISFC, "disabled": nil}
	if a.Tenant.Disabled {
		kv["disabled"] = true
	}
	out, err := setKeys(doc, kv)
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	tr.Put(path, out)
	return nil
}

// setKeys replaces (or appends) top-level keys of a YAML mapping, keeping comments and the other
// keys; a nil value removes the key.
func setKeys(doc []byte, kv map[string]any) ([]byte, error) {
	var root yaml.Node
	if err := yaml.Unmarshal(doc, &root); err != nil {
		return nil, err
	}
	if len(root.Content) == 0 || root.Content[0].Kind != yaml.MappingNode {
		return nil, errors.New("not a YAML mapping")
	}
	m := root.Content[0]
	for _, key := range slices.Sorted(maps.Keys(kv)) {
		var val *yaml.Node
		if v := kv[key]; v != nil {
			val = &yaml.Node{}
			if err := val.Encode(v); err != nil {
				return nil, err
			}
		}
		set := false
		for i := 0; i+1 < len(m.Content); i += 2 {
			if m.Content[i].Value != key {
				continue
			}
			if val == nil {
				m.Content = slices.Delete(m.Content, i, i+2)
			} else {
				m.Content[i+1] = val
			}
			set = true
			break
		}
		if !set && val != nil {
			m.Content = append(m.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: key}, val)
		}
	}
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(&root); err != nil {
		return nil, err
	}
	return buf.Bytes(), enc.Close()
}
