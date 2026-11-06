package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"os"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// The soak (HUB-236, OBSERVABILITY §12 L5/L6): async traffic for a sustained period while a
// chaos loop takes the mock vendor down and up at random and the worker is restarted, then a
// reconciliation that is independent of Hub metrics: every request the gateway accepted (202)
// must reach a terminal state in hub.requests, and the tenant's callback receiver must have
// received exactly one terminal event for it.

// terminalStates are the final request states (contracts RequestState).
var terminalStates = []string{"succeeded", "dead_lettered", "expired", "failed"}

// chaosKinds are the outage shapes the chaos loop picks from, as mockvendor behaviours.
var chaosKinds = map[string]map[string]any{
	"refuse":   {"down": map[string]any{"mode": "refuse"}},
	"reset":    {"down": map[string]any{"mode": "reset"}},
	"http_503": {"status": 503, "body": map[string]any{"error": "unavailable"}},
	// Longer than the endpoint's 3 s timeout: every attempt times out.
	"timeout": {"status": 200, "latency": map[string]any{"mode": "fixed", "fixed_ms": 3500}, "body": map[string]any{"status": "success", "processed": 1, "unprocessed": []any{}}},
}

// SoakResult is the JSON written by `hubload soak`.
type SoakResult struct {
	Kind         string         `json:"kind"` // "soak"
	Label        string         `json:"label"`
	StartedAt    time.Time      `json:"started_at"`
	TargetRPS    float64        `json:"target_rps"`
	DurationS    float64        `json:"duration_s"`
	Seed         uint64         `json:"seed"`
	Outcomes     map[string]int `json:"outcomes"` // invoke responses
	Accepted     int            `json:"accepted"` // distinct request ids answered 202
	AcceptLat    Quantiles      `json:"accept_latency"`
	Events       []ChaosEvent   `json:"events"`
	DownS        float64        `json:"vendor_down_s"`
	Reconcile    Reconcile      `json:"reconcile"`
	Timeline     []TimelineRow  `json:"timeline"`
	LoadAvgStart [3]float64     `json:"load_avg_start"`
	LoadAvgEnd   [3]float64     `json:"load_avg_end"`
	CPU          CPUUsage       `json:"cpu"`
}

// ChaosEvent is one outage or restart.
type ChaosEvent struct {
	AtS   float64 `json:"at_s"` // seconds since traffic start
	Kind  string  `json:"kind"` // refuse | reset | http_503 | timeout | worker_term | worker_kill
	ForS  float64 `json:"for_s,omitempty"`
	Error string  `json:"error,omitempty"`
}

// TimelineRow is one sample of the outbox during the soak.
type TimelineRow struct {
	AtS       float64        `json:"at_s"`
	Outbox    map[string]int `json:"outbox"` // status -> rows
	Accepted  int            `json:"accepted"`
	Callbacks int            `json:"callbacks"`
	VendorUp  bool           `json:"vendor_up"`
}

// Reconcile is the zero-loss proof.
type Reconcile struct {
	WaitedS            float64        `json:"waited_s"` // traffic stop -> reconciled (or gave up)
	Complete           bool           `json:"complete"` // every accepted request terminal and called back
	States             map[string]int `json:"states"`   // hub.requests status -> accepted requests
	Missing            int            `json:"missing"`  // accepted but no hub.requests row: lost
	NonTerminal        int            `json:"non_terminal"`
	DuplicateRows      int            `json:"duplicate_rows"`      // request ids with more than one hub.requests row
	CallbackRows       map[string]int `json:"callback_rows"`       // hub.callback_deliveries status -> rows
	CalledBack         int            `json:"called_back"`         // accepted requests with >= 1 terminal callback received
	NoCallback         int            `json:"no_callback"`         // terminal accepted requests without a callback
	DuplicateCallbacks int            `json:"duplicate_callbacks"` // requests with > 1 distinct terminal event received
	Redeliveries       int            `json:"redeliveries"`        // same event received more than once (at-least-once retries)
	UnknownCallbacks   int            `json:"unknown_callbacks"`   // callbacks for request ids the generator never saw accepted
	CallbackTypes      map[string]int `json:"callback_types"`
	MaxAttempts        int            `json:"max_attempts"`
	MeanAttempts       float64        `json:"mean_attempts"`
	DeadLetterCodes    map[string]int `json:"dead_letter_codes,omitempty"`
	Examples           []string       `json:"examples,omitempty"` // a few offending request ids
}

