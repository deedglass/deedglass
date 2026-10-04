package witness

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// verdictGuardAllow lists files outside the witness package that may produce the string
// "VERIFIED" (or witness.Verified) today, each with its reason. Keep it short; never add a file
// that decides a verdict.
var verdictGuardAllow = map[string]string{
	"internal/gate/witness.go":          "presentation constants (VerdictVerified = string(witness.Verified)) the Gate and CLI compare with; the verdict comes from Decide",
	"internal/deed/body.go":             "validates a deed's verdict field against the three verdict words; it decides nothing",
	"internal/deed/deedtest/samples.go": "fixed sample deeds for tests and the spec vectors",
	"internal/ledger/filter.go":         "the list of verdict words a ledger filter accepts; it decides nothing",
	"internal/ledger/fake/fake.go":      "the demo ledger's fake data; never a real action",
	"internal/ledger/fake/run.go":       "the demo ledger's fake amendments; never a real action",
	"cmd/deedglass/verify.go":           "presentation: counts deeds per verdict word in the CLI output",
	"cmd/deedglass/insights.go":         "presentation: the stats table's column header for counts the database already holds",
	"internal/witness/precedence.go":    "Decide: the one function that produces VERIFIED",
	"internal/witness/verdict.go":       "declares the Verified constant",
}

const witnessImport = "github.com/deedglass/deedglass/internal/witness"

// guardHit is one place that produces VERIFIED.
type guardHit struct {
	Pos  token.Position
	What string
}

// verdictGuard reports every place in one file that produces witness.Verified (or bare Verified in
// package witness) or the string "VERIFIED": an assignment or declaration value, a return value,
// a composite-literal element or value, a call argument or a sent value. Parentheses and
// conversions (string(witness.Verified)) are looked through. Comparisons (== and !=), switch
// cases, map keys and index lookups are allowed, and so is anything else that only reads it.
func verdictGuard(fset *token.FileSet, f *ast.File) []guardHit {
	inWitness := f.Name.Name == "witness"
	alias := ""
	for _, imp := range f.Imports {
		p, _ := strconv.Unquote(imp.Path.Value)
		if p != witnessImport {
			continue
		}
		alias = "witness"
		if imp.Name != nil {
			alias = imp.Name.Name
		}
	}
	isVerified := func(n ast.Node, parent ast.Node) (string, bool) {
		switch x := n.(type) {
		case *ast.BasicLit:
			if x.Kind == token.STRING {
				if s, err := strconv.Unquote(x.Value); err == nil && s == "VERIFIED" {
					return `"VERIFIED"`, true
				}
			}
		case *ast.SelectorExpr:
			if id, ok := x.X.(*ast.Ident); ok && alias != "" && id.Name == alias && x.Sel.Name == "Verified" {
				return alias + ".Verified", true
			}
		case *ast.Ident:
			if sel, ok := parent.(*ast.SelectorExpr); ok && sel.Sel == x {
				return "", false // a field or another package's Verified (gate.Verified is a state)
			}
			if inWitness && x.Name == "Verified" {
				return "Verified", true
			}
		}
		return "", false
	}
	isConversion := func(c *ast.CallExpr) bool {
		if len(c.Args) != 1 {
			return false
		}
		switch fn := c.Fun.(type) {
		case *ast.Ident:
			return fn.Name == "string" || fn.Name == "Verdict"
		case *ast.SelectorExpr:
			return fn.Sel.Name == "Verdict"
		}
		return false
	}
	contains := func(list []ast.Expr, e ast.Expr) bool {
		for _, x := range list {
			if x == e {
				return true
			}
		}
		return false
	}
	var hits []guardHit
	var stack []ast.Node
	ast.Inspect(f, func(n ast.Node) bool {
		if n == nil {
			stack = stack[:len(stack)-1]
			return true
		}
		var parent ast.Node
		if len(stack) > 0 {
			parent = stack[len(stack)-1]
		}
		stack = append(stack, n)
		what, ok := isVerified(n, parent)
		if !ok {
			return true
		}
		// Climb through parentheses and conversions to the expression that uses the value.
		e, i := n.(ast.Expr), len(stack)-2
		for ; i >= 0; i-- {
			if p, ok := stack[i].(*ast.ParenExpr); ok && p.X == e {
				e = p
				continue
			}
			if c, ok := stack[i].(*ast.CallExpr); ok && isConversion(c) && c.Args[0] == e {
				e = c
				continue
			}
			break
		}
		if i < 0 {
			return true
		}
		ctx := ""
		switch p := stack[i].(type) {
		case *ast.AssignStmt:
			if contains(p.Rhs, e) {
				ctx = "an assigned value"
			}
		case *ast.ValueSpec:
			if contains(p.Values, e) {
				ctx = "a declared value"
			}
		case *ast.ReturnStmt:
			ctx = "a return value"
		case *ast.CompositeLit:
			ctx = "a composite-literal element"
		case *ast.KeyValueExpr:
			if p.Value == e {
				ctx = "a composite-literal value" // the key is a lookup, allowed
			}
		case *ast.CallExpr:
			if contains(p.Args, e) {
				ctx = "a call argument"
			}
		case *ast.SendStmt:
			if p.Value == e {
				ctx = "a sent value"
			}
		}
		if ctx != "" {
			hits = append(hits, guardHit{Pos: fset.Position(n.Pos()), What: what + " as " + ctx})
		}
		return true
	})
	return hits
}

