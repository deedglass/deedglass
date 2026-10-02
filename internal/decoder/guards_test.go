package decoder

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

// The decoder's "must never happen" list (S20 §7) as tests.

func repoRoot(t *testing.T) string {
	t.Helper()
	return filepath.Join("..", "..")
}

// productFiles walks internal/ and cmd/ for non-test files with one of exts.
func productFiles(t *testing.T, exts ...string) []string {
	t.Helper()
	var out []string
	for _, top := range []string{"internal", "cmd", "pkg"} {
		root := filepath.Join(repoRoot(t), top)
		_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			if strings.HasSuffix(p, "_test.go") || strings.Contains(p, string(filepath.Separator)+"testdata"+string(filepath.Separator)) {
				return nil
			}
			for _, e := range exts {
				if strings.HasSuffix(p, e) {
					out = append(out, p)
				}
			}
			return nil
		})
	}
	return out
}

// TestMarkerLSNNeverComparedWithCommitLSN (ADR-003 point 5): no binary expression in product Go
// code has a marker LSN on one side and a commit LSN on the other, and no SQL compares the columns.
func TestMarkerLSNNeverComparedWithCommitLSN(t *testing.T) {
	name := func(e ast.Expr) string {
		switch x := e.(type) {
		case *ast.SelectorExpr:
			return x.Sel.Name
		case *ast.Ident:
			return x.Name
		case *ast.CallExpr:
			if s, ok := x.Fun.(*ast.SelectorExpr); ok {
				return s.Sel.Name + "()"
			}
		}
		return ""
	}
	isMarker := func(s string) bool { return strings.Contains(strings.ToLower(s), "markerlsn") }
	isCommit := func(s string) bool { return strings.Contains(strings.ToLower(s), "commitlsn") }
	cmp := map[token.Token]bool{token.EQL: true, token.NEQ: true, token.LSS: true, token.GTR: true, token.LEQ: true, token.GEQ: true}
	files := productFiles(t, ".go")
	if len(files) < 100 {
		t.Fatalf("only %d files found; is the walk rooted right?", len(files))
	}
	fset := token.NewFileSet()
	for _, p := range files {
		f, err := parser.ParseFile(fset, p, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			be, ok := n.(*ast.BinaryExpr)
			if !ok || !cmp[be.Op] {
				return true
			}
			l, r := name(be.X), name(be.Y)
			if (isMarker(l) && isCommit(r)) || (isCommit(l) && isMarker(r)) {
				t.Errorf("%s: compares %s with %s", fset.Position(be.Pos()), l, r)
			}
			return true
		})
	}
	sqlCmp := regexp.MustCompile(`(?i)marker_lsn\s*(=|<>|!=|<|>|<=|>=)\s*[a-z_.]*commit_lsn|commit_lsn\s*(=|<>|!=|<|>|<=|>=)\s*[a-z_.]*marker_lsn`)
	for _, p := range productFiles(t, ".go", ".sql") {
		b, _ := os.ReadFile(p)
		if sqlCmp.Match(b) {
			t.Errorf("%s compares marker_lsn with commit_lsn in SQL", p)
		}
	}
}

// TestRawHasNoXid (ADR-002 C): the peek's xid column never reaches the decoder.
func TestRawHasNoXid(t *testing.T) {
	rt := reflect.TypeOf(Raw{})
	if rt.NumField() != 2 || rt.Field(0).Name != "LSN" || rt.Field(1).Name != "Data" {
		t.Fatalf("Raw is %v, want exactly {LSN, Data}", rt)
	}
	b, _ := os.ReadFile("session.go")
	if regexp.MustCompile(`(?i)select\s+[^;]*\bxid\b[^;]*pg_logical_slot_peek`).Match(b) {
		t.Fatal("the peek statement reads the xid column")
	}
}

// TestEmitMessageThreeArguments (spike P1): PG 16 has only the three-argument form, so product
// code never calls the four-argument one.
func TestEmitMessageThreeArguments(t *testing.T) {
	call := regexp.MustCompile(`pg_logical_emit_message\s*\(`)
	n := 0
	for _, p := range productFiles(t, ".go", ".sql") {
		b, _ := os.ReadFile(p)
		s := string(b)
		for _, loc := range call.FindAllStringIndex(s, -1) {
			depth, commas, i := 1, 0, loc[1]
			for ; i < len(s) && depth > 0; i++ {
				switch s[i] {
				case '(':
					depth++
				case ')':
					depth--
				case ',':
					if depth == 1 {
						commas++
					}
				}
			}
			args := s[loc[1] : i-1]
			first := strings.ToLower(strings.TrimSpace(args))
			if !strings.HasPrefix(first, "true") && !strings.HasPrefix(first, "false") {
				continue // prose, or a signature in a REVOKE/GRANT: not a call
			}
			n++
			if commas != 2 {
				t.Errorf("%s: pg_logical_emit_message(%s) has %d arguments, want 3", p, args, commas+1)
			}
		}
	}
	if n < 2 {
		t.Fatalf("found only %d calls; begin_action and the heartbeat both call it", n)
	}
}

// TestNoVerdictWords: the decoder never decides a verdict (S20 §5): no verdict word in its
// non-test code.
func TestNoVerdictWords(t *testing.T) {
	ents, _ := os.ReadDir(".")
	for _, e := range ents {
		if !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		b, _ := os.ReadFile(e.Name())
		for _, w := range []string{"VERIFIED", "MISMATCH", "UNVERIFIABLE", "witness.Decide", "VerdictVerified"} {
			if strings.Contains(string(b), w) {
				t.Errorf("%s mentions %s: the decoder never decides a verdict", e.Name(), w)
			}
		}
	}
}
