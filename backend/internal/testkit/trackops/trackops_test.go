package trackops

import (
	"encoding/json"
	"reflect"
	"testing"

	"go.yaml.in/yaml/v3"
)

func TestMemberMatchesBlock(t *testing.T) {
	var y struct {
		Operations map[string]any `yaml:"operations"`
	}
	if err := yaml.Unmarshal([]byte(Block), &y); err != nil {
		t.Fatal(err)
	}
	var j struct {
		Operations map[string]any `json:"operations"`
	}
	if err := json.Unmarshal([]byte("{"+Member+"}"), &j); err != nil {
		t.Fatal(err)
	}
	yb, _ := json.Marshal(y.Operations)
	var yy map[string]any
	_ = json.Unmarshal(yb, &yy)
	if !reflect.DeepEqual(yy, j.Operations) {
		t.Fatalf("Member and Block differ:\n%s\n%s", yb, Member)
	}
}
