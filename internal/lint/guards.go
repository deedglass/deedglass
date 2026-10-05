package lint

import (
	"bufio"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Guard is a guard id, "G1" to "G10".
type Guard string

// The guards. doc.go says what each one enforces and why.
const (
	G1 Guard = "G1" // no SQL built from values
	G2 Guard = "G2" // the single egress door
	G3 Guard = "G3" // no secrets in logs
	G4 Guard = "G4" // no InsecureSkipVerify
	G5 Guard = "G5" // no math/rand
	// S34, security pass 2.
	G6  Guard = "G6"  // no os/exec outside the marked files
	G7  Guard = "G7"  // no unsafe, no reflect.SliceHeader or StringHeader
	G8  Guard = "G8"  // secrets compared in constant time
	G9  Guard = "G9"  // no text/template for HTML or email bodies
	G10 Guard = "G10" // every http.Server sets ReadHeaderTimeout; no ListenAndServe shortcuts
)

// All lists every guard in order.
var All = []Guard{G1, G2, G3, G4, G5, G6, G7, G8, G9, G10}

// Finding is one guard hit. File is relative to the root Check was given, with forward slashes.
type Finding struct {
	Guard, File string
	Line        int
	Message     string
}

// String formats f as file:line: G#: message.
func (f Finding) String() string {
	return fmt.Sprintf("%s:%d: %s: %s", f.File, f.Line, f.Guard, f.Message)
}

// AllowSQLBuild is the line comment that exempts a file from G1. A reason must follow it.
const AllowSQLBuild = "//deedglass:allow-sql-build"

// AllowMathRand is the line comment that exempts a file from G5: only a seeded, reproducible
// generator that never makes a secret, an id or a token (the dev demo data). A reason must follow.
const AllowMathRand = "//deedglass:allow-math-rand"

// AllowExec exempts a file from G6: it starts processes on purpose (Docker for init, the MCP
// server mcp wrap supervises, a browser). A reason must follow.
const AllowExec = "//deedglass:allow-exec"

// AllowUnsafe exempts a file from G7's unsafe import (a syscall that needs a pointer). A reason
// must follow. reflect.SliceHeader and StringHeader have no exemption.
const AllowUnsafe = "//deedglass:allow-unsafe"

// AllowCompare exempts one comparison from G8: a value whose name sounds secret but is public (a
// deed's prev_hash, a digest a person can recompute). Put it at the end of the line, or on the
// line before, with a reason.
const AllowCompare = "//deedglass:allow-compare"

// AllowTextTemplate exempts a file from G9: its text/template output is plain text that no
// browser renders (the plain-text part of an email). A reason must follow.
const AllowTextTemplate = "//deedglass:allow-text-template"

// marker reports whether f carries the marker with a reason; a bare marker is reported under g.
func (r *repo) marker(f *srcFile, g Guard, marker string) bool {
	marked := false
	for _, cg := range f.ast.Comments {
		for _, c := range cg.List {
			rest, ok := strings.CutPrefix(c.Text, marker)
			if !ok || (rest != "" && rest[0] != ' ' && rest[0] != '\t') {
				continue
			}
			if strings.TrimSpace(rest) == "" {
				r.report(g, f, c.Pos(), "the "+strings.TrimPrefix(marker, "//deedglass:")+" marker needs a reason after it")
				continue
			}
			marked = true
		}
	}
	return marked
}

// egressDir is the one package that may build clients and dial (S16 invariant 3).
const egressDir = "internal/connector/egress"

// skipDirs are directory names Check never enters, at any depth. Hidden directories are skipped too.
var skipDirs = map[string]bool{"vendor": true, ".tmp": true, "node_modules": true, "web": true, "testdata": true}

// Check runs guards (every guard when none is given) over every non-test .go file under root and
// returns the findings sorted by file, line and guard.
func Check(root string, guards ...Guard) ([]Finding, error) {
	if len(guards) == 0 {
		guards = All
	}
	run := map[Guard]bool{}
	for _, g := range guards {
		switch g {
		case G1, G2, G3, G4, G5, G6, G7, G8, G9, G10:
			run[g] = true
		default:
			return nil, fmt.Errorf("lint: unknown guard %q; use G1 to G10", g)
		}
	}
	r, err := load(root)
	if err != nil {
		return nil, err
	}
	for _, f := range r.files {
		if run[G1] {
			r.g1(f)
		}
		if run[G2] {
			r.g2(f)
		}
		if run[G3] {
			r.g3(f)
		}
		if run[G4] {
			r.g4(f)
		}
		if run[G5] {
			r.g5(f)
		}
		if run[G6] {
			r.g6(f)
		}
		if run[G7] {
			r.g7(f)
		}
		if run[G8] {
			r.g8(f)
		}
		if run[G9] {
			r.g9(f)
		}
		if run[G10] {
			r.g10(f)
		}
	}
	sort.Slice(r.out, func(i, j int) bool {
		a, b := r.out[i], r.out[j]
		if a.File != b.File {
			return a.File < b.File
		}
		if a.Line != b.Line {
			return a.Line < b.Line
		}
		if a.Guard != b.Guard {
			return guardIndex(a.Guard) < guardIndex(b.Guard)
		}
		return a.Message < b.Message
	})
	return r.out, nil
}

// srcFile is one parsed file.
type srcFile struct {
	rel     string            // path relative to the root, forward slashes
	dir     string            // directory of rel, "." for the root
	ast     *ast.File         // parsed with comments
	imports map[string]string // local name to import path; "_" and "." imports left out
}

type repo struct {
	fset   *token.FileSet
	module string
	files  []*srcFile
	consts map[string]map[string]ast.Expr // directory to const name to its value (nil when implicit)
	out    []Finding
	seen   map[string]bool
}

func load(root string) (*repo, error) {
	r := &repo{fset: token.NewFileSet(), consts: map[string]map[string]ast.Expr{}, seen: map[string]bool{}}
	r.module = modulePath(root)
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if p != root && (skipDirs[d.Name()] || strings.HasPrefix(d.Name(), ".")) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return nil
		}
		f, err := parser.ParseFile(r.fset, p, nil, parser.ParseComments|parser.SkipObjectResolution)
		if err != nil {
			return fmt.Errorf("lint: parse %s: %w", p, err)
		}
		if ast.IsGenerated(f) {
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		sf := &srcFile{rel: rel, dir: path.Dir(rel), ast: f, imports: map[string]string{}}
		for _, im := range f.Imports {
			ip, _ := strconv.Unquote(im.Path.Value)
			name := ip[strings.LastIndex(ip, "/")+1:]
			if im.Name != nil {
				name = im.Name.Name
			}
			if name != "_" && name != "." {
				sf.imports[name] = ip
			}
		}
		r.files = append(r.files, sf)
		r.collectConsts(sf)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return r, nil
}

// modulePath reads the module line of root/go.mod, or returns "".
func modulePath(root string) string {
	f, err := os.Open(filepath.Join(root, "go.mod"))
	if err != nil {
		return ""
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(sc.Text()), "module "); ok {
			return strings.Trim(strings.TrimSpace(rest), `"`)
		}
	}
	return ""
}

