package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"
)

// Config as code (DESIGN §16.1): export writes the live registry tree (no secrets: credentials
// are references, values live in the credential store) to a directory; plan diffs a directory
// against live, per object and per path; apply publishes the difference.

const configUsage = `config as code:
  export -out DIR [-force]
      Write the live registry tree (the latest published snapshot) to DIR. No secret is ever
      written: the tree holds credential references only.
  plan [-config-dir DIR] [-contracts-dir DIR]
      Diff DIR (default config) against live, per object with path-level changes. Objects
      are compared as loaded (formatting, comments, defaults and publish metadata are not
      changes); admin/ files are written by the control plane and skipped.
      Exit 0 with no changes, 3 when there are changes, 1 on errors.
` + applyUsage

// liveTree downloads the latest published snapshot's config tree.
func (c *adminClient) liveTree(ctx context.Context) (map[string][]byte, int64, error) {
	var snap struct {
		Version int64  `json:"version"`
		BlobURL string `json:"blob_url"`
	}
	if err := c.do(ctx, http.MethodGet, "/snapshots/latest?wait=0", nil, &snap); err != nil {
		return nil, 0, err
	}
	raw, err := c.raw(ctx, "/snapshots/"+strconv.FormatInt(snap.Version, 10)+"/blob")
	if err != nil {
		return nil, 0, err
	}
	if len(raw) > 2 && raw[0] == 0x1f && raw[1] == 0x8b { // still gzip (no transparent decoding)
		zr, err := gzip.NewReader(bytes.NewReader(raw))
		if err != nil {
			return nil, 0, fmt.Errorf("snapshot %d blob: %w", snap.Version, err)
		}
		if raw, err = io.ReadAll(io.LimitReader(zr, 256<<20)); err != nil {
			return nil, 0, fmt.Errorf("snapshot %d blob: %w", snap.Version, err)
		}
	}
	var blob struct {
		Config map[string]string `json:"config"`
	}
	if err := json.Unmarshal(raw, &blob); err != nil {
		return nil, 0, fmt.Errorf("snapshot %d blob: %w", snap.Version, err)
	}
	out := make(map[string][]byte, len(blob.Config))
	for p, s := range blob.Config {
		out[p] = []byte(s)
	}
	return out, snap.Version, nil
}

// raw GETs a non-JSON-envelope body (the snapshot blob).
func (c *adminClient) raw(ctx context.Context, path string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+"/admin/v1"+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 256<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		var env struct {
			Error *apiError `json:"error"`
		}
		if json.Unmarshal(b, &env) == nil && env.Error != nil {
			env.Error.Status = resp.StatusCode
			return nil, env.Error
		}
		return nil, fmt.Errorf("GET %s: HTTP %d", path, resp.StatusCode)
	}
	return b, nil
}

