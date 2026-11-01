package registry

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
)

// The types in this file are the on-disk registry objects (DESIGN §3, §8, TECH_SPEC §8.2). Each
// file holds one object. They are plain data: loaders fill them, validation reads them, and a
// built Snapshot owns them afterwards. Nothing mutates an object once it is in a Snapshot.

// Duration accepts Go durations ("500ms", "6h") plus whole days ("30d").
type Duration time.Duration

// UnmarshalYAML parses "30d" as 30×24h and anything else with time.ParseDuration.
func (d *Duration) UnmarshalYAML(n *yaml.Node) error {
	s := strings.TrimSpace(n.Value)
	if days, ok := strings.CutSuffix(s, "d"); ok {
		v, err := strconv.Atoi(days)
		if err != nil || v < 0 {
			return fmt.Errorf("invalid duration %q", s)
		}
		*d = Duration(time.Duration(v) * 24 * time.Hour)
		return nil
	}
	v, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("invalid duration %q", s)
	}
	*d = Duration(v)
	return nil
}

// MarshalYAML writes days when the value is a whole number of days.
func (d Duration) MarshalYAML() (any, error) {
	td := time.Duration(d)
	if td > 0 && td%(24*time.Hour) == 0 {
		return fmt.Sprintf("%dd", td/(24*time.Hour)), nil
	}
	return td.String(), nil
}

// Std returns the value as a time.Duration.
func (d Duration) Std() time.Duration { return time.Duration(d) }

// ---- capabilities ----

// CapabilityFile is backend/config/capabilities/<name>.v<major>.yaml. The schema itself lives in
// contracts/schemas/capabilities (ADR-0004); this file only points at it.
type CapabilityFile struct {
	Capability string   `yaml:"capability"` // "events.track@v1"
	SchemaRef  string   `yaml:"schema_ref"` // path under contracts/, the request schema of the default (or first) op
	DefaultTTL Duration `yaml:"default_ttl"`
}

// ---- vendors ----

// Vendor is backend/config/vendors/<vendor>.yaml (DESIGN §8.3). Same for every account.
type Vendor struct {
	Vendor    string              `yaml:"vendor"`
	Auth      string              `yaml:"auth"` // static_headers | bearer | basic | query | hmac | oauth2_cc | mtls
	Regions   map[string]string   `yaml:"regions"`
	Endpoints map[string]Endpoint `yaml:"endpoints"` // key "upload@1"
	Defaults  VendorDefaults      `yaml:"defaults"`
	DocsURL   string              `yaml:"docs_url,omitempty"`
}

// Endpoint is one vendor endpoint version.
type Endpoint struct {
	Method           string `yaml:"method"`
	Path             string `yaml:"path"`
	TimeoutMS        int    `yaml:"timeout_ms"`
	ConnectTimeoutMS int    `yaml:"connect_timeout_ms"`
	Idempotent       bool   `yaml:"idempotent"`
	BatchMax         int    `yaml:"batch_max,omitempty"`
}

// VendorDefaults are limits shared by every account of a vendor unless the account overrides them.
type VendorDefaults struct {
	RateLimit   RateLimit     `yaml:"rate_limit"`
	Breaker     BreakerConfig `yaml:"breaker"`
	HealthProbe *HealthProbe  `yaml:"health_probe,omitempty"`
}

// RateLimit is the vendor default per-account limit.
type RateLimit struct {
	PerAccountRPS int `yaml:"per_account_rps"`
}

// BreakerConfig parametrises the (account, endpoint) circuit breaker (DESIGN §10.4).
type BreakerConfig struct {
	WindowS             int     `yaml:"window_s"`
	MinCalls            int     `yaml:"min_calls"`
	FailureRatio        float64 `yaml:"failure_ratio"`
	ConsecutiveTimeouts int     `yaml:"consecutive_timeouts"`
	OpenS               int     `yaml:"open_s"`
	OpenMaxS            int     `yaml:"open_max_s"`
	HalfOpenProbes      int     `yaml:"half_open_probes"`
}

// HealthProbe is the synthetic probe run while a breaker is open.
type HealthProbe struct {
	Endpoint string `yaml:"endpoint"`
	Dry      bool   `yaml:"dry"`
	EveryS   int    `yaml:"every_s"`
}

// ---- accounts ----

// AccountState is the lifecycle state (DESIGN §6.5).
type AccountState string

const (
	AccountActive   AccountState = "active"
	AccountDraining AccountState = "draining"
	AccountRetired  AccountState = "retired"
)

