package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/deedglass/deedglass/internal/scan"
	"github.com/deedglass/deedglass/internal/shellguard"
)

// The parity test (growth pick 3): each fixture config, pasted into /check, gives the same report
// as `deedglass scan` on a directory that holds only that file, apart from the clock and the label.
func TestScanConfigParity(t *testing.T) {
	for fixture, name := range map[string]string{
		"supabase.cursor-mcp.json":            ".cursor/mcp.json",
		"postgres.claude_desktop_config.json": "claude_desktop_config.json",
		"routed.mcp.json":                     ".mcp.json",
	} {
		t.Run(fixture, func(t *testing.T) {
			b, err := os.ReadFile(filepath.Join("testdata", fixture))
			if err != nil {
				t.Fatal(err)
			}
			dir := t.TempDir()
			p := filepath.Join(dir, filepath.FromSlash(name))
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(p, b, 0o644); err != nil {
				t.Fatal(err)
			}
			want, err := scan.Scan(context.Background(), scan.Options{Dir: dir, DirLabel: ".", MaxFiles: 10, GOOS: "js", Version: version})
			if err != nil {
				t.Fatal(err)
			}
			var got scan.Report
			out := ScanConfig(string(b), name)
			if err := json.Unmarshal([]byte(out), &got); err != nil {
				t.Fatalf("%v: %s", err, out)
			}
			for _, r := range []*scan.Report{want, &got} {
				r.ScannedAt, r.TookMS = "", 0
			}
			wb, _ := json.Marshal(want)
			gb, _ := json.Marshal(&got)
			if string(wb) != string(gb) {
				t.Fatalf("browser scan differs from the CLI scan:\n cli: %s\n web: %s", wb, gb)
			}
			if got.Score.Grade == "" || len(got.Configs) != 1 || len(got.Configs[0].Servers) == 0 {
				t.Fatalf("the config wasn't read: %s", gb)
			}
		})
	}
}

func TestScanConfigRejects(t *testing.T) {
	for _, c := range [][2]string{{"{}", "/etc/passwd"}, {strings.Repeat("x", 1<<20+1), ".mcp.json"}} {
		if out := ScanConfig(c[0], c[1]); !strings.Contains(out, `"error"`) {
			t.Fatalf("%q: want an error, got %s", c[1], out)
		}
	}
}

// CheckCommand answers what guard shell --no-files --command answers, on every line of the
// shell guard's own fixtures.
func TestCheckCommandFixtures(t *testing.T) {
	for _, f := range []string{"asks.txt", "ordinary.txt", "incidents.txt", "limits.txt"} {
		b, err := os.ReadFile(filepath.Join("..", "..", "internal", "shellguard", "testdata", f))
		if err != nil {
			t.Fatal(err)
		}
		n := 0
		for _, line := range strings.Split(string(b), "\n") {
			cmd := strings.TrimSpace(line)
			if cmd == "" || strings.HasPrefix(cmd, "#") {
				continue
			}
			if i := strings.Index(cmd, "\t"); i >= 0 {
				cmd = cmd[:i]
			}
			n++
			var got struct {
				Decision string               `json:"decision"`
				Reason   string               `json:"reason"`
				Findings []shellguard.Finding `json:"findings"`
			}
			if err := json.Unmarshal([]byte(CheckCommand(cmd)), &got); err != nil {
				t.Fatal(err)
			}
			want := shellguard.Check(cmd, shellguard.Options{})
			if want == nil {
				want = []shellguard.Finding{}
			}
			if !reflect.DeepEqual(got.Findings, want) || got.Reason != shellguard.Reason(want, false) || (got.Decision == "ask") != (len(want) > 0) {
				t.Fatalf("%s: %q: got %+v, want %+v", f, cmd, got, want)
			}
		}
		if n == 0 {
			t.Fatalf("%s: no commands read", f)
		}
	}
}
