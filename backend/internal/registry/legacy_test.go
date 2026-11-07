package registry

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"go.yaml.in/yaml/v3"
)

// testdata/legacy is backend/config and its contracts side as they were before ADR-0023 (git
// 01eb0c5): the shape archived snapshot blobs of format 1 still hold.
func legacySources() (fs.FS, fs.FS) {
	return os.DirFS("testdata/legacy/config"), os.DirFS("testdata/legacy/contracts")
}

func TestUpgradeLegacyTreeBuilds(t *testing.T) {
	cfg, contracts := legacySources()
	if !IsLegacyTree(cfg) {
		t.Fatal("testdata/legacy is not detected as a legacy tree")
	}
	up, lg, err := UpgradeLegacy(cfg, contracts)
	if err != nil {
		t.Fatal(err)
	}
	if lg == nil || lg.IntegrationCapability["int_evt_brandA"] != "events.track@v1" {
		t.Fatalf("legacy info %+v", lg)
	}
	snap, err := Build(Sources{Config: up}, time.Now())
	if err != nil {
		t.Fatalf("upgraded legacy tree: %v", err)
	}
	op := snap.Integrations["int_evt_brandA"].Versions[1].Op("default")
	if op == nil || !op.Idempotent || op.Description != "Track a single event for a user." || op.RequestSchema.Body.Compiled() == nil {
		t.Fatalf("operation not copied from the capability: %+v", op)
	}
	if len(op.PII) != 2 {
		t.Fatalf("x-pii tags of the capability schema lost: %+v", op.PII)
	}
	if _, ok := up["capabilities/events.track.v1.yaml"]; ok {
		t.Fatal("capabilities/ kept")
	}
	if ep := snap.Vendors["clevertap"].Endpoints["upload@1"]; ep.RequestSchema == nil {
		t.Fatal("endpoint got no request_schema")
	}
	for name, f := range up {
		if regexp.MustCompile(`(?m)^\s*(capability|operation):`).Match(f.Data) {
			t.Errorf("%s still names a capability:\n%s", name, f.Data)
		}
	}
	// Comments survive.
	if !strings.Contains(string(up["transformers/T1/7.yaml"].Data), "CleverTap Upload Events API") {
		t.Error("transformer header comment lost")
	}
}

func TestUpgradeLegacyLeavesCurrentTreesAlone(t *testing.T) {
	out, lg, err := UpgradeLegacy(os.DirFS("../../config"), nil)
	if err != nil || lg != nil {
		t.Fatalf("current tree: %v %+v", err, lg)
	}
	b, _ := os.ReadFile("../../config/vendors/clevertap.yaml")
	if !bytes.Equal(out["vendors/clevertap.yaml"].Data, b) {
		t.Fatal("a current tree must come back unchanged")
	}
}

// legacyBlob packs testdata/legacy the way the pre-ADR-0023 Pack did (format 1).
func legacyBlob(t *testing.T, edit func(*blob)) ([]byte, string) {
	t.Helper()
	cfg, contracts := legacySources()
	b := blob{Format: legacyBlobFormat, Config: map[string]string{}, Contracts: map[string]string{}}
	files, err := readTree(cfg)
	if err != nil {
		t.Fatal(err)
	}
	for p, d := range files {
		b.Config[p] = string(d)
	}
	cfiles, err := readTree(contracts)
	if err != nil {
		t.Fatal(err)
	}
	for p, d := range cfiles {
		b.Contracts[p] = string(d)
	}
	_, lg, err := UpgradeLegacy(cfg, contracts)
	if err != nil {
		t.Fatal(err)
	}
	b.Checksum = lg.Checksum
	if edit != nil {
		edit(&b)
	}
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if err := json.NewEncoder(zw).Encode(b); err != nil {
		t.Fatal(err)
	}
	_ = zw.Close()
	return buf.Bytes(), lg.Checksum
}

// legacyTestdataChecksum is the checksum the pre-ADR-0023 registry (git 01eb0c5) built for
// testdata/legacy: the legacy checksum must reproduce it exactly, or every archived blob would
// be refused as corrupt.
const legacyTestdataChecksum = "sha256:7fac223bb8e1c1502d07238e1cf577e0b64d5a9a8fe1fd2f111fe2459c223f2f"

func TestUnpackUpgradesLegacyBlobs(t *testing.T) {
	data, sum := legacyBlob(t, nil)
	if sum != legacyTestdataChecksum {
		t.Fatalf("legacy checksum %s, the old registry built %s", sum, legacyTestdataChecksum)
	}
	snap, src, err := Unpack(data, sum, time.Now())
	if err != nil {
		t.Fatalf("legacy blob: %v", err)
	}
	if snap.Legacy == nil || snap.Legacy.Checksum != sum || snap.Checksum == sum {
		t.Fatalf("legacy %+v, checksum %s", snap.Legacy, snap.Checksum)
	}
	if snap.Integrations["int_evt_brandB"].Active().Op("default") == nil {
		t.Fatal("operations missing after the upgrade")
	}
	// Repacked, the upgraded content is a current blob that rebuilds to the same checksum.
	again, err := Pack(src, snap)
	if err != nil {
		t.Fatal(err)
	}
	cur, _, err := Unpack(again, snap.Checksum, time.Now())
	if err != nil || cur.Legacy != nil || cur.Checksum != snap.Checksum {
		t.Fatalf("repacked: %v legacy=%v", err, cur != nil && cur.Legacy != nil)
	}

	tampered, _ := legacyBlob(t, func(b *blob) {
		b.Contracts["schemas/capabilities/events.track.v1.request.json"] = strings.Replace(b.Contracts["schemas/capabilities/events.track.v1.request.json"], `"maxLength": 256`, `"maxLength": 9999`, 1)
	})
	if _, _, err := Unpack(tampered, sum, time.Now()); !errors.Is(err, ErrCorruptBlob) {
		t.Fatalf("tampered legacy blob: %v, want ErrCorruptBlob", err)
	}
}

func TestUpgradeLegacyDocs(t *testing.T) {
	cfg, contracts := legacySources()
	_, lg, err := UpgradeLegacy(cfg, contracts)
	if err != nil {
		t.Fatal(err)
	}
	header, key, err := lg.UpgradeIntegrationDoc([]byte("integration: int_x\nowner_tenant: orders-service\ncapability: events.track@v1\nactive_version: 1\n"))
	if err != nil || key != "events.track@v1" || strings.Contains(string(header), "capability") {
		t.Fatalf("header %s %q %v", header, key, err)
	}
	// A draft written before the change: no operations, a capability copy in the header fields.
	draft := []byte("integration: int_x\ncapability: events.track@v1\nversion: 2\ntargets: []\n")
	out, err := lg.UpgradeVersionDoc(draft, "")
	if err != nil {
		t.Fatal(err)
	}
	var v IntegrationVersion
	if err := yaml.Unmarshal(out, &v); err != nil || v.Op("default") == nil || v.Op("default").RequestSchema.Body == nil {
		t.Fatalf("draft %s: %v", out, err)
	}
	// An upgraded document is left alone.
	if again, err := lg.UpgradeVersionDoc(out, "events.track@v1"); err != nil || !bytes.Equal(again, out) {
		t.Fatalf("second upgrade changed the document: %v", err)
	}
	if _, err := lg.UpgradeVersionDoc([]byte("integration: int_x\nversion: 1\n"), "nope@v1"); err == nil {
		t.Fatal("unknown capability accepted")
	}
}