// moduleRoot walks up from the package directory to go.mod.
func moduleRoot(t testing.TB) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		up := filepath.Dir(dir)
		if up == dir {
			t.Fatal("go.mod not found above the package directory")
		}
		dir = up
	}
}

func TestVerdictGuard(t *testing.T) {
	root := moduleRoot(t)
	fset := token.NewFileSet()
	files := 0
	used := map[string]bool{}
	var bad []string
	for _, top := range []string{"internal", "cmd"} {
		err := filepath.WalkDir(filepath.Join(root, top), func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			name := d.Name()
			if d.IsDir() {
				if name == "testdata" || name == "node_modules" || name == "vendor" || (strings.HasPrefix(name, ".") && path != root) {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
				return nil
			}
			rel, _ := filepath.Rel(root, path)
			rel = filepath.ToSlash(rel)
			f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
			if err != nil {
				// A file mid-edit by someone else shouldn't hide the rest of the sweep.
				t.Logf("skipped %s: %v", rel, err)
				return nil
			}
			files++
			hits := verdictGuard(fset, f)
			if len(hits) == 0 {
				return nil
			}
			if why, ok := verdictGuardAllow[rel]; ok {
				used[rel] = true
				t.Logf("allowed: %s (%d uses): %s", rel, len(hits), why)
				return nil
			}
			for _, h := range hits {
				bad = append(bad, rel+":"+strconv.Itoa(h.Pos.Line)+": "+h.What)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if files < 20 {
		t.Fatalf("only %d files parsed; the walk is broken", files)
	}
	sort.Strings(bad)
	for _, b := range bad {
		t.Errorf("%s: only Decide (internal/witness/precedence.go) produces VERIFIED; compare with it, or ask Decide", b)
	}
	// Allowlist entries that produce nothing any more: log them so the list shrinks.
	var stale []string
	for rel := range verdictGuardAllow {
		if !used[rel] && !strings.HasPrefix(rel, "internal/witness/") {
			stale = append(stale, rel)
		}
	}
	sort.Strings(stale)
	for _, s := range stale {
		t.Logf("allowlist entry %s no longer produces VERIFIED; remove it", s)
	}
}

func TestVerdictGuardCatches(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, filepath.Join("testdata", "guard", "bad.go.txt"), nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	hits := verdictGuard(fset, f)
	got := map[int]bool{}
	for _, h := range hits {
		got[h.Pos.Line] = true
	}
	// Every line marked "// want" in the file must be reported, and nothing else.
	src, err := os.ReadFile(filepath.Join("testdata", "guard", "bad.go.txt"))
	if err != nil {
		t.Fatal(err)
	}
	want := map[int]bool{}
	for i, line := range strings.Split(string(src), "\n") {
		if strings.HasSuffix(strings.TrimSpace(line), "// want") {
			want[i+1] = true
		}
	}
	if len(want) < 6 {
		t.Fatalf("bad.go.txt marks only %d lines", len(want))
	}
	for l := range want {
		if !got[l] {
			t.Errorf("bad.go.txt:%d isn't reported", l)
		}
	}
	for _, h := range hits {
		if !want[h.Pos.Line] {
			t.Errorf("bad.go.txt:%d reported but allowed: %s", h.Pos.Line, h.What)
		}
	}

	// Inside package witness, a bare Verified counts too.
	g, err := parser.ParseFile(fset, "inline.go", "package witness\n\nfunc f() Verdict {\n\treturn Verified\n}\n\nfunc g(v Verdict) bool { return v == Verified }\n", 0)
	if err != nil {
		t.Fatal(err)
	}
	if hs := verdictGuard(fset, g); len(hs) != 1 || hs[0].Pos.Line != 4 {
		t.Errorf("package witness: got %v", hs)
	}
}
