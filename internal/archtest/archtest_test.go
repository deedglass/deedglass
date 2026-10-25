package archtest

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"
)

const module = "github.com/deedglass/deedglass"

type pkg struct {
	ImportPath string
	Standard   bool
	Deps       []string
	Imports    []string
}

// listDeps runs go list -deps -json over the module and returns every non-test package it reports.
func listDeps(t *testing.T) map[string]pkg { return listDepsIn(t, "../..") }

// listDepsIn is listDeps for the module rooted at dir (a fixture module in testdata, too).
func listDepsIn(t *testing.T, dir string) map[string]pkg {
	t.Helper()
	gobin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go is not on PATH")
	}
	cmd := exec.Command(gobin, "list", "-deps", "-json", module+"/...")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GOWORK=off")
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		t.Fatalf("go list: %v\n%s", err, errb.String())
	}
	all := map[string]pkg{}
	dec := json.NewDecoder(&out)
	for {
		var p pkg
		if err := dec.Decode(&p); err == io.EOF {
			break
		} else if err != nil {
			t.Fatal(err)
		}
		all[p.ImportPath] = p
	}
	return all
}

// nonStd returns p's transitive dependencies outside the standard library.
func nonStd(all map[string]pkg, p pkg) []string {
	var out []string
	for _, d := range p.Deps {
		if dp, ok := all[d]; ok && dp.Standard {
			continue
		}
		if !strings.Contains(strings.SplitN(d, "/", 2)[0], ".") {
			continue // standard library (vendored golang.org/x inside std shows up as vendor/…)
		}
		out = append(out, d)
	}
	return out
}

func TestLayering(t *testing.T) {
	all := listDeps(t)
	get := func(rel string) (pkg, bool) {
		p, ok := all[module+"/"+rel]
		return p, ok
	}

	// ids, buildinfo, the terminal mark and the vault's key-ring parser: standard library only.
	for _, rel := range []string{"internal/ids", "internal/buildinfo", "internal/cli/mark", "internal/vault/keyring"} {
		p, ok := get(rel)
		if !ok {
			t.Fatalf("%s not found", rel)
		}
		if deps := nonStd(all, p); len(deps) > 0 {
			t.Errorf("%s must import only the standard library, got %v", rel, deps)
		}
	}

	// config: standard library plus the YAML library, and (S16) two leaves it validates with: the
	// vault's key-ring parser and the egress classifier, which itself imports only buildinfo.
	configLeaves := map[string]bool{"go.yaml.in/yaml/v3": true, module + "/internal/vault/keyring": true,
		module + "/internal/connector/egress": true, module + "/internal/buildinfo": true}
	if p, ok := get("internal/config"); !ok {
		t.Fatal("internal/config not found")
	} else {
		for _, d := range nonStd(all, p) {
			if !configLeaves[d] {
				t.Errorf("internal/config may import only the standard library, go.yaml.in/yaml/v3, internal/vault/keyring and internal/connector/egress, got %s", d)
			}
		}
	}
	if p, ok := get("internal/connector/egress"); !ok {
		t.Fatal("internal/connector/egress not found")
	} else {
		for _, d := range nonStd(all, p) {
			if d != module+"/internal/buildinfo" {
				t.Errorf("internal/connector/egress may import only the standard library and internal/buildinfo, got %s", d)
			}
		}
	}

	// The pure packages. Vacuous while they are empty, and never failing on an empty package. The
	// record exporters (TY-06) are here too: an export runs offline, from the records alone. So is
	// the foreign change-feed witness (TY-05): it reads events it is given and connects to nothing.
	forbidden := []string{"net/http", "database/sql", "github.com/jackc/", module + "/internal/store", module + "/internal/httpx"}
	for _, rel := range []string{"internal/deed", "internal/sqlguard", "internal/policy", "internal/witness", "internal/witness/l2", "internal/witness/feed", "internal/witness/feed/debezium", "internal/fhirexport", "internal/recordexport", "internal/lockdown"} {
		p, ok := get(rel)
		if !ok {
			continue
		}
		for _, d := range p.Deps {
			for _, f := range forbidden {
				if d == f || (strings.HasSuffix(f, "/") && strings.HasPrefix(d, f)) || strings.HasPrefix(d, f+"/") {
					t.Errorf("%s must stay pure, but depends on %s", rel, d)
				}
			}
		}
	}

	// httpx defines interfaces (IdemStore, HealthSource, authenticators) and store implements them;
	// cmd/deedglass wires them. Neither imports the other (S03).
	for rel, bad := range map[string]string{"internal/httpx": module + "/internal/store", "internal/store": module + "/internal/httpx"} {
		if p, ok := get(rel); ok {
			for _, d := range p.Deps {
				if d == bad || strings.HasPrefix(d, bad+"/") {
					t.Errorf("%s must not depend on %s", rel, d)
				}
			}
		}
	}

	// HC-P02 (ADR-105, DL-109): the FHIR connector is an adapter. The core (Gate, Witness, deeds,
	// policy) imports nothing from it, and it imports no third-party package (no vendor SDK).
	fhirPkg := module + "/internal/connector/fhir"
	for path, p := range all {
		core := false
		for _, c := range []string{"internal/gate", "internal/witness", "internal/deed", "internal/policy"} {
			core = core || path == module+"/"+c || strings.HasPrefix(path, module+"/"+c+"/")
		}
		if !core {
			continue
		}
		for _, d := range p.Deps {
			if d == fhirPkg || strings.HasPrefix(d, fhirPkg+"/") {
				t.Errorf("%s imports %s; the core never imports a protocol adapter", path, d)
			}
		}
	}
	if p, ok := get("internal/connector/fhir"); !ok {
		t.Fatal("internal/connector/fhir not found")
	} else {
		for _, d := range p.Imports {
			if strings.Contains(strings.SplitN(d, "/", 2)[0], ".") && !strings.HasPrefix(d, module+"/") {
				t.Errorf("internal/connector/fhir may import only the standard library and this module, got %s", d)
			}
		}
	}

	// Nothing in internal/ imports cmd/.
	for path, p := range all {
		if !strings.HasPrefix(path, module+"/internal/") {
			continue
		}
		for _, d := range p.Deps {
			if strings.HasPrefix(d, module+"/cmd/") {
				t.Errorf("%s imports %s; internal packages never import cmd/", path, d)
			}
		}
	}
}

// TestRules checks the rule matcher itself, so a vacuous pass can't hide a broken check.
func TestRules(t *testing.T) {
	all := map[string]pkg{
		"net/http":           {ImportPath: "net/http", Standard: true},
		"go.yaml.in/yaml/v3": {ImportPath: "go.yaml.in/yaml/v3"},
	}
	p := pkg{Deps: []string{"net/http", "go.yaml.in/yaml/v3", "vendor/golang.org/x/net/http2/hpack"}}
	if got := nonStd(all, p); len(got) != 1 || got[0] != "go.yaml.in/yaml/v3" {
		t.Fatalf("nonStd = %v", got)
	}
}
