package registry

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"slices"
	"strings"
	"testing/fstest"

	"go.yaml.in/yaml/v3"
)

// Legacy trees (before ADR-0023) bound every integration to a capability: capabilities/*.yaml
// pointed at JSON Schemas on the contracts side of the sources, integration.yaml and transformer
// files named the capability, and vendor endpoints had no request schema. Snapshot blobs packed
// that way (blob format 1), integration drafts and pending integrations written before the
// change still hold that shape. UpgradeLegacy turns such a tree into the current one:
//
//   - each integration version gets operations copied from its capability (description,
//     idempotent, the request schema as request_schema.body); the response schema is dropped,
//     since response transformers are no longer checked against a schema;
//   - integration.yaml loses capability and gains interactive: true when the capability was
//     interactive;
//   - transformer files lose capability and operation;
//   - vendor endpoints without a request schema get request_schema: {} (nothing checked);
//   - capabilities/ is dropped.
//
// Comments and layout of every file are kept. A tree without capabilities is returned as is.

// LegacyCapability is what a legacy capability contributes to the upgraded tree.
type LegacyCapability struct {
	Key         string
	Interactive bool
	Operations  map[string]LegacyOperation
}

// LegacyOperation is one capability operation.
type LegacyOperation struct {
	Description string
	Idempotent  bool
	Body        map[string]any
}

// Legacy describes an upgrade: the legacy checksum the content had and what the capabilities
// were, so drafts and pending integrations of the same database can be upgraded too.
type Legacy struct {
	Checksum     string
	Capabilities map[string]*LegacyCapability
	// IntegrationCapability maps each integration of the tree to its capability.
	IntegrationCapability map[string]string
}

// IsLegacyTree reports whether config still holds capabilities.
func IsLegacyTree(config fs.FS) bool {
	entries, err := fs.ReadDir(config, "capabilities")
	return err == nil && len(entries) > 0
}

// UpgradeLegacy upgrades a legacy config tree whose capability schemas are in contracts (the
// files capabilities/*.yaml name with schema_ref). It returns the upgraded tree and what it
// found; a tree without capabilities comes back unchanged with a nil *Legacy.
func UpgradeLegacy(config, contracts fs.FS) (fstest.MapFS, *Legacy, error) {
	files, err := readTree(config)
	if err != nil {
		return nil, nil, err
	}
	if !IsLegacyTree(config) {
		return toMapFS(files), nil, nil
	}
	lg := &Legacy{Capabilities: map[string]*LegacyCapability{}, IntegrationCapability: map[string]string{}}
	schemas, err := lg.readCapabilities(files, contracts)
	if err != nil {
		return nil, nil, err
	}
	lg.Checksum = legacyChecksum(files, schemas)
	out := map[string][]byte{}
	for _, name := range slices.Sorted(mapKeys(files)) {
		b := files[name]
		var err error
		switch {
		case strings.HasPrefix(name, "capabilities/"):
			continue
		case strings.HasPrefix(name, "vendors/") && strings.HasSuffix(name, ".yaml"):
			b, err = upgradeVendor(b)
		case strings.HasPrefix(name, "transformers/") && strings.HasSuffix(name, ".yaml"):
			b, err = removeKeys(b, "capability", "operation")
		case strings.HasPrefix(name, "integrations/") && strings.HasSuffix(name, "/integration.yaml"):
			var key string
			b, key, err = lg.UpgradeIntegrationDoc(b)
			if key != "" {
				lg.IntegrationCapability[strings.Split(name, "/")[1]] = key
			}
		}
		if err != nil {
			return nil, nil, fmt.Errorf("upgrade %s: %w", name, err)
		}
		out[name] = b
	}
	for _, name := range slices.Sorted(mapKeys(out)) {
		parts := strings.Split(name, "/")
		if len(parts) != 4 || parts[0] != "integrations" || parts[2] != "versions" || !strings.HasSuffix(name, ".yaml") {
			continue
		}
		b, err := lg.UpgradeVersionDoc(out[name], lg.IntegrationCapability[parts[1]])
		if err != nil {
			return nil, nil, fmt.Errorf("upgrade %s: %w", name, err)
		}
		out[name] = b
	}
	return toMapFS(out), lg, nil
}

type legacySchemas struct {
	key      string
	ops      []string
	req, res map[string][]byte
}

