package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"path"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing/fstest"

	"go.yaml.in/yaml/v3"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/control/adminapi"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/registry"
)

const applyUsage = `  apply -reason TEXT [-config-dir DIR] [-yes]
      Publish the difference between DIR and live through the admin API (same pipeline, change
      log and validation as the console). Applied: integration metadata and pause, a new
      integration version that DIR also makes active (draft + publish), account config and
      quota, tenant settings. Anything else (new accounts, API keys, vendors, transformers,
      edits of published versions) is refused before any change is made. Without -yes it only
      prints the actions.`

// serverOwned reports files the control plane writes itself (admin metadata): plan and apply
// never treat them as local changes.
func serverOwned(p string) bool { return strings.HasPrefix(p, "admin/") }

// loadTree loads a config tree for semantic comparison (nil when it does not load).
func loadTree(files map[string][]byte) *registry.Config {
	m := fstest.MapFS{}
	for p, b := range files {
		m[p] = &fstest.MapFile{Data: b}
	}
	cfg, err := registry.Load(registry.Sources{Config: m})
	if err != nil {
		return nil
	}
	return cfg
}

// sameObject reports whether the object a file defines is equal in both loaded trees, so
// formatting, comments, defaults and server-written publish metadata are not changes.
func sameObject(p string, live, local *registry.Config) bool {
	if live == nil || local == nil {
		return false
	}
	parts := strings.Split(strings.TrimSuffix(p, path.Ext(p)), "/")
	eq := func(a, b any) bool { return a != nil && b != nil && reflect.DeepEqual(a, b) }
	switch {
	case len(parts) == 2 && parts[0] == "vendors": // schemas compare by document, not compiled form
		a, b := live.Vendors[parts[1]], local.Vendors[parts[1]]
		return a != nil && b != nil && sameYAML(a, b)
	case len(parts) == 2 && parts[0] == "accounts":
		return eq(live.Accounts[parts[1]], local.Accounts[parts[1]])
	case len(parts) == 2 && parts[0] == "tenants":
		return eq(live.Tenants[parts[1]], local.Tenants[parts[1]])
	case len(parts) == 2 && parts[0] == "registrar":
		return eq(live.Registrar[parts[1]], local.Registrar[parts[1]])
	case len(parts) == 3 && parts[0] == "integrations" && parts[2] == "integration":
		a, b := live.Integrations[parts[1]], local.Integrations[parts[1]]
		return a != nil && b != nil && reflect.DeepEqual(a.IntegrationFile, b.IntegrationFile)
	case len(parts) == 4 && parts[0] == "integrations" && parts[2] == "versions":
		a, b := live.Integrations[parts[1]], local.Integrations[parts[1]]
		n, err := strconv.Atoi(parts[3])
		if a == nil || b == nil || err != nil || a.Versions[n] == nil || b.Versions[n] == nil {
			return false
		}
		va, vb := *a.Versions[n], *b.Versions[n]
		va.Publish, vb.Publish = registry.PublishMeta{}, registry.PublishMeta{} // written by the publisher
		return sameYAML(&va, &vb)
	}
	return false
}

// sameYAML compares two objects by their YAML rendering: operation and endpoint schemas compare
// by their documents (their compiled forms are separate graphs).
func sameYAML(a, b any) bool {
	ya, erra := yaml.Marshal(a)
	yb, errb := yaml.Marshal(b)
	return erra == nil && errb == nil && string(ya) == string(yb)
}

// semanticDiff is diffTrees without server-owned files and without files whose object is
// unchanged.
func semanticDiff(live, local map[string][]byte) ([]fileChange, *registry.Config, *registry.Config) {
	lc, oc := loadTree(live), loadTree(local)
	var out []fileChange
	for _, c := range diffTrees(live, local) {
		if serverOwned(c.Path) || (c.Kind == "change" && sameObject(c.Path, lc, oc)) {
			continue
		}
		out = append(out, c)
	}
	return out, lc, oc
}

// action is one admin call apply makes.
type action struct {
	what         string // human summary
	method, path string
	body         map[string]any
	then         *action // follow-up call (publish after draft)
}

