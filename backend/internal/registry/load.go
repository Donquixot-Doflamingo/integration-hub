package registry

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"go.yaml.in/yaml/v3"
)

// Problem is one config error, qualified by file and YAML path:
// "integrations/int_evt_brandA/versions/3.yaml: delivery.wait_ms: must be ≤ 30000".
type Problem struct {
	File    string
	Path    string
	Message string
}

func (p Problem) String() string {
	if p.Path == "" {
		return p.File + ": " + p.Message
	}
	return p.File + ": " + p.Path + ": " + p.Message
}

// ConfigErrors is every problem found in one pass. It is an error when non-empty.
type ConfigErrors []Problem

func (ps ConfigErrors) Error() string {
	lines := make([]string, len(ps))
	for i, p := range ps {
		lines[i] = p.String()
	}
	return fmt.Sprintf("%d config problem(s):\n  %s", len(ps), strings.Join(lines, "\n  "))
}

func (ps *ConfigErrors) add(file, path, format string, args ...any) {
	*ps = append(*ps, Problem{File: file, Path: path, Message: fmt.Sprintf(format, args...)})
}

// Sources are where config is read from: the registry directory (backend/config) and the
// contracts directory (repo-root contracts/) that holds capability schemas.
type Sources struct {
	Config    fs.FS
	Contracts fs.FS
}

// Capability is a capability with its compiled schemas.
type Capability struct {
	Key         string // "events.track@v1"
	Minor       int
	Interactive bool
	DefaultTTL  time.Duration
	Operations  map[string]*Operation
	File        string
	SchemaRef   string // contracts-relative path of the root request schema
	SchemaDir   string // contracts-relative directory of every schema file of this capability
}

// Operation is one operation of a capability.
type Operation struct {
	Name           string
	Description    string
	Idempotent     bool
	Request        *jsonschema.Schema
	Response       *jsonschema.Schema
	RequestID      string // schema $id, recorded by the request.validated audit event
	RequestFile    string // file names relative to Capability.SchemaDir
	ResponseFile   string
	RequestSchema  json.RawMessage
	ResponseSchema json.RawMessage
	// PII lists the x-pii tagged locations of the request (JSON pointer; "*" = any array item).
	PII []PIIPath
}

// PIIPath is one x-pii tagged location.
type PIIPath struct {
	Pointer string
	Kind    string
}

// Integration is an integration with all of its versions.
type Integration struct {
	IntegrationFile
	Versions map[int]*IntegrationVersion
	File     string
	// VersionFiles maps version number to its file, for error messages.
	VersionFiles map[int]string
}

// Config is everything loaded from Sources, before cross-reference validation.
type Config struct {
	Capabilities map[string]*Capability
	Vendors      map[string]*Vendor
	Accounts     map[string]*Account
	Transformers map[TransformerRef]*TransformerFile
	Integrations map[string]*Integration
	Tenants      map[string]*Tenant
	Registrar    map[string]*RegistrarEndpoint
	// files maps "<kind>/<id>" to the file the object came from.
	files map[string]string
}

func (c *Config) file(kind, id string) string { return c.files[kind+"/"+id] }

// Delivery and audit defaults (DESIGN §2 D3, D4, D10; §13.2).
const (
	DefaultWaitMS    = 3000
	MaxWaitMS        = 30000
	DefaultRetention = 30 * 24 * time.Hour
)

