package main

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/deedglass/deedglass/internal/config"
)

func TestRowsCoverKeys(t *testing.T) {
	var b bytes.Buffer
	if err := run(&b); err != nil {
		t.Fatal(err)
	}
	var rows []Row
	if err := json.Unmarshal(b.Bytes(), &rows); err != nil {
		t.Fatal(err)
	}
	if len(rows) != len(config.Keys) {
		t.Fatalf("%d rows for %d keys", len(rows), len(config.Keys))
	}
	for _, r := range rows {
		if r.Secret && r.Default != "" {
			t.Fatalf("%s: a secret's default reached the docs", r.Env)
		}
		if r.Doc == "" || r.Type == "" {
			t.Fatalf("%s: incomplete row", r.Env)
		}
	}
}
