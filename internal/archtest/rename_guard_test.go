package archtest

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestRenameGuard runs scripts/rename_guard.sh in a throwaway git repo: clean files pass, a planted
// retired identifier fails with the exact message, and allowed paths and entries pass.
func TestRenameGuard(t *testing.T) {
	for _, tool := range []string{"git", "bash", "perl"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s is not on PATH", tool)
		}
	}
	script, err := os.ReadFile("../../scripts/rename_guard.sh")
	if err != nil {
		t.Fatal(err)
	}
	// The token is assembled at run time so this file doesn't need an allow entry.
	old := "t" + "xv"
	cases := []struct {
		name    string
		files   map[string]string
		allow   string
		wantOK  bool
		wantOut string
	}{
		{name: "clean", wantOK: true, files: map[string]string{"a.go": "package a // deedglass\n"}},
		{name: "planted token", files: map[string]string{"cmd/deedglass/main.go": "package main\n\n// " + old + "_x\n"},
			wantOut: `cmd/deedglass/main.go:3: retired identifier "` + old + `". Rename it to deedglass (docs/NAMING.md).`},
		{name: "old module name", files: map[string]string{"go.mod": "module github.com/testa" + "mur/x\n"},
			wantOut: `go.mod:1: retired identifier "testa` + `mur"`},
		{name: "upper-case env name", files: map[string]string{"x.sh": "export " + strings.ToUpper(old) + "_PG=18\n"},
			wantOut: `x.sh:1: retired identifier "` + old + `"`},
		{name: "workspace paths are allowed", wantOK: true, files: map[string]string{
			"README.md": "See ~/" + old + "/ops/adr/ and $HOME/" + old + "/product and /Users/someone/" + old + "/ops.\n"}},
		{name: "a longer word is not the token", wantOK: true, files: map[string]string{"a.txt": old + "1 x" + old + " " + old + "abc\n"}},
		{name: "allow entry", wantOK: true, allow: "docs/NAMING.md::" + old + "::the rename map\n",
			files: map[string]string{"docs/NAMING.md": "| " + old + " | deedglass |\n"}},
		{name: "allow entry without a reason is malformed",
			allow: "docs/NAMING.md::" + old + "::\n", files: map[string]string{"docs/NAMING.md": "| " + old + " |\n"},
			wantOut: "malformed allow entry"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			write := func(rel, body string) {
				p := filepath.Join(dir, rel)
				if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			write("scripts/rename_guard.sh", string(script))
			os.Chmod(filepath.Join(dir, "scripts/rename_guard.sh"), 0o755)
			for rel, body := range c.files {
				write(rel, body)
			}
			if c.allow != "" {
				write(".rename-allow", c.allow)
			}
			if out, err := exec.Command("git", "-C", dir, "init", "-q").CombinedOutput(); err != nil {
				t.Fatalf("git init: %v %s", err, out)
			}
			out, err := exec.Command("bash", filepath.Join(dir, "scripts/rename_guard.sh")).CombinedOutput()
			if c.wantOK != (err == nil) {
				t.Fatalf("ok=%v, want %v\n%s", err == nil, c.wantOK, out)
			}
			if c.wantOK && !strings.Contains(string(out), "rename-guard: OK") {
				t.Fatalf("output %s", out)
			}
			if c.wantOut != "" && !strings.Contains(string(out), c.wantOut) {
				t.Fatalf("output %q, want it to contain %q", out, c.wantOut)
			}
		})
	}
}