// readCapabilities reads capabilities/*.yaml and their schemas, in key order.
func (lg *Legacy) readCapabilities(files map[string][]byte, contracts fs.FS) ([]legacySchemas, error) {
	if contracts == nil {
		return nil, errors.New("legacy tree: capability schemas are missing (no contracts side)")
	}
	var all []legacySchemas
	for _, name := range slices.Sorted(mapKeys(files)) {
		if !strings.HasPrefix(name, "capabilities/") || !strings.HasSuffix(name, ".yaml") {
			continue
		}
		var cf struct {
			Capability string `yaml:"capability"`
			SchemaRef  string `yaml:"schema_ref"`
		}
		if err := yaml.Unmarshal(files[name], &cf); err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		root, err := fs.ReadFile(contracts, cf.SchemaRef)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		var hdr struct {
			Interactive bool `json:"interactive"`
			Operations  map[string]struct {
				Description    string `json:"description"`
				Idempotent     bool   `json:"idempotent"`
				RequestSchema  string `json:"request_schema"`
				ResponseSchema string `json:"response_schema"`
			} `json:"operations"`
		}
		if err := json.Unmarshal(root, &hdr); err != nil {
			return nil, fmt.Errorf("%s: %w", cf.SchemaRef, err)
		}
		lc := &LegacyCapability{Key: cf.Capability, Interactive: hdr.Interactive, Operations: map[string]LegacyOperation{}}
		ls := legacySchemas{key: cf.Capability, req: map[string][]byte{}, res: map[string][]byte{}}
		dir := path.Dir(cf.SchemaRef)
		for _, op := range slices.Sorted(mapKeys(hdr.Operations)) {
			o := hdr.Operations[op]
			req, err := fs.ReadFile(contracts, path.Join(dir, o.RequestSchema))
			if err != nil {
				return nil, fmt.Errorf("%s operation %s: %w", cf.Capability, op, err)
			}
			res, err := fs.ReadFile(contracts, path.Join(dir, o.ResponseSchema))
			if err != nil {
				return nil, fmt.Errorf("%s operation %s: %w", cf.Capability, op, err)
			}
			var body map[string]any
			if err := json.Unmarshal(req, &body); err != nil {
				return nil, fmt.Errorf("%s operation %s: %w", cf.Capability, op, err)
			}
			for _, k := range []string{"$id", "interactive", "operations", "x-capability", "x-capability-minor"} {
				delete(body, k) // capability header fields of the root schema (and its urn), not part of the body schema
			}
			lc.Operations[op] = LegacyOperation{Description: o.Description, Idempotent: o.Idempotent, Body: body}
			ls.ops = append(ls.ops, op)
			ls.req[op], ls.res[op] = req, res
		}
		lg.Capabilities[cf.Capability] = lc
		all = append(all, ls)
	}
	slices.SortFunc(all, func(a, b legacySchemas) int { return strings.Compare(a.key, b.key) })
	return all, nil
}

// legacyChecksum is the checksum a legacy snapshot was archived with: every config file, then
// every capability operation's request and response schema.
func legacyChecksum(files map[string][]byte, schemas []legacySchemas) string {
	h := sha256.New()
	for _, p := range slices.Sorted(mapKeys(files)) {
		h.Write([]byte(p))
		h.Write([]byte{0})
		h.Write(files[p])
		h.Write([]byte{0})
	}
	for _, s := range schemas {
		for _, op := range s.ops {
			h.Write([]byte(s.key + "/" + op))
			h.Write(s.req[op])
			h.Write(s.res[op])
		}
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil))
}

// UpgradeIntegrationDoc upgrades a legacy integration.yaml (also a pending integration's
// header): capability removed, interactive set from it. It returns the capability it named.
func (lg *Legacy) UpgradeIntegrationDoc(doc []byte) ([]byte, string, error) {
	root, m, err := mapping(doc)
	if err != nil {
		return nil, "", err
	}
	key := scalar(m, "capability")
	if key == "" {
		return doc, "", nil
	}
	removeKey(m, "capability")
	if c := lg.Capabilities[key]; c != nil && c.Interactive && scalar(m, "interactive") == "" {
		m.Content = append(m.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: "interactive"}, &yaml.Node{Kind: yaml.ScalarNode, Value: "true", Tag: "!!bool"})
	}
	out, err := marshal(root)
	return out, key, err
}