// collectConsts records every const the file declares, at any depth, under its directory.
func (r *repo) collectConsts(f *srcFile) {
	m := r.consts[f.dir]
	if m == nil {
		m = map[string]ast.Expr{}
		r.consts[f.dir] = m
	}
	ast.Inspect(f.ast, func(n ast.Node) bool {
		gd, ok := n.(*ast.GenDecl)
		if !ok || gd.Tok != token.CONST {
			return true
		}
		for _, s := range gd.Specs {
			vs := s.(*ast.ValueSpec)
			for i, id := range vs.Names {
				var v ast.Expr
				if i < len(vs.Values) {
					v = vs.Values[i]
				}
				m[id.Name] = v
			}
		}
		return true
	})
}

func (r *repo) report(g Guard, f *srcFile, pos token.Pos, msg string) {
	line := r.fset.Position(pos).Line
	key := fmt.Sprintf("%s\x00%s\x00%d\x00%s", g, f.rel, line, msg)
	if r.seen[key] {
		return
	}
	r.seen[key] = true
	r.out = append(r.out, Finding{Guard: string(g), File: f.rel, Line: line, Message: msg})
}

// importName is the local name f uses for the import path p, or "".
func (f *srcFile) importName(p string) string {
	for name, ip := range f.imports {
		if ip == p {
			return name
		}
	}
	return ""
}

