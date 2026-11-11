package roles

import (
	"context"
	"log/slog"
	"sync"
	"time"

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
	keys := &isfcsync.KeySync{Source: client, Store: store, BackendClientID: cfg.ISFC.BackendClientID, Metrics: metrics, Log: log,
		Notify: func(ctx context.Context) { // gateways reload their key map; polling is the fallback
			if err := st.rdb.Publish(ctx, ISFCKeysChannel, "keys").Err(); err != nil {
				log.WarnContext(ctx, "isfc key ping failed: gateways reload on their next poll", slog.String("error", err.Error()))
			}
		}}
	rec := &isfcsync.Reconciler{Source: client, Editor: pub, Store: store, Metrics: metrics, Log: log, Keys: keys,
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

// keyPoll is the key-table poll that backs up the hub:isfc:keys ping (a var: tests shorten it).
var keyPoll = time.Minute

// runCallerKeyLoader keeps the gateway's ISFC caller-key map current (AUTH_PLAN §6.2): it loads the
// table at start (retrying until the first load succeeds, so a node never serves with a map it has
// not read), reloads on every hub:isfc:keys ping and every keyPoll, and keeps the last good map
// when the database cannot be read. It touches no registry snapshot. It returns when ctx ends.
func runCallerKeyLoader(ctx context.Context, st *stores, km *isfc.KeyMap, log *slog.Logger) {
	load := func() bool {
		lctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		if err := isfcsync.LoadKeyMap(lctx, st.pg, km); err != nil {
			if ctx.Err() == nil {
				log.WarnContext(ctx, "isfc caller keys not loaded: keeping the current map", slog.String("error", err.Error()))
			}
			return false
		}
		return true
	}
	ping := make(<-chan *redis.Message)
	if st.rdb != nil {
		sub := st.rdb.Subscribe(ctx, ISFCKeysChannel)
		defer func() { _ = sub.Close() }()
		ping = sub.Channel()
	}
	for !load() { // until the first load: ISFC credentials get 503 NOT_READY meanwhile
		sleepCtx(ctx, 2*time.Second)
		if ctx.Err() != nil {
			return
		}
	}
	log.InfoContext(ctx, "isfc caller keys loaded", slog.Int("keys", km.Len()))
	t := time.NewTicker(keyPoll)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ping:
		case <-t.C:
		}
		load()
	}
}

func sleepCtx(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}
