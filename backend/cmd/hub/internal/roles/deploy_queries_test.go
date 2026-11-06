package roles

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/prometheus/promql/parser"
	"go.yaml.in/yaml/v3"
)

// HUB-232: alert rules (deploy/alerts) and dashboards (deploy/dashboards) may only query metric
// and label names this binary exports. The catalogue comes from catalogRegistry, so renaming a
// metric or a label in code fails here instead of silently emptying a panel or an alert.

// designAlerts is DESIGN §16.3: every one must have a rule (OBSERVABILITY §11.1 names).
var designAlerts = []string{
	"BreakerOpenTooLong", "ParkedOldestAgeHigh", "QueueOldestAgeHigh", "DLQSurge", "UpstreamAuthFailed",
	"TransformFailed", "SnapshotBehind", "CredentialExpiringSoon", "HubOverheadP99High", "AuditEventsDropped",
}

const runbookBase = "https://github.com/Donquixot-Doflamingo/integration-hub/blob/main/docs/runbooks/"

// histogramNames are the exported classic histograms: only these have _bucket series, and their
// bare name has no series without native-histogram scraping. summaryNames have quantile series.
// Every entry must be exported (checked), so the lists cannot go stale.
var (
	histogramNames = []string{
		"hub_overhead_ms", "hub_stage_duration_ms", "hub_wait_ms", "vendor_latency_ms",
		"redis_op_duration_seconds", "callback_latency_seconds", "go_sched_latencies_seconds",
	}
	summaryNames = []string{"go_gc_duration_seconds"}
)

// targetLabels are attached by the Prometheus scrape config, not by code (OBSERVABILITY §3).
var targetLabels = []string{"job", "instance", "pod", "namespace", "role"}

type labelSet map[string]bool

func newLabelSet(names ...string) labelSet {
	s := labelSet{}
	for _, n := range names {
		s[n] = true
	}
	return s
}

