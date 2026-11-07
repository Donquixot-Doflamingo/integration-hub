package registry

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
)

// Problem is one config error, qualified by file and YAML path:
// "integrations/int_evt_brandA/versions/3.yaml: delivery.wait_ms: must be ≤ 30000".
// Check names the publish check it fails (admin contract ValidationCheck) when the loader knows
// it; Sample and Field locate a sample check failure (sample name, JSON pointer in the checked
// object).
type Problem struct {
	File    string
	Path    string
	Message string
	Check   string
	Sample  string
	Field   string
}

// Publish checks the registry names itself (admin contract ValidationCheck.check).
const (
	CheckSchemasCompile = "schemas_compile"
	CheckGoldensPass    = "goldens_pass"
	CheckEndpointSchema = "endpoint_schema"
	CheckInputSchema    = "input_schema"
)

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

// addCompile records a schemas_compile problem (a schema of the loaded file is unusable).
func (ps *ConfigErrors) addCompile(file, path, format string, args ...any) {
	*ps = append(*ps, Problem{File: file, Path: path, Message: fmt.Sprintf(format, args...), Check: CheckSchemasCompile})
}

// Sources are where config is read from: the registry directory (backend/config). Every schema
// lives in it, inline in its integration version or vendor file (ADR-0023).
type Sources struct {
	Config fs.FS
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
	// DefaultTTL is how long an accepted request may wait for delivery when the version does not
	// say (ADR-0023: it used to come from the capability).
	DefaultTTL = 6 * time.Hour
)

var (
	idPattern       = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{2,63}$`)
	opNamePattern   = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}$`)
	transformerID   = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]{0,63}$`)
	keyHashPattern  = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	endpointPattern = regexp.MustCompile(`^[a-z][a-z0-9_]*@[1-9][0-9]*$`)
)

// Load reads every object from src. It reports all decode and per-file problems at once; cross-
// references are checked by Validate.
func Load(src Sources) (*Config, error) {
	c := &Config{
		Vendors: map[string]*Vendor{}, Accounts: map[string]*Account{},
		Transformers: map[TransformerRef]*TransformerFile{}, Integrations: map[string]*Integration{},
		Tenants: map[string]*Tenant{}, Registrar: map[string]*RegistrarEndpoint{}, files: map[string]string{},
	}
	var ps ConfigErrors
	eachYAML(src.Config, "vendors", &ps, func(file string, data []byte) {
		var v Vendor
		if decode(file, data, &v, &ps) && c.put(file, "vendor", v.Vendor, &ps) {
			for _, name := range sortedKeys(v.Endpoints) {
				ep := v.Endpoints[name]
				if !endpointPattern.MatchString(name) {
					ps.add(file, "endpoints."+name, "endpoint keys look like name@version (e.g. upload@1)")
				}
				if ep.TimeoutMS <= 0 {
					ps.add(file, "endpoints."+name+".timeout_ms", "must be > 0")
				}
				if ep.ConnectTimeoutMS <= 0 {
					ps.add(file, "endpoints."+name+".connect_timeout_ms", "must be > 0")
				}
				compileEndpoint(&ep, file, "endpoints."+name, &ps)
				v.Endpoints[name] = ep
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
				switch k.State {
				case KeyActive, KeyRevoked:
				case KeyExpiring:
					if k.ExpiresAt == nil {
						ps.add(file, at+".expires_at", "an expiring key needs expires_at (the end of its rotation overlap)")
					}
				default:
					ps.add(file, at+".state", "must be active, expiring or revoked")
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
			if t.Kind == KindResponse && t.VendorEndpoint != "" {
				ps.add(file, "vendor_endpoint", "response transformers stand alone: no vendor_endpoint (ADR-0023)")
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
			if len(v.Operations) == 0 {
				ps.addCompile(vfile, "operations", "declare at least one operation (single-operation integrations use default)")
			}
			for _, name := range sortedKeys(v.Operations) {
				op := v.Operations[name]
				if op == nil {
					op = &Operation{}
					v.Operations[name] = op
				}
				compileOperation(op, name, vfile, "operations."+name, ps)
			}
			in.Versions[n] = &v
			in.VersionFiles[n] = vfile
		})
		c.Integrations[f.Integration] = in
		c.files["integration/"+f.Integration] = file
	}
}

// applyDefaults fills the DESIGN §2 defaults: wait mode with a 3 s window, fail_fast on an open
// circuit, re-resolve pending requests, a 6 h TTL, masked audit kept 30 days.
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
	if d.TTL == 0 {
		d.TTL = Duration(DefaultTTL)
	}
	if v.Audit.Payloads == "" {
		v.Audit.Payloads = PayloadMasked
	}
	if v.Audit.Retention == 0 {
		v.Audit.Retention = Duration(DefaultRetention)
	}
}