type soakConfig struct {
	label, url, integration, apiKey, mockControl, pgDSN, receiverAddr, restartHook, out, idsOut string
	rps                                                                                         float64
	duration, drainTimeout, minUp, maxUp, minDown, maxDown, timeout                             time.Duration
	restarts, bodyKB, maxInFlight, vendorLatency                                                int
	seed                                                                                        uint64
	cpu                                                                                         cpuTargets
	kinds                                                                                       string
}

func soakCmd(ctx context.Context, args []string, getenv func(string) string, stdout, stderr io.Writer) int {
	var c soakConfig
	fs := flag.NewFlagSet("hubload soak", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&c.label, "label", "soak", "result label")
	fs.StringVar(&c.url, "url", "http://127.0.0.1:8080", "gateway base URL")
	fs.StringVar(&c.integration, "integration", "", "async_callback integration id (required)")
	fs.StringVar(&c.mockControl, "mock-control", "", "mockvendor control URL (required)")
	fs.StringVar(&c.pgDSN, "pg-dsn", getenv("HUB_PG_DSN"), "Postgres DSN (reconciliation)")
	fs.StringVar(&c.receiverAddr, "receiver-addr", "127.0.0.1:8097", "callback receiver listen address (the registrar endpoint's host:port)")
	fs.StringVar(&c.restartHook, "restart-hook", "", "executable run with 'term' or 'kill' to restart the worker (empty: no restarts)")
	fs.IntVar(&c.restarts, "restarts", 2, "worker restarts during the soak (alternating term, kill)")
	fs.StringVar(&c.out, "out", "", "write the JSON result here (default stdout)")
	fs.StringVar(&c.idsOut, "ids-out", "", "write the accepted request ids here, one per line")
	fs.Float64Var(&c.rps, "rps", 100, "async arrivals per second")
	fs.DurationVar(&c.duration, "duration", 4*time.Minute, "traffic duration")
	fs.DurationVar(&c.drainTimeout, "drain-timeout", 8*time.Minute, "longest wait after traffic for every request to settle")
	fs.DurationVar(&c.minUp, "min-up", 30*time.Second, "shortest healthy period between outages")
	fs.DurationVar(&c.maxUp, "max-up", 60*time.Second, "longest healthy period between outages")
	fs.DurationVar(&c.minDown, "min-down", 10*time.Second, "shortest outage")
	fs.DurationVar(&c.maxDown, "max-down", 40*time.Second, "longest outage")
	fs.DurationVar(&c.timeout, "timeout", 10*time.Second, "client timeout per invoke")
	fs.StringVar(&c.kinds, "kinds", "refuse,reset,http_503,timeout", "outage kinds to pick from")
	fs.IntVar(&c.bodyKB, "body-kb", 4, "canonical body size in KiB")
	fs.IntVar(&c.maxInFlight, "max-inflight", 4000, "arrivals beyond this many outstanding are dropped")
	fs.IntVar(&c.vendorLatency, "vendor-latency-ms", 20, "healthy mockvendor latency")
	fs.Uint64Var(&c.seed, "seed", 0, "chaos seed (0: random, recorded in the result)")
	fs.Var(&c.cpu, "cpu", "name=pid or name=comm:<comm> to report CPU for (repeatable)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	c.apiKey = getenv("HUBLOAD_API_KEY")
	if err := c.validate(); err != nil {
		fmt.Fprintln(stderr, "hubload soak:", err)
		return 2
	}
	res, err := runSoak(ctx, c, stderr)
	if err != nil {
		fmt.Fprintln(stderr, "hubload soak:", err)
		return 1
	}
	if err := writeJSON(c.out, stdout, res); err != nil {
		fmt.Fprintln(stderr, "hubload soak:", err)
		return 1
	}
	if !res.Reconcile.Complete || res.Reconcile.Missing > 0 || res.Reconcile.DuplicateCallbacks > 0 {
		return 3 // the soak's acceptance failed; the result says why
	}
	return 0
}

func (c soakConfig) validate() error {
	var errs []error
	if c.integration == "" || c.mockControl == "" || c.pgDSN == "" {
		errs = append(errs, errors.New("-integration, -mock-control and -pg-dsn (or HUB_PG_DSN) are required"))
	}
	if c.apiKey == "" {
		errs = append(errs, errors.New("HUBLOAD_API_KEY is required"))
	}
	if c.rps <= 0 || c.duration <= 0 || c.minUp <= 0 || c.maxUp < c.minUp || c.minDown <= 0 || c.maxDown < c.minDown {
		errs = append(errs, errors.New("rates, durations and up/down ranges must be positive and ordered"))
	}
	for k := range strings.SplitSeq(c.kinds, ",") {
		if _, ok := chaosKinds[k]; !ok {
			errs = append(errs, fmt.Errorf("-kinds: unknown outage kind %q", k))
		}
	}
	return errors.Join(errs...)
}

func runSoak(ctx context.Context, c soakConfig, log io.Writer) (*SoakResult, error) {
	if c.seed == 0 {
		c.seed = rand.Uint64() // #nosec G404 -- chaos schedule seed, not security
	}
	rng := rand.New(rand.NewPCG(c.seed, c.seed^0x9e3779b97f4a7c15)) // #nosec G404 -- reproducible chaos schedule, not security
	ctl := &http.Client{Timeout: 10 * time.Second}
	pool, err := pgxpool.New(ctx, c.pgDSN)
	if err != nil {
		return nil, fmt.Errorf("postgres: %w", err)
	}
	defer pool.Close()
	if err := setHealthyVendor(ctx, ctl, c.mockControl, c.vendorLatency); err != nil {
		return nil, err
	}
	_ = mockCall(ctx, ctl, http.MethodDelete, c.mockControl, "/__control/routes/chaos", nil)

	recv := newReceiver()
	ln, err := (&net.ListenConfig{}).Listen(ctx, "tcp", c.receiverAddr)
	if err != nil {
		return nil, fmt.Errorf("callback receiver: %w", err)
	}
	srv := &http.Server{Handler: recv, ReadHeaderTimeout: 5 * time.Second}
	var bg sync.WaitGroup
	bg.Go(func() { _ = srv.Serve(ln) })
	defer func() {
		sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(sctx)
		bg.Wait()
	}()

	res := &SoakResult{Kind: "soak", Label: c.label, TargetRPS: c.rps, Seed: c.seed, StartedAt: time.Now().UTC(), LoadAvgStart: loadAvg()}
	var mu sync.Mutex
	accepted := map[string]bool{}
	g := &generator{client: newHTTPClient(c.maxInFlight, c.timeout), target: rateConfig{mode: "async", url: c.url, integration: c.integration, apiKey: c.apiKey, bodyKB: c.bodyKB}.target(),
		rate: c.rps, maxInFlight: c.maxInFlight, onResponse: func(status int, body []byte) {
			var a struct {
				RequestID string `json:"request_id"`
			}
			if status == http.StatusAccepted && json.Unmarshal(body, &a) == nil && a.RequestID != "" {
				mu.Lock()
				accepted[a.RequestID] = true
				mu.Unlock()
			}
		}}

	start := time.Now()
	cpu0 := c.cpu.snap()
	tctx, stopTraffic := context.WithCancel(ctx)
	var events []ChaosEvent
	var evMu sync.Mutex
	var vendorUp = newFlag(true)
	addEvent := func(e ChaosEvent) {
		evMu.Lock()
		events = append(events, e)
		evMu.Unlock()
		fmt.Fprintf(log, "soak: t=%.0fs %s %.0fs %s\n", e.AtS, e.Kind, e.ForS, e.Error)
	}
	var chaos sync.WaitGroup
	chaos.Go(func() { c.chaosLoop(tctx, ctl, rng, start, vendorUp, addEvent) })
	if c.restartHook != "" && c.restarts > 0 {
		at := restartTimes(rng, c.duration, c.restarts)
		chaos.Go(func() { c.restartLoop(tctx, start, at, addEvent) })
	}
	var timeline []TimelineRow
	var tlMu sync.Mutex
	sctx, stopSampling := context.WithCancel(ctx)
	var sampling sync.WaitGroup
	sampling.Go(func() {
		sampleTimeline(sctx, pool, start, 5*time.Second, func(r TimelineRow) {
			mu.Lock()
			r.Accepted = len(accepted)
			mu.Unlock()
			r.Callbacks, r.VendorUp = recv.total(), vendorUp.get()
			tlMu.Lock()
			timeline = append(timeline, r)
			tlMu.Unlock()
		})
	})

	fmt.Fprintf(log, "soak: async %.0f/s for %s, seed %d\n", c.rps, c.duration, c.seed)
	gr := g.run(ctx, c.duration)
	stopTraffic()
	chaos.Wait()
	// The vendor is healthy from here on: everything parked or retrying must now settle.
	if err := mockCall(ctx, ctl, http.MethodDelete, c.mockControl, "/__control/routes/chaos", nil); err != nil {
		fmt.Fprintf(log, "warning: restore vendor: %v\n", err)
	}
	vendorUp.set(true)
	res.DurationS = round3(gr.elapsed.Seconds())
	res.Outcomes = map[string]int{}
	var lat []time.Duration
	for _, s := range gr.samples {
		res.Outcomes[s.outcome]++
		if s.outcome == "202" {
			lat = append(lat, s.latency)
		}
	}
	res.AcceptLat = quantilesOf(lat)
	mu.Lock()
	ids := slices.Sorted(func(yield func(string) bool) {
		for id := range accepted {
			if !yield(id) {
				return
			}
		}
	})
	mu.Unlock()
	res.Accepted = len(ids)
	if c.idsOut != "" {
		_ = os.WriteFile(c.idsOut, []byte(strings.Join(ids, "\n")+"\n"), 0o600)
	}

	recStart := time.Now()
	fmt.Fprintf(log, "soak: traffic done, %d accepted; reconciling (up to %s)\n", len(ids), c.drainTimeout)
	res.Reconcile = reconcileLoop(ctx, pool, recv, ids, start.Add(-time.Minute), c.drainTimeout, log)
	res.Reconcile.WaitedS = round3(time.Since(recStart).Seconds())
	stopSampling()
	sampling.Wait()
	res.CPU = cpuBetween(cpu0, c.cpu.snap())
	res.LoadAvgEnd = loadAvg()
	evMu.Lock()
	res.Events = events
	evMu.Unlock()
	for _, e := range res.Events {
		res.DownS += e.ForS
	}
	res.DownS = round3(res.DownS)
	tlMu.Lock()
	res.Timeline = timeline
	tlMu.Unlock()
	return res, nil
}

// chaosLoop alternates healthy and outage periods until ctx ends, then restores the vendor.
func (c soakConfig) chaosLoop(ctx context.Context, ctl *http.Client, rng *rand.Rand, start time.Time, up *flagVal, add func(ChaosEvent)) {
	kinds := strings.Split(c.kinds, ",")
	between := func(lo, hi time.Duration) time.Duration { return lo + time.Duration(rng.Int64N(int64(hi-lo)+1)) }
	for {
		if !sleepCtx(ctx, between(c.minUp, c.maxUp)) {
			return
		}
		kind := kinds[rng.IntN(len(kinds))]
		down := between(c.minDown, c.maxDown)
		ev := ChaosEvent{AtS: round3(time.Since(start).Seconds()), Kind: kind, ForS: round3(down.Seconds())}
		spec := map[string]any{"match": map[string]string{"method": "*", "path": "/**"}, "priority": 100, "behaviour": chaosKinds[kind]}
		if err := mockPut(context.WithoutCancel(ctx), ctl, c.mockControl, "/__control/routes/chaos", spec); err != nil {
			ev.Error, ev.ForS = err.Error(), 0
			add(ev)
			continue
		}
		up.set(false)
		completed := sleepCtx(ctx, down)
		if !completed {
			ev.ForS = round3(time.Since(start).Seconds() - ev.AtS)
		}
		if err := mockCall(context.WithoutCancel(ctx), ctl, http.MethodDelete, c.mockControl, "/__control/routes/chaos", nil); err != nil {
			ev.Error = err.Error()
		}
		up.set(true)
		add(ev)
		if !completed {
			return
		}
	}
}

// restartTimes spreads n restarts over the middle of the soak (10 %..80 % of d), sorted.
func restartTimes(rng *rand.Rand, d time.Duration, n int) []time.Duration {
	out := make([]time.Duration, n)
	for i := range out {
		out[i] = d/10 + time.Duration(rng.Int64N(int64(d*7/10)))
	}
	slices.Sort(out)
	return out
}

func (c soakConfig) restartLoop(ctx context.Context, start time.Time, at []time.Duration, add func(ChaosEvent)) {
	for i, d := range at {
		if !sleepCtx(ctx, time.Until(start.Add(d))) {
			return
		}
		kind := "term"
		if i%2 == 1 {
			kind = "kill"
		}
		ev := ChaosEvent{AtS: round3(time.Since(start).Seconds()), Kind: "worker_" + kind}
		cmd := exec.CommandContext(context.WithoutCancel(ctx), c.restartHook, kind) // #nosec G204 -- operator-supplied restart hook of a load-test tool
		if out, err := cmd.CombinedOutput(); err != nil {
			ev.Error = strings.TrimSpace(err.Error() + " " + string(out))
		}
		add(ev)
	}
}

func sleepCtx(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return ctx.Err() == nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

type flagVal struct {
	mu sync.Mutex
	v  bool
}

func newFlag(v bool) *flagVal { return &flagVal{v: v} }

func (f *flagVal) set(v bool) { f.mu.Lock(); f.v = v; f.mu.Unlock() }

func (f *flagVal) get() bool { f.mu.Lock(); defer f.mu.Unlock(); return f.v }

func sampleTimeline(ctx context.Context, pool *pgxpool.Pool, start time.Time, every time.Duration, emit func(TimelineRow)) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		row := TimelineRow{AtS: round3(time.Since(start).Seconds()), Outbox: map[string]int{}}
		if rows, err := pool.Query(ctx, `SELECT status, count(*) FROM hub.outbox GROUP BY status`); err == nil {
			for rows.Next() {
				var s string
				var n int
				if rows.Scan(&s, &n) == nil {
					row.Outbox[s] = n
				}
			}
			rows.Close()
		}
		emit(row)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// receiver is the tenant's callback endpoint: it records every POST by request id and event.
type receiver struct {
	mu    sync.Mutex
	posts int
	byReq map[string]map[string]int // request id -> event id -> times received
	types map[string]string         // event id -> type
}

func newReceiver() *receiver {
	return &receiver{byReq: map[string]map[string]int{}, types: map[string]string{}}
}

func (r *receiver) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	var ev struct {
		EventID   string `json:"event_id"`
		Type      string `json:"type"`
		RequestID string `json:"request_id"`
	}
	b, err := io.ReadAll(io.LimitReader(req.Body, 1<<20))
	if err != nil || req.Method != http.MethodPost || json.Unmarshal(b, &ev) != nil || ev.RequestID == "" || ev.EventID == "" {
		http.Error(w, "bad callback", http.StatusBadRequest)
		return
	}
	r.mu.Lock()
	r.posts++
	if r.byReq[ev.RequestID] == nil {
		r.byReq[ev.RequestID] = map[string]int{}
	}
	r.byReq[ev.RequestID][ev.EventID]++
	r.types[ev.EventID] = ev.Type
	r.mu.Unlock()
	w.WriteHeader(http.StatusNoContent)
}

func (r *receiver) total() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.posts
}

