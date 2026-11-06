package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"slices"

	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/common/expfmt"
)

// scrape is one /metrics read, by family name. It is requested as delimited protobuf so native
// histograms (hub_overhead_ms has NativeHistogramBucketFactor 1.1, about 9 % wide buckets)
// come with it; the classic buckets are the fallback.
type scrape map[string]*dto.MetricFamily

const protoAccept = `application/vnd.google.protobuf;proto=io.prometheus.client.MetricFamily;encoding=delimited;q=0.7,text/plain;version=0.0.4;q=0.3`

func scrapeMetrics(ctx context.Context, c *http.Client, url string) (scrape, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", protoAccept)
	resp, err := c.Do(req)
	if err != nil {
		return nil, fmt.Errorf("scrape %s: %w", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("scrape %s: status %d", url, resp.StatusCode)
	}
	dec := expfmt.NewDecoder(resp.Body, expfmt.ResponseFormat(resp.Header))
	out := scrape{}
	for {
		mf := &dto.MetricFamily{}
		if err := dec.Decode(mf); err != nil {
			if errors.Is(err, io.EOF) {
				return out, nil
			}
			return nil, fmt.Errorf("scrape %s: decode: %w", url, err)
		}
		out[mf.GetName()] = mf
	}
}

func labelsMatch(m *dto.Metric, match map[string]string) bool {
	for k, v := range match {
		found := false
		for _, lp := range m.GetLabel() {
			if lp.GetName() == k {
				found = lp.GetValue() == v
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// sumValue adds the counter or gauge values of every series of name matching match.
func (s scrape) sumValue(name string, match map[string]string) float64 {
	var v float64
	for _, m := range s[name].GetMetric() {
		if !labelsMatch(m, match) {
			continue
		}
		switch {
		case m.Counter != nil:
			v += m.GetCounter().GetValue()
		case m.Gauge != nil:
			v += m.GetGauge().GetValue()
		case m.Untyped != nil:
			v += m.GetUntyped().GetValue()
		}
	}
	return v
}

// maxValue is the largest gauge value of the matching series (0 when none).
func (s scrape) maxValue(name string, match map[string]string) float64 {
	v := 0.0
	for _, m := range s[name].GetMetric() {
		if labelsMatch(m, match) {
			v = math.Max(v, m.GetGauge().GetValue())
		}
	}
	return v
}

// hist is a histogram, or the difference of two scrapes of one, summed over matching series.
type hist struct {
	count   float64
	sum     float64
	bounds  []float64       // classic upper bounds, ascending (+Inf implied)
	cum     []float64       // classic cumulative counts per bound
	native  bool            // native buckets usable (same schema across series and scrapes)
	schema  int32           // native schema: bucket i is (base^(i-1), base^i], base = 2^(2^-schema)
	zeroThr float64         // native zero bucket width
	zero    float64         // native zero bucket count
	pos     map[int]float64 // native positive bucket index -> count
	neg     map[int]float64 // native negative bucket index -> count (bucket i is [-base^i, -base^(i-1)))
}

// histOf sums the matching series of name in s.
func (s scrape) histOf(name string, match map[string]string) hist {
	h := hist{pos: map[int]float64{}, neg: map[int]float64{}, native: true, schema: math.MinInt32}
	for _, m := range s[name].GetMetric() {
		if !labelsMatch(m, match) || m.Histogram == nil {
			continue
		}
		dh := m.GetHistogram()
		h.count += float64(dh.GetSampleCount())
		h.sum += dh.GetSampleSum()
		for i, b := range dh.GetBucket() {
			if i >= len(h.bounds) {
				h.bounds = append(h.bounds, b.GetUpperBound())
				h.cum = append(h.cum, 0)
			}
			h.cum[i] += float64(b.GetCumulativeCount())
		}
		if len(dh.GetPositiveSpan())+len(dh.GetNegativeSpan()) == 0 && dh.GetZeroCount() == 0 {
			if dh.GetSampleCount() > 0 {
				h.native = false // no native data although observations exist
			}
			continue
		}
		if h.schema != math.MinInt32 && h.schema != dh.GetSchema() {
			h.native = false
		}
		h.schema, h.zeroThr = dh.GetSchema(), dh.GetZeroThreshold()
		h.zero += float64(dh.GetZeroCount())
		addSpans(h.pos, dh.GetPositiveSpan(), dh.GetPositiveDelta())
		addSpans(h.neg, dh.GetNegativeSpan(), dh.GetNegativeDelta())
	}
	if h.schema == math.MinInt32 {
		h.native = h.count == 0
	}
	return h
}

// addSpans decodes delta-encoded native bucket counts into dst (index -> count).
func addSpans(dst map[int]float64, spans []*dto.BucketSpan, deltas []int64) {
	idx, cnt, k := 0, int64(0), 0
	for _, sp := range spans {
		idx += int(sp.GetOffset())
		for range sp.GetLength() {
			if k >= len(deltas) {
				return
			}
			cnt += deltas[k]
			dst[idx] += float64(cnt)
			idx++
			k++
		}
	}
}

// minus is h - before (observations made between two scrapes).
func (h hist) minus(before hist) hist {
	out := hist{count: h.count - before.count, sum: h.sum - before.sum, bounds: h.bounds, native: h.native && before.native,
		schema: h.schema, zeroThr: h.zeroThr, zero: h.zero - before.zero, pos: map[int]float64{}, neg: map[int]float64{}}
	if before.count > 0 && before.schema != math.MinInt32 && before.schema != h.schema {
		out.native = false
	}
	out.cum = slices.Clone(h.cum)
	for i := range out.cum {
		if i < len(before.cum) {
			out.cum[i] -= before.cum[i]
		}
	}
	for i, c := range h.pos {
		if d := c - before.pos[i]; d > 0 {
			out.pos[i] = d
		}
	}
	for i, c := range h.neg {
		if d := c - before.neg[i]; d > 0 {
			out.neg[i] = d
		}
	}
	return out
}

// quantile estimates the q-quantile: from native buckets when available (geometric
// interpolation inside a bucket), else from classic buckets (linear, like histogram_quantile).
func (h hist) quantile(q float64) float64 {
	if h.count <= 0 {
		return math.NaN()
	}
	rank := q * h.count
	if h.native && len(h.pos)+len(h.neg)+int(h.zero) > 0 {
		base := math.Pow(2, math.Pow(2, -float64(h.schema)))
		cum := 0.0
		// Negative buckets first, most negative (highest index) first.
		nidx := make([]int, 0, len(h.neg))
		for i := range h.neg {
			nidx = append(nidx, i)
		}
		slices.Sort(nidx)
		for j := len(nidx) - 1; j >= 0; j-- {
			i := nidx[j]
			c := h.neg[i]
			if cum+c >= rank {
				hi, lo := math.Pow(base, float64(i)), math.Max(math.Pow(base, float64(i-1)), h.zeroThr)
				f := (rank - cum) / c
				return -hi * math.Pow(lo/hi, f)
			}
			cum += c
		}
		if rank <= cum+h.zero {
			return 0
		}
		cum += h.zero
		idx := make([]int, 0, len(h.pos))
		for i := range h.pos {
			idx = append(idx, i)
		}
		slices.Sort(idx)
		for _, i := range idx {
			c := h.pos[i]
			if cum+c >= rank {
				lo, hi := math.Pow(base, float64(i-1)), math.Pow(base, float64(i))
				lo = math.Max(lo, h.zeroThr)
				f := (rank - cum) / c
				return lo * math.Pow(hi/lo, f)
			}
			cum += c
		}
		if len(idx) == 0 {
			return 0
		}
		return math.Pow(base, float64(idx[len(idx)-1]))
	}
	prevBound, prevCum := 0.0, 0.0
	for i, b := range h.bounds {
		if h.cum[i] >= rank {
			c := h.cum[i] - prevCum
			if c <= 0 {
				return b
			}
			return prevBound + (b-prevBound)*(rank-prevCum)/c
		}
		prevBound, prevCum = b, h.cum[i]
	}
	return math.Inf(1) // in the +Inf bucket: beyond the largest bound
}

// fractionAbove is the share of observations above bound, exact when bound is a classic
// bucket boundary (5 ms is one for hub_overhead_ms), else -1.
func (h hist) fractionAbove(bound float64) float64 {
	if h.count <= 0 {
		return 0
	}
	for i, b := range h.bounds {
		if b == bound {
			return (h.count - h.cum[i]) / h.count
		}
	}
	return -1
}

func (h hist) mean() float64 {
	if h.count <= 0 {
		return math.NaN()
	}
	return h.sum / h.count
}

// HistSummary is a histogram delta reported in a result.
type HistSummary struct {
	Count      float64 `json:"count"`
	Mean       float64 `json:"mean"`
	P50        float64 `json:"p50"`
	P95        float64 `json:"p95"`
	P99        float64 `json:"p99"`
	OverBudget float64 `json:"over_budget_fraction,omitempty"` // share above the budget boundary
	Negative   float64 `json:"negative_fraction,omitempty"`    // share of negative observations (native buckets)
	Native     bool    `json:"native"`                         // quantiles from native buckets
}

func summarize(h hist, budget float64) HistSummary {
	s := HistSummary{Count: h.count, Native: h.native}
	if h.count <= 0 {
		return s
	}
	s.Mean, s.P50, s.P95, s.P99 = round3(h.mean()), round3(h.quantile(0.5)), round3(h.quantile(0.95)), round3(h.quantile(0.99))
	if budget > 0 {
		s.OverBudget = h.fractionAbove(budget)
	}
	var neg float64
	for _, c := range h.neg {
		neg += c
	}
	s.Negative = round3(neg / h.count)
	return s
}

func round3(v float64) float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return -1
	}
	return math.Round(v*1000) / 1000
}
