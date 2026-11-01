package transform

import (
	"errors"
	"fmt"
	"time"
)

// ErrTimeBudget marks a run that took longer than TimeBudget. The budget is wall-clock time, so
// a GC pause or CPU starvation can exceed it: callers re-run once and treat a repeat as
// retryable (a transformer that is really too slow keeps failing and is dead-lettered).
var ErrTimeBudget = errors.New("transform time budget exceeded")

// checkOutput enforces MaxOutputBytes on a built body or mapped response (DESIGN §7.1).
func (p *Program) checkOutput(path string, v any) error {
	if n, over := estimateSize(v, p.limits.MaxOutputBytes); over {
		return &EvalError{Transformer: p.ref, Path: path, Err: fmt.Errorf("output is larger than %d bytes (at least %d)", p.limits.MaxOutputBytes, n)}
	}
	return nil
}

// checkTime enforces TimeBudget after a run. expr-lang cannot pre-empt an expression, so long
// loops are cut short by the VM memory budget and node limits; this check catches the rest.
func (p *Program) checkTime(path string, start time.Time) error {
	if d := time.Since(start); d > p.limits.TimeBudget {
		return &EvalError{Transformer: p.ref, Path: path, Err: fmt.Errorf("%w: took %s, budget %s", ErrTimeBudget, d.Round(time.Microsecond), p.limits.TimeBudget)}
	}
	return nil
}

// estimateSize approximates the JSON size of v without encoding it, stopping once it passes limit.
func estimateSize(v any, limit int) (int, bool) {
	n := 0
	var walk func(any) bool
	walk = func(v any) bool {
		switch x := v.(type) {
		case string:
			n += len(x) + 2
		case map[string]any:
			n += 2
			for k, e := range x {
				n += len(k) + 4
				if !walk(e) {
					return false
				}
			}
		case []any:
			n += 2
			for _, e := range x {
				n++
				if !walk(e) {
					return false
				}
			}
		case []string:
			for _, e := range x {
				n += len(e) + 3
			}
		default:
			n += 8 // numbers, bools, null
		}
		return n <= limit
	}
	walk(v)
	return n, n > limit
}
