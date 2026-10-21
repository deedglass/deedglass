package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/deedglass/deedglass/internal/httpx"
)

// CLIError is an error code the CLI prints in its --json errors (doc_url
// https://docs.deedglass.com/errors/<code>) that isn't an API catalog code. They are read from
// cmd/deedglass's source: every ExitError{ErrCode: "…"} literal and every failNext(e, exit, "…", msg, next).
type CLIError struct {
	Code     string   `json:"code"`
	Exit     []int    `json:"exit"`
	Messages []string `json:"messages"`
	Next     []string `json:"next,omitempty"`
}

var exitNames = map[string]int{"ExitOK": 0, "ExitFailure": 1, "ExitUsage": 2, "ExitAuth": 3, "ExitNotAuthed": 3}

func strLit(e ast.Expr) (string, bool) {
	if b, ok := e.(*ast.BasicLit); ok && b.Kind == token.STRING {
		s, err := strconv.Unquote(b.Value)
		return s, err == nil
	}
	return "", false
}

func exitOf(e ast.Expr) (int, bool) {
	switch x := e.(type) {
	case *ast.Ident:
		n, ok := exitNames[x.Name]
		return n, ok
	case *ast.BasicLit:
		n, err := strconv.Atoi(x.Value)
		return n, err == nil
	}
	return 0, false
}

// msgOf finds the sentence of an Err: errors.New("…") or fmt.Errorf("…") value.
func msgOf(e ast.Expr) string {
	c, ok := e.(*ast.CallExpr)
	if !ok || len(c.Args) == 0 {
		return ""
	}
	s, _ := strLit(c.Args[0])
	if strings.Contains(s, "%") {
		return ""
	}
	return s
}

// CLIErrors parses dir (cmd/deedglass) and returns its CLI-only codes, sorted.
func CLIErrors(dir string) ([]CLIError, error) {
	fset := token.NewFileSet()
	files, err := filepath.Glob(filepath.Join(dir, "*.go"))
	if err != nil {
		return nil, err
	}
	by := map[string]*CLIError{}
	add := func(code string, exit int, haveExit bool, msg, next string) {
		if code == "" {
			return
		}
		if _, isAPI := httpx.Lookup(httpx.Code(code)); isAPI {
			return
		}
		c := by[code]
		if c == nil {
			c = &CLIError{Code: code}
			by[code] = c
		}
		if haveExit && !containsInt(c.Exit, exit) {
			c.Exit = append(c.Exit, exit)
		}
		msg = strings.TrimSpace(msg)
		if msg != "" && !strings.HasSuffix(msg, ".") {
			msg += "."
		}
		if msg != "" && len(c.Messages) < 3 && !containsStr(c.Messages, msg) {
			c.Messages = append(c.Messages, msg)
		}
		if next != "" && len(c.Next) < 2 && !containsStr(c.Next, next) {
			c.Next = append(c.Next, next)
		}
	}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		af, err := parser.ParseFile(fset, f, nil, 0)
		if err != nil {
			return nil, err
		}
		ast.Inspect(af, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.CompositeLit:
				id, ok := x.Type.(*ast.Ident)
				if !ok || id.Name != "ExitError" {
					return true
				}
				var code, msg, next string
				var exit int
				var haveExit bool
				for _, el := range x.Elts {
					kv, ok := el.(*ast.KeyValueExpr)
					if !ok {
						continue
					}
					switch kv.Key.(*ast.Ident).Name {
					case "ErrCode":
						code, _ = strLit(kv.Value)
					case "Code":
						exit, haveExit = exitOf(kv.Value)
					case "Err":
						msg = msgOf(kv.Value)
					case "Next":
						next, _ = strLit(kv.Value)
					}
				}
				add(code, exit, haveExit, msg, next)
			case *ast.CallExpr:
				fn, ok := x.Fun.(*ast.Ident)
				if !ok || fn.Name != "failNext" || len(x.Args) < 5 {
					return true
				}
				code, _ := strLit(x.Args[2])
				exit, haveExit := exitOf(x.Args[1])
				msg, _ := strLit(x.Args[3])
				next, _ := strLit(x.Args[4])
				if strings.Contains(msg, "%") {
					msg = ""
				}
				add(code, exit, haveExit, msg, next)
			}
			return true
		})
	}
	out := make([]CLIError, 0, len(by))
	for _, c := range by {
		sort.Ints(c.Exit)
		out = append(out, *c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Code < out[j].Code })
	return out, nil
}

func containsInt(xs []int, x int) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

func containsStr(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}
