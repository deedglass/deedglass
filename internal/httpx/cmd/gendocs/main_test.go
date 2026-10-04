package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/deedglass/deedglass/internal/httpx"
)

func TestEntriesCoverCatalog(t *testing.T) {
	es := Entries()
	if len(es) != len(httpx.Catalog()) || len(es) < 40 {
		t.Fatalf("%d entries for %d codes", len(es), len(httpx.Catalog()))
	}
	seen := map[string]bool{}
	for _, e := range es {
		if seen[e.Code] {
			t.Fatalf("duplicate %s", e.Code)
		}
		seen[e.Code] = true
		if e.DocURL != "https://docs.deedglass.com/errors/"+e.Code || e.Owner == "" || e.DefaultMessage == "" {
			t.Fatalf("incomplete entry %+v", e)
		}
		if strings.Contains(e.DefaultMessage, "{") {
			t.Fatalf("%s: default message has a placeholder", e.Code)
		}
	}
	for _, c := range []string{"budget_exceeded", "rate_limited", "service_unavailable"} {
		for _, e := range es {
			if e.Code == c && !e.Retryable {
				t.Fatalf("%s should be retryable", c)
			}
		}
	}
	for _, e := range es {
		if e.Code == "policy_denied" && e.Retryable {
			t.Fatal("policy_denied is not retryable")
		}
	}
}

func TestRun(t *testing.T) {
	var b bytes.Buffer
	if err := run([]string{"errors"}, &b); err != nil {
		t.Fatal(err)
	}
	var out []Entry
	if err := json.Unmarshal(b.Bytes(), &out); err != nil || len(out) != len(httpx.Catalog()) {
		t.Fatalf("output: %v (%d)", err, len(out))
	}
	if err := run(nil, &b); err == nil {
		t.Fatal("no subcommand should fail")
	}
}

func TestCLIErrors(t *testing.T) {
	ce, err := CLIErrors("../../../../cmd/deedglass")
	if err != nil {
		t.Fatal(err)
	}
	by := map[string]CLIError{}
	for _, c := range ce {
		by[c.Code] = c
		if _, isAPI := httpx.Lookup(httpx.Code(c.Code)); isAPI {
			t.Fatalf("%s is an API code", c.Code)
		}
	}
	for _, want := range []string{"config_invalid", "not_built", "database_unreachable", "migration_changed"} {
		if _, ok := by[want]; !ok {
			t.Fatalf("missing CLI code %s (have %d)", want, len(ce))
		}
	}
	if d := by["database_unreachable"]; len(d.Exit) == 0 || len(d.Messages) == 0 {
		t.Fatalf("database_unreachable: %+v", d)
	}
}
