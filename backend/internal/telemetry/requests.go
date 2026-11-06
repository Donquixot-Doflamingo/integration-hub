package telemetry

import (
	"strconv"
	"strings"
	"sync"

	"github.com/prometheus/client_golang/prometheus"
)

// Overflow is the label value that replaces every bounded-dimension label once a metric has
// reached its series cap (OBSERVABILITY §3.1). Mode, code and kind keep their real values: they
// are enums and never the source of a cardinality explosion.
const Overflow = "__overflow__"

// Series caps per metric (OBSERVABILITY §3.2 estimates × ~2 headroom). A new combination past
// the cap is counted under Overflow and in metric_series_overflow_total{metric}.
const (
	RequestsSeriesCap  = 10_000
	LLMTokensSeriesCap = 4_000
	LLMCostSeriesCap   = 2_000
)

// LabelGuard caps the distinct label combinations one metric may create. Admit is O(1) and
// takes a mutex only to record a combination it has not seen; the steady state is a read.
type LabelGuard struct {
	metric   string
	max      int
	overflow prometheus.Counter

	mu   sync.RWMutex
	seen map[string]struct{}
}

// NewLabelGuard builds a guard for metric that admits at most max combinations.
func NewLabelGuard(metric string, max int, overflow *prometheus.CounterVec) *LabelGuard {
	return &LabelGuard{metric: metric, max: max, overflow: overflow.WithLabelValues(metric), seen: map[string]struct{}{}}
}

// Admit reports whether the combination may be used as is. A combination admitted once stays
// admitted, so a series never flips between its own labels and Overflow.
func (g *LabelGuard) Admit(values ...string) bool {
	key := strings.Join(values, "\x00")
	g.mu.RLock()
	_, ok := g.seen[key]
	n := len(g.seen)
	g.mu.RUnlock()
	if ok {
		return true
	}
	if n >= g.max {
		g.overflow.Inc()
		return false
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if _, ok := g.seen[key]; ok {
		return true
	}
	if len(g.seen) >= g.max {
		g.overflow.Inc()
		return false
	}
	g.seen[key] = struct{}{}
	return true
}

// RequestLabels are the labels of hub_requests_total (DESIGN §16.2). Version is internal and
// never shown to callers. Code is "ok" for a success, else a canonical error code (§4.4).
type RequestLabels struct {
	Tenant, Integration, Vendor, Account string
	Version                              int
	Mode, Code                           string
}

// CodeOK is the code label of a successful request.
const CodeOK = "ok"

// CountRequest adds one finished request to hub_requests_total. It is called exactly once per
// request: by the edge for requests that end in the gateway (sync, dry-run excluded, and any
// request refused before it was queued) and by delivery for queued ones (wait tail, async).
func (m *Metrics) CountRequest(l RequestLabels) {
	if m == nil || m.Requests == nil {
		return
	}
	version := ""
	if l.Version > 0 {
		version = strconv.Itoa(l.Version)
	}
	code := l.Code
	if code == "" {
		code = CodeOK
	}
	vals := []string{l.Tenant, l.Integration, l.Vendor, l.Account, version, l.Mode, code}
	if !m.requestsGuard.Admit(vals...) {
		vals = []string{Overflow, Overflow, Overflow, Overflow, Overflow, l.Mode, code}
	}
	m.Requests.WithLabelValues(vals...).Inc()
}

// LLMUsage is one response's token usage (DESIGN §15). Emitted by response mapping once
// llm.chat lands (HUB-333); registered now so dashboards and alerts can be built against it.
type LLMUsage struct {
	Tenant, Integration, Account, Model string
	InputTokens, OutputTokens           int
	CostUSD                             float64
}

// RecordLLMUsage adds u to llm_tokens_total and llm_cost_usd_total.
func (m *Metrics) RecordLLMUsage(u LLMUsage) {
	if m == nil || m.LLMTokens == nil {
		return
	}
	base := []string{u.Tenant, u.Integration, u.Account, u.Model}
	for _, k := range []struct {
		kind string
		n    int
	}{{"input", u.InputTokens}, {"output", u.OutputTokens}} {
		if k.n <= 0 {
			continue
		}
		vals := append(append(make([]string, 0, 5), base...), k.kind)
		if !m.llmTokensGuard.Admit(vals...) {
			vals = []string{Overflow, Overflow, Overflow, Overflow, k.kind}
		}
		m.LLMTokens.WithLabelValues(vals...).Add(float64(k.n))
	}
	if u.CostUSD > 0 {
		vals := base
		if !m.llmCostGuard.Admit(vals...) {
			vals = []string{Overflow, Overflow, Overflow, Overflow}
		}
		m.LLMCost.WithLabelValues(vals...).Add(u.CostUSD)
	}
}

func (m *Metrics) registerRequestMetrics() {
	m.SeriesOverflow = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "metric_series_overflow_total",
		Help: "Observations counted under the __overflow__ labels because the metric reached its series cap.",
	}, []string{"metric"})
	m.Requests = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "hub_requests_total",
		Help: "Finished requests by tenant, integration, vendor, account, integration version, delivery mode and final code (ok or a canonical error code).",
	}, []string{"tenant", "integration", "vendor", "account", "version", "mode", "code"})
	m.LLMTokens = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "llm_tokens_total",
		Help: "LLM tokens by tenant, integration, account, model and kind (input or output).",
	}, []string{"tenant", "integration", "account", "model", "kind"})
	m.LLMCost = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "llm_cost_usd_total",
		Help: "LLM cost in USD by tenant, integration, account and model, from the vendor's price table.",
	}, []string{"tenant", "integration", "account", "model"})
	m.requestsGuard = NewLabelGuard("hub_requests_total", m.caps.requests, m.SeriesOverflow)
	m.llmTokensGuard = NewLabelGuard("llm_tokens_total", m.caps.llmTokens, m.SeriesOverflow)
	m.llmCostGuard = NewLabelGuard("llm_cost_usd_total", m.caps.llmCost, m.SeriesOverflow)
	m.Registry.MustRegister(m.SeriesOverflow, m.Requests, m.LLMTokens, m.LLMCost)
}

type seriesCaps struct{ requests, llmTokens, llmCost int }

// SetSeriesCapsForTest replaces the series caps (tests that exercise the overflow bucket).
// It must be called before the first observation.
func (m *Metrics) SetSeriesCapsForTest(requests, llmTokens, llmCost int) {
	m.caps = seriesCaps{requests, llmTokens, llmCost}
	m.requestsGuard = NewLabelGuard("hub_requests_total", requests, m.SeriesOverflow)
	m.llmTokensGuard = NewLabelGuard("llm_tokens_total", llmTokens, m.SeriesOverflow)
	m.llmCostGuard = NewLabelGuard("llm_cost_usd_total", llmCost, m.SeriesOverflow)
}
