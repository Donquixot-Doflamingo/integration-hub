package edge

import (
	"context"

	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/audit"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/core"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/creds"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/delivery"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/registry"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/telemetry"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/upstream"
)

// Snapshots is the read side of the registry store. The hot path reads only this pointer
// (DESIGN §18.5 rule 1).
type Snapshots interface {
	Snapshot() *registry.Snapshot
}

// Deps are the gateway's collaborators, wired by cmd/hub.
type Deps struct {
	Snapshots Snapshots
	Creds     creds.Source // warm credential cache (creds.Cache)
	Upstream  Upstream
	// DebugHeaders adds X-Hub-Integration-Version to 200 responses (dev and support only; the
	// contract says callers must never act on it).
	DebugHeaders bool
	Audit        audit.Sink    // default audit.Discard
	Masker       *audit.Masker // x-pii masking key for the masked policy
	// Breakers and Limiter are admission (M5): circuit breakers and the account limiter.
	Breakers *upstream.Breakers
	Limiter  *upstream.AccountLimiter
	// Queueing enables wait and async delivery (nil: every mode is delivered synchronously).
	Queueing *Queueing
	// Idempotency enables Idempotency-Key deduplication (nil: keys are required where the
	// integration says so, but not deduplicated).
	Idempotency Idempotency
	Metrics     *telemetry.Metrics
	Log         *slog.Logger
	Now         func() time.Time
}

// Upstream sends one attempt to a vendor (upstream.Client).
type Upstream = delivery.Upstream

// Server is the gateway's public HTTP surface: /v1 caller API (DESIGN §4).
type Server struct {
	mux       *http.ServeMux
	deps      Deps
	attempter *delivery.Attempter
	tenants   tenantLimiter
	// rejections is ratelimit_rejections_total (shared with the account limiter when wired).
	rejections *prometheus.CounterVec
	log        *slog.Logger
	metrics    *telemetry.Metrics
}

// New builds the gateway server and registers its routes.
func New(d Deps) *Server {
	if d.Log == nil {
		d.Log = slog.New(slog.DiscardHandler)
	}
	if d.Creds == nil {
		d.Creds = noCreds{}
	}
	if d.Upstream == nil {
		d.Upstream = noUpstream{}
	}
	if d.Audit == nil {
		d.Audit = audit.Discard{}
	}
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.Metrics == nil {
		d.Metrics = telemetry.NewMetrics("gateway", "test")
	}
	s := &Server{mux: http.NewServeMux(), deps: d, log: d.Log.With(slog.String("component", "edge")), metrics: d.Metrics}
	s.attempter = &delivery.Attempter{Creds: d.Creds, Upstream: d.Upstream, Breakers: d.Breakers, Limiter: d.Limiter, Log: s.log, Now: d.Now}
	if d.Limiter != nil {
		s.rejections = d.Limiter.Rejections()
	}
	s.routes()
	return s
}

// Handler is the full edge chain: request id → recover → routes.
func (s *Server) Handler() http.Handler {
	return withRequestID(withRecover(s.log, s.mux))
}

func (s *Server) routes() {
	s.mux.Handle("GET /v1/integrations", s.authenticate(http.HandlerFunc(s.listIntegrations)))
	s.mux.Handle("GET /v1/integrations/{integration_id}", s.authenticate(http.HandlerFunc(s.getIntegration)))
	s.mux.Handle("GET /v1/capabilities/{capability}", s.authenticate(http.HandlerFunc(s.getCapability)))
	s.mux.Handle("POST /v1/integrations/{integration_id}/dry-run", s.authenticate(http.HandlerFunc(s.dryRun)))
	s.mux.Handle("POST /v1/integrations/{integration_id}/invoke", s.authenticate(http.HandlerFunc(s.invoke)))
	s.mux.Handle("POST /v1/integrations/{integration_id}/invoke/{operation}", s.authenticate(http.HandlerFunc(s.invoke)))
	s.mux.Handle("GET /v1/requests/{request_id}", s.authenticate(http.HandlerFunc(s.getRequestStatus)))
}

// noCreds serves empty credentials (discovery-only servers and tests without vendor calls).
type noCreds struct{}

func (noCreds) Get(_ context.Context, accountID string) (creds.Credentials, error) {
	return creds.Credentials{AccountID: accountID}, nil
}

// noUpstream fails every attempt as unreachable (servers wired without an upstream client).
type noUpstream struct{}

func (noUpstream) Attempt(context.Context, upstream.Target, core.VendorRequest) (upstream.Attempt, error) {
	return upstream.Attempt{}, &upstream.TransportError{Kind: upstream.KindConnect, Phase: "connect", Err: errors.New("no upstream client")}
}