// ---- G1: no SQL built from values ----

var sqlWord = regexp.MustCompile(`(?i)\b(SELECT|INSERT|UPDATE|DELETE|CREATE|ALTER|DROP|GRANT|REVOKE|TRUNCATE|COPY|SET\s+ROLE|SET\s+LOCAL|WHERE)\b`)

// sqlSinks are the method names that run SQL text (pgx and database/sql).
var sqlSinks = map[string]bool{
	"Exec": true, "Query": true, "QueryRow": true, "Queue": true, "CopyFrom": true,
	"ExecContext": true, "QueryContext": true, "QueryRowContext": true,
}

func (r *repo) g1(f *srcFile) {
	marked := false
	for _, cg := range f.ast.Comments {
		for _, c := range cg.List {
			rest, ok := strings.CutPrefix(c.Text, AllowSQLBuild)
			if !ok || (rest != "" && rest[0] != ' ' && rest[0] != '\t') {
				continue
			}
			if strings.TrimSpace(rest) == "" {
				r.report(G1, f, c.Pos(), "the allow-sql-build marker needs a reason after it")
				continue
			}
			marked = true
		}
	}
	if marked {
		return
	}
	for _, d := range f.ast.Decls {
		switch d := d.(type) {
		case *ast.FuncDecl:
			if d.Body != nil {
				r.g1Body(f, d.Body)
			}
		case *ast.GenDecl:
			ast.Inspect(d, func(n ast.Node) bool {
				if fl, ok := n.(*ast.FuncLit); ok {
					r.g1Body(f, fl.Body)
					return false
				}
				return true
			})
		}
	}
}

// g1Scope is one function body: the pieces assigned to each local name.
type g1Scope struct {
	r      *repo
	f      *srcFile
	fmt    string
	locals map[string][]ast.Expr // nil entries stand for values the guard cannot see
	aug    map[string]bool       // the name has a += assignment
}

func (r *repo) g1Body(f *srcFile, body *ast.BlockStmt) {
	s := &g1Scope{r: r, f: f, fmt: f.importName("fmt"), locals: map[string][]ast.Expr{}, aug: map[string]bool{}}
	ast.Inspect(body, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.AssignStmt:
			switch {
			case x.Tok == token.ADD_ASSIGN && len(x.Lhs) == 1 && len(x.Rhs) == 1:
				if id, ok := x.Lhs[0].(*ast.Ident); ok {
					s.locals[id.Name] = append(s.locals[id.Name], x.Rhs[0])
					s.aug[id.Name] = true
				}
			case x.Tok == token.DEFINE || x.Tok == token.ASSIGN:
				for i, l := range x.Lhs {
					id, ok := l.(*ast.Ident)
					if !ok || id.Name == "_" {
						continue
					}
					var v ast.Expr
					if len(x.Lhs) == len(x.Rhs) {
						v = x.Rhs[i]
					}
					s.locals[id.Name] = append(s.locals[id.Name], v)
				}
			default:
				for _, l := range x.Lhs {
					if id, ok := l.(*ast.Ident); ok {
						s.locals[id.Name] = append(s.locals[id.Name], nil)
					}
				}
			}
		case *ast.GenDecl:
			if x.Tok == token.VAR {
				for _, sp := range x.Specs {
					vs := sp.(*ast.ValueSpec)
					for i, id := range vs.Names {
						if i < len(vs.Values) && len(vs.Values) == len(vs.Names) {
							s.locals[id.Name] = append(s.locals[id.Name], vs.Values[i])
						} else if len(vs.Values) > 0 {
							s.locals[id.Name] = append(s.locals[id.Name], nil)
						}
					}
				}
			}
		case *ast.RangeStmt:
			for _, e := range []ast.Expr{x.Key, x.Value} {
				if id, ok := e.(*ast.Ident); ok {
					s.locals[id.Name] = append(s.locals[id.Name], nil)
				}
			}
		}
		return true
	})
	ast.Inspect(body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || !sqlSinks[sel.Sel.Name] {
			return true
		}
		for _, a := range call.Args {
			if at, how := s.build(a, map[string]bool{}); at != nil {
				r.report(G1, f, at.Pos(), fmt.Sprintf("SQL text built with %s from a non-constant value reaches %s; pass values as $n parameters, or mark the file %s <reason>", how, sel.Sel.Name, AllowSQLBuild))
			}
		}
		return true
	})
}