func (s labelSet) sorted() []string {
	out := make([]string, 0, len(s))
	for k := range s {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// metricCatalog maps every queryable series name (including _bucket/_sum/_count) to its labels.
type metricCatalog map[string]labelSet

// exportedCatalog reads names and label names from the registry's descriptors.
func exportedCatalog(t *testing.T, reg *prometheus.Registry) metricCatalog {
	t.Helper()
	ch := make(chan *prometheus.Desc)
	go func() { reg.Describe(ch); close(ch) }()
	base := metricCatalog{}
	for d := range ch {
		name, labels, err := parseDesc(d.String())
		if err != nil {
			t.Fatal(err)
		}
		base[name] = labels
	}
	cat := metricCatalog{}
	for name, labels := range base {
		cat[name] = labels
	}
	for _, h := range histogramNames {
		labels, ok := base[h]
		if !ok {
			t.Fatalf("histogramNames lists %q, which is not exported", h)
		}
		delete(cat, h) // bare name: native histograms only, which the rules do not rely on
		cat[h+"_bucket"] = union(labels, newLabelSet("le"))
		cat[h+"_sum"], cat[h+"_count"] = labels, labels
	}
	for _, s := range summaryNames {
		labels, ok := base[s]
		if !ok {
			t.Fatalf("summaryNames lists %q, which is not exported", s)
		}
		cat[s] = union(labels, newLabelSet("quantile"))
		cat[s+"_sum"], cat[s+"_count"] = labels, labels
	}
	for name, labels := range cat {
		cat[name] = union(labels, newLabelSet(targetLabels...))
	}
	return cat
}

// parseDesc reads `Desc{fqName: "x", help: "...", unit: "", constLabels: {a="1"}, variableLabels: {b,c}}`.
func parseDesc(s string) (string, labelSet, error) {
	const fq, cl, vl = `fqName: "`, `, constLabels: {`, `, variableLabels: {`
	i, j, k := strings.Index(s, fq), strings.LastIndex(s, cl), strings.LastIndex(s, vl)
	if i < 0 || j < 0 || k < j {
		return "", nil, fmt.Errorf("unexpected descriptor format: %s", s)
	}
	rest := s[i+len(fq):]
	name := rest[:strings.IndexByte(rest, '"')]
	labels := labelSet{}
	for _, pair := range splitNonEmpty(s[j+len(cl):k-1], ",") {
		labels[pair[:strings.IndexByte(pair, '=')]] = true
	}
	for _, l := range splitNonEmpty(strings.TrimSuffix(s[k+len(vl):], "}}"), ",") {
		labels[strings.TrimSuffix(strings.TrimPrefix(l, "c("), ")")] = true
	}
	return name, labels, nil
}

func splitNonEmpty(s, sep string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(s, sep)
}

func union(a, b labelSet) labelSet {
	out := labelSet{}
	for k := range a {
		out[k] = true
	}
	for k := range b {
		out[k] = true
	}
	return out
}

// queryChecker parses PromQL and reports metric and label names the catalogue does not have:
// selector names and matchers, aggregation grouping labels and vector-matching labels. It infers
// the labels each sub-expression can carry so `sum by (x)` is checked against its input.
type queryChecker struct {
	cat  metricCatalog
	errs []string
}

func (q *queryChecker) check(query string) []string {
	q.errs = nil
	e, err := parser.ParseExpr(query)
	if err != nil {
		return []string{fmt.Sprintf("does not parse: %v", err)}
	}
	q.walk(e)
	return q.errs
}

func (q *queryChecker) errorf(format string, args ...any) {
	q.errs = append(q.errs, fmt.Sprintf(format, args...))
}

// walk returns the labels the expression's result may carry; nil for scalars and strings.
func (q *queryChecker) walk(e parser.Expr) labelSet {
	switch n := e.(type) {
	case *parser.VectorSelector:
		return q.selector(n)
	case *parser.MatrixSelector:
		return q.walk(n.VectorSelector)
	case *parser.SubqueryExpr:
		return q.walk(n.Expr)
	case *parser.ParenExpr:
		return q.walk(n.Expr)
	case *parser.UnaryExpr:
		return q.walk(n.Expr)
	case *parser.StepInvariantExpr:
		return q.walk(n.Expr)
	case *parser.NumberLiteral, *parser.StringLiteral:
		return nil
	case *parser.Call:
		return q.call(n)
	case *parser.AggregateExpr:
		return q.aggregate(n)
	case *parser.BinaryExpr:
		return q.binary(n)
	}
	q.errorf("unsupported expression %T", e)
	return nil
}

func (q *queryChecker) selector(n *parser.VectorSelector) labelSet {
	name := n.Name
	for _, m := range n.LabelMatchers {
		if m.Name == "__name__" && name == "" {
			name = m.Value
		}
	}
	labels, ok := q.cat[name]
	if !ok {
		q.errorf("metric %q is not exported", name)
		return newLabelSet(targetLabels...)
	}
	for _, m := range n.LabelMatchers {
		if m.Name != "__name__" && !labels[m.Name] {
			q.errorf("metric %q has no label %q (has %v)", name, m.Name, labels.sorted())
		}
	}
	return labels
}

func (q *queryChecker) call(n *parser.Call) labelSet {
	var out labelSet
	for _, a := range n.Args {
		if l := q.walk(a); l != nil && out == nil {
			out = l
		}
	}
	switch n.Func.Name {
	case "scalar":
		return nil
	case "vector", "time":
		return labelSet{}
	case "histogram_quantile":
		out = union(out, nil)
		delete(out, "le")
	case "label_replace", "label_join":
		if s, ok := n.Args[1].(*parser.StringLiteral); ok {
			out = union(out, newLabelSet(s.Val))
		}
	}
	return out
}

func (q *queryChecker) aggregate(n *parser.AggregateExpr) labelSet {
	in := q.walk(n.Expr)
	if n.Param != nil {
		q.walk(n.Param)
	}
	if n.Without {
		out := union(in, nil)
		for _, l := range n.Grouping {
			delete(out, l)
		}
		return out
	}
	for _, l := range n.Grouping {
		if !in[l] {
			q.errorf("%s by (%s): label %q is not on its input (has %v)", n.Op, strings.Join(n.Grouping, ", "), l, in.sorted())
		}
	}
	switch n.Op {
	case parser.TOPK, parser.BOTTOMK, parser.LIMITK, parser.LIMIT_RATIO:
		return in
	}
	return newLabelSet(n.Grouping...)
}

func (q *queryChecker) binary(n *parser.BinaryExpr) labelSet {
	l, r := q.walk(n.LHS), q.walk(n.RHS)
	if l == nil {
		return r
	}
	if r == nil {
		return l
	}
	vm := n.VectorMatching
	if vm == nil {
		return l
	}
	if vm.On {
		for _, m := range vm.MatchingLabels {
			if !l[m] || !r[m] {
				q.errorf("%s on (%s): label %q is not on both sides", n.Op, strings.Join(vm.MatchingLabels, ", "), m)
			}
		}
	}
	switch n.Op {
	case parser.LOR:
		return union(l, r)
	case parser.LAND, parser.LUNLESS:
		return l
	}
	switch vm.Card {
	case parser.CardManyToOne:
		return union(l, newLabelSet(vm.Include...))
	case parser.CardOneToMany:
		return union(r, newLabelSet(vm.Include...))
	case parser.CardOneToOne, parser.CardManyToMany: // many-to-many is set operators, returned above
	}
	if vm.On {
		return newLabelSet(vm.MatchingLabels...)
	}
	out := union(l, nil)
	for _, m := range vm.MatchingLabels {
		delete(out, m)
	}
	return out
}

func deployDir(t *testing.T, sub string) string {
	t.Helper()
	dir, err := filepath.Abs(".")
	if err != nil {
		t.Fatal(err)
	}
	for {
		p := filepath.Join(dir, "deploy", sub)
		if st, err := os.Stat(p); err == nil && st.IsDir() {
			return p
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("deploy/%s not found above the test directory", sub)
		}
		dir = parent
	}
}

func globFiles(t *testing.T, dir, pattern string) []string {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(dir, pattern))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatalf("no %s in %s", pattern, dir)
	}
	return files
}