// UpgradeVersionDoc upgrades a legacy integration version document (also a draft's spec):
// capability removed and, when it has no operations, operations copied from capability key.
func (lg *Legacy) UpgradeVersionDoc(doc []byte, key string) ([]byte, error) {
	root, m, err := mapping(doc)
	if err != nil {
		return nil, err
	}
	if k := scalar(m, "capability"); k != "" {
		key = k
	}
	changed := removeKey(m, "capability")
	if find(m, "operations") == nil {
		c := lg.Capabilities[key]
		if c == nil {
			return nil, fmt.Errorf("no operations and unknown capability %q", key)
		}
		ops := map[string]any{}
		for name, o := range c.Operations {
			op := map[string]any{"idempotent": o.Idempotent, "request_schema": map[string]any{"body": o.Body}}
			if o.Description != "" {
				op["description"] = o.Description
			}
			ops[name] = op
		}
		var n yaml.Node
		if err := n.Encode(ops); err != nil {
			return nil, err
		}
		// operations go right after version, where a reader expects them.
		at := len(m.Content)
		for i := 0; i+1 < len(m.Content); i += 2 {
			if m.Content[i].Value == "version" {
				at = i + 2
			}
		}
		m.Content = slices.Insert(m.Content, at, &yaml.Node{Kind: yaml.ScalarNode, Value: "operations"}, &n)
		changed = true
	}
	if !changed {
		return doc, nil
	}
	return marshal(root)
}

func upgradeVendor(doc []byte) ([]byte, error) {
	root, m, err := mapping(doc)
	if err != nil {
		return nil, err
	}
	eps := find(m, "endpoints")
	if eps == nil || eps.Kind != yaml.MappingNode {
		return doc, nil
	}
	changed := false
	for i := 1; i < len(eps.Content); i += 2 {
		ep := eps.Content[i]
		if ep.Kind == yaml.MappingNode && find(ep, "request_schema") == nil {
			ep.Content = append(ep.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: "request_schema"},
				&yaml.Node{Kind: yaml.MappingNode, Tag: "!!map", Style: yaml.FlowStyle})
			changed = true
		}
	}
	if !changed {
		return doc, nil
	}
	return marshal(root)
}

func removeKeys(doc []byte, keys ...string) ([]byte, error) {
	root, m, err := mapping(doc)
	if err != nil {
		return nil, err
	}
	changed := false
	for _, k := range keys {
		changed = removeKey(m, k) || changed
	}
	if !changed {
		return doc, nil
	}
	return marshal(root)
}

func mapping(doc []byte) (*yaml.Node, *yaml.Node, error) {
	var root yaml.Node
	if err := yaml.Unmarshal(doc, &root); err != nil {
		return nil, nil, err
	}
	if len(root.Content) == 0 || root.Content[0].Kind != yaml.MappingNode {
		return nil, nil, errors.New("not a YAML mapping")
	}
	return &root, root.Content[0], nil
}

func find(m *yaml.Node, key string) *yaml.Node {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i+1]
		}
	}
	return nil
}

func scalar(m *yaml.Node, key string) string {
	if n := find(m, key); n != nil && n.Kind == yaml.ScalarNode {
		return n.Value
	}
	return ""
}

func removeKey(m *yaml.Node, key string) bool {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			// Keep a head comment of the removed key on the next key.
			if c := m.Content[i].HeadComment; c != "" && i+2 < len(m.Content) && m.Content[i+2].HeadComment == "" {
				m.Content[i+2].HeadComment = c
			}
			m.Content = slices.Delete(m.Content, i, i+2)
			return true
		}
	}
	return false
}

func readTree(fsys fs.FS) (map[string][]byte, error) {
	files := map[string][]byte{}
	err := fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := fs.ReadFile(fsys, p)
		files[p] = b
		return err
	})
	return files, err
}

func toMapFS(files map[string][]byte) fstest.MapFS {
	m := fstest.MapFS{}
	for p, b := range files {
		m[p] = &fstest.MapFile{Data: bytes.Clone(b)}
	}
	return m
}

// marshal writes a YAML document with the registry's two-space indentation.
func marshal(root *yaml.Node) ([]byte, error) {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(root); err != nil {
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