// Account is backend/config/accounts/<account>.yaml: one credentialed instance of a vendor.
// Secrets are never here; CredentialsRef points at the credential source.
type Account struct {
	Account        string            `yaml:"account"`
	Vendor         string            `yaml:"vendor"`
	Region         string            `yaml:"region"`
	State          AccountState      `yaml:"state"`
	CredentialsRef string            `yaml:"credentials_ref"` // localfile://<id>, kms://..., vault://...
	Config         map[string]string `yaml:"config"`          // non-secret: project id, brand code, model
	Quota          Quota             `yaml:"quota"`
}

// Quota is the vendor quota of one account.
type Quota struct {
	RPS   int `yaml:"rps"`
	Daily int `yaml:"daily"`
}

// ---- transformers ----

// TransformerKind says which direction a transformer maps.
type TransformerKind string

const (
	KindRequest  TransformerKind = "request"
	KindResponse TransformerKind = "response"
)

// TransformerFile is backend/config/transformers/<id>/<version>.yaml. M1 loads the header and keeps
// the mapping sections raw; M2 (internal/transform) compiles them.
type TransformerFile struct {
	Transformer    string          `yaml:"transformer"`
	Version        int             `yaml:"version"`
	Kind           TransformerKind `yaml:"kind"`
	Capability     string          `yaml:"capability"`
	VendorEndpoint string          `yaml:"vendor_endpoint,omitempty"` // "clevertap.upload@1" (request transformers)
	DocsURL        string          `yaml:"docs_url,omitempty"`        // vendor doc the mapping was checked against (AGENTS §3.3)
	// Operation of a multi-operation capability this transformer maps; empty = the only operation.
	Operation string `yaml:"operation,omitempty"`
	// AllowCredsInBody lets creds.* appear in body targets (vendor requires it; platform review).
	AllowCredsInBody bool      `yaml:"allow_creds_in_body,omitempty"`
	Request          yaml.Node `yaml:"request,omitempty"`
	Response         yaml.Node `yaml:"response,omitempty"`
	Errors           yaml.Node `yaml:"errors,omitempty"`
	Batch            yaml.Node `yaml:"batch,omitempty"`
	// Session (response transformers) maps the vendor session data of a multi-step operation.
	Session yaml.Node `yaml:"session,omitempty"`
}

// TransformerRef is "T2@3".
type TransformerRef struct {
	ID      string
	Version int
}

func (r TransformerRef) String() string { return fmt.Sprintf("%s@%d", r.ID, r.Version) }

// ParseTransformerRef parses "T2@3".
func ParseTransformerRef(s string) (TransformerRef, error) {
	id, v, ok := strings.Cut(s, "@")
	n, err := strconv.Atoi(v)
	if !ok || id == "" || err != nil || n < 1 {
		return TransformerRef{}, fmt.Errorf("want <id>@<version>, got %q", s)
	}
	return TransformerRef{ID: id, Version: n}, nil
}

// UnmarshalYAML reads the "T2@3" form.
func (r *TransformerRef) UnmarshalYAML(n *yaml.Node) error {
	ref, err := ParseTransformerRef(n.Value)
	if err != nil {
		return err
	}
	*r = ref
	return nil
}

// MarshalYAML writes the "T2@3" form.
func (r TransformerRef) MarshalYAML() (any, error) { return r.String(), nil }

// ---- integrations ----

// IntegrationFile is backend/config/integrations/<id>/integration.yaml: the stable ID, its owner,
// capability and which version is active. Versions live next to it in versions/<n>.yaml.
type IntegrationFile struct {
	Integration   string   `yaml:"integration"`
	OwnerTenant   string   `yaml:"owner_tenant"`
	Capability    string   `yaml:"capability"`
	Grants        []string `yaml:"grants"`
	ActiveVersion int      `yaml:"active_version"`
	Paused        bool     `yaml:"paused"`
	Description   string   `yaml:"description,omitempty"`
	// MovesMoneyOrQuota makes Idempotency-Key mandatory and gates hubmcp invokes (contracts).
	MovesMoneyOrQuota bool `yaml:"moves_money_or_quota"`
}

// IntegrationVersion is one immutable version (DESIGN §3.4). The repeated header fields
// (integration, owner_tenant, grants, capability) let the §3.4 record be pasted verbatim; they
// must match integration.yaml.
type IntegrationVersion struct {
	Integration string          `yaml:"integration"`
	OwnerTenant string          `yaml:"owner_tenant,omitempty"`
	Grants      []string        `yaml:"grants,omitempty"`
	Capability  string          `yaml:"capability,omitempty"`
	Version     int             `yaml:"version"`
	Targets     []Target        `yaml:"targets"`
	Failover    []Target        `yaml:"failover,omitempty"`
	Delivery    Delivery        `yaml:"delivery"`
	Breaker     BreakerOverride `yaml:"breaker,omitempty"`
	Webhooks    []Subscription  `yaml:"webhooks,omitempty"`
	Audit       AuditPolicy     `yaml:"audit"`
	Publish     PublishMeta     `yaml:"publish"`
}