// isSprint reports whether call is fmt.Sprintf or fmt.Sprint.
func (s *g1Scope) isSprint(e ast.Expr) (*ast.CallExpr, bool) {
	call, ok := e.(*ast.CallExpr)
	if !ok || s.fmt == "" {
		return nil, false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return nil, false
	}
	id, ok := sel.X.(*ast.Ident)
	if !ok || id.Name != s.fmt || (sel.Sel.Name != "Sprintf" && sel.Sel.Name != "Sprint") {
		return nil, false
	}
	return call, true
}

// build returns the node where e is SQL text built from a non-constant value, and how it was built.
func (s *g1Scope) build(e ast.Expr, seen map[string]bool) (ast.Node, string) {
	e = ast.Unparen(e)
	if call, ok := s.isSprint(e); ok {
		sql, nonConst := false, false
		for _, a := range call.Args {
			sql = sql || s.hasSQL(a, map[string]bool{})
			nonConst = nonConst || !s.isConst(a, map[string]bool{})
		}
		if sql && nonConst {
			return call, "fmt." + call.Fun.(*ast.SelectorExpr).Sel.Name
		}
		return nil, ""
	}
	if b, ok := e.(*ast.BinaryExpr); ok && b.Op == token.ADD {
		if s.hasSQL(b, map[string]bool{}) && !s.isConst(b, map[string]bool{}) {
			return b, "+"
		}
		return nil, ""
	}
	id, ok := e.(*ast.Ident)
	if !ok || seen[id.Name] {
		return nil, ""
	}
	pieces, ok := s.locals[id.Name]
	if !ok {
		return nil, ""
	}
	seen[id.Name] = true
	defer delete(seen, id.Name)
	for _, p := range pieces {
		if p == nil {
			continue
		}
		if at, how := s.build(p, seen); at != nil {
			return at, how
		}
	}
	if s.aug[id.Name] {
		sql := false
		for _, p := range pieces {
			sql = sql || (p != nil && s.hasSQL(p, map[string]bool{}))
		}
		if sql {
			for _, p := range pieces {
				if p != nil && !s.isConst(p, map[string]bool{}) {
					return p, "+="
				}
			}
		}
	}
	return nil, ""
}

// isConst reports whether e is made only of literals and named constants.
func (s *g1Scope) isConst(e ast.Expr, seen map[string]bool) bool {
	switch x := ast.Unparen(e).(type) {
	case *ast.BasicLit:
		return true
	case *ast.BinaryExpr:
		return s.isConst(x.X, seen) && s.isConst(x.Y, seen)
	case *ast.Ident:
		if pieces, ok := s.locals[x.Name]; ok {
			if seen[x.Name] {
				return true // a cycle adds nothing new
			}
			seen[x.Name] = true
			defer delete(seen, x.Name)
			for _, p := range pieces {
				if p == nil || !s.isConst(p, seen) {
					return false
				}
			}
			return true
		}
		if _, ok := s.r.consts[s.f.dir][x.Name]; ok {
			return true
		}
		return x.Name == "true" || x.Name == "false" || x.Name == "iota"
	case *ast.SelectorExpr:
		_, ok := s.r.pkgConst(s.f, x)
		return ok
	}
	return false
}

