package adminapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"

	"go.yaml.in/yaml/v3"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/control"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/registry"
)

func init() { registrars = append(registrars, (*Server).registerIntegrationMeta) }

func (s *Server) registerIntegrationMeta() {
	s.handle("createIntegration", RoleEditor, true, "POST /integrations", s.createIntegration)
	s.handle("updateIntegration", RoleEditor, true, "PATCH /integrations/{integration_id}", s.updateIntegration)
}

// checkTenants refuses tenants the snapshot does not know (400 at their pointer).
func checkTenants(snap *registry.Snapshot, owner string, grants []string) error {
	if owner != "" {
		if _, ok := snap.Tenants[owner]; !ok {
			return badRequest("/owner_tenant", fmt.Sprintf("unknown tenant %q", owner))
		}
	}
	for i, g := range grants {
		if _, ok := snap.Tenants[g]; !ok {
			return badRequest(fmt.Sprintf("/grants/%d", i), fmt.Sprintf("unknown tenant %q", g))
		}
		if slices.Index(grants, g) != i {
			return badRequest(fmt.Sprintf("/grants/%d", i), fmt.Sprintf("tenant %q is granted twice", g))
		}
	}
	return nil
}

// metaOf is the change-log document of an integration's editable fields.
func metaOf(f registry.IntegrationFile) map[string]any {
	grants := f.Grants
	if grants == nil {
		grants = []string{}
	}
	return map[string]any{"owner_tenant": f.OwnerTenant, "capability": f.Capability, "description": nullable(f.Description),
		"grants": grants, "moves_money_or_quota": f.MovesMoneyOrQuota}
}

