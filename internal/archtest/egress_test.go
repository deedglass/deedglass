package archtest

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// goFiles parses every non-test .go file under the module's code directories.
func goFiles(t *testing.T) map[string]*ast.File {
	t.Helper()
	root := filepath.Join("..", "..")
	out := map[string]*ast.File{}
	fset := token.NewFileSet()
	for _, dir := range []string{"internal", "cmd", "pkg", "examples", "attack", "bench"} {
		base := filepath.Join(root, dir)
		err := filepath.WalkDir(base, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil // a missing directory is fine
			}
			if d.IsDir() {
				if n := d.Name(); n == "node_modules" || n == "testdata" || strings.HasPrefix(n, ".") {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
			if err != nil {
				t.Fatalf("parse %s: %v", path, err)
			}
			rel, _ := filepath.Rel(root, path)
			out[filepath.ToSlash(rel)] = f
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(out) < 100 {
		t.Fatalf("found only %d Go files; is the walk rooted right?", len(out))
	}
	return out
}

// importName is the local name a file uses for path, or "".
func importName(f *ast.File, path string) string {
	for _, im := range f.Imports {
		if strings.Trim(im.Path.Value, `"`) == path {
			if im.Name != nil {
				return im.Name.Name
			}
			return path[strings.LastIndex(path, "/")+1:]
		}
	}
	return ""
}

// TestSingleEgressDoor (S16 invariant 3): outside internal/connector/egress, no non-test code
// builds an http.Client or an http.Transport, dials on its own, or uses the package-level
// clients (http.DefaultClient, http.DefaultTransport, http.Get, Post, Head, PostForm).
func TestSingleEgressDoor(t *testing.T) {
	allowed := map[string]bool{"internal/connector/egress": true}
	banned := map[string]map[string]bool{
		"net/http":   {"Client": true, "Transport": true, "DefaultClient": true, "DefaultTransport": true, "Get": true, "Post": true, "Head": true, "PostForm": true},
		"net":        {"Dialer": true, "Dial": true, "DialTimeout": true, "DialTCP": true, "DialUDP": true, "DialIP": true},
		"crypto/tls": {"Dial": true, "DialWithDialer": true},
	}
	var hits []string
	for path, f := range goFiles(t) {
		if allowed[filepath.ToSlash(filepath.Dir(path))] {
			continue
		}
		names := map[string]string{}
		for pkg := range banned {
			if n := importName(f, pkg); n != "" && n != "_" {
				names[n] = pkg
			}
		}
		if len(names) == 0 {
			continue
		}
		ast.Inspect(f, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			id, ok := sel.X.(*ast.Ident)
			if !ok {
				return true
			}
			if pkg, ok := names[id.Name]; ok && banned[pkg][sel.Sel.Name] {
				// http.Client and http.Transport as types (fields, parameters) are fine; building
				// one is not. A composite literal or new() is what builds one.
				if pkg == "net/http" && (sel.Sel.Name == "Client" || sel.Sel.Name == "Transport") {
					return true
				}
				hits = append(hits, path+": "+pkg+"."+sel.Sel.Name)
			}
			return true
		})
		ast.Inspect(f, func(n ast.Node) bool {
			var typ ast.Expr
			switch x := n.(type) {
			case *ast.CompositeLit:
				typ = x.Type
			case *ast.CallExpr:
				if id, ok := x.Fun.(*ast.Ident); ok && id.Name == "new" && len(x.Args) == 1 {
					typ = x.Args[0]
				}
			}
			if sel, ok := typ.(*ast.SelectorExpr); ok {
				if id, ok := sel.X.(*ast.Ident); ok && names[id.Name] == "net/http" && (sel.Sel.Name == "Client" || sel.Sel.Name == "Transport") {
					hits = append(hits, path+": builds an http."+sel.Sel.Name)
				}
			}
			return true
		})
	}
	sort.Strings(hits)
	for _, h := range hits {
		t.Errorf("%s (use internal/connector/egress: Policy.Client, FirstParty or Wrap)", h)
	}
}

// TestOpenerCallSites (S16 invariant 2): vault.Opener.Open is called only from
// internal/connector (Instance.Secret) and the vault itself. Handlers hold a vault.Writer.
func TestOpenerCallSites(t *testing.T) {
	allowed := map[string]bool{"internal/connector": true, "internal/vault": true}
	var hits []string
	for path, f := range goFiles(t) {
		if allowed[filepath.ToSlash(filepath.Dir(path))] {
			continue
		}
		vaultName := importName(f, "github.com/deedglass/deedglass/internal/vault")
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			// vault.Open(…) and any x.Open(ctx, vault.Ref{…}) shaped call.
			if id, ok := sel.X.(*ast.Ident); ok && vaultName != "" && id.Name == vaultName && (sel.Sel.Name == "Open" || sel.Sel.Name == "Rewrap") {
				hits = append(hits, path+": "+vaultName+"."+sel.Sel.Name)
			}
			if sel.Sel.Name == "Open" && len(call.Args) == 2 {
				if cl, ok := call.Args[1].(*ast.CompositeLit); ok {
					if ts, ok := cl.Type.(*ast.SelectorExpr); ok && ts.Sel.Name == "Ref" {
						hits = append(hits, path+": Open(ctx, vault.Ref{…})")
					}
				}
			}
			return true
		})
		// Holding an Opener at all is the smell: only the registry wiring may name the type.
		ast.Inspect(f, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			if id, ok := sel.X.(*ast.Ident); ok && vaultName != "" && id.Name == vaultName && sel.Sel.Name == "Opener" {
				hits = append(hits, path+": names vault.Opener")
			}
			return true
		})
	}
	sort.Strings(hits)
	for _, h := range hits {
		t.Errorf("%s: only internal/connector and the vault open credentials", h)
	}
}