// callbackStats summarises what the receiver saw for the accepted ids.
type callbackStats struct {
	calledBack, duplicates, redeliveries, unknown int
	types                                         map[string]int
	withCallback                                  map[string]bool
	dupExamples                                   []string
}

func (r *receiver) stats(accepted map[string]bool) callbackStats {
	r.mu.Lock()
	defer r.mu.Unlock()
	s := callbackStats{types: map[string]int{}, withCallback: map[string]bool{}}
	for req, evs := range r.byReq {
		if !accepted[req] {
			s.unknown++
			continue
		}
		terminal := 0
		for id, n := range evs {
			t := r.types[id]
			s.types[t]++
			if n > 1 {
				s.redeliveries++
			}
			if t != "delivery.status" {
				terminal++
			}
		}
		if terminal > 0 {
			s.calledBack++
			s.withCallback[req] = true
		}
		if terminal > 1 {
			s.duplicates++
			if len(s.dupExamples) < 5 {
				s.dupExamples = append(s.dupExamples, req)
			}
		}
	}
	return s
}

// reconcileLoop polls until every accepted request is terminal, no callback is pending and every
// terminal request was called back, or until maxWait; then it returns the final picture.
func reconcileLoop(ctx context.Context, pool *pgxpool.Pool, recv *receiver, ids []string, since time.Time, maxWait time.Duration, log io.Writer) Reconcile {
	deadline := time.Now().Add(maxWait)
	acc := make(map[string]bool, len(ids))
	for _, id := range ids {
		acc[id] = true
	}
	for {
		rec, err := reconcileOnce(ctx, pool, recv, ids, acc, since)
		if err != nil {
			fmt.Fprintf(log, "reconcile: %v\n", err)
		}
		if err == nil && rec.Complete || time.Now().After(deadline) || ctx.Err() != nil {
			return rec
		}
		fmt.Fprintf(log, "reconcile: %d non-terminal, %d missing, %d without callback, callbacks %v\n", rec.NonTerminal, rec.Missing, rec.NoCallback, rec.CallbackRows)
		if !sleepCtx(ctx, 5*time.Second) {
			return rec
		}
	}
}