type ruleFile struct {
	Groups []struct {
		Name  string `yaml:"name"`
		Rules []struct {
			Alert       string            `yaml:"alert"`
			Record      string            `yaml:"record"`
			Expr        string            `yaml:"expr"`
			For         string            `yaml:"for"`
			Labels      map[string]string `yaml:"labels"`
			Annotations map[string]string `yaml:"annotations"`
		} `yaml:"rules"`
	} `yaml:"groups"`
}

func readYAML(t *testing.T, path string, v any) {
	t.Helper()
	b, err := os.ReadFile(path) //nolint:gosec // repository file chosen by the test
	if err != nil {
		t.Fatal(err)
	}
	if err := yaml.Unmarshal(b, v); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
}

// TestAlertRulesUseExportedMetrics checks every rule expression against the catalogue.
func TestAlertRulesUseExportedMetrics(t *testing.T) {
	q := &queryChecker{cat: exportedCatalog(t, catalogRegistry(t))}
	for _, path := range globFiles(t, deployDir(t, "alerts"), "*.rules.yaml") {
		var rf ruleFile
		readYAML(t, path, &rf)
		for _, g := range rf.Groups {
			for _, r := range g.Rules {
				for _, e := range q.check(r.Expr) {
					t.Errorf("%s: %s%s: %s", filepath.Base(path), r.Alert, r.Record, e)
				}
			}
		}
	}
}