// hasSQL reports whether a string literal or string constant inside e holds an SQL keyword. It
// looks through +, parentheses, local variables, named constants and fmt.Sprint/Sprintf arguments.
func (s *g1Scope) hasSQL(e ast.Expr, seen map[string]bool) bool {
	switch x := ast.Unparen(e).(type) {
	case *ast.BasicLit:
		if x.Kind != token.STRING {
			return false
		}
		v, err := strconv.Unquote(x.Value)
		return err == nil && sqlWord.MatchString(v)
	case *ast.BinaryExpr:
		return x.Op == token.ADD && (s.hasSQL(x.X, seen) || s.hasSQL(x.Y, seen))
	case *ast.CallExpr:
		if _, ok := s.isSprint(x); ok {
			for _, a := range x.Args {
				if s.hasSQL(a, seen) {
					return true
				}
			}
		}
		return false
	case *ast.Ident:
		if seen[x.Name] {
			return false
		}
		seen[x.Name] = true
		defer delete(seen, x.Name)
		if pieces, ok := s.locals[x.Name]; ok {
			for _, p := range pieces {
				if p != nil && s.hasSQL(p, seen) {
					return true
				}
			}
			return false
		}
		if v := s.r.consts[s.f.dir][x.Name]; v != nil {
			return s.r.constSQL(s.f.dir, v, map[string]bool{})
		}
	case *ast.SelectorExpr:
		if v, ok := s.r.pkgConst(s.f, x); ok && v != nil {
			id := x.X.(*ast.Ident)
			return s.r.constSQL(strings.TrimPrefix(s.f.imports[id.Name], s.r.module+"/"), v, map[string]bool{})
		}
	}
	return false
}

// pkgConst resolves pkg.Name to a constant of a package in this module.
func (r *repo) pkgConst(f *srcFile, x *ast.SelectorExpr) (ast.Expr, bool) {
	id, ok := x.X.(*ast.Ident)
	if !ok || r.module == "" {
		return nil, false
	}
	ip, ok := f.imports[id.Name]
	if !ok || !strings.HasPrefix(ip, r.module+"/") {
		return nil, false
	}
	v, ok := r.consts[strings.TrimPrefix(ip, r.module+"/")][x.Sel.Name]
	return v, ok
}

// constSQL reports whether a constant's value, declared in dir, holds an SQL keyword.
func (r *repo) constSQL(dir string, e ast.Expr, seen map[string]bool) bool {
	switch x := ast.Unparen(e).(type) {
	case *ast.BasicLit:
		if x.Kind != token.STRING {
			return false
		}
		v, err := strconv.Unquote(x.Value)
		return err == nil && sqlWord.MatchString(v)
	case *ast.BinaryExpr:
		return x.Op == token.ADD && (r.constSQL(dir, x.X, seen) || r.constSQL(dir, x.Y, seen))
	case *ast.Ident:
		if seen[x.Name] {
			return false
		}
		seen[x.Name] = true
		if v := r.consts[dir][x.Name]; v != nil {
			return r.constSQL(dir, v, seen)
		}
	}
	return false
}

// ---- G2: the single egress door (mirrors internal/archtest TestSingleEgressDoor) ----

var egressBanned = map[string]map[string]bool{
	"net/http":   {"Client": true, "Transport": true, "DefaultClient": true, "DefaultTransport": true, "Get": true, "Post": true, "Head": true, "PostForm": true},
	"net":        {"Dialer": true, "Dial": true, "DialTimeout": true, "DialTCP": true, "DialUDP": true, "DialIP": true},
	"crypto/tls": {"Dial": true, "DialWithDialer": true},
}

const egressHint = "use internal/connector/egress (Policy.Client, FirstParty or Wrap)"

