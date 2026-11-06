package main

import (
	"cmp"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// reportCmd renders every result JSON of a run directory as Markdown. Labels follow the run
// script's convention: cmp-direct-<ms>, cmp-hub-<ms>, sync-<rps>, async-<rps>, soak.
func reportCmd(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("hubload report", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dir := fs.String("dir", "", "results directory (required)")
	pgDSN := fs.String("pg-dsn", "", "re-read the audit ledger for every sync/async result window first (rewrites the JSON)")
	if err := fs.Parse(args); err != nil || *dir == "" {
		fmt.Fprintln(stderr, "usage: hubload report -dir <results dir> [-pg-dsn DSN]")
		return 2
	}
	if *pgDSN != "" {
		if err := refreshLedger(ctx, *dir, *pgDSN); err != nil {
			fmt.Fprintln(stderr, "hubload report:", err)
			return 1
		}
	}
	rates, soaks, err := loadResults(*dir)
	if err != nil {
		fmt.Fprintln(stderr, "hubload report:", err)
		return 1
	}
	renderReport(stdout, rates, soaks)
	return 0
}

func loadResults(dir string) ([]RateResult, []SoakResult, error) {
	files, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil {
		return nil, nil, err
	}
	var rates []RateResult
	var soaks []SoakResult
	for _, f := range files {
		b, err := os.ReadFile(f) //nolint:gosec // G304: result files of the operator's run directory
		if err != nil {
			return nil, nil, err
		}
		var k struct{ Kind string }
		if json.Unmarshal(b, &k) != nil {
			continue
		}
		switch k.Kind {
		case "rate":
			var r RateResult
			if err := json.Unmarshal(b, &r); err != nil {
				return nil, nil, fmt.Errorf("%s: %w", f, err)
			}
			rates = append(rates, r)
		case "soak":
			var s SoakResult
			if err := json.Unmarshal(b, &s); err != nil {
				return nil, nil, fmt.Errorf("%s: %w", f, err)
			}
			soaks = append(soaks, s)
		}
	}
	return rates, soaks, nil
}

func renderReport(w io.Writer, rates []RateResult, soaks []SoakResult) {
	byPrefix := func(p string) []RateResult {
		var out []RateResult
		for _, r := range rates {
			if strings.HasPrefix(r.Label, p) {
				out = append(out, r)
			}
		}
		slices.SortFunc(out, func(a, b RateResult) int {
			return cmp.Or(cmp.Compare(a.VendorLatencyMS, b.VendorLatencyMS), cmp.Compare(a.TargetRPS, b.TargetRPS), cmp.Compare(a.Label, b.Label))
		})
		return out
	}
	renderComparison(w, byPrefix("cmp-direct-"), byPrefix("cmp-hub-"))
	renderSync(w, byPrefix("sync-"))
	renderAsync(w, byPrefix("async-"))
	for _, s := range soaks {
		renderSoak(w, s)
	}
}

func f1(v float64) string {
	if v < 0 {
		return "n/a"
	}
	return fmt.Sprintf("%.1f", v)
}

func f2(v float64) string {
	if v < 0 {
		return "n/a"
	}
	return fmt.Sprintf("%.2f", v)
}

func triple(a, b, c float64) string { return f1(a) + " / " + f1(b) + " / " + f1(c) }

// diff formats a difference of two latencies, which may legitimately be negative.
func diff(v float64) string { return fmt.Sprintf("%.1f", v) }

func overheadOf(r RateResult, source string) HistSummary {
	for _, k := range slices.Sorted(maps.Keys(r.Overhead)) {
		if strings.HasPrefix(k, source+"/") {
			return r.Overhead[k]
		}
	}
	return HistSummary{}
}

// ledgerOf reads the audit ledger's overhead of a run as a HistSummary (-1 when absent).
func ledgerOf(r RateResult) HistSummary {
	if r.Ledger == nil || r.Ledger.Rows == 0 {
		return HistSummary{P50: -1, P95: -1, P99: -1}
	}
	return HistSummary{Count: float64(r.Ledger.Rows), P50: r.Ledger.P50, P95: r.Ledger.P95, P99: r.Ledger.P99, OverBudget: r.Ledger.Over5}
}

// wallOf is the ledger overhead without excluding the limiter wait (-1 when absent).
func wallOf(r RateResult) HistSummary {
	if r.Ledger == nil || r.Ledger.Rows == 0 {
		return HistSummary{P50: -1, P95: -1, P99: -1}
	}
	return HistSummary{Count: float64(r.Ledger.Rows), P50: r.Ledger.WallP50, P95: r.Ledger.WallP95, P99: r.Ledger.WallP99, OverBudget: r.Ledger.WallOver5}
}

func errorsOf(r RateResult) string {
	var parts []string
	for _, k := range slices.Sorted(maps.Keys(r.Outcomes)) {
		if !strings.HasPrefix(k, "2") {
			parts = append(parts, fmt.Sprintf("%s ×%d", k, r.Outcomes[k]))
		}
	}
	if len(parts) == 0 {
		return "0"
	}
	return strings.Join(parts, ", ")
}

func renderComparison(w io.Writer, direct, hub []RateResult) {
	if len(direct)+len(hub) == 0 {
		return
	}
	fmt.Fprintln(w, "### Direct to the vendor vs through the Hub (sync)")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "| Vendor answers in | Calls/s | Direct p50 / p95 / p99 (ms) | Through the Hub p50 / p95 / p99 (ms) | Hub adds p50 / p95 / p99 (ms) | Hub's own count (audit ledger) p50 / p95 / p99 (ms) | Same, incl. limiter wait p50 / p95 / p99 (ms) | Errors (direct; Hub) |")
	fmt.Fprintln(w, "| --- | --- | --- | --- | --- | --- | --- | --- |")
	for _, h := range hub {
		var d *RateResult
		for i := range direct {
			if direct[i].VendorLatencyMS == h.VendorLatencyMS {
				d = &direct[i]
			}
		}
		if d == nil {
			continue
		}
		o, wall := ledgerOf(h), wallOf(h)
		fmt.Fprintf(w, "| %d ms | %.0f | %s | %s | %s | %s | %s | %s; %s |\n", h.VendorLatencyMS, h.TargetRPS,
			triple(d.LatencyOK.P50, d.LatencyOK.P95, d.LatencyOK.P99),
			triple(h.LatencyOK.P50, h.LatencyOK.P95, h.LatencyOK.P99),
			diff(h.LatencyOK.P50-d.LatencyOK.P50)+" / "+diff(h.LatencyOK.P95-d.LatencyOK.P95)+" / "+diff(h.LatencyOK.P99-d.LatencyOK.P99),
			triple(o.P50, o.P95, o.P99), triple(wall.P50, wall.P95, wall.P99), errorsOf(*d), errorsOf(h))
	}
	fmt.Fprintln(w)
}

func cores(r CPUUsage, names ...string) string {
	var parts []string
	for _, n := range names {
		if v, ok := r.Cores[n]; ok {
			parts = append(parts, fmt.Sprintf("%s %.2f", n, v))
		}
	}
	return strings.Join(parts, ", ")
}

func renderSync(w io.Writer, rs []RateResult) {
	if len(rs) == 0 {
		return
	}
	fmt.Fprintln(w, "### Sync ramp")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "| Target RPS | Achieved 2xx/s | Client p50 / p99 / max (ms) | Overhead (ledger) p50 / p95 / p99 (ms) | > 5 ms | Incl. limiter wait p50 / p95 / p99 (ms) | > 5 ms | Metric `hub_overhead_ms` negative share | Vendor p99 (ms) | Errors | CPU cores used | Machine busy | Load avg (start) |")
	fmt.Fprintln(w, "| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |")
	for _, r := range rs {
		l, wall, o := ledgerOf(r), wallOf(r), overheadOf(r, "gateway")
		fmt.Fprintf(w, "| %.0f | %.1f | %s / %s / %s | %s / %s / %s | %.2f %% | %s / %s / %s | %.2f %% | %.0f %% | %s | %s | %s | %.0f %% | %.2f |\n",
			r.TargetRPS, r.AchievedRPS, f1(r.LatencyOK.P50), f1(r.LatencyOK.P99), f1(r.LatencyOK.Max), f2(l.P50), f2(l.P95), f2(l.P99), l.OverBudget*100,
			f2(wall.P50), f2(wall.P95), f2(wall.P99), wall.OverBudget*100,
			o.Negative*100, f1(r.VendorLatency["gateway"].P99), errorsOf(r), cores(r.CPU, "gateway", "postgres", "pg_cluster", "redis", "mockvendor", "hubload"), r.CPU.MachineBusy*100, r.LoadAvgStart[0])
	}
	fmt.Fprintln(w)
	if len(rs) > 0 {
		top := rs[len(rs)-1]
		fmt.Fprintf(w, "Stage p99 at %.0f RPS (gateway, `hub_stage_duration_ms`, own time per stage): ", top.TargetRPS)
		var parts []string
		for _, k := range slices.Sorted(maps.Keys(top.Stages)) {
			if name, ok := strings.CutPrefix(k, "gateway/"); ok {
				parts = append(parts, fmt.Sprintf("%s %.3f ms", name, top.Stages[k].P99))
			}
		}
		fmt.Fprintln(w, strings.Join(parts, ", ")+".")
		fmt.Fprintln(w)
	}
}