// TestAlertRulesCoverDesign: each DESIGN §16.3 alert has one rule with a severity, a summary, a
// description and its runbook link, and promtool tests that see it both fire and stay quiet.
func TestAlertRulesCoverDesign(t *testing.T) {
	dir := deployDir(t, "alerts")
	seen := map[string]int{}
	for _, path := range globFiles(t, dir, "*.rules.yaml") {
		var rf ruleFile
		readYAML(t, path, &rf)
		for _, g := range rf.Groups {
			for _, r := range g.Rules {
				if r.Alert == "" {
					continue
				}
				seen[r.Alert]++
				if s := r.Labels["severity"]; s != "page" && s != "warn" {
					t.Errorf("%s: severity %q, want page or warn", r.Alert, s)
				}
				for _, a := range []string{"summary", "description"} {
					if r.Annotations[a] == "" {
						t.Errorf("%s: no %s annotation", r.Alert, a)
					}
				}
				if got, want := r.Annotations["runbook_url"], runbookBase+r.Alert+".md"; got != want {
					t.Errorf("%s: runbook_url %q, want %q", r.Alert, got, want)
				}
			}
		}
	}
	for _, a := range designAlerts {
		if seen[a] != 1 {
			t.Errorf("DESIGN §16.3 alert %s has %d rules, want 1", a, seen[a])
		}
	}

	type unitTests struct {
		RuleFiles []string `yaml:"rule_files"`
		Tests     []struct {
			AlertRuleTest []struct {
				Alertname string `yaml:"alertname"`
				ExpAlerts []any  `yaml:"exp_alerts"`
			} `yaml:"alert_rule_test"`
		} `yaml:"tests"`
	}
	firing, quiet := map[string]bool{}, map[string]bool{}
	for _, path := range globFiles(t, dir, "*_test.yml") {
		var ut unitTests
		readYAML(t, path, &ut)
		for _, rf := range ut.RuleFiles {
			if _, err := os.Stat(filepath.Join(dir, rf)); err != nil {
				t.Errorf("%s: rule file %s: %v", filepath.Base(path), rf, err)
			}
		}
		for _, tc := range ut.Tests {
			for _, a := range tc.AlertRuleTest {
				if len(a.ExpAlerts) > 0 {
					firing[a.Alertname] = true
				} else {
					quiet[a.Alertname] = true
				}
			}
		}
	}
	for a := range seen {
		if !firing[a] || !quiet[a] {
			t.Errorf("%s: promtool tests need a firing and a non-firing case (firing %t, quiet %t)", a, firing[a], quiet[a])
		}
	}
}

var (
	builtinVar = regexp.MustCompile(`\$\{?(__rate_interval|__interval|__range)\}?`)
	userVar    = regexp.MustCompile(`\$\{([A-Za-z_]\w*)(?::\w+)?\}|\$([A-Za-z_]\w*)`)
	labelVals  = regexp.MustCompile(`^label_values\((.+),\s*([A-Za-z_]\w*)\)$`)
)

// expandVars replaces Grafana variables with values PromQL can parse and returns the user
// variables it found. Variables are only allowed inside label-matcher strings.
func expandVars(expr string) (string, []string) {
	expr = builtinVar.ReplaceAllString(expr, "5m")
	var vars []string
	for _, m := range userVar.FindAllStringSubmatch(expr, -1) {
		vars = append(vars, m[1]+m[2])
	}
	return userVar.ReplaceAllString(expr, "x"), vars
}

type dashboardPanel struct {
	ID         *int             `json:"id"`
	Type       string           `json:"type"`
	Title      string           `json:"title"`
	GridPos    map[string]int   `json:"gridPos"`
	Datasource *datasourceRef   `json:"datasource"`
	Targets    []dashTarget     `json:"targets"`
	Panels     []dashboardPanel `json:"panels"`
}

type datasourceRef struct {
	Type string `json:"type"`
	UID  string `json:"uid"`
}

type dashTarget struct {
	RefID      string         `json:"refId"`
	Expr       string         `json:"expr"`
	Datasource *datasourceRef `json:"datasource"`
}