func (r *repo) g2(f *srcFile) {
	if f.dir == egressDir {
		return
	}
	names := map[string]string{}
	for pkg := range egressBanned {
		if n := f.importName(pkg); n != "" {
			names[n] = pkg
		}
	}
	if len(names) == 0 {
		return
	}
	ast.Inspect(f.ast, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		id, ok := sel.X.(*ast.Ident)
		if !ok {
			return true
		}
		if pkg, ok := names[id.Name]; ok && egressBanned[pkg][sel.Sel.Name] {
			// http.Client and http.Transport as types (fields, parameters) are fine; building one
			// is not. A composite literal or new() is what builds one, checked below.
			if pkg == "net/http" && (sel.Sel.Name == "Client" || sel.Sel.Name == "Transport") {
				return true
			}
			r.report(G2, f, sel.Pos(), fmt.Sprintf("%s.%s outside %s; %s", pkg, sel.Sel.Name, egressDir, egressHint))
		}
		return true
	})
	ast.Inspect(f.ast, func(n ast.Node) bool {
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
				r.report(G2, f, n.Pos(), fmt.Sprintf("builds an http.%s outside %s; %s", sel.Sel.Name, egressDir, egressHint))
			}
		}
		return true
	})
}

// ---- G3: no secrets in logs ----

var secretKeys = map[string]bool{"password": true, "secret": true, "token": true, "dsn": true, "authorization": true, "api_key": true}

// secretKey reports whether a log key names a secret.
func secretKey(k string) bool {
	k = strings.ToLower(k)
	return secretKeys[k] || strings.HasSuffix(k, "_token") || strings.HasSuffix(k, "_secret") || strings.HasSuffix(k, "_password")
}

// slogAttrs are the log/slog attribute constructors; the key is the first argument.
var slogAttrs = map[string]bool{
	"String": true, "Any": true, "Int": true, "Int64": true, "Uint64": true, "Float64": true,
	"Bool": true, "Time": true, "Duration": true, "Group": true,
}

// logMethods maps a logging method to the index of its message; -1 means no message (With).
var logMethods = map[string]int{
	"Info": 0, "Warn": 0, "Error": 0, "Debug": 0,
	"InfoContext": 1, "WarnContext": 1, "ErrorContext": 1, "DebugContext": 1,
	"Log": 2, "With": -1,
}

func (r *repo) g3(f *srcFile) {
	if f.dir == "internal/vault" || strings.HasPrefix(f.dir, "internal/vault/") {
		return
	}
	slogName := f.importName("log/slog")
	check := func(e ast.Expr) {
		lit, ok := e.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return
		}
		k, err := strconv.Unquote(lit.Value)
		if err == nil && secretKey(k) {
			r.report(G3, f, lit.Pos(), fmt.Sprintf("log attribute keyed %q; never log a credential, log its id or a redacted form", k))
		}
	}
	ast.Inspect(f.ast, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		if id, ok := sel.X.(*ast.Ident); ok && slogName != "" && id.Name == slogName && slogAttrs[sel.Sel.Name] {
			if len(call.Args) > 0 {
				check(call.Args[0])
			}
		}
		if msg, ok := logMethods[sel.Sel.Name]; ok {
			for i := msg + 1; i < len(call.Args); i += 2 {
				check(call.Args[i])
			}
		}
		return true
	})
}

// ---- G4: no InsecureSkipVerify ----

func (r *repo) g4(f *srcFile) {
	const msg = "InsecureSkipVerify switches off TLS checks; trust a specific root with RootCAs instead"
	ast.Inspect(f.ast, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.SelectorExpr:
			if x.Sel.Name == "InsecureSkipVerify" {
				r.report(G4, f, x.Sel.Pos(), msg)
			}
		case *ast.KeyValueExpr:
			if id, ok := x.Key.(*ast.Ident); ok && id.Name == "InsecureSkipVerify" {
				r.report(G4, f, id.Pos(), msg)
			}
		}
		return true
	})
}

// ---- G5: no math/rand ----

func (r *repo) g5(f *srcFile) {
	if r.marker(f, G5, AllowMathRand) {
		return
	}
	for _, im := range f.ast.Imports {
		ip, _ := strconv.Unquote(im.Path.Value)
		if ip == "math/rand" || ip == "math/rand/v2" {
			r.report(G5, f, im.Pos(), fmt.Sprintf("imports %s; use crypto/rand", ip))
		}
	}
}