func exportCmd(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("hubctl export", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var a adminFlags
	a.register(fs)
	out := fs.String("out", "", "directory to write (must be empty unless -force)")
	force := fs.Bool("force", false, "write into a non-empty directory (files not in live are removed)")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 || *out == "" {
		fmt.Fprintln(stderr, configUsage)
		return 2
	}
	c, err := a.client()
	if err != nil {
		fmt.Fprintf(stderr, "hubctl: %v\n", err)
		return 1
	}
	files, version, err := c.liveTree(context.Background())
	if err != nil {
		fmt.Fprintf(stderr, "hubctl: %v\n", err)
		return 1
	}
	if err := writeTree(*out, files, *force); err != nil {
		fmt.Fprintf(stderr, "hubctl: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "exported snapshot %d: %d files to %s\n", version, len(files), *out)
	return 0
}

// writeTree writes files under dir. With force, files under dir that are not in files are
// removed, so the directory equals live afterwards.
func writeTree(dir string, files map[string][]byte, force bool) error {
	local, err := readTree(dir)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if len(local) > 0 && !force {
		return fmt.Errorf("%s is not empty (use -force to overwrite it)", dir)
	}
	for p := range local {
		if _, ok := files[p]; !ok {
			if err := os.Remove(filepath.Join(dir, filepath.FromSlash(p))); err != nil {
				return err
			}
		}
	}
	for p, b := range files {
		if strings.Contains(p, "..") || filepath.IsAbs(p) {
			return fmt.Errorf("refusing unsafe path %q from the snapshot", p)
		}
		full := filepath.Join(dir, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil { //nolint:gosec // registry config directory, readable by design
			return err
		}
		if err := os.WriteFile(full, b, 0o644); err != nil { //nolint:gosec // registry config, no secrets
			return err
		}
	}
	return nil
}

// readTree reads every regular file under dir, keyed by slash path relative to dir.
func readTree(dir string) (map[string][]byte, error) {
	if _, err := os.Stat(dir); err != nil {
		return nil, err
	}
	out := map[string][]byte{}
	root := os.DirFS(dir)
	err := fs.WalkDir(root, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := fs.ReadFile(root, p)
		out[p] = b
		return err
	})
	return out, err
}

func planCmd(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("hubctl plan", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var a adminFlags
	a.register(fs)
	dir := fs.String("config-dir", "config", "local registry config directory")
	contracts := fs.String("contracts-dir", "../contracts", "contracts directory (capability schemas)")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 {
		fmt.Fprintln(stderr, configUsage)
		return 2
	}
	c, err := a.client()
	if err != nil {
		fmt.Fprintf(stderr, "hubctl: %v\n", err)
		return 1
	}
	local, err := readTree(*dir)
	if err != nil {
		fmt.Fprintf(stderr, "hubctl: %v\n", err)
		return 1
	}
	live, version, err := c.liveTree(context.Background())
	if err != nil {
		fmt.Fprintf(stderr, "hubctl: %v\n", err)
		return 1
	}
	changes, _, _ := semanticDiff(live, local, *contracts)
	if len(changes) == 0 {
		fmt.Fprintf(stdout, "No changes: %s matches snapshot %d.\n", *dir, version)
		return 0
	}
	printPlan(stdout, changes, version)
	return 3
}

// fileChange is one changed file of a plan, with its path-level differences.
type fileChange struct {
	Path   string
	Object string // "vendor clevertap", "integration int_x v8", ...
	Kind   string // add, change, remove
	Lines  []string
}

// diffTrees compares live (before) with local (after).
func diffTrees(live, local map[string][]byte) []fileChange {
	paths := map[string]bool{}
	for p := range live {
		paths[p] = true
	}
	for p := range local {
		paths[p] = true
	}
	var out []fileChange
	for _, p := range slices.Sorted(maps.Keys(paths)) {
		before, inLive := live[p]
		after, inLocal := local[p]
		fc := fileChange{Path: p, Object: objectOf(p)}
		switch {
		case !inLive:
			fc.Kind = "add"
		case !inLocal:
			fc.Kind = "remove"
		case bytes.Equal(before, after):
			continue
		default:
			fc.Kind = "change"
			fc.Lines = pathDiff(before, after)
			if len(fc.Lines) == 0 {
				fc.Lines = []string{"(formatting or comments only)"}
			}
		}
		out = append(out, fc)
	}
	return out
}

// objectOf names the registry object a config file defines.
func objectOf(p string) string {
	parts := strings.Split(strings.TrimSuffix(p, filepath.Ext(p)), "/")
	switch {
	case len(parts) == 2 && parts[0] == "vendors":
		return "vendor " + parts[1]
	case len(parts) == 2 && parts[0] == "accounts":
		return "account " + parts[1]
	case len(parts) == 2 && parts[0] == "tenants":
		return "tenant " + parts[1]
	case len(parts) == 2 && parts[0] == "registrar":
		return "registrar endpoint " + parts[1]
	case len(parts) == 3 && parts[0] == "transformers":
		return "transformer " + parts[1] + "@" + parts[2]
	case len(parts) >= 3 && parts[0] == "transformers" && parts[2] == "fixtures":
		return "transformer " + parts[1] + " fixtures"
	case len(parts) == 3 && parts[0] == "integrations" && parts[2] == "integration":
		return "integration " + parts[1]
	case len(parts) == 4 && parts[0] == "integrations" && parts[2] == "versions":
		return "integration " + parts[1] + " v" + parts[3]
	}
	return "file " + p
}

// pathDiff lists the leaf paths that differ between two YAML or JSON documents.
func pathDiff(before, after []byte) []string {
	var a, b any
	if yaml.Unmarshal(before, &a) != nil || yaml.Unmarshal(after, &b) != nil {
		return []string{"(content changed)"}
	}
	fa, fb := map[string]any{}, map[string]any{}
	flatten("", a, fa)
	flatten("", b, fb)
	keys := map[string]bool{}
	for k := range fa {
		keys[k] = true
	}
	for k := range fb {
		keys[k] = true
	}
	sorted := slices.Sorted(maps.Keys(keys))
	var out []string
	for _, k := range sorted {
		va, ina := fa[k]
		vb, inb := fb[k]
		switch {
		case !ina:
			out = append(out, fmt.Sprintf("+ %s: %s", k, show(vb)))
		case !inb:
			out = append(out, fmt.Sprintf("- %s: %s", k, show(va)))
		case !reflect.DeepEqual(va, vb):
			out = append(out, fmt.Sprintf("~ %s: %s → %s", k, show(va), show(vb)))
		}
	}
	return out
}

func flatten(prefix string, v any, out map[string]any) {
	switch t := v.(type) {
	case map[string]any:
		if len(t) == 0 && prefix != "" {
			out[prefix] = t
		}
		for k, x := range t {
			key := k
			if prefix != "" {
				key = prefix + "." + k
			}
			flatten(key, x, out)
		}
	case []any:
		if len(t) == 0 && prefix != "" {
			out[prefix] = t
		}
		for i, x := range t {
			flatten(fmt.Sprintf("%s[%d]", prefix, i), x, out)
		}
	default:
		out[prefix] = t
	}
}

func show(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprint(v)
	}
	return string(b)
}

func printPlan(w io.Writer, changes []fileChange, version int64) {
	counts := map[string]int{}
	for _, c := range changes {
		counts[c.Kind]++
		sign := map[string]string{"add": "+", "change": "~", "remove": "-"}[c.Kind]
		fmt.Fprintf(w, "%s %s (%s)\n", sign, c.Object, c.Path)
		for _, l := range c.Lines {
			fmt.Fprintf(w, "    %s\n", l)
		}
	}
	fmt.Fprintf(w, "\nPlan against snapshot %d: %d to add, %d to change, %d to remove.\n", version, counts["add"], counts["change"], counts["remove"])
}
