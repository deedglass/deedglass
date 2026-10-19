package pgmap_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// fixtureKey keys the leaf digests in testdata (not a secret: the fixtures are synthetic).
var fixtureKey = []byte("pgmap fixtures key, not a secret")

type fixtureFrame struct {
	Tables  json.RawMessage `json:"tables"`
	Changes json.RawMessage `json:"changes"`
	Flags   []string        `json:"flags,omitempty"`
	Total   int             `json:"total"`
}

func loadFixture(t *testing.T, name string) []fixtureFrame {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var fs []fixtureFrame
	if err := json.Unmarshal(b, &fs); err != nil {
		t.Fatal(err)
	}
	return fs
}