// guardIndex orders guards by number (G10 after G9).
func guardIndex(g string) int {
	n, _ := strconv.Atoi(strings.TrimPrefix(g, "G"))
	return n
}

// imports reports where f imports path, or token.NoPos.
func (f *srcFile) importPos(p string) token.Pos {
	for _, im := range f.ast.Imports {
		if ip, _ := strconv.Unquote(im.Path.Value); ip == p {
			return im.Pos()
		}
	}
	return token.NoPos
}

// ---- G6: no os/exec outside marked files ----

func (r *repo) g6(f *srcFile) {
	pos := f.importPos("os/exec")
	if pos == token.NoPos || r.marker(f, G6, AllowExec) {
		return
	}
	r.report(G6, f, pos, "imports os/exec; starting a process needs a reason: mark the file "+AllowExec+" <reason>")
}

// ---- G7: no unsafe, no slice or string headers ----

func (r *repo) g7(f *srcFile) {
	if pos := f.importPos("unsafe"); pos != token.NoPos && !r.marker(f, G7, AllowUnsafe) {
		r.report(G7, f, pos, "imports unsafe; use the standard library's safe API, or mark the file "+AllowUnsafe+" <reason> for a syscall")
	}
	reflectName := f.importName("reflect")
	if reflectName == "" {
		return
	}
	ast.Inspect(f.ast, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		if id, ok := sel.X.(*ast.Ident); ok && id.Name == reflectName && (sel.Sel.Name == "SliceHeader" || sel.Sel.Name == "StringHeader") {
			r.report(G7, f, sel.Pos(), "reflect."+sel.Sel.Name+" reinterprets memory; use unsafe.Slice or unsafe.String nowhere, and the standard conversions everywhere")
		}
		return true
	})
}

// ---- G8: secrets compared in constant time ----

var secretName = regexp.MustCompile(`(?i)(mac|signature|token|hash|secret|csrf)`)

// operandNames lists the identifiers an operand is made of: a variable, a field, a conversion or
// call's argument. Literals, nil and len() calls make it a non-secret comparison.
func operandNames(e ast.Expr) (names []string, trivial bool) {
	switch x := ast.Unparen(e).(type) {
	case *ast.BasicLit:
		return nil, true
	case *ast.Ident:
		if x.Name == "nil" || x.Name == "true" || x.Name == "false" {
			return nil, true
		}
		return []string{x.Name}, false
	case *ast.SelectorExpr:
		inner, _ := operandNames(x.X)
		return append(inner, x.Sel.Name), false
	case *ast.CallExpr:
		if id, ok := x.Fun.(*ast.Ident); ok && (id.Name == "len" || id.Name == "cap") {
			return nil, true
		}
		var out []string
		for _, a := range x.Args {
			n, _ := operandNames(a)
			out = append(out, n...)
		}
		if sel, ok := x.Fun.(*ast.SelectorExpr); ok {
			out = append(out, sel.Sel.Name)
		}
		return out, false
	case *ast.IndexExpr:
		return operandNames(x.X)
	case *ast.SliceExpr:
		return operandNames(x.X)
	case *ast.StarExpr:
		return operandNames(x.X)
	case *ast.UnaryExpr:
		return operandNames(x.X)
	}
	return nil, false
}

// allowedLines are the lines that carry, or follow a line that carries, the AllowCompare marker
// with a reason.
func (r *repo) allowedLines(f *srcFile, g Guard, marker string) map[int]bool {
	out := map[int]bool{}
	for _, cg := range f.ast.Comments {
		for _, c := range cg.List {
			rest, ok := strings.CutPrefix(c.Text, marker)
			if !ok || (rest != "" && rest[0] != ' ' && rest[0] != '\t') {
				continue
			}
			if strings.TrimSpace(rest) == "" {
				r.report(g, f, c.Pos(), "the "+strings.TrimPrefix(marker, "//deedglass:")+" marker needs a reason after it")
				continue
			}
			line := r.fset.Position(c.Pos()).Line
			out[line], out[line+1] = true, true
		}
	}
	return out
}

