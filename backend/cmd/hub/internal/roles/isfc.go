package roles

import (
	"context"
	"log/slog"
	"sync"

	"github.com/redis/go-redis/v9"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/control"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/isfc"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/isfcsync"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/telemetry"
)

// Redis channels of the ISFC sync (like hub:snapshot and hub:creds): a ping to the leader worker
// to reconcile now (events, other nodes), and a ping to gateways that caller keys changed.
const (
	ISFCSyncChannel = "hub:isfc:sync"
	ISFCKeysChannel = "hub:isfc:keys"
)

// isfcSync is the reconciler of a role that runs it (the worker's leader loop, the admin's "Sync
// now"). Both share one construction; only the worker runs the loop.
type isfcSync struct {
	rec  *isfcsync.Reconciler
	loop *isfcsync.Loop
}

// newISFCSync builds the reconciler for cfg, or returns nil when tenants are not from ISFC. The
// client secret joins the redaction set before anything can log.
func newISFCSync(cfg Config, m *telemetry.Metrics, log *slog.Logger, secrets *telemetry.SecretSet, st *stores, reg *roleRegistry, metrics *isfcsync.Metrics) (*isfcSync, error) {
	if !cfg.ISFC.FromISFC() {
		return nil, nil
	}
	secrets.Add(string(cfg.ISFC.ClientSecret))
	client, err := isfc.New(isfc.Config{BaseURL: cfg.ISFC.AuthURL, ClientID: cfg.ISFC.ClientID, ClientSecret: isfc.Secret(cfg.ISFC.ClientSecret),
		AuthClientID: cfg.ISFC.AuthClientID})
	if err != nil {
		return nil, err
	}
	pub := &control.Publisher{
		Pool: st.pg, Archive: reg.archive, Bootstrap: reg.src, Guard: control.RetireGuard(st.pg),
		Notify: func(ctx context.Context, v int64) {
			if err := st.rdb.Publish(ctx, control.SnapshotChannel, v).Err(); err != nil {
				log.WarnContext(ctx, "snapshot ping failed: nodes adopt it on their next poll", slog.String("error", err.Error()))
			}
		},
	}
	store := isfcsync.PGStore{Pool: st.pg}
	rec := &isfcsync.Reconciler{Source: client, Editor: pub, Store: store, Metrics: metrics, Log: log,
		Config: isfcsync.Config{ConsoleClientID: cfg.consoleClientID(), BackendClientID: cfg.ISFC.BackendClientID,
			PlatformTenantID: cfg.ISFCPlatformTenantID, DryRun: cfg.ISFC.SyncDryRun}}
	return &isfcSync{rec: rec, loop: &isfcsync.Loop{Runner: rec, Elector: isfcsync.PGElector{Pool: st.pg},
		Interval: cfg.ISFC.ReconcileInterval, Metrics: metrics, Log: log}}, nil
}

// run is the worker's leader-only loop plus the Redis ping that triggers it; it returns when ctx
// ends.
func (s *isfcSync) run(ctx context.Context, rdb *redis.Client) {
	var wg sync.WaitGroup
	defer wg.Wait()
	if rdb != nil {
		sub := rdb.Subscribe(ctx, ISFCSyncChannel)
		defer func() { _ = sub.Close() }()
		wg.Go(func() {
			for {
				select {
				case <-ctx.Done():
					return
				case _, ok := <-sub.Channel():
					if !ok {
						return
					}
					s.loop.Trigger()
				}
			}
		})
	}
	s.loop.Run(ctx)
}
