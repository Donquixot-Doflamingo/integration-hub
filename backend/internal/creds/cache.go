package creds

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/telemetry"
)

// Loader is the slow, decrypting side of a credential source (LocalFile today; KMS and Vault
// later). Only Cache.Warm and the cache's background refresh call it.
type Loader interface {
	Load(ctx context.Context, accountIDs []string) (map[string]AccountSecrets, error)
}

// CacheOptions tunes a Cache. Zero values take the documented defaults.
type CacheOptions struct {
	// TTL is how long a loaded slot may serve requests without a successful reload. Default 5 min.
	TTL time.Duration
	// RefreshEvery is the background reload period. Default TTL / 2, so a healthy source keeps
	// every entry fresh and one failed reload does not expire anything.
	RefreshEvery time.Duration
	// MinRefreshGap rate-limits reloads triggered by cache misses. Default 1 s.
	MinRefreshGap time.Duration
	// Now is the clock. Default time.Now.
	Now func() time.Time
	// Logger receives background reload failures. Default: discard.
	Logger *slog.Logger
}

func (o *CacheOptions) withDefaults() {
	if o.TTL <= 0 {
		o.TTL = 5 * time.Minute
	}
	if o.RefreshEvery <= 0 {
		o.RefreshEvery = o.TTL / 2
	}
	if o.MinRefreshGap <= 0 {
		o.MinRefreshGap = time.Second
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Logger == nil {
		o.Logger = slog.New(slog.DiscardHandler)
	}
}

// Cache is the decrypted credential cache (DESIGN §9, TECH_SPEC §9). It implements Source for
// the request path: Get is a lock-free map lookup that never decrypts. Warm (at snapshot load,
// before the swap) and Run (background refresh) are the only callers of the Loader.
type Cache struct {
	loader  Loader
	secrets *telemetry.SecretSet
	opts    CacheOptions

	gen  atomic.Pointer[generation]
	kick chan struct{} // cap 1: a miss asks Run for an early reload

	mu         sync.Mutex // serialises reloads; guards wanted and lastReload
	wanted     []string
	lastReload time.Time

	misses   *prometheus.CounterVec
	decrypts prometheus.Counter
}

var _ Source = (*Cache)(nil)

type versionKey struct {
	account string
	version int
}

type entry struct {
	creds   Credentials
	expires time.Time
}

// generation is one immutable cache state, published with a single pointer store.
type generation struct {
	active    map[string]*entry     // account → active slot
	byVersion map[versionKey]*entry // account + version → active or next slot
}

// NewCache builds a cache over loader. Every value it loads is registered with secrets for
// log redaction. Its metrics (creds_cache_miss_total, creds_decrypts_total) are registered on
// m.Registry.
func NewCache(loader Loader, secrets *telemetry.SecretSet, m *telemetry.Metrics, opts CacheOptions) (*Cache, error) {
	if loader == nil || secrets == nil || m == nil {
		return nil, errors.New("creds: NewCache needs a loader, a secret set and metrics")
	}
	opts.withDefaults()
	c := &Cache{
		loader: loader, secrets: secrets, opts: opts, kick: make(chan struct{}, 1),
		misses: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "creds_cache_miss_total",
			Help: "Credential lookups on the request path that found no warmed entry (must stay 0).",
		}, []string{"account"}),
		decrypts: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "creds_decrypts_total",
			Help: "Calls to the decrypting credential loader (warm-up and background refresh only).",
		}),
	}
	for _, col := range []prometheus.Collector{c.misses, c.decrypts} {
		if err := m.Registry.Register(col); err != nil {
			return nil, fmt.Errorf("creds: register metrics: %w", err)
		}
	}
	c.gen.Store(&generation{active: map[string]*entry{}, byVersion: map[versionKey]*entry{}})
	return c, nil
}

// Get returns the active credentials of accountID from the warmed cache. It never calls the
// loader: a missing or expired entry returns a *MissError (errors.Is ErrCacheMiss), counts
// creds_cache_miss_total and asks the background refresher for an early reload.
func (c *Cache) Get(_ context.Context, accountID string) (Credentials, error) {
	if e := c.gen.Load().active[accountID]; e != nil && c.opts.Now().Before(e.expires) {
		return e.creds, nil
	}
	return Credentials{}, c.miss(accountID)
}

// GetVersion returns a specific slot version (active or next) of accountID, for attempts
// pinned to a credential version. Same no-decrypt rule as Get.
func (c *Cache) GetVersion(_ context.Context, accountID string, version int) (Credentials, error) {
	if e := c.gen.Load().byVersion[versionKey{accountID, version}]; e != nil && c.opts.Now().Before(e.expires) {
		return e.creds, nil
	}
	return Credentials{}, c.miss(accountID)
}