func (s *Server) createIntegration(w http.ResponseWriter, r *http.Request) {
	var body struct {
		IntegrationID     string   `json:"integration_id"`
		OwnerTenant       string   `json:"owner_tenant"`
		Capability        string   `json:"capability"`
		Description       *string  `json:"description"`
		Grants            []string `json:"grants"`
		MovesMoneyOrQuota bool     `json:"moves_money_or_quota"`
		Reason            string   `json:"reason"`
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
	if !slugRE.MatchString(body.IntegrationID) {
		s.fail(w, r, badRequest("/integration_id", "integration_id must match "+slugRE.String()))
		return
	}
	if s.d.Pool == nil {
		s.fail(w, r, errors.New("no database"))
		return
	}
	c, err := s.catalog(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if _, taken := c.all[body.IntegrationID]; taken {
		s.fail(w, r, &Error{Status: http.StatusConflict, Code: CodeConflict, Message: fmt.Sprintf("Integration %s already exists", body.IntegrationID)})
		return
	}
	if err := checkTenants(c.snap, body.OwnerTenant, body.Grants); err != nil {
		s.fail(w, r, err)
		return
	}
	if _, ok := c.snap.Capabilities[body.Capability]; !ok {
		s.fail(w, r, badRequest("/capability", fmt.Sprintf("unknown capability %q", body.Capability)))
		return
	}
	f := registry.IntegrationFile{Integration: body.IntegrationID, OwnerTenant: body.OwnerTenant, Capability: body.Capability,
		Grants: body.Grants, MovesMoneyOrQuota: body.MovesMoneyOrQuota}
	if f.Grants == nil {
		f.Grants = []string{}
	}
	if body.Description != nil {
		f.Description = *body.Description
	}
	header, err := yaml.Marshal(&f)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	actor := principal(r).Actor()
	p, changeID, err := control.CreatePending(r.Context(), s.d.Pool, control.PendingIntegration{Integration: f.Integration, Header: header, Author: actor},
		control.Change{Actor: actor, Action: "createIntegration", ObjectType: "integration", ObjectID: f.Integration, Reason: reason, After: metaOf(f)})
	if err != nil {
		s.fail(w, r, controlError(err))
		return
	}
	in, err := pendingIntegration(p)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	w.Header().Set("ETag", integrationETag(in, false))
	writeJSON(w, http.StatusCreated, map[string]any{"change_id": changeID, "integration": s.integrationView(r.Context(), in, false, p.CreatedAt)})
}

// integrationPatch is the merge-patch of updateIntegration.
type integrationPatch struct {
	Description       json.RawMessage `json:"description"`
	Grants            *[]string       `json:"grants"`
	MovesMoneyOrQuota *bool           `json:"moves_money_or_quota"`
	Reason            string          `json:"reason"`
}

// apply merges p into f.
func (p integrationPatch) apply(f registry.IntegrationFile) (registry.IntegrationFile, error) {
	if len(p.Description) > 0 {
		var d *string
		if err := json.Unmarshal(p.Description, &d); err != nil {
			return f, badRequest("/description", "description must be a string or null")
		}
		f.Description = ""
		if d != nil {
			f.Description = *d
		}
	}
	if p.Grants != nil {
		f.Grants = slices.Clone(*p.Grants)
		if f.Grants == nil {
			f.Grants = []string{}
		}
	}
	if p.MovesMoneyOrQuota != nil {
		f.MovesMoneyOrQuota = *p.MovesMoneyOrQuota
	}
	return f, nil
}

// setKeys writes the editable fields of f into an integration.yaml document, keeping the rest
// of the file (comments, order) as it is.
func setKeys(doc []byte, f registry.IntegrationFile) ([]byte, error) {
	var root yaml.Node
	if err := yaml.Unmarshal(doc, &root); err != nil {
		return nil, err
	}
	if len(root.Content) == 0 || root.Content[0].Kind != yaml.MappingNode {
		return nil, errors.New("integration.yaml is not a YAML mapping")
	}
	m := root.Content[0]
	set := func(key string, value any) error {
		var n yaml.Node
		if err := n.Encode(value); err != nil {
			return err
		}
		for i := 0; i+1 < len(m.Content); i += 2 {
			if m.Content[i].Value == key {
				if value == nil {
					m.Content = slices.Delete(m.Content, i, i+2)
				} else {
					m.Content[i+1] = &n
				}
				return nil
			}
		}
		if value != nil {
			m.Content = append(m.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: key}, &n)
		}
		return nil
	}
	var desc any
	if f.Description != "" {
		desc = f.Description
	}
	grants := f.Grants
	if grants == nil {
		grants = []string{}
	}
	for _, kv := range []struct {
		k string
		v any
	}{{"description", desc}, {"grants", grants}, {"moves_money_or_quota", f.MovesMoneyOrQuota}} {
		if err := set(kv.k, kv.v); err != nil {
			return nil, err
		}
	}
	return yaml.Marshal(&root)
}

// updateIntegration merge-patches description, grants and moves_money_or_quota. A published
// integration changes through the publish pipeline (integration.yaml of the config tree); one
// without a published version changes its pending row. If-Match is checked under the same lock.
func (s *Server) updateIntegration(w http.ResponseWriter, r *http.Request) {
	var patch integrationPatch
	if err := decode(w, r, &patch); err != nil {
		s.fail(w, r, err)
		return
	}
	reason, err := reasonOf(r, patch.Reason)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	c, in, err := s.mutationTarget(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if patch.Grants != nil {
		if err := checkTenants(c.snap, "", *patch.Grants); err != nil {
			s.fail(w, r, err)
			return
		}
	}
	id, actor := in.Integration, principal(r).Actor()
	_, hasDraft := c.draft(id)
	change := func(before, after registry.IntegrationFile) control.Change {
		return control.Change{Actor: actor, Action: "updateIntegration", ObjectType: "integration", ObjectID: id, Reason: reason,
			Before: metaOf(before), After: metaOf(after)}
	}
	if _, pending := c.pending[id]; pending {
		p, changeID, err := control.UpdatePending(r.Context(), s.d.Pool, id, func(cur control.PendingIntegration) ([]byte, control.Change, error) {
			curIn, err := pendingIntegration(cur)
			if err != nil {
				return nil, control.Change{}, err
			}
			if tag := integrationETag(curIn, hasDraft); !ifMatch(r, tag) {
				return nil, control.Change{}, preconditionFailed(tag)
			}
			next, err := patch.apply(curIn.IntegrationFile)
			if err != nil {
				return nil, control.Change{}, err
			}
			header, err := setKeys(cur.Header, next)
			return header, change(curIn.IntegrationFile, next), err
		})
		if err != nil {
			s.fail(w, r, controlError(err))
			return
		}
		updated, err := pendingIntegration(p)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		w.Header().Set("ETag", integrationETag(updated, hasDraft))
		writeJSON(w, http.StatusOK, map[string]any{"change_id": changeID, "integration": s.integrationView(r.Context(), updated, hasDraft, p.CreatedAt)})
		return
	}
	res, err := s.d.Publisher.Edit(r.Context(), func(t control.Tree, snap *registry.Snapshot) (control.Change, error) {
		cur, ok := snap.Integrations[id]
		if !ok {
			return control.Change{}, notFound("Integration")
		}
		if tag := integrationETag(cur, hasDraft); !ifMatch(r, tag) {
			return control.Change{}, preconditionFailed(tag)
		}
		next, err := patch.apply(cur.IntegrationFile)
		if err != nil {
			return control.Change{}, err
		}
		doc, ok := t.Get(cur.File)
		if !ok {
			return control.Change{}, fmt.Errorf("%w: %s", control.ErrNotFound, cur.File)
		}
		out, err := setKeys(doc, next)
		if err != nil {
			return control.Change{}, err
		}
		t.Put(cur.File, out)
		return change(cur.IntegrationFile, next), nil
	})
	if err != nil {
		s.fail(w, r, publishError(err, id))
		return
	}
	updated := res.Snapshot.Integrations[id]
	w.Header().Set("ETag", integrationETag(updated, hasDraft))
	writeJSON(w, http.StatusOK, map[string]any{"change_id": res.ChangeID, "integration": s.integrationView(r.Context(), updated, hasDraft, res.Snapshot.BuiltAt)})
}
