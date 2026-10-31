// Command hubctl is the config-as-code and operations CLI (DESIGN §16.1). M2 ships
// `hubctl transform test`; plan, apply, dry-run and replay arrive with the control plane (M7).
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/registry"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/transform"
)

const usage = `usage: hubctl <command> [flags]

commands:
  transform test [-config-dir DIR] [-contracts-dir DIR] [-v] [TRANSFORMER...]
      Run the golden fixtures of every transformer (or only the given IDs, e.g. T1 or T1@7).
      Exit 0 when all pass, 1 when any fails, 2 on usage or config errors.`

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) >= 1 && args[0] == "creds" {
		return credsCmd(args[1:], stdout, stderr)
	}
	if len(args) >= 2 && args[0] == "transform" && args[1] == "test" {
		return transformTest(args[2:], stdout, stderr)
	}
	fmt.Fprintln(stderr, usage)
	return 2
}

func transformTest(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("hubctl transform test", flag.ContinueOnError)
	fs.SetOutput(stderr)
	configDir := fs.String("config-dir", "config", "registry config directory")
	contractsDir := fs.String("contracts-dir", "../contracts", "contracts directory (capability schemas)")
	verbose := fs.Bool("v", false, "print passing cases too")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	src := registry.Sources{Config: os.DirFS(*configDir), Contracts: os.DirFS(*contractsDir)}
	cfg, err := registry.Load(src)
	if err != nil {
		fmt.Fprintf(stderr, "hubctl: config does not load:\n%v\n", err)
		return 2
	}
	refs := selectTransformers(cfg, fs.Args())
	if len(refs) == 0 {
		fmt.Fprintln(stderr, "hubctl: no matching transformers")
		return 2
	}
	passed, failed := 0, 0
	for _, ref := range refs {
		prog, err := cfg.CompileTransformer(ref)
		if err != nil {
			fmt.Fprintf(stdout, "FAIL %s (compile)\n    %v\n", ref, err)
			failed++
			continue
		}
		fixtures, err := registry.TransformerFixtures(src.Config, ref)
		if err != nil {
			fmt.Fprintf(stdout, "FAIL %s\n    %v\n", ref, err)
			failed++
			continue
		}
		for _, r := range transform.RunGoldens(prog, fixtures) {
			if r.Passed() {
				passed++
				if *verbose {
					fmt.Fprintf(stdout, "PASS %s %s\n", r.Transformer, r.Case)
				}
				continue
			}
			failed++
			fmt.Fprintf(stdout, "FAIL %s %s\n", r.Transformer, r.Case)
			if r.Err != "" {
				fmt.Fprintf(stdout, "    %s\n", r.Err)
			}
			for _, d := range r.Diffs {
				fmt.Fprintf(stdout, "    %s\n", d)
			}
		}
	}
	fmt.Fprintf(stdout, "%d passed, %d failed (%d transformers)\n", passed, failed, len(refs))
	if failed > 0 {
		return 1
	}
	return 0
}

// selectTransformers returns refs matching "T1" or "T1@7" filters (all when none), sorted.
func selectTransformers(cfg *registry.Config, filters []string) []registry.TransformerRef {
	var refs []registry.TransformerRef
	for ref := range cfg.Transformers {
		if len(filters) == 0 || slices.ContainsFunc(filters, func(f string) bool { return f == ref.ID || f == ref.String() }) {
			refs = append(refs, ref)
		}
	}
	slices.SortFunc(refs, func(a, b registry.TransformerRef) int {
		if c := strings.Compare(a.ID, b.ID); c != 0 {
			return c
		}
		return a.Version - b.Version
	})
	return refs
}