func (c *Cache) miss(accountID string) error {
	c.misses.WithLabelValues(accountID).Inc()
	select {
	case c.kick <- struct{}{}:
	default:
	}
	return &MissError{AccountID: accountID}
}

// Warm sets the accounts the cache serves (typically AccountIDs of the snapshot about to be
// swapped in) and loads them now, off the request path. Accounts that fail to load keep their
// previous, unexpired entry if they had one. The error lists every account that could not be
// loaded; accounts that did load are served either way, so the caller decides whether a
// partial warm blocks the swap.
func (c *Cache) Warm(ctx context.Context, accountIDs []string) error {
	ids := slices.Clone(accountIDs)
	slices.Sort(ids)
	ids = slices.Compact(ids)
	c.mu.Lock()
	defer c.mu.Unlock()
	c.wanted = ids
	return c.reloadLocked(ctx)
}

// Refresh reloads the current account set now.
func (c *Cache) Refresh(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.reloadLocked(ctx)
}

// Run refreshes the cache every RefreshEvery and soon after a miss (at most once per
// MinRefreshGap), until ctx ends. Start it once per process; it is the only background
// goroutine of the cache.
func (c *Cache) Run(ctx context.Context) {
	t := time.NewTicker(c.opts.RefreshEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			c.backgroundRefresh(ctx, false)
		case <-c.kick:
			c.backgroundRefresh(ctx, true)
		}
	}
}

func (c *Cache) backgroundRefresh(ctx context.Context, onMiss bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if onMiss && c.opts.Now().Sub(c.lastReload) < c.opts.MinRefreshGap {
		return
	}
	if err := c.reloadLocked(ctx); err != nil && ctx.Err() == nil {
		c.opts.Logger.WarnContext(ctx, "credential cache refresh failed", slog.Any("error", err), slog.Bool("on_miss", onMiss))
	}
}

// reloadLocked loads the wanted accounts and publishes a new generation. Callers hold c.mu.
func (c *Cache) reloadLocked(ctx context.Context) error {
	c.lastReload = c.opts.Now()
	if len(c.wanted) == 0 {
		c.gen.Store(&generation{active: map[string]*entry{}, byVersion: map[versionKey]*entry{}})
		return nil
	}
	c.decrypts.Inc()
	loaded, loadErr := c.loader.Load(ctx, c.wanted)
	now := c.opts.Now()
	expires := now.Add(c.opts.TTL)
	old := c.gen.Load()
	next := &generation{active: make(map[string]*entry, len(c.wanted)), byVersion: make(map[versionKey]*entry, len(c.wanted))}
	var secrets []string
	for _, id := range c.wanted {
		s, ok := loaded[id]
		if !ok {
			// Keep the last good entries until they expire (bounded staleness, fail closed after TTL).
			if e := old.active[id]; e != nil && now.Before(e.expires) {
				next.active[id] = e
				for k, ve := range old.byVersion {
					if k.account == id && now.Before(ve.expires) {
						next.byVersion[k] = ve
					}
				}
			}
			continue
		}
		act := &entry{creds: Credentials{AccountID: id, Version: s.Active.Version, Values: s.Active.Values}, expires: expires}
		next.active[id] = act
		next.byVersion[versionKey{id, s.Active.Version}] = act
		secrets = appendSecrets(secrets, s.Active.Values)
		if s.Next != nil {
			next.byVersion[versionKey{id, s.Next.Version}] = &entry{creds: Credentials{AccountID: id, Version: s.Next.Version, Values: s.Next.Values}, expires: expires}
			secrets = appendSecrets(secrets, s.Next.Values)
		}
	}
	// Register before publishing, so no request can use a value the logger does not redact.
	// Values are never removed: another component may have registered the same value, and a
	// retired secret is still worth redacting.
	c.secrets.Add(secrets...)
	c.gen.Store(next)
	return loadErr
}

// appendSecrets adds every value plus the derived forms auth schemes put on the wire (the
// base64 of username:password for basic auth), so redaction also catches those.
func appendSecrets(dst []string, values map[string]string) []string {
	for _, v := range values {
		dst = append(dst, v)
	}
	if u, p := values["username"], values["password"]; u != "" && p != "" {
		dst = append(dst, basicToken(u, p))
	}
	return dst
}

func basicToken(username, password string) string {
	return base64.StdEncoding.EncodeToString([]byte(username + ":" + password))
}
