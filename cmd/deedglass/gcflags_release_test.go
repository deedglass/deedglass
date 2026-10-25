package main

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// TestReleaseGcflags: .goreleaser.yaml and the Dockerfile build with scripts/gcflags.sh's flags, and
// every package those flags name is linked into the binary (a renamed package would otherwise
// silently drop out of the list).
func TestReleaseGcflags(t *testing.T) {
	out, err := exec.Command("sh", "../../scripts/gcflags.sh").Output()
	if err != nil {
		t.Fatal(err)
	}
	flags := strings.Fields(string(out))
	if len(flags) == 0 {
		t.Fatal("scripts/gcflags.sh printed nothing")
	}
	gr, err := os.ReadFile("../../.goreleaser.yaml")
	if err != nil {
		t.Fatal(err)
	}
	want := "    flags:\n      - -trimpath\n"
	for _, f := range flags {
		want += "      - " + f + "\n"
	}
	if !strings.Contains(string(gr), want) {
		t.Errorf(".goreleaser.yaml's build flags differ from -trimpath plus scripts/gcflags.sh:\n%s", want)
	}
	df, err := os.ReadFile("../../Dockerfile")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(df), "go build -trimpath $(sh scripts/gcflags.sh) ") {
		t.Error("the Dockerfile's go build doesn't use scripts/gcflags.sh")
	}
	// Release binaries are stripped (-s -w: no symbol table or DWARF, a smaller download) and carry
	// no local paths (-trimpath, above). Make's build and the MCPB bundles do the same.
	if !strings.Contains(string(gr), "    ldflags:\n      - -s -w\n") {
		t.Error(".goreleaser.yaml's ldflags don't start with -s -w")
	}
	if !strings.Contains(string(df), `-ldflags "-s -w`) {
		t.Error("the Dockerfile's go build isn't stripped (-ldflags \"-s -w …\")")
	}
	for _, f := range []string{"../../Makefile", "../../scripts/mcpb.sh"} {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(b), "-trimpath") || !strings.Contains(string(b), "-s -w") {
			t.Errorf("%s builds without -trimpath and -s -w", f)
		}
	}
	deps, err := exec.Command("go", "list", "-deps", ".").Output()
	if err != nil {
		t.Skipf("go list unavailable: %v", err)
	}
	linked := "\n" + string(deps)
	for _, f := range flags {
		pat := strings.TrimSuffix(strings.TrimPrefix(f, "-gcflags="), "=-l")
		if p, ok := strings.CutSuffix(pat, "/..."); ok {
			if !strings.Contains(linked, "\n"+p+"\n") && !strings.Contains(linked, "\n"+p+"/") {
				t.Errorf("%s matches no linked package", pat)
			}
		} else if !strings.Contains(linked, "\n"+pat+"\n") {
			t.Errorf("%s isn't linked into deedglass", pat)
		}
	}
}
