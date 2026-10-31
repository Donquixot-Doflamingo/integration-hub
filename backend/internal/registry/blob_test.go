package registry

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

func TestPackUnpackRoundTrip(t *testing.T) {
	src := devSources(t)
	snap, err := Build(src, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	data, err := Pack(src, snap)
	if err != nil {
		t.Fatal(err)
	}
	got, _, err := Unpack(data, snap.Checksum, time.Now())
	if err != nil {
		t.Fatalf("Unpack: %v", err)
	}
	if got.Checksum != snap.Checksum || len(got.Integrations) != len(snap.Integrations) {
		t.Fatalf("rebuilt snapshot differs: %s vs %s", got.Checksum, snap.Checksum)
	}
	if got.Capabilities["events.track@v1"].Operations["default"].Request == nil {
		t.Fatal("capability schema not compiled from the blob")
	}
}

// rewrite decodes a blob, applies fn and re-encodes it, to simulate tampering.
func rewrite(t *testing.T, data []byte, fn func(*blob)) []byte {
	t.Helper()
	zr, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(zr)
	var b blob
	if err := json.Unmarshal(raw, &b); err != nil {
		t.Fatal(err)
	}
	fn(&b)
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	_ = json.NewEncoder(zw).Encode(b)
	_ = zw.Close()
	return buf.Bytes()
}

func TestUnpackRefusesCorruptBlobs(t *testing.T) {
	src := devSources(t)
	snap, err := Build(src, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	data, err := Pack(src, snap)
	if err != nil {
		t.Fatal(err)
	}
	tests := map[string]struct {
		data []byte
		want string
	}{
		"truncated gzip": {data[:len(data)/2], ""},
		"not gzip":       {[]byte("hello"), ""},
		"tampered file": {rewrite(t, data, func(b *blob) {
			b.Config["accounts/ct-brandA-in.yaml"] = strings.Replace(b.Config["accounts/ct-brandA-in.yaml"], "rps: 50", "rps: 999", 1)
		}), ""},
		"wrong recorded checksum": {data, "sha256:" + strings.Repeat("f", 64)},
		"unknown format":          {rewrite(t, data, func(b *blob) { b.Format = 9 }), ""},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			if _, _, err := Unpack(tt.data, tt.want, time.Now()); !errors.Is(err, ErrCorruptBlob) {
				t.Fatalf("err %v, want ErrCorruptBlob", err)
			}
		})
	}
}