// Target is one (vendor, account, transformers) route with a weight.
type Target struct {
	Vendor      string          `yaml:"vendor"`
	Account     string          `yaml:"account"`
	Transformer TransformerPair `yaml:"transformer"`
	Weight      int             `yaml:"weight,omitempty"` // failover entries have no weight
}

// TransformerPair names the request and response transformers of a target.
type TransformerPair struct {
	Request  TransformerRef `yaml:"request"`
	Response TransformerRef `yaml:"response"`
}

// Mode is a delivery mode (DESIGN §5.2).
type Mode string

const (
	ModeSync          Mode = "sync"
	ModeWait          Mode = "wait"
	ModeAsync         Mode = "async"
	ModeAsyncCallback Mode = "async_callback"
)

// OnCircuitOpen is the sync behaviour when the breaker is open (DESIGN §5.3).
type OnCircuitOpen string

const (
	FailFast       OnCircuitOpen = "fail_fast"
	Failover       OnCircuitOpen = "failover"
	QueueIfAllowed OnCircuitOpen = "queue_if_allowed"
)

// Pending says what queued requests do at a cutover (DESIGN §6.3, D10).
type Pending string

const (
	ReResolve Pending = "re_resolve"
	Pinned    Pending = "pinned"
)

// Delivery is the caller-facing delivery contract of a version.
type Delivery struct {
	Mode          Mode          `yaml:"mode"`
	WaitMS        int           `yaml:"wait_ms,omitempty"`
	TTL           Duration      `yaml:"ttl,omitempty"`
	OnCircuitOpen OnCircuitOpen `yaml:"on_circuit_open,omitempty"`
	Pending       Pending       `yaml:"pending,omitempty"`
	// Polling declares that callers poll GET /v1/requests/{id} for wait/async results (DESIGN §5.2:
	// non-sync modes need a polling path or a callback subscription).
	Polling bool `yaml:"polling,omitempty"`
}

// BreakerOverride tightens the shared breaker for this integration (DESIGN §10.4).
type BreakerOverride struct {
	Override struct {
		OpenAfterConsecutiveTimeouts int `yaml:"open_after_consecutive_timeouts,omitempty"`
	} `yaml:"override,omitempty"`
}

// Subscription sends one event type to a tenant registrar endpoint.
type Subscription struct {
	Event    string `yaml:"event"`
	Endpoint string `yaml:"endpoint"`
}

// PayloadPolicy is the audit payload policy (DESIGN §13.2).
type PayloadPolicy string

const (
	PayloadFull         PayloadPolicy = "full"
	PayloadMasked       PayloadPolicy = "masked"
	PayloadSealed       PayloadPolicy = "sealed"
	PayloadMetadataOnly PayloadPolicy = "metadata_only"
)

// AuditPolicy is the audit configuration of a version.
type AuditPolicy struct {
	Payloads  PayloadPolicy `yaml:"payloads,omitempty"`
	Retention Duration      `yaml:"retention,omitempty"`
}

// PublishMeta records who published a version and why.
type PublishMeta struct {
	Author string `yaml:"author"`
	Reason string `yaml:"reason"`
}

// ---- tenants and registrar ----

// Tenant is backend/config/tenants/<tenant>.yaml.
type Tenant struct {
	Tenant    string      `yaml:"tenant"`
	APIKeys   []APIKey    `yaml:"api_keys"`
	RateLimit TenantLimit `yaml:"rate_limit"`
	Status    string      `yaml:"status,omitempty"` // active | suspended
}

// APIKey is a tenant key, stored only as a hash (DESIGN §16.5).
type APIKey struct {
	KeyID  string   `yaml:"key_id"`
	Hash   string   `yaml:"hash"` // "sha256:<64 hex>" of the full key
	Scopes []string `yaml:"scopes"`
	State  string   `yaml:"state"` // active | expiring | revoked
}

// TenantLimit is the tenant-level fairness budget at the edge.
type TenantLimit struct {
	RPS   int `yaml:"rps"`
	Burst int `yaml:"burst"`
}

// RegistrarEndpoint is backend/config/registrar/<endpoint_id>.yaml: a tenant webhook destination.
type RegistrarEndpoint struct {
	EndpointID string `yaml:"endpoint_id"`
	Tenant     string `yaml:"tenant"`
	URL        string `yaml:"url"`
	SecretRef  string `yaml:"secret_ref"`
}