type dashboard struct {
	ID            any              `json:"id"`
	UID           string           `json:"uid"`
	Title         string           `json:"title"`
	Tags          []string         `json:"tags"`
	SchemaVersion int              `json:"schemaVersion"`
	Panels        []dashboardPanel `json:"panels"`
	Templating    struct {
		List []struct {
			Name       string          `json:"name"`
			Type       string          `json:"type"`
			Query      json.RawMessage `json:"query"`
			Datasource *datasourceRef  `json:"datasource"`
		} `json:"list"`
	} `json:"templating"`
}

const dsVar = "${datasource}"

// TestDashboardsAreValidAndUseExportedMetrics lints each dashboard (strict JSON, stable uid,
// unique panel ids, datasource as a variable, every variable defined) and checks every PromQL
// target and label_values() variable query against the catalogue. Grafana itself is not run.
func TestDashboardsAreValidAndUseExportedMetrics(t *testing.T) {
	q := &queryChecker{cat: exportedCatalog(t, catalogRegistry(t))}
	uids := map[string]string{}
	for _, path := range globFiles(t, deployDir(t, "dashboards"), "*.json") {
		file := filepath.Base(path)
		t.Run(file, func(t *testing.T) {
			b, err := os.ReadFile(path) //nolint:gosec // repository file chosen by the test
			if err != nil {
				t.Fatal(err)
			}
			var d dashboard
			if err := json.Unmarshal(b, &d); err != nil {
				t.Fatalf("not valid JSON: %v", err)
			}
			if d.ID != nil {
				t.Errorf("id = %v, want null (an exported dashboard; Grafana assigns ids on import)", d.ID)
			}
			if !strings.HasPrefix(d.UID, "hub-") || len(d.UID) > 40 {
				t.Errorf("uid %q: want a stable hub-* uid of at most 40 characters", d.UID)
			}
			if other, dup := uids[d.UID]; dup {
				t.Errorf("uid %q also used by %s", d.UID, other)
			}
			uids[d.UID] = file
			if d.Title == "" || d.SchemaVersion < 36 || !slices.Contains(d.Tags, "integration-hub") {
				t.Errorf("want a title, schemaVersion >= 36 and tag integration-hub; got %q, %d, %v", d.Title, d.SchemaVersion, d.Tags)
			}

			defined := map[string]bool{}
			hasDS := false
			for _, v := range d.Templating.List {
				defined[v.Name] = true
				switch v.Type {
				case "datasource":
					hasDS = v.Name == "datasource" && string(v.Query) == `"prometheus"`
				case "query":
					if v.Datasource == nil || v.Datasource.UID != dsVar {
						t.Errorf("variable %s: datasource must be %s", v.Name, dsVar)
					}
					var query struct {
						Query string `json:"query"`
					}
					if err := json.Unmarshal(v.Query, &query); err != nil {
						t.Errorf("variable %s: query: %v", v.Name, err)
						continue
					}
					m := labelVals.FindStringSubmatch(query.Query)
					if m == nil {
						t.Errorf("variable %s: query %q, want label_values(<selector>, <label>)", v.Name, query.Query)
						continue
					}
					sel, _ := expandVars(m[1])
					for _, e := range q.check(fmt.Sprintf("sum by (%s) (%s)", m[2], sel)) {
						t.Errorf("variable %s: %s", v.Name, e)
					}
				}
			}
			if !hasDS {
				t.Error(`want a "datasource" variable of type datasource for prometheus (no hard-coded datasource uids)`)
			}

			ids := map[int]bool{}
			var walk func(ps []dashboardPanel)
			walk = func(ps []dashboardPanel) {
				for _, p := range ps {
					where := fmt.Sprintf("panel %q", p.Title)
					if p.ID == nil || ids[*p.ID] {
						t.Errorf("%s: missing or duplicate id", where)
					} else {
						ids[*p.ID] = true
					}
					if p.GridPos["w"] == 0 || p.GridPos["h"] == 0 {
						t.Errorf("%s: no gridPos", where)
					}
					walk(p.Panels)
					if p.Type == "row" || p.Type == "text" {
						continue
					}
					if p.Datasource == nil || p.Datasource.UID != dsVar {
						t.Errorf("%s: datasource must be %s", where, dsVar)
					}
					if len(p.Targets) == 0 {
						t.Errorf("%s: no targets", where)
					}
					refs := map[string]bool{}
					for _, tg := range p.Targets {
						if tg.RefID == "" || refs[tg.RefID] {
							t.Errorf("%s: missing or duplicate refId %q", where, tg.RefID)
						}
						refs[tg.RefID] = true
						if tg.Datasource != nil && tg.Datasource.UID != dsVar {
							t.Errorf("%s/%s: datasource must be %s", where, tg.RefID, dsVar)
						}
						expr, vars := expandVars(tg.Expr)
						for _, v := range vars {
							if !defined[v] {
								t.Errorf("%s/%s: variable $%s is not defined", where, tg.RefID, v)
							}
						}
						for _, e := range q.check(expr) {
							t.Errorf("%s/%s: %s", where, tg.RefID, e)
						}
					}
				}
			}
			walk(d.Panels)
			if len(ids) == 0 {
				t.Error("no panels")
			}
		})
	}
}

