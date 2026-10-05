package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestCompletionGoldens pins the three scripts (go test ./cmd/deedglass -run TestCompletionGoldens
// -update rewrites testdata/completion/). They carry no version, so they are reproducible.
func TestCompletionGoldens(t *testing.T) {
	for _, sh := range []string{"bash", "zsh", "fish"} {
		t.Run(sh, func(t *testing.T) {
			r := cli(t, nil, false, "completion", sh)
			if r.code != 0 || r.stderr != "" {
				t.Fatalf("%+v", r)
			}
			path := filepath.Join("testdata", "completion", "deedglass."+sh)
			if *update {
				_ = os.MkdirAll(filepath.Dir(path), 0o755)
				if err := os.WriteFile(path, []byte(r.stdout), 0o644); err != nil {
					t.Fatal(err)
				}
				return
			}
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if r.stdout != string(want) {
				t.Fatalf("%s drifted from the command table; rerun with -update and review the diff", path)
			}
			if strings.Contains(r.stdout, "0.1.0") {
				t.Fatal("a completion script must not carry a version")
			}
		})
	}
}

// TestCompletionCoversTable: every built, visible command path and each of its flags is in every script.
func TestCompletionCoversTable(t *testing.T) {
	scripts := map[string]string{}
	for _, sh := range []string{"bash", "zsh", "fish"} {
		scripts[sh] = cli(t, nil, false, "completion", sh).stdout
	}
	nodes := complTree()
	for _, c := range commands {
		if !c.Built() || c.Hidden {
			continue
		}
		key := strings.Join(c.Path, " ")
		if _, ok := nodes[key]; !ok {
			t.Errorf("%s missing from the tree", key)
		}
		for sh, s := range scripts {
			if !strings.Contains(s, "|"+key+"|") {
				t.Errorf("%s: %q not completable", sh, key)
			}
			for _, f := range nodes[key].flags {
				if !strings.Contains(s, f.name) {
					t.Errorf("%s: %s %s missing", sh, key, f.dash())
				}
			}
		}
	}
	if r := cli(t, nil, false, "completion", "bash", "extra"); r.code != 2 {
		t.Fatalf("an argument must be a usage error: %+v", r)
	}
}

// TestCompletionScriptsRun: the scripts parse in their shells, and bash completes words, flags and values.
func TestCompletionScriptsRun(t *testing.T) {
	dir := t.TempDir()
	for _, sh := range []string{"bash", "zsh", "fish"} {
		p := filepath.Join(dir, "c."+sh)
		if err := os.WriteFile(p, []byte(cli(t, nil, false, "completion", sh).stdout), 0o600); err != nil {
			t.Fatal(err)
		}
		bin, err := exec.LookPath(sh)
		if err != nil {
			t.Logf("%s not installed; syntax not checked", sh)
			continue
		}
		if out, err := exec.Command(bin, "-n", p).CombinedOutput(); err != nil {
			t.Fatalf("%s -n: %v\n%s", sh, err, out)
		}
	}
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("no bash")
	}
	script := `source "$1"
t() { COMP_WORDS=("$@"); COMP_CWORD=$((${#COMP_WORDS[@]}-1)); COMPREPLY=(); _deedglass; echo "${COMPREPLY[*]}"; }
t deedglass ag
t deedglass agent keys ''
t deedglass scan --format ''
t deedglass version --c
t deedglass completion ''
`
	out, err := exec.Command(bash, "-c", script, "x", filepath.Join(dir, "c.bash")).CombinedOutput()
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	want := "agent\ncreate list roll revoke\nmd html json sarif\n--check\nzsh bash fish\n"
	if string(out) != want {
		t.Fatalf("bash completion:\n%s\nwant\n%s", out, want)
	}
}
