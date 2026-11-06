package registry

import (
	"errors"
	"maps"
	"slices"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/testkit/multiop"
)

// HUB-343 / HUB-345: per-operation transformer pairs and the operations_have_transformers check,
// on the two-operation overlay of internal/testkit/multiop (send and verify on two endpoints).

const (
	opV          = multiop.VersionFile
	verifyPair   = "      verify: { request: TS-VERIFY-REQ@1, response: TS-VERIFY-RES@1 }\n"
	opsBlock     = "    operations:\n" + verifyPair
	sendPairLine = "      verify: { request: TS-SEND-REQ@1, response: TS-SEND-RES@1 }\n"
)

// multiopFiles is validFiles plus the overlay, with overrides applied (empty value deletes).
func multiopFiles(overrides map[string]string) map[string]string {
	m := files(nil)
	maps.Copy(m, multiop.Files(multiop.Config()))
	for k, v := range overrides {
		if v == "" {
			delete(m, k)
			continue
		}
		m[k] = v
	}
	return m
}

func multiopSources(t testing.TB, m map[string]string) Sources {
	t.Helper()
	contracts := contractsFS(t)
	for p, s := range multiop.Files(multiop.Contracts()) {
		contracts[p] = &fstest.MapFile{Data: []byte(s)}
	}
	return Sources{Config: mapFS(m), Contracts: contracts}
}

// opVersion returns the overlay version file with the n-th (1-based) occurrence of old replaced.
func opVersion(t *testing.T, n int, old, repl string) string {
	t.Helper()
	s := multiop.Files(multiop.Config())[opV]
	at := -1
	for range n {
		next := strings.Index(s[at+1:], old)
		if next < 0 {
			t.Fatalf("version file has fewer than %d %q", n, old)
		}
		at += 1 + next
	}
	return s[:at] + repl + s[at+len(old):]
}

func TestPerOperationPairsBuild(t *testing.T) {
	snap, err := Build(multiopSources(t, multiopFiles(nil)), time.Now()) // runs every golden too
	if err != nil {
		t.Fatal(err)
	}
	v := snap.Integrations[multiop.Integration].Active()
	send := TransformerPair{Request: TransformerRef{"TS-SEND-REQ", 1}, Response: TransformerRef{"TS-SEND-RES", 1}}
	verify := TransformerPair{Request: TransformerRef{"TS-VERIFY-REQ", 1}, Response: TransformerRef{"TS-VERIFY-RES", 1}}
	for _, tg := range []Target{v.Targets[0], v.Failover[0]} {
		if tg.Pair(multiop.OpSend) != send || tg.Pair(multiop.OpVerify) != verify || tg.Pair("") != send {
			t.Fatalf("%s: send %v verify %v", tg.Account, tg.Pair(multiop.OpSend), tg.Pair(multiop.OpVerify))
		}
		if got := tg.Pairs(); !slices.Equal(got, []TransformerPair{send, verify}) {
			t.Fatalf("pairs %v", got)
		}
	}
	for _, ref := range []TransformerRef{send.Request, send.Response, verify.Request, verify.Response} {
		if snap.Programs[ref] == nil {
			t.Fatalf("%s not compiled", ref)
		}
	}
}