// planActions maps file changes to admin calls, or refuses them all with the reasons.
func planActions(changes []fileChange, live, local *registry.Config, reason string) ([]action, []string) {
	var acts []action
	var refused []string
	refuse := func(c fileChange, why string) {
		refused = append(refused, fmt.Sprintf("%s (%s): %s", c.Object, c.Path, why))
	}
	if live == nil || local == nil {
		return nil, []string{"the local or live tree does not load as a registry: fix it first (hubctl plan shows the files)"}
	}
	newVersions := map[string]int{} // integration → version file added
	for _, c := range changes {
		parts := strings.Split(strings.TrimSuffix(c.Path, path.Ext(c.Path)), "/")
		if len(parts) == 4 && parts[0] == "integrations" && parts[2] == "versions" && c.Kind == "add" {
			n, _ := strconv.Atoi(parts[3])
			newVersions[parts[1]] = n
		}
	}
	for _, c := range changes {
		parts := strings.Split(strings.TrimSuffix(c.Path, path.Ext(c.Path)), "/")
		switch {
		case len(parts) == 2 && parts[0] == "accounts" && c.Kind == "change":
			a, b := live.Accounts[parts[1]], local.Accounts[parts[1]]
			if a.Vendor != b.Vendor || a.Region != b.Region || a.State != b.State || a.CredentialsRef != b.CredentialsRef {
				refuse(c, "only config and quota change through apply (vendor, region and credentials are immutable; use the lifecycle endpoint for state)")
				continue
			}
			patch := map[string]any{}
			for k := range a.Config {
				if _, ok := b.Config[k]; !ok {
					patch[k] = nil
				}
			}
			for k, v := range b.Config {
				if a.Config[k] != v {
					patch[k] = v
				}
			}
			body := map[string]any{"reason": reason}
			if len(patch) > 0 {
				body["config"] = patch
			}
			if a.Quota != b.Quota {
				body["quota"] = map[string]any{"rps": b.Quota.RPS, "daily": b.Quota.Daily}
			}
			acts = append(acts, action{what: "update account " + parts[1], method: http.MethodPatch, path: "/accounts/" + url.PathEscape(parts[1]), body: body})
		case len(parts) == 2 && parts[0] == "tenants" && c.Kind == "change":
			a, b := live.Tenants[parts[1]], local.Tenants[parts[1]]
			if !reflect.DeepEqual(a.APIKeys, b.APIKeys) || a.Status != b.Status {
				refuse(c, "API keys hold secrets: mint, rotate and revoke them through the admin API, not files")
				continue
			}
			body := map[string]any{"reason": reason, "rate_limit": map[string]any{"rps": b.RateLimit.RPS, "burst": b.RateLimit.Burst},
				"disabled": b.Disabled, "description": nullIfEmpty(b.Description), "contact": nullIfEmpty(b.Contact)}
			if b.DisplayName != "" {
				body["display_name"] = b.DisplayName
			}
			acts = append(acts, action{what: "update tenant " + parts[1], method: http.MethodPatch, path: "/tenants/" + url.PathEscape(parts[1]), body: body})
		case len(parts) == 3 && parts[0] == "integrations" && parts[2] == "integration" && c.Kind == "change":
			id := parts[1]
			a, b := live.Integrations[id], local.Integrations[id]
			if a.OwnerTenant != b.OwnerTenant || a.Interactive != b.Interactive {
				refuse(c, "owner and interactive are fixed at creation: create a new integration ID instead")
				continue
			}
			if a.ActiveVersion != b.ActiveVersion && newVersions[id] != b.ActiveVersion {
				refuse(c, fmt.Sprintf("active_version %d → %d without a new version file: roll back through the admin API (it publishes a copy)", a.ActiveVersion, b.ActiveVersion))
				continue
			}
			if a.Description != b.Description || !slices.Equal(a.Grants, b.Grants) || a.MovesMoneyOrQuota != b.MovesMoneyOrQuota {
				grants := b.Grants
				if grants == nil {
					grants = []string{}
				}
				acts = append(acts, action{what: "update integration " + id, method: http.MethodPatch, path: "/integrations/" + url.PathEscape(id),
					body: map[string]any{"reason": reason, "description": nullIfEmpty(b.Description), "grants": grants, "moves_money_or_quota": b.MovesMoneyOrQuota}})
			}
			if a.Paused != b.Paused {
				op := "resume"
				if b.Paused {
					op = "pause"
				}
				acts = append(acts, action{what: op + " integration " + id, method: http.MethodPost, path: "/integrations/" + url.PathEscape(id) + "/" + op,
					body: map[string]any{"reason": reason}})
			}
		case len(parts) == 4 && parts[0] == "integrations" && parts[2] == "versions" && c.Kind == "add":
			id := parts[1]
			n, _ := strconv.Atoi(parts[3])
			a, b := live.Integrations[id], local.Integrations[id]
			if a == nil || b == nil {
				refuse(c, "new integrations are created through the admin API (createIntegration), then versioned here")
				continue
			}
			latest := slices.Max(slices.Collect(maps.Keys(a.Versions)))
			if n != latest+1 {
				refuse(c, fmt.Sprintf("the next version of %s is %d (versions are numbered by the control plane)", id, latest+1))
				continue
			}
			if b.ActiveVersion != n {
				refuse(c, fmt.Sprintf("set active_version: %d in integration.yaml as well: apply publishes the new version", n))
				continue
			}
			publish := &action{what: fmt.Sprintf("publish %s v%d", id, n), method: http.MethodPost,
				path: fmt.Sprintf("/integrations/%s/versions/%d/publish", url.PathEscape(id), n),
				body: map[string]any{"expected_active_version": a.ActiveVersion, "reason": reason}}
			acts = append(acts, action{what: fmt.Sprintf("draft %s v%d", id, n), method: http.MethodPost, path: "/integrations/" + url.PathEscape(id) + "/versions",
				body: map[string]any{"spec": adminapi.SpecOf(b.Versions[n]), "reason": reason}, then: publish})
		case len(parts) == 4 && parts[0] == "integrations" && parts[2] == "versions":
			refuse(c, "published versions are immutable: add a new version file instead")
		default:
			refuse(c, "not applied by hubctl: use the admin API or console for this object")
		}
	}
	return acts, refused
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func applyCmd(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("hubctl apply", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var a adminFlags
	a.register(fs)
	dir := fs.String("config-dir", "config", "local registry config directory")
	_ = fs.String("contracts-dir", "", "ignored since ADR-0023 (schemas live in the config tree)")
	reason := fs.String("reason", "", "why (recorded in the change log of every change; required)")
	yes := fs.Bool("yes", false, "make the changes (default: print them only)")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 {
		fmt.Fprintln(stderr, applyUsage)
		return 2
	}
	if len(strings.TrimSpace(*reason)) < 3 {
		fmt.Fprintln(stderr, "hubctl: -reason is required (at least 3 characters)")
		return 2
	}
	c, err := a.client()
	if err != nil {
		fmt.Fprintf(stderr, "hubctl: %v\n", err)
		return 1
	}
	local, err := readTree(*dir)
	if err != nil {
		fmt.Fprintf(stderr, "hubctl: %v\n", err)
		return 1
	}
	ctx := context.Background()
	live, version, err := c.liveTree(ctx)
	if err != nil {
		fmt.Fprintf(stderr, "hubctl: %v\n", err)
		return 1
	}
	changes, lc, oc := semanticDiff(live, local)
	if len(changes) == 0 {
		fmt.Fprintf(stdout, "No changes: %s matches snapshot %d.\n", *dir, version)
		return 0
	}
	acts, refused := planActions(changes, lc, oc, *reason)
	if len(refused) > 0 {
		fmt.Fprintln(stderr, "hubctl: nothing applied; these changes cannot be applied from files:")
		for _, r := range refused {
			fmt.Fprintf(stderr, "  %s\n", r)
		}
		return 1
	}
	for _, act := range acts {
		fmt.Fprintf(stdout, "%s: %s %s\n", act.what, act.method, act.path)
		if act.then != nil {
			fmt.Fprintf(stdout, "%s: %s %s\n", act.then.what, act.then.method, act.then.path)
		}
	}
	if !*yes {
		fmt.Fprintf(stdout, "\n%d change(s) against snapshot %d; run again with -yes to apply.\n", len(acts), version)
		return 3
	}
	done := 0
	for _, act := range acts {
		for step := &act; step != nil; step = step.then {
			var res struct {
				ChangeID string `json:"change_id"`
			}
			if err := c.do(ctx, step.method, step.path, step.body, &res); err != nil {
				fmt.Fprintf(stderr, "hubctl: %s failed after %d change(s):\n%v\n", step.what, done, err)
				return 1
			}
			done++
			fmt.Fprintf(stdout, "  done %s (change %s)\n", step.what, res.ChangeID)
		}
	}
	fmt.Fprintf(stdout, "%d change(s) applied.\n", done)
	return 0
}
