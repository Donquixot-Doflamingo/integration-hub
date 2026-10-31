package transform

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

// fixedNow is the clock pinned in stdlib tests and goldens.
var fixedNow = time.Date(2026, 10, 31, 9, 15, 0, 0, time.UTC)

// run compiles src as a single request-body expression and evaluates it with in.
func run(t *testing.T, src string, in Input) (any, error) {
	t.Helper()
	yamlSrc := reqHead + "  body:\n    x: '" + strings.ReplaceAll(src, "'", "''") + "'\n"
	p, err := Compile(specFromYAML(t, yamlSrc), Options{})
	if err != nil {
		return nil, err
	}
	if in.Now.IsZero() {
		in.Now = fixedNow
	}
	out, err := p.eval(p.body, p.env(in, nil))
	if err != nil {
		return nil, err
	}
	return out.(map[string]any)["x"], nil
}

type fnCase struct {
	src     string
	want    any
	wantErr string
}

func runCases(t *testing.T, cases []fnCase) {
	t.Helper()
	for _, c := range cases {
		t.Run(c.src, func(t *testing.T) {
			got, err := run(t, c.src, Input{Req: map[string]any{"s": "Ünïcødé ✓", "n": 3.0}})
			if c.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), c.wantErr) {
					t.Fatalf("err %v, want %q", err, c.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !equalJSON(got, c.want) {
				t.Fatalf("got %#v, want %#v", got, c.want)
			}
		})
	}
}

// equalJSON compares values after normalising numbers, so int/int64/float64 of equal value match.
func equalJSON(a, b any) bool {
	return normalize(a) == normalize(b)
}

func normalize(v any) string {
	switch x := v.(type) {
	case int:
		return normalize(float64(x))
	case int64:
		return normalize(float64(x))
	case float64:
		return "n:" + strconv.FormatFloat(x, 'g', -1, 64)
	case string:
		return "s:" + x
	case bool:
		return "b:" + strconv.FormatBool(x)
	case nil:
		return "null"
	case []string:
		parts := make([]string, len(x))
		for i, e := range x {
			parts[i] = normalize(e)
		}
		return "[" + strings.Join(parts, ",") + "]"
	case []any:
		parts := make([]string, len(x))
		for i, e := range x {
			parts[i] = normalize(e)
		}
		return "[" + strings.Join(parts, ",") + "]"
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		parts := make([]string, len(keys))
		for i, k := range keys {
			parts[i] = k + "=" + normalize(x[k])
		}
		return "{" + strings.Join(parts, ",") + "}"
	}
	return fmt.Sprintf("%T:%v", v, v)
}
