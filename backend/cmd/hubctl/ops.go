package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strings"
)

const opsUsage = `operations:
  dry-run INTEGRATION BODY.json
      Show the vendor request the integration's active version would send for BODY (its
      highest-weight target), with credentials redacted. Nothing is sent.
  replay -reason TEXT [-integration ID] [-tenant ID] [-account ID] [-error-code CODE]
         [-from RFC3339] [-to RFC3339] [-dry-run] [-yes]
      Replay dead letters matching the filter (they re-resolve and re-transform). Prints the
      match count first; replays only with -yes.`

// sensitiveHeader reports headers whose values hubctl never prints, whatever the server sent.
func sensitiveHeader(name string) bool {
	n := strings.ToLower(name)
	for _, s := range []string{"authorization", "passcode", "secret", "token", "api-key", "apikey", "password", "signature"} {
		if strings.Contains(n, s) {
			return true
		}
	}
	return false
}

func dryRunCmd(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("hubctl dry-run", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var a adminFlags
	a.register(fs)
	if err := fs.Parse(args); err != nil || fs.NArg() != 2 {
		fmt.Fprintln(stderr, opsUsage)
		return 2
	}
	integration, file := fs.Arg(0), fs.Arg(1)
	raw, err := os.ReadFile(file) //nolint:gosec // the operator names the body file on the command line
	if err != nil {
		fmt.Fprintf(stderr, "hubctl: %v\n", err)
		return 1
	}
	var input map[string]any
	if err := json.Unmarshal(raw, &input); err != nil {
		fmt.Fprintf(stderr, "hubctl: %s is not a JSON object: %v\n", file, err)
		return 2
	}
	c, err := a.client()
	if err != nil {
		fmt.Fprintf(stderr, "hubctl: %v\n", err)
		return 1
	}
	ctx := context.Background()
	var in struct {
		Active *struct {
			Version int `json:"version"`
			Spec    struct {
				Targets []struct {
					AccountID   string `json:"account_id"`
					Weight      int    `json:"weight"`
					Transformer struct {
						Request string `json:"request"`
					} `json:"transformer"`
				} `json:"targets"`
			} `json:"spec"`
		} `json:"active"`
	}
	if err := c.do(ctx, http.MethodGet, "/integrations/"+url.PathEscape(integration), nil, &in); err != nil {
		fmt.Fprintf(stderr, "hubctl: %v\n", err)
		return 1
	}
	if in.Active == nil || len(in.Active.Spec.Targets) == 0 {
		fmt.Fprintf(stderr, "hubctl: %s has no active version\n", integration)
		return 1
	}
	target := in.Active.Spec.Targets[0]
	for _, t := range in.Active.Spec.Targets[1:] {
		if t.Weight > target.Weight {
			target = t
		}
	}
	var res struct {
		OK            bool `json:"ok"`
		VendorRequest *struct {
			Method  string            `json:"method"`
			URL     string            `json:"url"`
			Headers map[string]string `json:"headers"`
			Body    any               `json:"body"`
		} `json:"vendor_request"`
		CanonicalError any `json:"canonical_error"`
		CompileErrors  []struct {
			Path    string `json:"path"`
			Message string `json:"message"`
		} `json:"compile_errors"`
	}
	req := map[string]any{"ref": target.Transformer.Request, "account_id": target.AccountID, "input": input}
	if err := c.do(ctx, http.MethodPost, "/transformers/dry-run", req, &res); err != nil {
		fmt.Fprintf(stderr, "hubctl: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "%s v%d → %s via %s (nothing sent)\n", integration, in.Active.Version, target.AccountID, target.Transformer.Request)
	if !res.OK || res.VendorRequest == nil {
		for _, e := range res.CompileErrors {
			fmt.Fprintf(stdout, "  error %s: %s\n", e.Path, e.Message)
		}
		if res.CanonicalError != nil {
			b, _ := json.MarshalIndent(res.CanonicalError, "  ", "  ")
			fmt.Fprintf(stdout, "  canonical error: %s\n", b)
		}
		return 1
	}
	vr := res.VendorRequest
	fmt.Fprintf(stdout, "%s %s\n", vr.Method, vr.URL)
	for _, k := range slices.Sorted(maps.Keys(vr.Headers)) {
		v := vr.Headers[k]
		if sensitiveHeader(k) {
			v = "***"
		}
		fmt.Fprintf(stdout, "%s: %s\n", k, v)
	}
	if vr.Body != nil {
		b, _ := json.MarshalIndent(vr.Body, "", "  ")
		fmt.Fprintf(stdout, "\n%s\n", b)
	}
	return 0
}

func replayCmd(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("hubctl replay", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var a adminFlags
	a.register(fs)
	reason := fs.String("reason", "", "why (recorded in the change log; required)")
	dry := fs.Bool("dry-run", false, "only count the matching entries")
	yes := fs.Bool("yes", false, "replay without asking")
	filter := map[string]*string{}
	for flagName, key := range map[string]string{"integration": "integration_id", "tenant": "tenant_id", "account": "account_id",
		"vendor": "vendor_id", "error-code": "reason", "from": "from", "to": "to"} {
		filter[key] = fs.String(flagName, "", "filter: "+key)
	}
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 {
		fmt.Fprintln(stderr, opsUsage)
		return 2
	}
	f := map[string]any{"replayed": false}
	for k, v := range filter {
		if *v != "" {
			f[k] = *v
		}
	}
	if len(f) == 1 {
		fmt.Fprintln(stderr, "hubctl: replay needs at least one filter (-integration, -tenant, -account, -vendor, -error-code, -from, -to)")
		return 2
	}
	if len(strings.TrimSpace(*reason)) < 3 {
		fmt.Fprintln(stderr, "hubctl: -reason is required (at least 3 characters)")
		return 2
	}
	c, err := a.client()
	if err != nil {
		fmt.Fprintf(stderr, "hubctl: %v\n", err)
		return 1
	}
	type result struct {
		ChangeID  *string `json:"change_id"`
		Matched   int     `json:"matched"`
		Affected  int     `json:"affected"`
		Truncated bool    `json:"truncated"`
	}
	ctx := context.Background()
	var count result
	if err := c.do(ctx, http.MethodPost, "/dlq/replay", map[string]any{"filter": f, "dry_run": true, "reason": *reason}, &count); err != nil {
		fmt.Fprintf(stderr, "hubctl: %v\n", err)
		return 1
	}
	more := ""
	if count.Truncated {
		more = " (first page; more match)"
	}
	fmt.Fprintf(stdout, "%d dead letter(s) match%s\n", count.Matched, more)
	if *dry || count.Matched == 0 {
		return 0
	}
	if !*yes {
		fmt.Fprintln(stderr, "hubctl: not replaying without -yes (or use -dry-run to only count)")
		return 2
	}
	total := 0
	for {
		var res result
		if err := c.do(ctx, http.MethodPost, "/dlq/replay", map[string]any{"filter": f, "dry_run": false, "reason": *reason}, &res); err != nil {
			fmt.Fprintf(stderr, "hubctl: after %d replayed: %v\n", total, err)
			return 1
		}
		total += res.Affected
		id := ""
		if res.ChangeID != nil {
			id = " change " + *res.ChangeID
		}
		fmt.Fprintf(stdout, "replayed %d%s\n", res.Affected, id)
		if !res.Truncated || res.Affected == 0 {
			break
		}
	}
	fmt.Fprintf(stdout, "%d dead letter(s) replayed\n", total)
	return 0
}
