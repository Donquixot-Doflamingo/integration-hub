package transform

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestOverDeepExpressionRejectedAtCompile(t *testing.T) {
	deep := strings.Repeat("merge({}, ", 25) + "{}" + strings.Repeat(")", 25)
	_, err := compileSrc(t, reqHead+"  body:\n    x: '"+deep+"'\n")
	if err == nil || !strings.Contains(err.Error(), "expression is nested") {
		t.Fatalf("err %v", err)
	}
	_, err = compileSrc(t, reqHead+"  body:\n    x: "+strings.Repeat("1 + ", 600)+"1\n")
	if err == nil {
		t.Fatal("expression over MaxNodes compiled")
	}
}

func TestOutputLargerThanLimitFails(t *testing.T) {
	p, err := Compile(specFromYAML(t, reqHead+"  body:\n    blob: req.event.properties.text\n"), Options{RequestSchema: eventsSchema(t), Limits: Limits{
		MaxNodes: 500, MaxDepth: 24, MemoryBudget: 200_000, MaxOutputBytes: 1024, TimeBudget: time.Second}})
	if err != nil {
		t.Fatal(err)
	}
	in := Input{Req: map[string]any{"event": map[string]any{"properties": map[string]any{"text": strings.Repeat("x", 2000)}}}}
	_, err = p.BuildRequest(in)
	var ee *EvalError
	if !errors.As(err, &ee) || ee.Path != "request.body" || !strings.Contains(err.Error(), "larger than 1024 bytes") {
		t.Fatalf("err %v", err)
	}
	small := Input{Req: map[string]any{"event": map[string]any{"properties": map[string]any{"text": "ok"}}}}
	if _, err := p.BuildRequest(small); err != nil {
		t.Fatalf("small body rejected: %v", err)
	}
}

// TestPathologicalMapAbortsWithinBudget: a loop far beyond the budget is cut short by the VM
// memory budget, well inside the time budget + 1 ms (HUB-066).
func TestPathologicalMapAbortsWithinBudget(t *testing.T) {
	for _, src := range []string{
		`map(1..10000000, # * 2)`,
		`map(1..3000, map(1..3000, # + 1))`,
		`filter(1..10000000, # % 2 == 0)`,
	} {
		t.Run(src, func(t *testing.T) {
			p, err := compileSrc(t, reqHead+"  body:\n    x: '"+src+"'\n")
			if err != nil {
				t.Fatal(err)
			}
			start := time.Now()
			_, err = p.BuildRequest(Input{})
			took := time.Since(start)
			var ee *EvalError
			if !errors.As(err, &ee) {
				t.Fatalf("pathological expression completed: %v", err)
			}
			if !strings.Contains(ee.Detail(), "memory budget") {
				t.Fatalf("aborted for another reason: %s", ee.Detail())
			}
			if raceEnabled {
				return // the race detector slows the VM ~10x; `make test` also runs this test without -race
			}
			if budget := DefaultLimits().TimeBudget + time.Millisecond; took > budget {
				t.Fatalf("took %s, want <= %s", took, budget)
			}
		})
	}
}

func TestTimeBudgetIsChecked(t *testing.T) {
	p, err := Compile(specFromYAML(t, reqHead+"  body:\n    x: 'len(map(1..5000, # * 2))'\n"), Options{Limits: Limits{
		MaxNodes: 500, MaxDepth: 24, MemoryBudget: 1_000_000, MaxOutputBytes: 1 << 20, TimeBudget: time.Nanosecond}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.BuildRequest(Input{}); err == nil || !strings.Contains(err.Error(), "budget 1ns") {
		t.Fatalf("err %v", err)
	}
}

func TestEstimateSize(t *testing.T) {
	n, over := estimateSize(map[string]any{"a": []any{"xy", 1.0, nil}}, 100)
	if over || n < 10 || n > 40 {
		t.Fatalf("estimate %d over %v", n, over)
	}
	if _, over := estimateSize(strings.Repeat("x", 200), 100); !over {
		t.Fatal("over limit not detected")
	}
}
