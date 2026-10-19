package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// deedglass export reads the TY-11 release bundle offline and writes each format.
func TestRecordExport(t *testing.T) {
	src := filepath.Join("..", "..", "testdata", "release-bundles", "0.4.0", "bundle.tar.gz")
	dir := t.TempDir()
	for _, c := range []struct{ format, want, marker string }{
		{"otel-genai", "11 spans · 12 records read · 1 without one", `"name":"execute_tool postgres.execute"`},
		{"ocsf", "11 OCSF events · 12 records read · 1 without one", `"class_uid":6003`},
		{"prov-json", "1 PROV-JSON document · 12 records read", `"actedOnBehalfOf"`},
		{"fhir", "0 FHIR resources · 12 records read · 12 without one", ""},
	} {
		out := filepath.Join(dir, "x-"+c.format)
		r := cli(t, nil, false, "export", src, "--format", c.format, "--out", out)
		if r.code != 0 || !strings.Contains(r.stdout, c.want) {
			t.Fatalf("%s: %+v", c.format, r)
		}
		b, err := os.ReadFile(out)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Contains(b, []byte(c.marker)) {
			t.Errorf("%s: output lacks %s", c.format, c.marker)
		}
	}

	// Default name, from the bundle's.
	wd, _ := os.Getwd()
	abs, _ := filepath.Abs(src)
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	r := cli(t, nil, false, "export", abs, "--format", "ocsf")
	_ = os.Chdir(wd)
	if r.code != 0 || !strings.Contains(r.stdout, "bundle.ocsf.ndjson") {
		t.Fatalf("default name: %+v", r)
	}

	// --json and stdout.
	r = cli(t, nil, false, "--json", "export", src, "--format", "prov-json", "--out", filepath.Join(dir, "p.json"))
	var res map[string]any
	if r.code != 0 || json.Unmarshal([]byte(r.stdout), &res) != nil || res["object"] != "record_export" || res["records"] != 12.0 {
		t.Fatalf("--json: %+v", r)
	}
	r = cli(t, nil, false, "export", src, "--format", "otel-genai", "--out", "-")
	if r.code != 0 || strings.Count(r.stdout, "\n") != 11 || !strings.Contains(r.stderr, "to stdout") {
		t.Fatalf("stdout: %+v", r)
	}
}

func TestRecordExportRefusals(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join("..", "..", "testdata", "release-bundles", "0.4.0", "bundle.tar.gz")
	for name, args := range map[string][]string{
		"no format":      {"export", src},
		"unknown format": {"export", src, "--format", "csv"},
		"no file":        {"export", "--format", "ocsf"},
		"two files":      {"export", src, src, "--format", "ocsf"},
	} {
		if r := cli(t, nil, false, args...); r.code != 2 {
			t.Errorf("%s: %+v", name, r)
		}
	}
	if r := cli(t, nil, false, "export", filepath.Join(dir, "missing.tar.gz"), "--format", "ocsf"); r.code != 2 {
		t.Errorf("missing file: %+v", r)
	}

	// A bundle with one byte changed in its records no longer matches its manifest: nothing written.
	b, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	junk := filepath.Join(dir, "junk.jsonl")
	_ = os.WriteFile(junk, []byte("not a record\n"), 0o600)
	out := filepath.Join(dir, "out.ndjson")
	if r := cli(t, nil, false, "export", junk, "--format", "ocsf", "--out", out); r.code != 1 || !strings.Contains(r.stderr, "Record 1") {
		t.Errorf("junk: %+v", r)
	}
	if _, err := os.Stat(out); err == nil {
		t.Error("a failed export left a file")
	}
	broken := filepath.Join(dir, "broken.tar.gz")
	_ = os.WriteFile(broken, b[:len(b)/2], 0o600)
	if r := cli(t, nil, false, "export", broken, "--format", "ocsf", "--out", out); r.code != 1 {
		t.Errorf("truncated bundle: %+v", r)
	}
	if _, err := os.Stat(out); err == nil {
		t.Error("a failed export left a file")
	}
}
