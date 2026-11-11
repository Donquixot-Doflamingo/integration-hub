package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/registry"
)

// Linking existing tenants to ISFC (AUTH_PLAN §15 step 3, HUB-498). A tenant created in the Hub
// before ISFC owned tenants has no `isfc` block, so the reconciler would create a second Hub
// tenant for the same ISFC tenant. `tenants link` writes isfc.tenant_id into the EXISTING tenant
// file, so the sync finds it by that UUID and only updates the ISFC copy.
//
// This is config as code on the tenant YAML tree (-config-dir), offline: the admin contract has
// no operation that writes the ISFC link (isfc is read-only on updateTenant) and `hubctl apply`
// therefore refuses it. See the contract change request in the HUB-498 ticket.

const tenantsUsage = `tenants:
  tenants link [-config-dir DIR] [-key KEY] [-name NAME] [-force] HUB-TENANT-ID ISFC-TENANT-UUID
      Write isfc.tenant_id into the existing tenant file DIR/tenants/HUB-TENANT-ID.yaml (default
      DIR config), so the ISFC sync updates this tenant instead of creating a duplicate. Offline:
      no admin call, no ISFC call. Comments and the rest of the file are kept. -key and -name
      seed the ISFC copy (defaults: the Hub tenant id and display name); the next sync
      overwrites them from ISFC. Refuses a UUID already linked to another tenant, and a tenant
      already linked to another UUID unless -force. Beware: a UUID that ISFC does not know (or
      does not link to the Hub console client) makes the sync DISABLE the tenant; run the sync
      in report-only mode (HUB_ISFC_SYNC_DRY_RUN=true) first.
  tenants link --report [-config-dir DIR]
      List Hub tenants with no ISFC link. Exit 0 when every tenant is linked, 3 when some are not.`

var isfcUUID = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

