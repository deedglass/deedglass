//go:build demo

package demo_test

import (
	"bufio"
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

var updateScan = flag.Bool("update-scan", false, "rewrite testdata/scan-score.golden.json")

// TestDemoScanScore is the demo's Agent Blast-Radius Score golden (S47 §3.2, recorded in S47B):
// deedglass scan of examples/demo with --postgres-dsn-env as acme_owner on the running stack's
// orders-db with the hero's trigger on (make demo-scan-golden). The demo's own client configs
// route through Deedglass, so it grades C, not the D S47 expected; the trigger shows up on
// public.orders.
func TestDemoScanScore(t *testing.T) {
	if os.Getenv("DEEDGLASS_DEMO") != "1" {
		t.Skip("needs the demo stack: make demo-up, then make demo-scan-golden")
	}
	root, _ := filepath.Abs("../..")
	pass := envFileValue(t, filepath.Join(root, "examples/demo/.env"), "ACME_OWNER_PASSWORD")
	run := func(env []string, name string, args ...string) []byte {
		t.Helper()
		cmd := exec.Command(name, args...)
		cmd.Dir = root
		cmd.Env = append(os.Environ(), env...)
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("%s %s: %v", name, strings.Join(args, " "), err)
		}
		return out
	}
	run(nil, "make", "demo-trigger-on")
	out := run([]string{"NO_COLOR=1", "DEMO_SCAN_DSN=postgres://acme_owner:" + pass + "@127.0.0.1:55434/acme?sslmode=disable"},
		"bin/deedglass", "scan", "--dir", "examples/demo", "--postgres-dsn-env", "DEMO_SCAN_DSN", "--no-write", "--json")
	var rep struct {
		Score json.RawMessage `json:"score"`
	}
	if err := json.Unmarshal(out, &rep); err != nil {
		t.Fatal(err)
	}
	var v any
	json.Unmarshal(rep.Score, &v)
	got, _ := json.MarshalIndent(v, "", "  ")
	got = append(got, '\n')
	path := "testdata/scan-score.golden.json"
	if *updateScan {
		os.WriteFile(path, got, 0o644)
	}
	want, _ := os.ReadFile(path)
	var wv any
	json.Unmarshal(want, &wv)
	wantN, _ := json.MarshalIndent(wv, "", "  ")
	if !bytes.Equal(got, append(wantN, '\n')) {
		t.Fatalf("score differs from %s:\n%s", path, got)
	}
	if !bytes.Contains(got, []byte("trigger orders_ship_open_batch")) {
		t.Fatal("the trigger isn't among the deductions")
	}
}

func envFileValue(t *testing.T, path, key string) string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if v, ok := strings.CutPrefix(sc.Text(), key+"="); ok {
			return v
		}
	}
	t.Fatalf("%s has no %s", path, key)
	return ""
}