func reconcileOnce(ctx context.Context, pool *pgxpool.Pool, recv *receiver, ids []string, acc map[string]bool, since time.Time) (Reconcile, error) {
	rec := Reconcile{States: map[string]int{}, CallbackRows: map[string]int{}}
	rows, err := pool.Query(ctx, `SELECT request_id, status, attempts, COALESCE(error_code, '') FROM hub.requests
		WHERE request_id = ANY($1) AND created_at >= $2`, ids, since)
	if err != nil {
		return rec, err
	}
	seen := map[string]int{}
	terminal := map[string]bool{}
	var attemptsSum int
	for rows.Next() {
		var id, status, code string
		var attempts int
		if err := rows.Scan(&id, &status, &attempts, &code); err != nil {
			rows.Close()
			return rec, err
		}
		seen[id]++
		if seen[id] > 1 {
			rec.DuplicateRows++
			continue
		}
		rec.States[status]++
		attemptsSum += attempts
		rec.MaxAttempts = max(rec.MaxAttempts, attempts)
		if slices.Contains(terminalStates, status) {
			terminal[id] = true
			if status == "dead_lettered" {
				if rec.DeadLetterCodes == nil {
					rec.DeadLetterCodes = map[string]int{}
				}
				rec.DeadLetterCodes[code]++
			}
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return rec, err
	}
	if len(seen) > 0 {
		rec.MeanAttempts = round3(float64(attemptsSum) / float64(len(seen)))
	}
	rec.Missing = len(ids) - len(seen)
	rec.NonTerminal = len(seen) - len(terminal)
	crow, err := pool.Query(ctx, `SELECT status, count(*) FROM hub.callback_deliveries WHERE request_id = ANY($1) GROUP BY status`, ids)
	if err != nil {
		return rec, err
	}
	for crow.Next() {
		var s string
		var n int
		if crow.Scan(&s, &n) == nil {
			rec.CallbackRows[s] = n
		}
	}
	crow.Close()
	cs := recv.stats(acc)
	rec.CalledBack, rec.DuplicateCallbacks, rec.Redeliveries, rec.UnknownCallbacks, rec.CallbackTypes = cs.calledBack, cs.duplicates, cs.redeliveries, cs.unknown, cs.types
	for id := range terminal {
		if !cs.withCallback[id] {
			rec.NoCallback++
			if len(rec.Examples) < 5 {
				rec.Examples = append(rec.Examples, "no_callback:"+id)
			}
		}
	}
	for _, id := range ids {
		if seen[id] == 0 && len(rec.Examples) < 10 {
			rec.Examples = append(rec.Examples, "missing:"+id)
		}
	}
	for _, id := range cs.dupExamples {
		rec.Examples = append(rec.Examples, "duplicate_callback:"+id)
	}
	pending := rec.CallbackRows["pending"] + rec.CallbackRows["in_flight"] + rec.CallbackRows["retry_scheduled"]
	rec.Complete = rec.Missing == 0 && rec.NonTerminal == 0 && rec.NoCallback == 0 && pending == 0
	return rec, nil
}