func renderAsync(w io.Writer, rs []RateResult) {
	if len(rs) == 0 {
		return
	}
	fmt.Fprintln(w, "### Async ramp")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "| Target msg/s | Accepted 202/s | Accept p50 / p99 / max (ms) | Server accept (metric) p99 | Worker overhead (ledger) p50 / p99 | Worker finished/s | Max queue depth | Max oldest age (s) | Drain after stop (s) | Errors | CPU cores used | Machine busy | Load avg (start) |")
	fmt.Fprintln(w, "| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |")
	for _, r := range rs {
		o := overheadOf(r, "gateway")
		q := r.Queue
		if q == nil {
			q = &QueueStats{DrainS: -1}
		}
		l := ledgerOf(r)
		fmt.Fprintf(w, "| %.0f | %.1f | %s / %s / %s | %s | %s / %s | %.1f | %.0f | %.1f | %s | %s | %s | %.0f %% | %.2f |\n",
			r.TargetRPS, r.AchievedRPS, f1(r.LatencyOK.P50), f1(r.LatencyOK.P99), f1(r.LatencyOK.Max), f2(o.P99), f2(l.P50), f2(l.P99), q.WorkerRate, q.MaxDepth,
			q.MaxOldestAgeS, f1(q.DrainS), errorsOf(r), cores(r.CPU, "gateway", "worker", "scheduler", "postgres", "pg_cluster", "mockvendor", "hubload"), r.CPU.MachineBusy*100, r.LoadAvgStart[0])
	}
	fmt.Fprintln(w)
}