// TestQueryCheckerFindsUnknownNames proves the checker catches what it exists to catch.
func TestQueryCheckerFindsUnknownNames(t *testing.T) {
	q := &queryChecker{cat: exportedCatalog(t, catalogRegistry(t))}
	tests := []struct {
		name, query, want string
	}{
		{"known names", `sum by (account) (increase(hub_requests_total{code="ok"}[5m]))`, ""},
		{"histogram bucket and quantile", `histogram_quantile(0.99, sum by (le, integration) (rate(hub_overhead_ms_bucket[5m])))`, ""},
		{"scrape labels", `snapshot_version{instance="gw-1"} and on (instance) scheduler_leader == 1`, ""},
		{"label_values form", `sum by (tenant) (hub_requests_total{tenant=~"x"})`, ""},
		{"unknown metric", `rate(dlq_entries_total[10m])`, `metric "dlq_entries_total" is not exported`},
		{"unknown matcher label", `ratelimit_rejections_total{tenant="t1"}`, `has no label "tenant"`},
		{"unknown grouping label", `max by (vendor) (parked_oldest_age_s)`, `label "vendor" is not on its input`},
		{"grouping after aggregation", `sum by (account) (sum by (vendor) (breaker_state))`, `label "account" is not on its input`},
		{"bare classic histogram", `hub_overhead_ms`, `metric "hub_overhead_ms" is not exported`},
		{"bucket of a counter", `hub_requests_total_bucket`, `is not exported`},
		{"le after histogram_quantile", `sum by (le) (histogram_quantile(0.9, rate(hub_wait_ms_bucket[5m])))`, `label "le" is not on its input`},
		{"on label missing", `breaker_state and on (tenant) dlq_count`, `label "tenant" is not on both sides`},
		{"parse error", `sum by (account (x)`, "does not parse"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			errs := strings.Join(q.check(tc.query), "; ")
			if tc.want == "" && errs != "" {
				t.Fatalf("unexpected: %s", errs)
			}
			if !strings.Contains(errs, tc.want) {
				t.Fatalf("errors %q, want %q", errs, tc.want)
			}
		})
	}
}

func TestExpandVars(t *testing.T) {
	got, vars := expandVars(`sum(rate(x{tenant=~"$tenant", integration=~"${integration:regex}"}[$__rate_interval])) / increase(y[${__range}])`)
	if want := `sum(rate(x{tenant=~"x", integration=~"x"}[5m])) / increase(y[5m])`; got != want {
		t.Fatalf("expanded %q, want %q", got, want)
	}
	if !slices.Equal(vars, []string{"tenant", "integration"}) {
		t.Fatalf("vars %v", vars)
	}
}
