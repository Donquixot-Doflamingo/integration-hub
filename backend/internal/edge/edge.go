package edge

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/registry"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/telemetry"
)

// Snapshots is the read side of the registry store. The hot path reads only this pointer
// (DESIGN §18.5 rule 1).
type Snapshots interface {
	Snapshot() *registry.Snapshot
}

// Deps are the gateway's collaborators, wired by cmd/hub.
type Deps struct {
	Snapshots Snapshots
	Metrics   *telemetry.Metrics
	Log       *slog.Logger
	Now       func() time.Time
}

// Server is the gateway's public HTTP surface: /v1 caller API (DESIGN §4).
type Server struct {
	mux     *http.ServeMux
	deps    Deps
	log     *slog.Logger
	metrics *telemetry.Metrics
}

// New builds the gateway server and registers its routes.
func New(d Deps) *Server {
	if d.Log == nil {
		d.Log = slog.New(slog.DiscardHandler)
	}
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.Metrics == nil {
		d.Metrics = telemetry.NewMetrics("gateway", "test")
	}
	s := &Server{mux: http.NewServeMux(), deps: d, log: d.Log.With(slog.String("component", "edge")), metrics: d.Metrics}
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
}