func renderSoak(w io.Writer, s SoakResult) {
	rc := s.Reconcile
	fmt.Fprintf(w, "### Soak (%s)\n\n", s.Label)
	fmt.Fprintf(w, "- Traffic: async %.0f msg/s for %.0f s (seed %d); %d accepted (202), invoke outcomes %v; accept p50 / p99 / max %s / %s / %s ms.\n",
		s.TargetRPS, s.DurationS, s.Seed, s.Accepted, s.Outcomes, f1(s.AcceptLat.P50), f1(s.AcceptLat.P99), f1(s.AcceptLat.Max))
	fmt.Fprintf(w, "- Chaos: %d events, vendor down %.0f s in total (%.0f %% of the traffic window).\n", len(s.Events), s.DownS, 100*s.DownS/max(s.DurationS, 1))
	fmt.Fprintf(w, "- Reconciled after %.0f s: complete=%v, missing (lost) %d, non-terminal %d, duplicate rows %d.\n", rc.WaitedS, rc.Complete, rc.Missing, rc.NonTerminal, rc.DuplicateRows)
	fmt.Fprintf(w, "- Callbacks: %d requests called back, %d terminal without callback, %d duplicate (more than one terminal event), %d redeliveries of the same event, %d unknown; callback rows %v.\n",
		rc.CalledBack, rc.NoCallback, rc.DuplicateCallbacks, rc.Redeliveries, rc.UnknownCallbacks, rc.CallbackRows)
	fmt.Fprintf(w, "- Attempts per request: mean %.2f, max %d. Dead-letter codes: %v.\n\n", rc.MeanAttempts, rc.MaxAttempts, rc.DeadLetterCodes)
	fmt.Fprintln(w, "| State in hub.requests | Accepted requests |")
	fmt.Fprintln(w, "| --- | --- |")
	for _, k := range slices.Sorted(maps.Keys(rc.States)) {
		fmt.Fprintf(w, "| %s | %d |\n", k, rc.States[k])
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, "| t (s) | Event | Duration (s) |")
	fmt.Fprintln(w, "| --- | --- | --- |")
	events := slices.Clone(s.Events)
	slices.SortStableFunc(events, func(a, b ChaosEvent) int { return cmp.Compare(a.AtS, b.AtS) })
	for _, e := range events {
		d := "-"
		if e.ForS > 0 {
			d = fmt.Sprintf("%.0f", e.ForS)
		}
		kind := e.Kind
		if e.Error != "" {
			kind += " (error: " + e.Error + ")"
		}
		fmt.Fprintf(w, "| %.0f | %s | %s |\n", e.AtS, kind, d)
	}
	fmt.Fprintln(w)
}

// refreshLedger re-reads the audit ledger for the window of every sync and async result in dir
// (results written before a ledger column existed, or before the audit writer had flushed).
func refreshLedger(ctx context.Context, dir, dsn string) error {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return err
	}
	defer pool.Close()
	files, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil {
		return err
	}
	defaults := map[string]string{"sync": "int_load_sync", "async": "int_load_async"}
	for _, f := range files {
		b, err := os.ReadFile(f) //nolint:gosec // G304: result files of the operator's run directory
		if err != nil {
			return err
		}
		var r RateResult
		if json.Unmarshal(b, &r) != nil || r.Kind != "rate" || r.Mode == "direct" {
			continue
		}
		if r.Integration == "" {
			r.Integration = defaults[r.Mode]
		}
		end := r.StartedAt.Add(time.Duration(r.DurationS * float64(time.Second)))
		if l := ledgerOverhead(ctx, pool, r.Integration, r.StartedAt, end); l != nil {
			r.Ledger = l
		}
		if err := writeJSON(f, nil, r); err != nil {
			return err
		}
	}
	return nil
}
