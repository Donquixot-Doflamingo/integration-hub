package isfcsync

import (
	"bytes"

	"go.yaml.in/yaml/v3"
)

func decodeYAML(b []byte, v any) error {
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	return dec.Decode(v)
}
