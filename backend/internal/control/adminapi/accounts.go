package adminapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"math"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/control"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/core"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/creds"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/registry"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/telemetry"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/upstream"
)

// Accounts (HUB-203, DESIGN §6.5, §6.6, §16.1): credentialed vendor instances. Config lives in
// accounts/<id>.yaml of the config tree; secrets live only in the credential store
// (credentials_ref localfile://<id>) and are write-only: responses carry slot metadata.

func init() { registrars = append(registrars, (*Server).registerAccounts) }

func (s *Server) registerAccounts() {
	s.handle("listAccounts", RoleViewer, false, "GET /accounts", s.listAccounts)
	s.handle("createAccount", RoleAdmin, true, "POST /accounts", s.createAccount)
	s.handle("getAccount", RoleViewer, false, "GET /accounts/{account_id}", s.getAccount)
	s.handle("updateAccount", RoleAdmin, true, "PATCH /accounts/{account_id}", s.updateAccount)
	s.handle("transitionAccountLifecycle", RoleAdmin, true, "POST /accounts/{account_id}/lifecycle", s.transitionAccount)
	s.handle("testAccountConnection", RoleEditor, false, "POST /accounts/{account_id}/test-connection", s.testAccountConnection)
	s.handle("addNextCredential", RoleAdmin, true, "POST /accounts/{account_id}/credentials/next", s.addNextCredential)
	s.handle("promoteNextCredential", RoleAdmin, true, "POST /accounts/{account_id}/credentials/promote", s.promoteNextCredential)
	s.handle("deleteOldCredential", RoleAdmin, true, "POST /accounts/{account_id}/credentials/delete-old", s.deleteOldCredential)
}

// ---- helpers shared by the access resources (accounts, tenants, api keys, registrar) ----

var accessSlugRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{2,63}$`)

// accessETag is the entity tag of a live registry object: a hash of its canonical JSON.
func accessETag(v any) string {
	b, _ := json.Marshal(v)
	sum := sha256.Sum256(b)
	return `"` + hex.EncodeToString(sum[:8]) + `"`
}

// accessIfMatch refuses a write whose If-Match names another revision (412).
func accessIfMatch(r *http.Request, etag string) error {
	if m := strings.TrimSpace(r.Header.Get("If-Match")); m != "" && m != "*" && m != etag {
		return &Error{Status: http.StatusPreconditionFailed, Code: CodePreconditionFailed, Details: []Detail{},
			Message: "If-Match does not match the current ETag", Meta: map[string]any{"etag": etag}}
	}
	return nil
}

// accessSetKey replaces (or appends) a top-level key of a YAML mapping file with v, keeping the
// file's comments and the other keys; v == nil removes the key.
func accessSetKey(doc []byte, key string, v any) ([]byte, error) {
	var root yaml.Node
	if err := yaml.Unmarshal(doc, &root); err != nil {
		return nil, err
	}
	if len(root.Content) == 0 || root.Content[0].Kind != yaml.MappingNode {
		return nil, errors.New("not a YAML mapping")
	}
	m := root.Content[0]
	var val *yaml.Node
	if v != nil {
		val = &yaml.Node{}
		if err := val.Encode(v); err != nil {
			return nil, err
		}
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value != key {
			continue
		}
		if val == nil {
			m.Content = slices.Delete(m.Content, i, i+2)
		} else {
			m.Content[i+1] = val
		}
		return yaml.Marshal(&root)
	}
	if val != nil {
		m.Content = append(m.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: key}, val)
	}
	return yaml.Marshal(&root)
}

// accessSetKeys applies accessSetKey to path of t for every key of kv (in sorted order).
func accessSetKeys(t control.Tree, path string, kv map[string]any) error {
	doc, ok := t.Get(path)
	if !ok {
		return fmt.Errorf("%w: %s", control.ErrNotFound, path)
	}
	var err error
	for _, k := range slices.Sorted(maps.Keys(kv)) {
		if doc, err = accessSetKey(doc, k, kv[k]); err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
	}
	t.Put(path, doc)
	return nil
}

// accessPage pages items sorted by id: the cursor is the last id of the previous page.
func accessPage[T any](r *http.Request, ids []string, item func(id string) T) ([]T, *string, error) {
	limit, cursor, err := page(r, 1)
	if err != nil {
		return nil, nil, err
	}
	slices.Sort(ids)
	out := []T{}
	for _, id := range ids {
		if len(cursor) > 0 && id <= cursor[0] {
			continue
		}
		if len(out) == limit {
			return out, nextCursor(ids[slices.Index(ids, id)-1]), nil
		}
		out = append(out, item(id))
	}
	return out, nil, nil
}

// accessSnapshot returns the current registry snapshot (the publisher's view, else Deps.Snapshot).
//
// It shares the cached snapshot of the integrations and vendors handlers (rebuilt only when the
// archive's checksum changes): a read used to gunzip and fully rebuild the snapshot every time,
// and stamped BuiltAt with "now", which objectTimes then used as a moving created/updated time
// for objects older than the change log (HUB-451).
func (s *Server) accessSnapshot(ctx context.Context) (*registry.Snapshot, error) {
	return s.snapshot(ctx)
}

func (s *Server) accessRotator() (*control.Rotator, error) {
	if s.d.Rotator == nil {
		return nil, errors.New("no credential store is configured")
	}
	return s.d.Rotator, nil
}

func (s *Server) accessPublisher() (*control.Publisher, error) {
	if s.d.Publisher == nil {
		return nil, errors.New("no publisher")
	}
	return s.d.Publisher, nil
}

// integral reads a non-negative whole number from a JSON value.
func accessInt(v any, pointer string) (int, error) {
	f, ok := v.(float64)
	if !ok || f < 0 || f != math.Trunc(f) || f > math.MaxInt32 {
		return 0, badRequest(pointer, "must be a non-negative whole number")
	}
	return int(f), nil
}

// ---- views ----

func (s *Server) accountSlots(ctx context.Context, id string) []creds.SlotView {
	if s.d.Rotator == nil {
		return []creds.SlotView{}
	}
	sec, ok, err := s.d.Rotator.ReadCredentials(id)
	if err != nil {
		s.d.Log.WarnContext(ctx, "reading credential slots failed", slog.String("account", id), slog.String("error", err.Error()))
	}
	if !ok {
		return []creds.SlotView{}
	}
	return sec.Views()
}

type accountUse struct {
	Integration string `json:"integration_id"`
	Version     int    `json:"version"`
	Role        string `json:"role"`
}

// accountUses lists the active integration versions that route to account.
func accountUses(snap *registry.Snapshot, account string) []accountUse {
	out := []accountUse{}
	for _, id := range slices.Sorted(maps.Keys(snap.Integrations)) {
		in := snap.Integrations[id]
		v := in.Active()
		if v == nil {
			continue
		}
		add := func(role string, ts ...registry.Target) {
			if slices.ContainsFunc(ts, func(t registry.Target) bool { return t.Account == account }) {
				out = append(out, accountUse{Integration: id, Version: in.ActiveVersion, Role: role})
			}
		}
		add("target", v.Targets...)
		add("failover", v.Failover...)
		if v.Shadow != nil {
			add("shadow", v.Shadow.Target)
		}
	}
	return out
}

func (s *Server) accountView(ctx context.Context, snap *registry.Snapshot, a *registry.Account) map[string]any {
	created, updated := s.objectTimes(ctx, "account", a.Account, snap.BuiltAt)
	pinned, sessions := 0, 0
	if a.State != registry.AccountRetired && s.d.Publisher != nil && s.d.Publisher.Guard != nil {
		var err error
		if pinned, sessions, err = s.d.Publisher.Guard(ctx, snap, a.Account); err != nil {
			s.d.Log.WarnContext(ctx, "counting account references failed", slog.String("account", a.Account), slog.String("error", err.Error()))
		}
	}
	slots := []creds.SlotView{}
	if a.State != registry.AccountRetired {
		slots = s.accountSlots(ctx, a.Account)
	}
	cfg := a.Config
	if cfg == nil {
		cfg = map[string]string{}
	}
	return map[string]any{
		"account_id": a.Account, "vendor_id": a.Vendor, "region": a.Region, "state": a.State, "config": cfg,
		"quota": map[string]any{"rps": a.Quota.RPS, "daily": a.Quota.Daily}, "credentials": slots,
		"integrations_using": accountUses(snap, a.Account), "pinned_requests": pinned, "open_sessions": sessions,
		"created_at": created, "updated_at": updated,
	}
}

func (s *Server) accountSummary(ctx context.Context, snap *registry.Snapshot, a *registry.Account, slots []creds.SlotView) map[string]any {
	_, updated := s.objectTimes(ctx, "account", a.Account, snap.BuiltAt)
	out := map[string]any{"account_id": a.Account, "vendor_id": a.Vendor, "region": a.Region, "state": a.State,
		"active_cred_version": nil, "next_cred_version": nil, "credential_expires_at": nil, "updated_at": updated}
	if a.State != registry.AccountRetired {
		for _, sl := range slots {
			switch sl.Slot {
			case "active":
				out["active_cred_version"], out["credential_expires_at"] = sl.Version, sl.ExpiresAt
			case "next":
				out["next_cred_version"] = sl.Version
			}
		}
	}
	ids := map[string]bool{}
	for _, u := range accountUses(snap, a.Account) {
		ids[u.Integration] = true
	}
	out["integrations_using"] = len(ids)
	return out
}

// ---- handlers ----

func (s *Server) listAccounts(w http.ResponseWriter, r *http.Request) {
	snap, err := s.accessSnapshot(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	q := r.URL.Query()
	vendor, state, sub := q.Get("vendor_id"), q.Get("state"), strings.ToLower(q.Get("q"))
	all := map[string][]creds.SlotView{}
	if s.d.Rotator != nil {
		if all, err = s.d.Rotator.CredentialSlots(); err != nil {
			s.d.Log.WarnContext(r.Context(), "reading credential slots failed", slog.String("error", err.Error()))
			all = map[string][]creds.SlotView{}
		}
	}
	var ids []string
	for id, a := range snap.Accounts {
		if (vendor == "" || a.Vendor == vendor) && (state == "" || string(a.State) == state) && (sub == "" || strings.Contains(strings.ToLower(id), sub)) {
			ids = append(ids, id)
		}
	}
	ctx := r.Context()
	items, next, err := accessPage(r, ids, func(id string) map[string]any {
		return s.accountSummary(ctx, snap, snap.Accounts[id], all[id])
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "next_cursor": next})
}

func (s *Server) getAccount(w http.ResponseWriter, r *http.Request) {
	snap, err := s.accessSnapshot(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	a, ok := snap.Accounts[r.PathValue("account_id")]
	if !ok {
		s.fail(w, r, notFound("Account"))
		return
	}
	w.Header().Set("ETag", accessETag(a))
	writeJSON(w, http.StatusOK, s.accountView(r.Context(), snap, a))
}

// writeAccount answers an account change with the account of the new snapshot.
func (s *Server) writeAccount(w http.ResponseWriter, r *http.Request, status int, res control.Result, id string) {
	a := res.Snapshot.Accounts[id]
	w.Header().Set("ETag", accessETag(a))
	writeJSON(w, status, map[string]any{"change_id": res.ChangeID, "account": s.accountView(r.Context(), res.Snapshot, a)})
}

// accountQuota merge-patches base with a JSON AccountQuota.
func accountQuota(m map[string]any, base registry.Quota) (registry.Quota, error) {
	for k, v := range m {
		switch k {
		case "rps":
			n, err := accessInt(v, "/quota/rps")
			if err != nil {
				return base, err
			}
			base.RPS = n
		case "daily":
			if v == nil {
				base.Daily = 0
				continue
			}
			n, err := accessInt(v, "/quota/daily")
			if err != nil {
				return base, err
			}
			base.Daily = n
		default:
			return base, badRequest("/quota/"+k, "unknown quota field")
		}
	}
	return base, nil
}

// accountConfig merge-patches base with patch: strings set a key, null removes it.
func accountConfig(base map[string]string, patch map[string]any) (map[string]string, error) {
	out := maps.Clone(base)
	if out == nil {
		out = map[string]string{}
	}
	for k, v := range patch {
		switch v := v.(type) {
		case nil:
			delete(out, k)
		case string:
			out[k] = v
		default:
			return nil, badRequest("/config/"+k, "account config values are strings")
		}
	}
	return out, nil
}

func (s *Server) createAccount(w http.ResponseWriter, r *http.Request) {
	var body struct {
		AccountID           string            `json:"account_id"`
		VendorID            string            `json:"vendor_id"`
		Region              string            `json:"region"`
		Config              map[string]any    `json:"config"`
		Quota               map[string]any    `json:"quota"`
		Credentials         map[string]string `json:"credentials"`
		CredentialExpiresAt *time.Time        `json:"credential_expires_at"`
		Reason              string            `json:"reason"`
	}
	if err := decode(w, r, &body); err != nil {
		s.fail(w, r, err)
		return
	}
	reason, err := reasonOf(r, body.Reason)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	id := body.AccountID
	if !accessSlugRE.MatchString(id) {
		s.fail(w, r, badRequest("/account_id", "account_id must match "+accessSlugRE.String()))
		return
	}
	for k, v := range body.Credentials {
		if k == "" || v == "" {
			s.fail(w, r, badRequest("/credentials", "credential names and values must not be empty"))
			return
		}
	}
	cfg, err := accountConfig(nil, body.Config)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	pub, err := s.accessPublisher()
	if err != nil {
		s.fail(w, r, err)
		return
	}
	// The credential store is only needed when the vendor's scheme takes credentials: an auth
	// none account (ADR-0023) can be created on an admin without one.
	var rot *control.Rotator
	created := false
	ctx := r.Context()
	res, err := pub.Edit(ctx, func(t control.Tree, snap *registry.Snapshot) (control.Change, error) {
		path := "accounts/" + id + ".yaml"
		if _, ok := snap.Accounts[id]; ok {
			return control.Change{}, fmt.Errorf("%w: account %q exists", control.ErrConflict, id)
		}
		if _, ok := t.Get(path); ok {
			return control.Change{}, fmt.Errorf("%w: %s exists", control.ErrConflict, path)
		}
		v, ok := snap.Vendors[body.VendorID]
		if !ok {
			return control.Change{}, badRequest("/vendor_id", fmt.Sprintf("unknown vendor %q", body.VendorID))
		}
		if _, ok := v.Regions[body.Region]; !ok {
			return control.Change{}, badRequest("/region", fmt.Sprintf("vendor %q has no region %q", v.Vendor, body.Region))
		}
		// Auth none (ADR-0023): a public API, the account has no credentials at all.
		needsCreds := creds.NeedsCredentials(v.Auth)
		switch {
		case needsCreds && len(body.Credentials) == 0:
			return control.Change{}, badRequest("/credentials", "credentials need at least one value")
		case !needsCreds && len(body.Credentials) > 0:
			return control.Change{}, badRequest("/credentials", fmt.Sprintf("vendor %q uses auth scheme none: omit credentials (or send {})", v.Vendor))
		}
		quota, err := accountQuota(body.Quota, registry.Quota{RPS: v.Defaults.RateLimit.PerAccountRPS})
		if err != nil {
			return control.Change{}, err
		}
		a := registry.Account{Account: id, Vendor: v.Vendor, Region: body.Region, State: registry.AccountActive, Config: cfg, Quota: quota}
		if needsCreds {
			a.CredentialsRef = "localfile://" + id
		}
		doc, err := yaml.Marshal(a)
		if err != nil {
			return control.Change{}, err
		}
		t.Put(path, append([]byte("# Created through the admin API (createAccount). Secrets live only in the credential store.\n"), doc...))
		if !needsCreds {
			return control.Change{Actor: principal(r).Actor(), Action: "createAccount", ObjectType: "account", ObjectID: id, Reason: reason,
				After: map[string]any{"vendor": a.Vendor, "region": a.Region, "config": a.Config, "quota": a.Quota, "credentials": []any{}}}, nil
		}
		if rot, err = s.accessRotator(); err != nil {
			return control.Change{}, err
		}
		sec := creds.AccountSecrets{Active: creds.NewSlot(creds.AccountSecrets{}, body.Credentials, body.CredentialExpiresAt, s.d.Now())}
		if err := rot.PutCredentials(ctx, id, &sec, true); err != nil {
			return control.Change{}, err
		}
		created = true
		return control.Change{Actor: principal(r).Actor(), Action: "createAccount", ObjectType: "account", ObjectID: id, Reason: reason,
			After: map[string]any{"vendor": a.Vendor, "region": a.Region, "config": a.Config, "quota": a.Quota, "credentials": sec.Views()}}, nil
	})
	if err != nil {
		if created { // the config edit did not land: drop the secrets it would have referenced
			if rerr := rot.PutCredentials(context.WithoutCancel(r.Context()), id, nil, false); rerr != nil {
				s.d.Log.ErrorContext(r.Context(), "removing credentials of a failed createAccount failed", slog.String("account", id), slog.String("error", rerr.Error()))
			}
		}
		s.fail(w, r, controlError(err))
		return
	}
	s.writeAccount(w, r, http.StatusCreated, res, id)
}

func (s *Server) updateAccount(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Config map[string]any `json:"config"`
		Quota  map[string]any `json:"quota"`
		Reason string         `json:"reason"`
	}
	if err := decode(w, r, &body); err != nil {
		s.fail(w, r, err)
		return
	}
	reason, err := reasonOf(r, body.Reason)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	pub, err := s.accessPublisher()
	if err != nil {
		s.fail(w, r, err)
		return
	}
	id := r.PathValue("account_id")
	res, err := pub.Edit(r.Context(), func(t control.Tree, snap *registry.Snapshot) (control.Change, error) {
		a, ok := snap.Accounts[id]
		if !ok {
			return control.Change{}, notFound("Account")
		}
		if err := accessIfMatch(r, accessETag(a)); err != nil {
			return control.Change{}, err
		}
		cfg, err := accountConfig(a.Config, body.Config)
		if err != nil {
			return control.Change{}, err
		}
		quota, err := accountQuota(body.Quota, a.Quota)
		if err != nil {
			return control.Change{}, err
		}
		if err := accessSetKeys(t, "accounts/"+id+".yaml", map[string]any{"config": cfg, "quota": quota}); err != nil {
			return control.Change{}, err
		}
		return control.Change{Actor: principal(r).Actor(), Action: "updateAccount", ObjectType: "account", ObjectID: id, Reason: reason,
			Before: map[string]any{"config": a.Config, "quota": a.Quota}, After: map[string]any{"config": cfg, "quota": quota}}, nil
	})
	if err != nil {
		s.fail(w, r, controlError(err))
		return
	}
	s.writeAccount(w, r, http.StatusOK, res, id)
}

func (s *Server) transitionAccount(w http.ResponseWriter, r *http.Request) {
	var body struct {
		TargetState string `json:"target_state"`
		Reason      string `json:"reason"`
	}
	if err := decode(w, r, &body); err != nil {
		s.fail(w, r, err)
		return
	}
	reason, err := reasonOf(r, body.Reason)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	to := registry.AccountState(body.TargetState)
	if !slices.Contains([]registry.AccountState{registry.AccountActive, registry.AccountDraining, registry.AccountRetired}, to) {
		s.fail(w, r, badRequest("/target_state", "target_state must be active, draining or retired"))
		return
	}
	pub, err := s.accessPublisher()
	if err != nil {
		s.fail(w, r, err)
		return
	}
	id := r.PathValue("account_id")
	res, err := pub.TransitionAccount(r.Context(), id, to, principal(r).Actor(), reason)
	var te *control.TransitionError
	if errors.As(err, &te) {
		s.fail(w, r, &Error{Status: http.StatusConflict, Code: CodeConflict, Details: []Detail{},
			Message: upperFirst(te.Error()), Meta: map[string]any{"from": string(te.From), "to": string(te.To)}})
		return
	}
	if err != nil {
		s.fail(w, r, controlError(err))
		return
	}
	if to == registry.AccountRetired && s.d.Rotator != nil {
		// DESIGN §6.5: credentials are deleted only at retire. The retire is already logged; a
		// failed delete leaves unused ciphertext behind (retired accounts are never loaded).
		if err := s.d.Rotator.PutCredentials(r.Context(), id, nil, false); err != nil {
			s.d.Log.ErrorContext(r.Context(), "deleting credentials of a retired account failed", slog.String("account", id), slog.String("error", err.Error()))
		}
	}
	s.writeAccount(w, r, http.StatusOK, res, id)
}

// ---- credential rotation (DESIGN §6.6): add next → test-connection → promote → delete old ----

// accountForCredentials checks that the account exists and still holds credentials: an account
// of an auth none vendor never had any (409, ADR-0023).
func (s *Server) accountForCredentials(r *http.Request) (*registry.Account, error) {
	snap, a, err := s.accountForProbe(r)
	if err != nil {
		return nil, err
	}
	if v := snap.Vendors[a.Vendor]; v != nil && !creds.NeedsCredentials(v.Auth) {
		return nil, &Error{Status: http.StatusConflict, Code: CodeConflict, Details: []Detail{},
			Message: "Account " + a.Account + " belongs to vendor " + v.Vendor + " (auth scheme none): it has no credentials"}
	}
	return a, nil
}

// accountForProbe checks that the account exists and is not retired.
func (s *Server) accountForProbe(r *http.Request) (*registry.Snapshot, *registry.Account, error) {
	snap, err := s.accessSnapshot(r.Context())
	if err != nil {
		return nil, nil, err
	}
	a, ok := snap.Accounts[r.PathValue("account_id")]
	if !ok {
		return nil, nil, notFound("Account")
	}
	if a.State == registry.AccountRetired {
		return nil, nil, &Error{Status: http.StatusConflict, Code: CodeConflict, Details: []Detail{},
			Message: "Account " + a.Account + " is retired: its credentials are deleted"}
	}
	return snap, a, nil
}

func (s *Server) writeSlots(w http.ResponseWriter, rot control.Rotation) {
	writeJSON(w, http.StatusOK, map[string]any{"change_id": rot.ChangeID, "account_id": rot.Account, "credentials": rot.Slots})
}

func (s *Server) addNextCredential(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Credentials map[string]string `json:"credentials"`
		ExpiresAt   *time.Time        `json:"expires_at"`
		Reason      string            `json:"reason"`
	}
	if err := decode(w, r, &body); err != nil {
		s.fail(w, r, err)
		return
	}
	reason, err := reasonOf(r, body.Reason)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if len(body.Credentials) == 0 {
		s.fail(w, r, badRequest("/credentials", "credentials need at least one value"))
		return
	}
	for k, v := range body.Credentials {
		if k == "" || v == "" {
			s.fail(w, r, badRequest("/credentials", "credential names and values must not be empty"))
			return
		}
	}
	a, err := s.accountForCredentials(r) // 404, or 409 for a retired or auth none account first
	if err != nil {
		s.fail(w, r, err)
		return
	}
	rot, err := s.accessRotator()
	if err != nil {
		s.fail(w, r, err)
		return
	}
	res, err := rot.AddNext(r.Context(), a.Account, body.Credentials, body.ExpiresAt, principal(r).Actor(), reason)
	if err != nil {
		s.fail(w, r, controlError(err))
		return
	}
	s.writeSlots(w, res)
}

func (s *Server) promoteNextCredential(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ExpectedNextVersion int    `json:"expected_next_version"`
		Reason              string `json:"reason"`
	}
	if err := decode(w, r, &body); err != nil {
		s.fail(w, r, err)
		return
	}
	reason, err := reasonOf(r, body.Reason)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if body.ExpectedNextVersion < 1 {
		s.fail(w, r, badRequest("/expected_next_version", "expected_next_version must be >= 1"))
		return
	}
	a, err := s.accountForCredentials(r) // 404, or 409 for a retired or auth none account first
	if err != nil {
		s.fail(w, r, err)
		return
	}
	rot, err := s.accessRotator()
	if err != nil {
		s.fail(w, r, err)
		return
	}
	res, err := rot.Promote(r.Context(), a.Account, body.ExpectedNextVersion, principal(r).Actor(), reason)
	if err != nil {
		s.fail(w, r, controlError(err))
		return
	}
	s.writeSlots(w, res)
}

func (s *Server) deleteOldCredential(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ExpectedPreviousVersion int    `json:"expected_previous_version"`
		Reason                  string `json:"reason"`
	}
	if err := decode(w, r, &body); err != nil {
		s.fail(w, r, err)
		return
	}
	reason, err := reasonOf(r, body.Reason)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if body.ExpectedPreviousVersion < 1 {
		s.fail(w, r, badRequest("/expected_previous_version", "expected_previous_version must be >= 1"))
		return
	}
	a, err := s.accountForCredentials(r) // 404, or 409 for a retired or auth none account first
	if err != nil {
		s.fail(w, r, err)
		return
	}
	rot, err := s.accessRotator()
	if err != nil {
		s.fail(w, r, err)
		return
	}
	res, err := rot.DeleteOld(r.Context(), a.Account, body.ExpectedPreviousVersion, principal(r).Actor(), reason)
	if err != nil {
		s.fail(w, r, controlError(err))
		return
	}
	s.writeSlots(w, res)
}

// probeClient sends connection tests when Deps.Upstream is nil: a private client (same egress
// allowlist, TLS rules and timeouts as the data plane) with its own metrics registry.
var probeClient = sync.OnceValues(func() (*upstream.Client, error) {
	return upstream.New(telemetry.NewMetrics("admin", "probe"), upstream.Options{})
})

// connectionTest is the contract's ConnectionTestResult.
type connectionTest struct {
	OK             bool      `json:"ok"`
	Slot           string    `json:"slot"`
	CredVersion    int       `json:"cred_version"`
	EndpointRef    string    `json:"endpoint_ref"`
	UpstreamStatus *int      `json:"upstream_status"`
	LatencyMS      *int64    `json:"latency_ms"`
	ErrorCode      *string   `json:"error_code"`
	Message        *string   `json:"message"`
	CheckedAt      time.Time `json:"checked_at"`
}

func (c *connectionTest) failed(code core.Code, msg string) {
	c.OK, c.ErrorCode, c.Message = false, core.Ptr(string(code)), &msg
}

// staticHeaders maps credential values into the headers a request transformer of the endpoint
// names with creds.<key> (static_headers vendors put credentials in their own headers).
func staticHeaders(snap *registry.Snapshot, vendorEndpoint string, values map[string]string, h map[string]string) {
	for _, ref := range slices.SortedFunc(maps.Keys(snap.Transformers), func(a, b registry.TransformerRef) int {
		return strings.Compare(b.String(), a.String())
	}) {
		tf := snap.Transformers[ref]
		if tf.Kind != registry.KindRequest || tf.VendorEndpoint != vendorEndpoint {
			continue
		}
		var req struct {
			Headers map[string]string `yaml:"headers"`
		}
		if tf.Request.Decode(&req) != nil {
			continue
		}
		for name, expr := range req.Headers {
			if key, ok := strings.CutPrefix(strings.TrimSpace(expr), "creds."); ok && values[key] != "" {
				h[name] = values[key]
			}
		}
		return
	}
}

// testAccountConnection runs the vendor's health probe (dry where declared) with the chosen
// slot's credentials. Non-mutating (no reason, no change_id) but audited in the change log.
func (s *Server) testAccountConnection(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Slot        string `json:"slot"`
		EndpointRef string `json:"endpoint_ref"`
	}
	if r.ContentLength != 0 {
		if err := decode(w, r, &body); err != nil {
			s.fail(w, r, err)
			return
		}
	}
	if body.Slot == "" {
		body.Slot = "active"
	}
	if body.Slot != "active" && body.Slot != "next" {
		s.fail(w, r, badRequest("/slot", "slot must be active or next"))
		return
	}
	snap, a, err := s.accountForProbe(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	v := snap.Vendors[a.Vendor]
	probe := v.Defaults.HealthProbe
	ep := body.EndpointRef
	if ep == "" {
		if probe == nil {
			s.fail(w, r, badRequest("/endpoint_ref", "vendor "+v.Vendor+" declares no health probe: name an endpoint_ref"))
			return
		}
		ep = probe.Endpoint
	}
	target, err := upstream.TargetFor(snap, a.Account, v.Vendor+"."+ep)
	if err != nil {
		s.fail(w, r, badRequest("/endpoint_ref", fmt.Sprintf("vendor %s has no endpoint %q", v.Vendor, ep)))
		return
	}
	var sec creds.AccountSecrets
	ok := false
	if creds.NeedsCredentials(v.Auth) { // auth none needs no credential store at all
		rot, err := s.accessRotator()
		if err != nil {
			s.fail(w, r, err)
			return
		}
		if sec, ok, err = rot.ReadCredentials(a.Account); err != nil {
			s.fail(w, r, err)
			return
		}
	} else if body.Slot == "active" { // auth none: the probe runs without credentials
		ok = true
	}
	slot := &sec.Active
	if body.Slot == "next" {
		slot = sec.Next
	}
	if !ok || slot == nil {
		s.fail(w, r, &Error{Status: http.StatusConflict, Code: CodeConflict, Details: []Detail{},
			Message: fmt.Sprintf("Account %s has no %s credential", a.Account, body.Slot)})
		return
	}
	res := connectionTest{OK: true, Slot: body.Slot, CredVersion: slot.Version, EndpointRef: ep, CheckedAt: s.d.Now().UTC()}
	s.probe(r.Context(), snap, target, probe, creds.Credentials{AccountID: a.Account, Version: slot.Version, Values: slot.Values}, &res)
	if _, err := control.RecordChange(r.Context(), s.d.Pool, control.Change{Actor: principal(r).Actor(), Action: "testAccountConnection",
		ObjectType: "account", ObjectID: a.Account, Reason: "connection test of the " + body.Slot + " credential (non-mutating)",
		After: map[string]any{"slot": res.Slot, "cred_version": res.CredVersion, "endpoint_ref": ep, "ok": res.OK,
			"upstream_status": res.UpstreamStatus, "error_code": res.ErrorCode}}); err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// probe sends one health-probe request with c applied by the vendor's auth scheme and fills res.
// Messages name statuses and credential keys, never values or bodies.
func (s *Server) probe(ctx context.Context, snap *registry.Snapshot, t upstream.Target, hp *registry.HealthProbe, c creds.Credentials, res *connectionTest) {
	req := core.VendorRequest{Method: t.Endpoint.Method, Path: t.Endpoint.Path, Body: map[string]any{},
		Headers: map[string]string{"X-Hub-Probe": "1", "Content-Type": "application/json"}}
	if hp != nil && hp.Dry && hp.Endpoint == t.EndpointName {
		req.Query = map[string]string{"dry": "1"}
	}
	if t.Vendor.Auth == creds.SchemeStaticHeaders {
		staticHeaders(snap, t.Vendor.Vendor+"."+t.EndpointName, c.Values, req.Headers)
	}
	if err := creds.ApplyVendor(t.Vendor, c, &req); err != nil {
		res.failed(core.CodeUpstreamAuthFailed, upperFirst(strings.TrimPrefix(err.Error(), "creds: ")))
		return
	}
	client, err := s.d.Upstream, error(nil)
	if client == nil {
		client, err = probeClient()
	}
	if err != nil {
		res.failed(core.CodeInternalError, "No upstream client")
		return
	}
	at, err := client.Attempt(ctx, t, req)
	res.LatencyMS = core.Ptr(at.Latency.Milliseconds())
	var te *upstream.TransportError
	switch {
	case errors.As(err, &te) && te.Kind == upstream.KindTimeout:
		res.failed(core.CodeUpstreamTimeout, "The vendor did not answer in time")
		return
	case err != nil:
		res.failed(core.CodeUpstreamUnavailable, "The vendor could not be reached ("+string(kindOf(te))+")")
		return
	}
	status := at.Response.Status
	res.UpstreamStatus = &status
	switch {
	case status < 400:
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		res.failed(core.CodeUpstreamAuthFailed, fmt.Sprintf("The vendor refused the credential (HTTP %d)", status))
	case status == http.StatusTooManyRequests:
		res.failed(core.CodeUpstreamRateLimited, "The vendor rate-limited the probe (HTTP 429)")
	case status >= 500:
		res.failed(core.CodeUpstreamUnavailable, fmt.Sprintf("The vendor failed (HTTP %d)", status))
	default:
		res.failed(core.CodeUpstreamRejected, fmt.Sprintf("The vendor rejected the probe (HTTP %d)", status))
	}
}

func kindOf(te *upstream.TransportError) upstream.Kind {
	if te == nil {
		return upstream.KindProtocol
	}
	return te.Kind
}