var (
	idPattern       = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{2,63}$`)
	capKeyPattern   = regexp.MustCompile(`^[a-z][a-z0-9_.]*@v[1-9][0-9]*$`)
	opNamePattern   = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}$`)
	transformerID   = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]{0,63}$`)
	keyHashPattern  = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	endpointPattern = regexp.MustCompile(`^[a-z][a-z0-9_]*@[1-9][0-9]*$`)
)

// Load reads every object from src. It reports all decode and per-file problems at once; cross-
// references are checked by Validate.
func Load(src Sources) (*Config, error) {
	c := &Config{
		Capabilities: map[string]*Capability{}, Vendors: map[string]*Vendor{}, Accounts: map[string]*Account{},
		Transformers: map[TransformerRef]*TransformerFile{}, Integrations: map[string]*Integration{},
		Tenants: map[string]*Tenant{}, Registrar: map[string]*RegistrarEndpoint{}, files: map[string]string{},
	}
	var ps ConfigErrors
	c.loadCapabilities(src, &ps)
	eachYAML(src.Config, "vendors", &ps, func(file string, data []byte) {
		var v Vendor
		if decode(file, data, &v, &ps) && c.put(file, "vendor", v.Vendor, &ps) {
			for name, ep := range v.Endpoints {
				if !endpointPattern.MatchString(name) {
					ps.add(file, "endpoints."+name, "endpoint keys look like name@version (e.g. upload@1)")
				}
				if ep.TimeoutMS <= 0 {
					ps.add(file, "endpoints."+name+".timeout_ms", "must be > 0")
				}
				if ep.ConnectTimeoutMS <= 0 {
					ps.add(file, "endpoints."+name+".connect_timeout_ms", "must be > 0")
				}
			}
			c.Vendors[v.Vendor] = &v
		}
	})
	eachYAML(src.Config, "accounts", &ps, func(file string, data []byte) {
		var a Account
		if decode(file, data, &a, &ps) && c.put(file, "account", a.Account, &ps) {
			switch a.State {
			case AccountActive, AccountDraining, AccountRetired:
			default:
				ps.add(file, "state", "must be active, draining or retired")
			}
			c.Accounts[a.Account] = &a
		}
	})
	c.loadTransformers(src, &ps)
	c.loadIntegrations(src, &ps)
	eachYAML(src.Config, "tenants", &ps, func(file string, data []byte) {
		var t Tenant
		if decode(file, data, &t, &ps) && c.put(file, "tenant", t.Tenant, &ps) {
			for i, k := range t.APIKeys {
				at := fmt.Sprintf("api_keys[%d]", i)
				if !keyHashPattern.MatchString(k.Hash) {
					ps.add(file, at+".hash", `must be "sha256:<64 lowercase hex>"; plaintext keys are never stored`)
				}
				if k.KeyID == "" {
					ps.add(file, at+".key_id", "required")
				}
			}
			c.Tenants[t.Tenant] = &t
		}
	})
	eachYAML(src.Config, "registrar", &ps, func(file string, data []byte) {
		var r RegistrarEndpoint
		if decode(file, data, &r, &ps) && c.put(file, "registrar", r.EndpointID, &ps) {
			c.Registrar[r.EndpointID] = &r
		}
	})
	if len(ps) > 0 {
		return c, ps
	}
	return c, nil
}

// put checks that the object's id is valid and matches its file name, and records the file.
func (c *Config) put(file, kind, id string, ps *ConfigErrors) bool {
	field := map[string]string{"vendor": "vendor", "account": "account", "tenant": "tenant", "registrar": "endpoint_id"}[kind]
	if !idPattern.MatchString(id) {
		ps.add(file, field, "id %q must match %s", id, idPattern)
		return false
	}
	if base := strings.TrimSuffix(path.Base(file), ".yaml"); base != id {
		ps.add(file, field, "id %q must equal the file name %q", id, base)
		return false
	}
	if prev, dup := c.files[kind+"/"+id]; dup {
		ps.add(file, field, "duplicate %s %q (also in %s)", kind, id, prev)
		return false
	}
	c.files[kind+"/"+id] = file
	return true
}

func eachYAML(fsys fs.FS, dir string, ps *ConfigErrors, fn func(file string, data []byte)) {
	entries, err := fs.ReadDir(fsys, dir)
	if errors.Is(err, fs.ErrNotExist) {
		return
	}
	if err != nil {
		ps.add(dir, "", "read directory: %v", err)
		return
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".yaml") {
			continue
		}
		file := dir + "/" + e.Name()
		data, err := fs.ReadFile(fsys, file)
		if err != nil {
			ps.add(file, "", "read: %v", err)
			continue
		}
		fn(file, data)
	}
}

// decode strictly decodes one YAML document: unknown fields are errors, so typos fail loudly.
func decode(file string, data []byte, v any, ps *ConfigErrors) bool {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(v); err != nil {
		ps.add(file, "", "%s", cleanYAMLError(err))
		return false
	}
	return true
}

var goTypeSuffix = regexp.MustCompile(` in type [\w.*\[\]]+`)

// cleanYAMLError turns "yaml: unmarshal errors:\n  line 4: field key not found in type registry.APIKey"
// into "line 4: field key not found".
func cleanYAMLError(err error) string {
	msg := strings.TrimPrefix(err.Error(), "yaml: ")
	msg = strings.TrimPrefix(msg, "unmarshal errors:\n")
	var parts []string
	for line := range strings.SplitSeq(msg, "\n") {
		if line = strings.TrimSpace(goTypeSuffix.ReplaceAllString(line, "")); line != "" {
			parts = append(parts, line)
		}
	}
	return strings.Join(parts, "; ")
}

// ---- capabilities ----

func (c *Config) loadCapabilities(src Sources, ps *ConfigErrors) {
	compiler := jsonschema.NewCompiler()
	compiler.DefaultDraft(jsonschema.Draft2020)
	compiler.AssertFormat()
	eachYAML(src.Config, "capabilities", ps, func(file string, data []byte) {
		var cf CapabilityFile
		if !decode(file, data, &cf, ps) {
			return
		}
		if !capKeyPattern.MatchString(cf.Capability) {
			ps.add(file, "capability", "want <name>@v<major>, got %q", cf.Capability)
			return
		}
		if _, dup := c.Capabilities[cf.Capability]; dup {
			ps.add(file, "capability", "duplicate capability %q", cf.Capability)
			return
		}
		capability, err := compileCapability(compiler, src.Contracts, cf)
		if err != nil {
			ps.add(file, "schema_ref", "%v", err)
			return
		}
		capability.File = file
		c.Capabilities[cf.Capability] = capability
		c.files["capability/"+cf.Capability] = file
	})
}

type capabilityHeader struct {
	XCapability string `json:"x-capability"`
	Minor       int    `json:"x-capability-minor"`
	Interactive bool   `json:"interactive"`
	Operations  map[string]struct {
		Description    string `json:"description"`
		Idempotent     bool   `json:"idempotent"`
		RequestSchema  string `json:"request_schema"`
		ResponseSchema string `json:"response_schema"`
	} `json:"operations"`
}

func compileCapability(compiler *jsonschema.Compiler, contracts fs.FS, cf CapabilityFile) (*Capability, error) {
	if contracts == nil {
		return nil, errors.New("no contracts directory configured")
	}
	root, err := fs.ReadFile(contracts, cf.SchemaRef)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", cf.SchemaRef, err)
	}
	var hdr capabilityHeader
	if err := json.Unmarshal(root, &hdr); err != nil {
		return nil, fmt.Errorf("%s: %w", cf.SchemaRef, err)
	}
	if hdr.XCapability != cf.Capability {
		return nil, fmt.Errorf("%s declares x-capability %q, want %q", cf.SchemaRef, hdr.XCapability, cf.Capability)
	}
	if len(hdr.Operations) == 0 {
		return nil, fmt.Errorf("%s declares no operations", cf.SchemaRef)
	}
	ttl := cf.DefaultTTL.Std()
	if ttl <= 0 {
		ttl = 6 * time.Hour
	}
	dir := path.Dir(cf.SchemaRef)
	capability := &Capability{
		Key: cf.Capability, Minor: hdr.Minor, Interactive: hdr.Interactive, DefaultTTL: ttl,
		Operations: map[string]*Operation{}, SchemaRef: cf.SchemaRef, SchemaDir: dir,
	}
	names := make([]string, 0, len(hdr.Operations))
	for name := range hdr.Operations {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		op := hdr.Operations[name]
		if !opNamePattern.MatchString(name) {
			return nil, fmt.Errorf("operation %q must match %s", name, opNamePattern)
		}
		reqRaw, req, err := compileFile(compiler, contracts, path.Join(dir, op.RequestSchema))
		if err != nil {
			return nil, fmt.Errorf("operation %s request: %w", name, err)
		}
		respRaw, resp, err := compileFile(compiler, contracts, path.Join(dir, op.ResponseSchema))
		if err != nil {
			return nil, fmt.Errorf("operation %s response: %w", name, err)
		}
		var id struct {
			ID string `json:"$id"`
		}
		_ = json.Unmarshal(reqRaw, &id)
		var tree any
		_ = json.Unmarshal(reqRaw, &tree)
		capability.Operations[name] = &Operation{
			Name: name, Description: op.Description, Idempotent: op.Idempotent,
			Request: req, Response: resp, RequestID: id.ID, RequestFile: op.RequestSchema, ResponseFile: op.ResponseSchema,
			RequestSchema: reqRaw, ResponseSchema: respRaw, PII: piiPaths(tree, "", nil),
		}
	}
	return capability, nil
}

func compileFile(compiler *jsonschema.Compiler, contracts fs.FS, name string) (json.RawMessage, *jsonschema.Schema, error) {
	raw, err := fs.ReadFile(contracts, name)
	if err != nil {
		return nil, nil, fmt.Errorf("read %s: %w", name, err)
	}
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		return nil, nil, fmt.Errorf("%s: %w", name, err)
	}
	url := "contracts:///" + name
	if err := compiler.AddResource(url, doc); err != nil {
		return nil, nil, fmt.Errorf("%s: %w", name, err)
	}
	sch, err := compiler.Compile(url)
	if err != nil {
		return nil, nil, fmt.Errorf("compile %s: %w", name, err)
	}
	return raw, sch, nil
}

// piiPaths walks properties/items and collects x-pii tags as instance JSON pointers.
// A tag on an object covers its subtree, so the walk stops there.
func piiPaths(node any, at string, out []PIIPath) []PIIPath {
	m, ok := node.(map[string]any)
	if !ok {
		return out
	}
	if kind, ok := m["x-pii"].(string); ok {
		ptr := at
		if ptr == "" {
			ptr = "/"
		}
		return append(out, PIIPath{Pointer: ptr, Kind: kind})
	}
	if props, ok := m["properties"].(map[string]any); ok {
		keys := make([]string, 0, len(props))
		for k := range props {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		for _, k := range keys {
			out = piiPaths(props[k], at+"/"+escapePointer(k), out)
		}
	}
	if items, ok := m["items"]; ok {
		out = piiPaths(items, at+"/*", out)
	}
	return out
}

func escapePointer(s string) string { return strings.NewReplacer("~", "~0", "/", "~1").Replace(s) }

// ---- transformers ----

func (c *Config) loadTransformers(src Sources, ps *ConfigErrors) {
	ids, err := fs.ReadDir(src.Config, "transformers")
	if errors.Is(err, fs.ErrNotExist) {
		return
	}
	if err != nil {
		ps.add("transformers", "", "read directory: %v", err)
		return
	}
	for _, d := range ids {
		if !d.IsDir() {
			continue
		}
		eachYAML(src.Config, "transformers/"+d.Name(), ps, func(file string, data []byte) {
			var t TransformerFile
			if !decode(file, data, &t, ps) {
				return
			}
			want, err := strconv.Atoi(strings.TrimSuffix(path.Base(file), ".yaml"))
			switch {
			case !transformerID.MatchString(t.Transformer):
				ps.add(file, "transformer", "id %q must match %s", t.Transformer, transformerID)
				return
			case t.Transformer != d.Name():
				ps.add(file, "transformer", "id %q must equal its directory %q", t.Transformer, d.Name())
				return
			case err != nil || want != t.Version:
				ps.add(file, "version", "version %d must equal the file name (<version>.yaml)", t.Version)
				return
			}
			if t.Kind != KindRequest && t.Kind != KindResponse {
				ps.add(file, "kind", "must be request or response")
			}
			if t.Kind == KindRequest && t.VendorEndpoint == "" {
				ps.add(file, "vendor_endpoint", "required for request transformers (vendor.endpoint@n)")
			}
			ref := TransformerRef{ID: t.Transformer, Version: t.Version}
			c.Transformers[ref] = &t
			c.files["transformer/"+ref.String()] = file
		})
	}
}

// ---- integrations ----

func (c *Config) loadIntegrations(src Sources, ps *ConfigErrors) {
	dirs, err := fs.ReadDir(src.Config, "integrations")
	if errors.Is(err, fs.ErrNotExist) {
		return
	}
	if err != nil {
		ps.add("integrations", "", "read directory: %v", err)
		return
	}
	for _, d := range dirs {
		if !d.IsDir() {
			continue
		}
		base := "integrations/" + d.Name()
		file := base + "/integration.yaml"
		data, err := fs.ReadFile(src.Config, file)
		if err != nil {
			ps.add(file, "", "missing integration.yaml")
			continue
		}
		var f IntegrationFile
		if !decode(file, data, &f, ps) {
			continue
		}
		if !idPattern.MatchString(f.Integration) {
			ps.add(file, "integration", "id %q must match %s", f.Integration, idPattern)
			continue
		}
		if f.Integration != d.Name() {
			ps.add(file, "integration", "id %q must equal its directory %q", f.Integration, d.Name())
			continue
		}
		in := &Integration{IntegrationFile: f, Versions: map[int]*IntegrationVersion{}, File: file, VersionFiles: map[int]string{}}
		eachYAML(src.Config, base+"/versions", ps, func(vfile string, data []byte) {
			var v IntegrationVersion
			if !decode(vfile, data, &v, ps) {
				return
			}
			n, err := strconv.Atoi(strings.TrimSuffix(path.Base(vfile), ".yaml"))
			if err != nil || n != v.Version || n < 1 {
				ps.add(vfile, "version", "version %d must equal the file name (<n>.yaml, n >= 1)", v.Version)
				return
			}
			applyDefaults(&v)
			in.Versions[n] = &v
			in.VersionFiles[n] = vfile
		})
		c.Integrations[f.Integration] = in
		c.files["integration/"+f.Integration] = file
	}
}

// applyDefaults fills the DESIGN §2 defaults: wait mode with a 3 s window, fail_fast on an open
// circuit, re-resolve pending requests, masked audit kept 30 days. TTL defaults to the
// capability's default during validation.
func applyDefaults(v *IntegrationVersion) {
	d := &v.Delivery
	if d.Mode == "" {
		d.Mode = ModeWait
	}
	if d.Mode == ModeWait && d.WaitMS == 0 {
		d.WaitMS = DefaultWaitMS
	}
	if d.OnCircuitOpen == "" {
		d.OnCircuitOpen = FailFast
	}
	if d.Pending == "" {
		d.Pending = ReResolve
	}
	if v.Audit.Payloads == "" {
		v.Audit.Payloads = PayloadMasked
	}
	if v.Audit.Retention == 0 {
		v.Audit.Retention = Duration(DefaultRetention)
	}
}
