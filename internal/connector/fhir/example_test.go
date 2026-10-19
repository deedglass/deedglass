package fhir

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"go.yaml.in/yaml/v3"
)

// TestExampleConfig keeps examples/connectors/fhir.yaml valid.
func TestExampleConfig(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "..", "examples", "connectors", "fhir.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Type   string         `yaml:"type"`
		Config map[string]any `yaml:"config"`
	}
	if err := yaml.Unmarshal(b, &doc); err != nil || doc.Type != Type {
		t.Fatalf("%v %q", err, doc.Type)
	}
	raw, _ := json.Marshal(doc.Config)
	if _, errs := New().Validate(context.Background(), devEnv(), raw); len(errs) > 0 {
		t.Fatal(errs)
	}
}
