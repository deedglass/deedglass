package learn

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
)

// S28 §3.6 "Never enforce, three ways": the type-level and source-level guards. (The third, a
// 30-day scheduler run whose every enforce activation has a person behind it, is an integration
// test.)

// TestNeverEnforceMethodSet pins PolicyWriter to exactly two methods: Insights can add a version
// and run it in shadow, and has no way to enforce anything.
func TestNeverEnforceMethodSet(t *testing.T) {
	t.Parallel()
	typ := reflect.TypeOf((*PolicyWriter)(nil)).Elem()
	var names []string
	for i := range typ.NumMethod() {
		names = append(names, typ.Method(i).Name)
	}
	slices.Sort(names)
	if want := []string{"ActivateShadow", "AddSuggestedVersion"}; !slices.Equal(names, want) {
		t.Fatalf("PolicyWriter has methods %v, want exactly %v (S28 §3.6: no way to enforce)", names, want)
	}
	for _, n := range names {
		if strings.Contains(strings.ToLower(n), "enforce") || strings.Contains(strings.ToLower(n), "promote") {
			t.Fatalf("method %s", n)
		}
	}
}

// enforceViolations lists what the source scan refuses in one parsed file: a reference to
// policy.ModeEnforce, or a call of a function or method named exactly Activate.
func enforceViolations(fset *token.FileSet, f *ast.File) []string {
	var out []string
	ast.Inspect(f, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.SelectorExpr:
			if id, ok := x.X.(*ast.Ident); ok && id.Name == "policy" && x.Sel.Name == "ModeEnforce" {
				out = append(out, fset.Position(x.Pos()).String()+": policy.ModeEnforce")
			}
		case *ast.CallExpr:
			switch fn := x.Fun.(type) {
			case *ast.Ident:
				if fn.Name == "Activate" {
					out = append(out, fset.Position(x.Pos()).String()+": call of Activate")
				}
			case *ast.SelectorExpr:
				if fn.Sel.Name == "Activate" {
					out = append(out, fset.Position(x.Pos()).String()+": call of ."+fn.Sel.Name)
				}
			case *ast.IndexExpr: // Activate[T](…)
				if id, ok := fn.X.(*ast.Ident); ok && id.Name == "Activate" {
					out = append(out, fset.Position(x.Pos()).String()+": call of Activate")
				}
			}
		}
		return true
	})
	return out
}

// TestNeverEnforceSource parses every non-test Go file of internal/learn (this directory only:
// internal/learn/sim activates its own simulated org's policy and is outside the rule) and fails
// on any reference to policy.ModeEnforce or any call of a method named Activate.
func TestNeverEnforceSource(t *testing.T) {
	t.Parallel()
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	scanned := 0
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		src, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		f, err := parser.ParseFile(fset, name, src, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		scanned++
		for _, v := range enforceViolations(fset, f) {
			t.Errorf("S28 §3.6: Insights never enforces: %s", v)
		}
	}
	if scanned < 20 {
		t.Fatalf("scanned only %d files; is the test running in internal/learn?", scanned)
	}
	for _, need := range []string{"lifecycle.go", "ports.go", "suggest.go", "schedule.go"} {
		if !slices.Contains(files, need) {
			t.Fatalf("%s not scanned", need)
		}
	}
}

// The scanner catches what it is meant to catch.
func TestNeverEnforceScannerSelfTest(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name, src string
		hits      int
	}{
		{"mode constant", `package x
import "github.com/deedglass/deedglass/internal/policy"
var m = policy.ModeEnforce`, 1},
		{"activate method", `package x
func f(s interface{ Activate(int) error }) { _ = s.Activate(3) }`, 1},
		{"activate func", `package x
func Activate() {}
func g() { Activate() }`, 1},
		{"generic activate", `package x
func Activate[T any]() {}
func g() { Activate[int]() }`, 1},
		{"both", `package x
import "github.com/deedglass/deedglass/internal/policy"
func f(s interface{ Activate(policy.Mode) }) { s.Activate(policy.ModeEnforce) }`, 2},
		{"shadow is fine", `package x
import "github.com/deedglass/deedglass/internal/policy"
func f(w interface{ ActivateShadow() }) { w.ActivateShadow(); _ = policy.ModeShadow }`, 0},
		{"a declaration is not a call", `package x
type w struct{}
func (w) Activate() {}`, 0},
	}
	for _, c := range cases {
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, c.name+".go", c.src, 0)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if got := enforceViolations(fset, f); len(got) != c.hits {
			t.Errorf("%s: %d hits %v, want %d", c.name, len(got), got, c.hits)
		}
	}
}
