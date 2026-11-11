package registry

import (
	"crypto/sha256"
	"encoding/hex"
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

// ---- vendors ----

// Vendor is backend/config/vendors/<vendor>.yaml (DESIGN §8.3). Same for every account.
type Vendor struct {
	Vendor    string              `yaml:"vendor"`
	Auth      string              `yaml:"auth"` // none | static_headers | bearer | basic | query | hmac | oauth2_cc | mtls
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
	// RequestSchema is the request the endpoint accepts (ADR-0023). Required; {} means nothing
	// besides method and path. Request transformer samples are checked against it at transformer
	// creation and at integration validate/publish, never on live calls.
	RequestSchema *EndpointRequestSchema `yaml:"request_schema"`
	// ResponseSchema is the vendor's response body, informational only: stored, never enforced.
	ResponseSchema map[string]any `yaml:"response_schema,omitempty"`
}

// EndpointRequestSchema is one JSON Schema per part of the vendor request. Credential headers and
// query values (creds.*) are never part of the checked request.
type EndpointRequestSchema struct {
	Path    *JSONSchema `yaml:"path,omitempty"`    // path parameters ({name} segments of Endpoint.Path)
	Query   *JSONSchema `yaml:"query,omitempty"`   // query parameters object
	Headers *JSONSchema `yaml:"headers,omitempty"` // non-credential headers object
	Body    *JSONSchema `yaml:"body,omitempty"`    // JSON body
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
	VendorEndpoint string          `yaml:"vendor_endpoint,omitempty"` // "clevertap.upload@1" (request transformers)
	DocsURL        string          `yaml:"docs_url,omitempty"`        // vendor doc the mapping was checked against (AGENTS §3.3)
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

// UnmarshalYAML reads the "T2@3" form; null (or empty) is the unset ref.
func (r *TransformerRef) UnmarshalYAML(n *yaml.Node) error {
	if n.Tag == "!!null" || (n.Kind == yaml.ScalarNode && n.Value == "" && n.Style == 0) {
		*r = TransformerRef{}
		return nil
	}
	ref, err := ParseTransformerRef(n.Value)
	if err != nil {
		return err
	}
	*r = ref
	return nil
}

// MarshalYAML writes the "T2@3" form.
func (r TransformerRef) MarshalYAML() (any, error) { return r.String(), nil }

// IsZero reports an unset ref (no response transformer: pass-through). yaml omitempty uses it.
func (r TransformerRef) IsZero() bool { return r.ID == "" && r.Version == 0 }

// ---- integrations ----

// IntegrationFile is backend/config/integrations/<id>/integration.yaml: the stable ID, its owner
// and which version is active. Versions live next to it in versions/<n>.yaml; each version
// declares its operations and their request schemas (ADR-0023).
type IntegrationFile struct {
	Integration   string   `yaml:"integration"`
	OwnerTenant   string   `yaml:"owner_tenant"`
	Grants        []string `yaml:"grants"`
	ActiveVersion int      `yaml:"active_version"`
	Paused        bool     `yaml:"paused"`
	PausedReason  string   `yaml:"paused_reason,omitempty"`
	Description   string   `yaml:"description,omitempty"`
	// Interactive: the operations are steps of one session a person is waiting on (X-Hub-Session);
	// only sync delivery is allowed (DESIGN §5.4). Fixed when the integration is created.
	Interactive bool `yaml:"interactive,omitempty"`
	// MovesMoneyOrQuota makes Idempotency-Key mandatory and gates hubmcp invokes (contracts).
	MovesMoneyOrQuota bool `yaml:"moves_money_or_quota"`
}

// IntegrationVersion is one immutable version (DESIGN §3.4). The repeated header fields
// (integration, owner_tenant, grants) let the §3.4 record be pasted verbatim; they must match
// integration.yaml.
type IntegrationVersion struct {
	Integration string   `yaml:"integration"`
	OwnerTenant string   `yaml:"owner_tenant,omitempty"`
	Grants      []string `yaml:"grants,omitempty"`
	Version     int      `yaml:"version"`
	// Operations are what callers invoke, each with the caller's request schema (ADR-0023).
	// Single-operation integrations use "default".
	Operations map[string]*Operation `yaml:"operations"`
	Targets    []Target              `yaml:"targets"`
	Failover   []Target              `yaml:"failover,omitempty"`
	Delivery   Delivery              `yaml:"delivery"`
	Breaker    BreakerOverride       `yaml:"breaker,omitempty"`
	Webhooks   []Subscription        `yaml:"webhooks,omitempty"`
	Audit      AuditPolicy           `yaml:"audit"`
	Shadow     *Shadow               `yaml:"shadow,omitempty"`
	Publish    PublishMeta           `yaml:"publish"`
}

// ShadowMode is how a shadow target receives its copy of the traffic (DESIGN §6.4).
type ShadowMode string

const (
	ShadowSend   ShadowMode = "send"    // call the shadow target for real
	ShadowDryRun ShadowMode = "dry_run" // transform only: for endpoints with side effects
)

// Shadow copies each request to a second target. Its result is audited, never returned.
type Shadow struct {
	Target      Target     `yaml:"target"`
	Mode        ShadowMode `yaml:"mode"`
	SampleRatio *float64   `yaml:"sample_ratio,omitempty"` // default 1
}

// Ratio is the share of requests copied, 0..1.
func (s *Shadow) Ratio() float64 {
	if s.SampleRatio == nil {
		return 1
	}
	return *s.SampleRatio
}

// Target is one (vendor, account, transformers) route with a weight.
type Target struct {
	Vendor  string `yaml:"vendor"`
	Account string `yaml:"account"`
	// Transformer is the default pair: it serves every operation Operations does not list (for a
	// single-operation integration, the only pair).
	Transformer TransformerPair `yaml:"transformer"`
	// Operations gives operations of a multi-operation integration their own pair, so each can go
	// to its own vendor endpoint (HUB-343, admin contract TargetRef.operations).
	Operations map[string]TransformerPair `yaml:"operations,omitempty"`
	Weight     int                        `yaml:"weight,omitempty"` // failover entries have no weight
}

// Pair is the transformer pair that serves operation op: its own pair when Operations lists
// it, else the default Transformer. A lookup in a small map: safe on the hot path.
func (t Target) Pair(op string) TransformerPair {
	if p, ok := t.Operations[op]; ok {
		return p
	}
	return t.Transformer
}

// Pairs is every pair the target references: the default first, then Operations by name.
func (t Target) Pairs() []TransformerPair {
	out := make([]TransformerPair, 0, 1+len(t.Operations))
	out = append(out, t.Transformer)
	for _, op := range sortedKeys(t.Operations) {
		out = append(out, t.Operations[op])
	}
	return out
}

// TransformerPair names the request and response transformers of a target. Response is
// optional: unset means pass-through (the caller gets the vendor's status and body, ADR-0023).
type TransformerPair struct {
	Request  TransformerRef `yaml:"request"`
	Response TransformerRef `yaml:"response,omitempty"`
}

// PassThrough reports a pair without a response transformer.
func (p TransformerPair) PassThrough() bool { return p.Response.IsZero() }

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
	Tenant string `yaml:"tenant"`
	// DisplayName, Description and Contact are display fields of the admin API (defaults: the id).
	DisplayName string      `yaml:"display_name,omitempty"`
	Description string      `yaml:"description,omitempty"`
	Contact     string      `yaml:"contact,omitempty"`
	APIKeys     []APIKey    `yaml:"api_keys"`
	RateLimit   TenantLimit `yaml:"rate_limit"`
	Status      string      `yaml:"status,omitempty"` // active | suspended
	// Disabled turns every key of the tenant off (401 at the edge, admin updateTenant).
	Disabled bool `yaml:"disabled,omitempty"`
	// Setup is "pending" until a tenant admin finishes the Hub-side setup (set by the ISFC sync
	// when it creates the tenant); "" and "complete" both mean complete, so YAML written before
	// ISFC tenants existed loads unchanged (AUTH_PLAN §6.1).
	Setup string `yaml:"setup,omitempty"`
	// ISFC is the read-only copy of the tenant's ISFC record, written only by the sync. nil: the
	// tenant is not linked to ISFC (legacy tenants).
	ISFC *TenantISFC `yaml:"isfc,omitempty"`
	// ISFCCallerClients lists the ISFC caller (backend) client ids that may call the Hub for
	// this tenant. Interim until ISFC-CR-1 puts tenants on API keys (AUTH_PLAN G1, D5); the
	// caller-key sync resolves a key's tenants from it.
	ISFCCallerClients []string `yaml:"isfc_caller_clients,omitempty"`
}

// Tenant setup states (hub-admin TenantSetup).
const (
	SetupPending  = "pending"
	SetupComplete = "complete"
)

// ISFC tenant statuses (hub-admin IsfcTenantStatus).
const (
	ISFCStatusActive   = "ACTIVE"
	ISFCStatusInactive = "INACTIVE"
)

// TenantISFC is the ISFC side of a tenant (AUTH_PLAN §2.1, §6.1). TenantID is the link.
type TenantISFC struct {
	TenantID string     `yaml:"tenant_id"` // ISFC tenant UUID
	Key      string     `yaml:"key"`
	Name     string     `yaml:"name"`
	Status   string     `yaml:"status"` // ACTIVE | INACTIVE
	Linked   bool       `yaml:"linked"` // present in the Hub console client's tenants list
	SyncedAt *time.Time `yaml:"synced_at,omitempty"`
	// DisabledBySync records that the sync (not an admin) set Tenant.Disabled, so a later
	// re-activation in ISFC re-enables only what the sync disabled.
	DisabledBySync bool `yaml:"disabled_by_sync,omitempty"`
}

// SetupState returns "pending" or "complete" (an empty Setup is complete).
func (t *Tenant) SetupState() string {
	if t.Setup == SetupPending {
		return SetupPending
	}
	return SetupComplete
}

// APIKey is a tenant key, stored only as a hash (DESIGN §16.5).
type APIKey struct {
	KeyID  string   `yaml:"key_id"`
	Hash   string   `yaml:"hash"` // "sha256:<64 hex>" of the full key
	Scopes []string `yaml:"scopes"`
	State  string   `yaml:"state"` // active | expiring | revoked
	// Prefix is the first 12 characters of the key, for identification (never the secret part).
	Prefix      string `yaml:"prefix,omitempty"`
	Description string `yaml:"description,omitempty"`
	// CreatedAt is when the key was minted; ExpiresAt ends it (a key's own expiry, or the end of
	// a rotation overlap, state expiring). ReplacedBy names the key a rotation minted for it.
	CreatedAt  *time.Time `yaml:"created_at,omitempty"`
	ExpiresAt  *time.Time `yaml:"expires_at,omitempty"`
	ReplacedBy string     `yaml:"replaced_by,omitempty"`
}

// API key states as stored (the admin API also reports expired: a key past its ExpiresAt).
const (
	KeyActive   = "active"
	KeyExpiring = "expiring"
	KeyRevoked  = "revoked"
)

// HashKey returns the stored form of an API key: "sha256:<hex>" (DESIGN §16.5). The edge
// hashes presented keys with it; the admin API hashes the keys it mints.
func HashKey(key string) string {
	sum := sha256.Sum256([]byte(key))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// Usable reports whether the key authenticates at now: not revoked and not past its expiry.
// A rotated key (expiring) keeps working until the overlap ends, then stops on every node
// without another snapshot (DESIGN §16.5).
func (k APIKey) Usable(now time.Time) bool {
	return k.State != KeyRevoked && (k.ExpiresAt == nil || now.Before(*k.ExpiresAt))
}

// TenantLimit is the tenant-level fairness budget at the edge.
type TenantLimit struct {
	RPS   int `yaml:"rps"`
	Burst int `yaml:"burst"`
}

// RegistrarEndpoint is backend/config/registrar/<endpoint_id>.yaml: a tenant webhook destination.
type RegistrarEndpoint struct {
	EndpointID  string `yaml:"endpoint_id"`
	Tenant      string `yaml:"tenant"`
	URL         string `yaml:"url"`
	SecretRef   string `yaml:"secret_ref"`
	Description string `yaml:"description,omitempty"`
	Disabled    bool   `yaml:"disabled,omitempty"`
	// SecretVersion is the active signing secret's slot version (default 1).
	SecretVersion int `yaml:"secret_version,omitempty"`
	// SecretOverlapUntil: until then callbacks also carry a signature with the previous secret.
	SecretOverlapUntil *time.Time `yaml:"secret_overlap_until,omitempty"`
}