func tenantsCmd(args []string, stdout, stderr io.Writer) int {
	if len(args) < 1 || args[0] != "link" {
		fmt.Fprintln(stderr, tenantsUsage)
		return 2
	}
	fs := flag.NewFlagSet("hubctl tenants link", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dir := fs.String("config-dir", "config", "registry config directory")
	report := fs.Bool("report", false, "list Hub tenants with no ISFC link")
	key := fs.String("key", "", "ISFC tenant key to seed the copy with (default: the Hub tenant id)")
	name := fs.String("name", "", "ISFC tenant name to seed the copy with (default: the Hub display name)")
	force := fs.Bool("force", false, "replace an existing link to another ISFC tenant")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	files, err := readTree(*dir)
	if err != nil {
		fmt.Fprintf(stderr, "hubctl: read %s: %v\n", *dir, err)
		return 2
	}
	if *report {
		if fs.NArg() != 0 {
			fmt.Fprintln(stderr, tenantsUsage)
			return 2
		}
		cfg := loadTree(files)
		if cfg == nil {
			fmt.Fprintf(stderr, "hubctl: config in %s does not load (hubctl plan shows the files)\n", *dir)
			return 2
		}
		un := unlinkedTenants(cfg)
		linked := len(cfg.Tenants) - len(un)
		fmt.Fprintf(stdout, "%d tenants, %d linked to ISFC, %d without an ISFC link\n", len(cfg.Tenants), linked, len(un))
		for _, id := range un {
			fmt.Fprintf(stdout, "  unlinked  %s\n", id)
		}
		if len(un) > 0 {
			fmt.Fprintln(stdout, "link each one: hubctl tenants link <hub-tenant-id> <isfc-tenant-uuid>")
			return 3
		}
		return 0
	}
	if fs.NArg() != 2 {
		fmt.Fprintln(stderr, tenantsUsage)
		return 2
	}
	p, data, err := linkTenant(files, fs.Arg(0), fs.Arg(1), linkOptions{Key: *key, Name: *name, Force: *force})
	if err != nil {
		fmt.Fprintf(stderr, "hubctl: %v\n", err)
		return 1
	}
	if err := os.WriteFile(filepath.Join(*dir, filepath.FromSlash(p)), data, 0o644); err != nil {
		fmt.Fprintf(stderr, "hubctl: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "linked Hub tenant %s to ISFC tenant %s (%s)\n", fs.Arg(0), strings.ToLower(fs.Arg(1)), p)
	fmt.Fprintln(stdout, "Next: commit the file; the sync updates this tenant from ISFC (isfc.key, name, status). If ISFC does not know this UUID, the sync disables the tenant.")
	return 0
}

// unlinkedTenants returns the ids of the Hub tenants that have no ISFC link, sorted.
func unlinkedTenants(cfg *registry.Config) []string {
	var out []string
	for id, t := range cfg.Tenants {
		if t.ISFC == nil || t.ISFC.TenantID == "" {
			out = append(out, id)
		}
	}
	slices.Sort(out)
	return out
}

type linkOptions struct {
	Key, Name string
	Force     bool
}

// linkTenant returns the path and new content of the tenant file with isfc.tenant_id set, or an
// error. files is the config tree (slash paths, as readTree returns it). The result is checked by
// loading the whole tree with the change applied, so every registry rule (UUID shape, duplicate
// link) holds before anything is written.
func linkTenant(files map[string][]byte, hubID, isfcID string, o linkOptions) (string, []byte, error) {
	if !isfcUUID.MatchString(isfcID) {
		return "", nil, fmt.Errorf("%q is not an ISFC tenant UUID", isfcID)
	}
	isfcID = strings.ToLower(isfcID)
	if hubID == "" || strings.ContainsAny(hubID, `/\`) || strings.Contains(hubID, "..") {
		return "", nil, fmt.Errorf("%q is not a Hub tenant id", hubID)
	}
	cfg := loadTree(files)
	if cfg == nil {
		return "", nil, errors.New("the config tree does not load: fix it first (hubctl plan shows the files)")
	}
	t, ok := cfg.Tenants[hubID]
	if !ok {
		return "", nil, fmt.Errorf("no Hub tenant %q: link only updates an existing tenant (the sync creates new ones)", hubID)
	}
	for id, other := range cfg.Tenants {
		if id != hubID && other.ISFC != nil && strings.EqualFold(other.ISFC.TenantID, isfcID) {
			return "", nil, fmt.Errorf("ISFC tenant %s is already linked to Hub tenant %q", isfcID, id)
		}
	}
	if t.ISFC != nil && !strings.EqualFold(t.ISFC.TenantID, isfcID) && !o.Force {
		return "", nil, fmt.Errorf("Hub tenant %q is already linked to ISFC tenant %s (use -force to relink)", hubID, t.ISFC.TenantID)
	}
	p := "tenants/" + hubID + ".yaml"
	raw, ok := files[p]
	if !ok {
		return "", nil, fmt.Errorf("tenant %q is not defined in %s", hubID, p)
	}
	key, name := o.Key, o.Name
	if key == "" {
		key = hubID
	}
	if name == "" {
		name = t.DisplayName
	}
	if name == "" {
		name = hubID
	}
	out, err := setISFCBlock(raw, t.ISFC, isfcID, key, name)
	if err != nil {
		return "", nil, fmt.Errorf("%s: %w", p, err)
	}
	next := maps0(files)
	next[p] = out
	if loadTree(next) == nil {
		return "", nil, errors.New("the linked tree does not load (is the ISFC UUID or key invalid?)")
	}
	return p, out, nil
}

func maps0(m map[string][]byte) map[string][]byte {
	c := make(map[string][]byte, len(m))
	for k, v := range m {
		c[k] = v
	}
	return c
}

// setISFCBlock edits the YAML as a node tree so comments, order and flow style survive. An
// existing block keeps its other fields; only tenant_id changes (and the seeded copy resets, as
// a different ISFC tenant's key, name and status do not apply).
func setISFCBlock(raw []byte, cur *registry.TenantISFC, isfcID, key, name string) ([]byte, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return nil, err
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) != 1 || doc.Content[0].Kind != yaml.MappingNode {
		return nil, errors.New("not a YAML mapping")
	}
	root := doc.Content[0]
	block := &yaml.Node{Kind: yaml.MappingNode}
	add := func(k string, v *yaml.Node) {
		block.Content = append(block.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: k}, v)
	}
	str := func(s string) *yaml.Node { return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: s} }
	add("tenant_id", str(isfcID))
	if cur != nil && strings.EqualFold(cur.TenantID, isfcID) {
		return raw, nil // already linked to this tenant: nothing to write
	}
	add("key", str(key))
	add("name", str(name))
	add("status", str(registry.ISFCStatusActive))
	add("linked", &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool", Value: "false"})
	for i := 0; i+1 < len(root.Content); i += 2 {
		if root.Content[i].Value == "isfc" {
			root.Content[i+1] = block
			return encodeYAML(&doc)
		}
	}
	root.Content = append(root.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: "isfc"}, block)
	return encodeYAML(&doc)
}

func encodeYAML(n *yaml.Node) ([]byte, error) {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(n); err != nil {
		return nil, err
	}
	return buf.Bytes(), enc.Close()
}