func TestOperationsHaveTransformers(t *testing.T) {
	const (
		defaulted = "operation verify uses the default pair, but %s is compiled for operation send: give verify its own pair under operations"
		shadow    = "shadow:\n  target:\n    vendor: stepvendor\n    account: sv-backup\n    transformer: { request: TS-SEND-REQ@1, response: TS-SEND-RES@1 }\n  mode: dry_run\npublish: {"
	)
	tests := []struct {
		name    string
		version func(t *testing.T) string
		want    []Problem
	}{
		{"operation key the capability does not declare", func(t *testing.T) string {
			return opVersion(t, 1, "      verify:", "      verfy:")
		}, []Problem{
			{opV, "targets[0].operations.verfy", `"verfy" is not an operation of capability test.two_step@v1 (it declares send, verify)`},
			{opV, "targets[0].transformer", strings.Replace(defaulted, "%s", "TS-SEND-REQ@1", 1)},
		}},
		{"listed pair compiled for another operation", func(t *testing.T) string {
			return opVersion(t, 1, verifyPair, sendPairLine)
		}, []Problem{
			{opV, "targets[0].operations.verify", "TS-SEND-REQ@1 is compiled for operation send, not verify"},
			{opV, "targets[0].operations.verify", "TS-SEND-RES@1 is compiled for operation send, not verify"},
		}},
		{"only the response of a listed pair is wrong", func(t *testing.T) string {
			return opVersion(t, 1, "response: TS-VERIFY-RES@1", "response: TS-SEND-RES@1")
		}, []Problem{{opV, "targets[0].operations.verify", "TS-SEND-RES@1 is compiled for operation send, not verify"}}},
		{"failover target without the verify pair", func(t *testing.T) string {
			return opVersion(t, 2, opsBlock, "")
		}, []Problem{{opV, "failover[0].transformer", strings.Replace(defaulted, "%s", "TS-SEND-REQ@1", 1)}}},
		{"shadow target without the verify pair", func(t *testing.T) string {
			return opVersion(t, 1, "publish: {", shadow)
		}, []Problem{{opV, "shadow.target.transformer", strings.Replace(defaulted, "%s", "TS-SEND-RES@1", 1)}}},
		{"empty operations map", func(t *testing.T) string {
			return opVersion(t, 1, opsBlock, "    operations: {}\n")
		}, []Problem{{opV, "targets[0].operations", "name at least one operation, or omit operations"}}},
		{"unknown transformer in a listed pair", func(t *testing.T) string {
			return opVersion(t, 1, "request: TS-VERIFY-REQ@1", "request: TS-VERIFY-REQ@9")
		}, []Problem{{opV, "targets[0].operations.verify.request", "unknown transformer TS-VERIFY-REQ@9"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := validateFiles(t, multiopSources(t, multiopFiles(map[string]string{opV: tt.version(t)})))
			var ps ConfigErrors
			if !errors.As(err, &ps) {
				t.Fatalf("want ConfigErrors, got %v", err)
			}
			for _, w := range tt.want {
				if !slices.Contains(ps, w) {
					t.Errorf("missing problem\n  %s\ngot:\n  %s", w, strings.ReplaceAll(err.Error(), "\n", "\n  "))
				}
			}
			// The same refusal through the snapshot build (config files, hubctl apply).
			if _, err := Build(multiopSources(t, multiopFiles(map[string]string{opV: tt.version(t)})), time.Now()); err == nil {
				t.Fatal("Build accepted the config")
			}
		})
	}
}

// TestOperationsValidShapes: listing every operation (the default pair then serves none) and a
// default pair compiled for the other operation are both valid.
func TestOperationsValidShapes(t *testing.T) {
	for name, version := range map[string]func(t *testing.T) string{
		"every operation listed": func(t *testing.T) string {
			return opVersion(t, 1, opsBlock, opsBlock+"      send: { request: TS-SEND-REQ@1, response: TS-SEND-RES@1 }\n")
		},
		"default pair serves verify, send listed": func(t *testing.T) string {
			s := opVersion(t, 1, "transformer: { request: TS-SEND-REQ@1, response: TS-SEND-RES@1 }", "transformer: { request: TS-VERIFY-REQ@1, response: TS-VERIFY-RES@1 }")
			return strings.Replace(s, verifyPair, "      send: { request: TS-SEND-REQ@1, response: TS-SEND-RES@1 }\n", 1)
		},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Build(multiopSources(t, multiopFiles(map[string]string{opV: version(t)})), time.Now()); err != nil {
				t.Fatal(err)
			}
		})
	}
}

// TestShadowSendChecksEveryOperationEndpoint: a sent shadow copy must be safe for every
// operation, so a non-idempotent endpoint of any pair forces dry_run.
func TestShadowSendChecksEveryOperationEndpoint(t *testing.T) {
	shadow := "shadow:\n  target:\n    vendor: stepvendor\n    account: sv-backup\n    transformer: { request: TS-SEND-REQ@1, response: TS-SEND-RES@1 }\n" +
		opsBlock + "  mode: send\npublish: {"
	_, err := validateFiles(t, multiopSources(t, multiopFiles(map[string]string{opV: opVersion(t, 1, "publish: {", shadow)})))
	want := Problem{opV, "shadow.mode", "stepvendor.send@1 is not idempotent (it has side effects): its shadow must be dry_run"}
	var ps ConfigErrors
	if !errors.As(err, &ps) || !slices.Contains(ps, want) {
		t.Fatalf("missing %s in %v", want, err)
	}
	if n := strings.Count(err.Error(), "is not idempotent"); n != 1 {
		t.Fatalf("%d idempotency problems, want 1 (verify@1 is idempotent): %v", n, err)
	}
}

// TestTargetOperationsRoundTrip: operations survive the version file round trip that publish,
// rollback and weight changes use (yaml.Marshal of the version).
func TestTargetOperationsRoundTrip(t *testing.T) {
	var v IntegrationVersion
	if err := yaml.Unmarshal([]byte(multiop.Files(multiop.Config())[opV]), &v); err != nil {
		t.Fatal(err)
	}
	b, err := yaml.Marshal(&v)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "operations:") {
		t.Fatalf("marshalled version has no operations:\n%s", b)
	}
	var back IntegrationVersion
	if err := yaml.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	if len(back.Targets[0].Operations) != 1 || back.Targets[0].Operations[multiop.OpVerify] != v.Targets[0].Operations[multiop.OpVerify] ||
		back.Failover[0].Operations[multiop.OpVerify] != v.Failover[0].Operations[multiop.OpVerify] {
		t.Fatalf("round trip lost operations: %+v", back.Targets)
	}
	// A single-operation target writes no operations key.
	single := Target{Vendor: "v", Account: "a", Transformer: v.Targets[0].Transformer}
	if b, _ := yaml.Marshal(single); strings.Contains(string(b), "operations") {
		t.Fatalf("empty operations written:\n%s", b)
	}
}
