// Package archtest enforces the package dependency rules of docs/tech/TECH_SPEC.md §4.1.
// A violation fails `make test`. Change the table only together with TECH_SPEC and an ADR.
package archtest

import (
	"encoding/json"
	"errors"
	"io"
	"os/exec"
	"slices"
	"strings"
	"testing"
)

const module = "github.com/Donquixot-Doflamingo/integration-hub/backend"

// allowed lists, per internal package, the other internal packages it may import.
// core and telemetry are leaves; every other package may also import them.
var allowed = map[string][]string{
	"core":      {},
	"telemetry": {},
	"transform": {},
	"registry":  {"transform"},
	"resolve":   {"registry"},
	"creds":     {"registry"},
	"queue":     {},
	"sessions":  {},
	"audit":     {"registry"},
	"upstream":  {"registry", "creds"},
	"callbacks": {"registry", "audit"},
	"delivery":  {"registry", "resolve", "transform", "creds", "upstream", "queue", "sessions", "audit", "callbacks"},
	"edge":      {"registry", "resolve", "transform", "creds", "upstream", "queue", "sessions", "audit", "delivery"},
	"webhooks":  {"registry", "transform", "audit", "callbacks"},
	"control":   {"registry", "transform", "resolve", "creds", "upstream", "queue", "audit", "sessions"},
	// testkit holds test-only helpers (in-process mockvendor, fixtures). Production code never imports it.
	"testkit": {},
}

var leaves = []string{"core", "telemetry"}

type listedPackage struct {
	ImportPath string
	Imports    []string
}

func listPackages(t *testing.T) []listedPackage {
	t.Helper()
	out, err := exec.CommandContext(t.Context(), "go", "list", "-json", module+"/...").Output()
	if err != nil {
		t.Fatalf("go list: %v", err)
	}
	var pkgs []listedPackage
	dec := json.NewDecoder(strings.NewReader(string(out)))
	for {
		var p listedPackage
		if err := dec.Decode(&p); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			t.Fatalf("decode go list output: %v", err)
		}
		pkgs = append(pkgs, p)
	}
	return pkgs
}

// topInternal returns the top-level internal package name of path ("upstream" for
// .../internal/upstream/breaker), or "" when path is not under internal/.
func topInternal(path string) string {
	rest, ok := strings.CutPrefix(path, module+"/internal/")
	if !ok {
		return ""
	}
	name, _, _ := strings.Cut(rest, "/")
	return name
}

func TestInternalImportsFollowLayering(t *testing.T) {
	for _, p := range listPackages(t) {
		from := topInternal(p.ImportPath)
		if from == "" || from == "archtest" {
			continue
		}
		permitted, known := allowed[from]
		if !known {
			t.Errorf("%s: package internal/%s is not in the layering table (TECH_SPEC §4.1); add it with an ADR", p.ImportPath, from)
			continue
		}
		for _, imp := range p.Imports {
			to := topInternal(imp)
			if to == "" || to == from {
				continue
			}
			if slices.Contains(leaves, to) && !slices.Contains(leaves, from) {
				continue
			}
			if !slices.Contains(permitted, to) {
				t.Errorf("%s imports internal/%s, which TECH_SPEC §4.1 does not allow", p.ImportPath, to)
			}
		}
	}
}

func TestPublicAndIsolatedPackagesAvoidInternal(t *testing.T) {
	// pkg/hubclient is a public SDK; mockvendor must behave like an outside vendor;
	// hubmcp talks to the Hub only through the contracted HTTP API.
	isolated := []string{module + "/pkg/", module + "/cmd/mockvendor", module + "/cmd/hubmcp"}
	for _, p := range listPackages(t) {
		if !slices.ContainsFunc(isolated, func(prefix string) bool { return strings.HasPrefix(p.ImportPath, prefix) }) {
			continue
		}
		for _, imp := range p.Imports {
			if strings.HasPrefix(p.ImportPath, module+"/cmd/mockvendor") && topInternal(imp) == "testkit" {
				continue // mockvendor's server code lives in internal/testkit/mockvendor
			}
			if strings.HasPrefix(imp, module+"/internal/") {
				t.Errorf("%s must not import %s", p.ImportPath, imp)
			}
		}
	}
}

func TestProductionCodeDoesNotImportTestkit(t *testing.T) {
	// go list reports non-test imports only, so any hit here is production code.
	for _, p := range listPackages(t) {
		if strings.HasPrefix(p.ImportPath, module+"/cmd/mockvendor") || topInternal(p.ImportPath) == "testkit" {
			continue
		}
		for _, imp := range p.Imports {
			if topInternal(imp) == "testkit" {
				t.Errorf("%s imports %s; testkit is for tests only", p.ImportPath, imp)
			}
		}
	}
}
