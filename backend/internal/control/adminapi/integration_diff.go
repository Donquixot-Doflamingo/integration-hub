package adminapi

import (
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"
)

// diffOp is one contract DiffOp: an RFC 6902-style operation on the spec.
type diffOp struct {
	Op   string `json:"op"`
	Path string `json:"path"`
	// Before and After are omitted when absent: the contract's untyped schema refuses null.
	Before any `json:"before,omitempty"`
	After  any `json:"after,omitempty"`
}

// escapePointer escapes a JSON pointer token (RFC 6901).
func escapePointer(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, "~", "~0"), "/", "~1")
}

// diffJSON lists the operations turning a into b (decoded JSON values). Objects are compared
// member by member, arrays element by element (removals of trailing elements come last, highest
// index first, so the operations apply in order).
func diffJSON(path string, a, b any) []diffOp {
	switch av := a.(type) {
	case map[string]any:
		bv, ok := b.(map[string]any)
		if !ok {
			break
		}
		var out []diffOp
		keys := slices.Sorted(maps.Keys(av))
		for k := range bv {
			if _, ok := av[k]; !ok {
				keys = append(keys, k)
			}
		}
		slices.Sort(keys)
		for _, k := range keys {
			p := path + "/" + escapePointer(k)
			x, inA := av[k]
			y, inB := bv[k]
			switch {
			case !inA:
				out = append(out, diffOp{Op: "add", Path: p, After: y})
			case !inB:
				out = append(out, diffOp{Op: "remove", Path: p, Before: x})
			default:
				out = append(out, diffJSON(p, x, y)...)
			}
		}
		return out
	case []any:
		bv, ok := b.([]any)
		if !ok {
			break
		}
		var out []diffOp
		for i := 0; i < min(len(av), len(bv)); i++ {
			out = append(out, diffJSON(path+"/"+strconv.Itoa(i), av[i], bv[i])...)
		}
		for i := len(av); i < len(bv); i++ {
			out = append(out, diffOp{Op: "add", Path: path + "/" + strconv.Itoa(i), After: bv[i]})
		}
		for i := len(av) - 1; i >= len(bv); i-- {
			out = append(out, diffOp{Op: "remove", Path: path + "/" + strconv.Itoa(i), Before: av[i]})
		}
		return out
	}
	if reflect.DeepEqual(a, b) {
		return nil
	}
	return []diffOp{{Op: "replace", Path: path, Before: a, After: b}}
}

// yamlLines renders v as YAML (keys sorted) split into lines.
func yamlLines(v any) []string {
	if v == nil {
		return nil
	}
	b, err := yaml.Marshal(v)
	if err != nil {
		return nil
	}
	return strings.Split(strings.TrimSuffix(string(b), "\n"), "\n")
}

// unifiedDiff is a unified diff (3 lines of context) of two line slices.
func unifiedDiff(fromName, toName string, a, b []string) string {
	// LCS table: specs are small, O(n·m) is fine.
	n, m := len(a), len(b)
	lcs := make([][]int, n+1)
	for i := range lcs {
		lcs[i] = make([]int, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if a[i] == b[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else {
				lcs[i][j] = max(lcs[i+1][j], lcs[i][j+1])
			}
		}
	}
	type line struct {
		kind byte // ' ', '-', '+'
		text string
		ai   int // line number in a (1-based) of ' ' and '-' lines
		bi   int
	}
	var script []line
	i, j := 0, 0
	for i < n || j < m {
		switch {
		case i < n && j < m && a[i] == b[j]:
			script = append(script, line{' ', a[i], i + 1, j + 1})
			i, j = i+1, j+1
		case i < n && (j == m || lcs[i+1][j] >= lcs[i][j+1]):
			script = append(script, line{'-', a[i], i + 1, j + 1})
			i++
		default:
			script = append(script, line{'+', b[j], i + 1, j + 1})
			j++
		}
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "--- %s\n+++ %s\n", fromName, toName)
	const ctx = 3
	var changed []int
	for k, l := range script {
		if l.kind != ' ' {
			changed = append(changed, k)
		}
	}
	for c := 0; c < len(changed); {
		last := c
		for last+1 < len(changed) && changed[last+1]-changed[last]-1 <= 2*ctx {
			last++
		}
		from, to := max(0, changed[c]-ctx), min(len(script), changed[last]+ctx+1)
		aStart, bStart, aLen, bLen := script[from].ai, script[from].bi, 0, 0
		for _, l := range script[from:to] {
			if l.kind != '+' {
				aLen++
			}
			if l.kind != '-' {
				bLen++
			}
		}
		if aLen == 0 {
			aStart--
		}
		if bLen == 0 {
			bStart--
		}
		fmt.Fprintf(&sb, "@@ -%d,%d +%d,%d @@\n", aStart, aLen, bStart, bLen)
		for _, l := range script[from:to] {
			sb.WriteByte(l.kind)
			sb.WriteString(l.text)
			sb.WriteByte('\n')
		}
		c = last + 1
	}
	return sb.String()
}