func (r *repo) g8(f *srcFile) {
	// Package names and the package's own constants are never secrets (go/token's token.EQL, a
	// const tokenApp naming a kind of token).
	skip := map[string]bool{}
	for name := range f.imports {
		skip[name] = true
	}
	for name := range r.consts[f.dir] {
		skip[name] = true
	}
	secretOperand := func(e ast.Expr) (string, bool) {
		names, trivial := operandNames(e)
		if trivial {
			return "", false
		}
		for _, n := range names {
			if strings.HasPrefix(n, "Err") || strings.HasPrefix(n, "err") {
				return "", false // a sentinel error, not a secret
			}
		}
		for _, n := range names {
			if !skip[n] && secretName.MatchString(n) {
				return n, true
			}
		}
		return "", false
	}
	allowed := r.allowedLines(f, G8, AllowCompare)
	bytesName, stringsName := f.importName("bytes"), f.importName("strings")
	flag := func(pos token.Pos, how, name string) {
		if allowed[r.fset.Position(pos).Line] {
			return
		}
		r.report(G8, f, pos, fmt.Sprintf("%s compares %s, which sounds secret; use subtle.ConstantTimeCompare or hmac.Equal, or mark a public value %s <reason>", how, name, AllowCompare))
	}
	ast.Inspect(f.ast, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.BinaryExpr:
			if x.Op != token.EQL && x.Op != token.NEQ {
				return true
			}
			_, lt := operandNames(x.X)
			_, rt := operandNames(x.Y)
			if lt || rt {
				return true
			}
			if name, ok := secretOperand(x.X); ok {
				flag(x.OpPos, x.Op.String(), name)
			} else if name, ok := secretOperand(x.Y); ok {
				flag(x.OpPos, x.Op.String(), name)
			}
		case *ast.CallExpr:
			sel, ok := x.Fun.(*ast.SelectorExpr)
			if !ok || len(x.Args) != 2 {
				return true
			}
			id, ok := sel.X.(*ast.Ident)
			if !ok || !((bytesName != "" && id.Name == bytesName && sel.Sel.Name == "Equal") || (stringsName != "" && id.Name == stringsName && sel.Sel.Name == "EqualFold")) {
				return true
			}
			for _, a := range x.Args {
				if name, ok := secretOperand(a); ok {
					flag(x.Pos(), id.Name+"."+sel.Sel.Name, name)
					break
				}
			}
		}
		return true
	})
}

// ---- G9: html/template for anything a browser or mail client renders ----

func (r *repo) g9(f *srcFile) {
	pos := f.importPos("text/template")
	if pos == token.NoPos || r.marker(f, G9, AllowTextTemplate) {
		return
	}
	r.report(G9, f, pos, "imports text/template, which doesn't escape HTML; use html/template, or mark plain-text-only output "+AllowTextTemplate+" <reason>")
}

// ---- G10: server timeouts ----

func (r *repo) g10(f *srcFile) {
	httpName := f.importName("net/http")
	if httpName == "" {
		return
	}
	ast.Inspect(f.ast, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.CompositeLit:
			sel, ok := x.Type.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			if id, ok := sel.X.(*ast.Ident); !ok || id.Name != httpName || sel.Sel.Name != "Server" {
				return true
			}
			set := false
			for _, el := range x.Elts {
				if kv, ok := el.(*ast.KeyValueExpr); ok {
					if k, ok := kv.Key.(*ast.Ident); ok && k.Name == "ReadHeaderTimeout" {
						set = true
					}
				}
			}
			if !set {
				r.report(G10, f, x.Pos(), "an http.Server without ReadHeaderTimeout lets a slow client hold a connection forever (Slowloris); set it")
			}
		case *ast.CallExpr:
			sel, ok := x.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			if id, ok := sel.X.(*ast.Ident); ok && id.Name == httpName {
				switch sel.Sel.Name {
				case "ListenAndServe", "ListenAndServeTLS", "Serve", "ServeTLS":
					r.report(G10, f, x.Pos(), "http."+sel.Sel.Name+" uses a server with no timeouts; build an http.Server with ReadHeaderTimeout")
				}
			}
		}
		return true
	})
}
