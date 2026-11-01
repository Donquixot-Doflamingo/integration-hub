package registry

import (
	"testing"
	"testing/fstest"
)

func TestFixtureDirPrefersPerVersionCases(t *testing.T) {
	cfg := fstest.MapFS{
		"transformers/T1/fixtures/a.input.json":    {Data: []byte(`{}`)},
		"transformers/T1/fixtures/v8/a.input.json": {Data: []byte(`{}`)},
	}
	if got := FixtureDir(cfg, TransformerRef{ID: "T1", Version: 8}); got != "transformers/T1/fixtures/v8" {
		t.Fatalf("v8: %s", got)
	}
	if got := FixtureDir(cfg, TransformerRef{ID: "T1", Version: 7}); got != "transformers/T1/fixtures" {
		t.Fatalf("v7: %s", got)
	}
}
