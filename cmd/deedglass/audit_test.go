package main

import (
	"bytes"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// TestHelpJSONGolden locks help --json's schema (S33 renders the CLI reference from it; S35
// generates completions from it).
func TestHelpJSONGolden(t *testing.T) {
	r := cli(t, nil, false, "help", "--json")
	var v struct {
		Object string `json:"object"`
		Groups []struct {
			Name     string `json:"name"`
			Commands []struct {
				Path  []string `json:"path"`
				Flags []struct {
					Name, Type string
				} `json:"flags"`
			} `json:"commands"`
		} `json:"groups"`
		GlobalFlags []any          `json:"global_flags"`
		ExitCodes   map[string]any `json:"exit_codes"`
	}
	if err := json.Unmarshal([]byte(r.stdout), &v); err != nil || v.Object != "command_tree" || len(v.Groups) != 9 || len(v.ExitCodes) != 4 || len(v.GlobalFlags) != 6 {
		t.Fatalf("%v %s", err, r.stdout[:min(300, len(r.stdout))])
	}
	golden(t, "help-json", r)
}

// TestJSONAudit (S32 §3.11): every built command run with --json against a server that refuses
// everything prints exactly one JSON value: on success an object with "object" on stdout, on
// failure nothing on stdout and one API-envelope error on stderr.
func TestJSONAudit(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"type":"authentication_error","code":"invalid_api_key","message":"The API key isn't valid. Create one in Console › Agents.","param":null,"request_id":"req_x","doc_url":"https://docs.deedglass.com/errors/invalid_api_key"}}`))
	}))
	defer srv.Close()
	// Long-running, interactive, or needing Docker or a browser: audited elsewhere (start: TestStartNoDockerJSON).
	skip := map[string]bool{"serve": true, "app": true, "mcp wrap": true, "mcp serve": true, "init": true, "start": true, "dev simulate": true,
		"connector gmail authorize": true, "actions list": true, "completion zsh": true, "completion bash": true, "completion fish": true}
	audited := 0
	for _, c := range commands {
		path := strings.Join(c.Path, " ")
		if !c.Built() || c.Hidden || skip[path] {
			continue
		}
		args := append([]string{"--json"}, c.Path...)
		if path == "scan" {
			args = append(args, "--dir", t.TempDir(), "--user-configs=false")
		}
		if path == "health pilot" {
			args = append(args, "--out", t.TempDir())
		}
		env := map[string]string{"DEEDGLASS_SERVER": srv.URL, "DEEDGLASS_API_KEY": "dg_test_Ab12Cd34_SENTINELsentinelSENTINELsentinel", "DEEDGLASS_ENV": "dev", "HOME": t.TempDir()}
		r := cli(t, env, false, args...)
		audited++
		out, errOut := strings.TrimSpace(r.stdout), strings.TrimSpace(r.stderr)
		if r.code == 0 {
			var v map[string]any
			dec := json.NewDecoder(strings.NewReader(out))
			if err := dec.Decode(&v); err != nil || dec.More() {
				t.Errorf("%s: stdout isn't exactly one JSON value: %.200s", path, out)
				continue
			}
			if _, ok := v["object"]; !ok {
				t.Errorf("%s: the JSON has no object field: %.200s", path, out)
			}
			continue
		}
		if out != "" && path != "doctor" && path != "verify" {
			t.Errorf("%s: exit %d with stdout %.200s", path, r.code, out)
		}
		var env2 struct {
			Error struct {
				Type    string `json:"type"`
				Code    string `json:"code"`
				Message string `json:"message"`
				DocURL  string `json:"doc_url"`
			} `json:"error"`
		}
		if path == "doctor" || path == "verify" {
			continue // a report on stdout, then the exit code
		}
		dec := json.NewDecoder(strings.NewReader(errOut))
		if err := dec.Decode(&env2); err != nil || env2.Error.Code == "" || env2.Error.Message == "" || !strings.HasPrefix(env2.Error.DocURL, "https://docs.deedglass.com/errors/") {
			t.Errorf("%s: stderr isn't one API error envelope: %.300s", path, errOut)
		}
	}
	t.Logf("%d commands audited with --json", audited)
	if audited < 40 {
		t.Fatalf("only %d commands audited", audited)
	}
}

// TestErrorCopyAudit (S32 §3.11): every error sentence the CLI writes (errors.New, fmt.Errorf and
// Next: in cmd/deedglass) is sentence case, has no exclamation mark or em dash and no banned word,
// and every Next line carries a next step.
func TestErrorCopyAudit(t *testing.T) {
	fset := token.NewFileSet()
	files, _ := filepath.Glob("*.go")
	reVerb := regexp.MustCompile(`(?i)\b(run|check|set|try|use|pass|add|start|wait|create|fix|remove|delete|stop|export|install|open|rerun|edit|move|give|ask|read|point|update|free|replace|follow|look|see|push|restart|activate|roll|type|choose|copy|sign|wrap|grant|revoke|put|keep|drop|turn|make|list|pick|send|generate|widen|connect|leave|restore|enable|disable|approve|deny|rotate|unset|reconnect)\b`)
	banned := loadBanned(t)
	var msgs, nexts int
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		af, err := parser.ParseFile(fset, f, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(af, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.KeyValueExpr:
				if id, ok := x.Key.(*ast.Ident); ok && id.Name == "Next" {
					if lit, ok := x.Value.(*ast.BasicLit); ok && lit.Kind == token.STRING {
						s, _ := strconv.Unquote(lit.Value)
						nexts++
						if s != "" && !reVerb.MatchString(s) {
							t.Errorf("%s: Next %q names no next step", fset.Position(lit.Pos()), s)
						}
						checkCopy(t, fset.Position(lit.Pos()).String(), s, banned)
					}
				}
			case *ast.CallExpr:
				sel, ok := x.Fun.(*ast.SelectorExpr)
				if !ok || len(x.Args) == 0 {
					return true
				}
				pkg, _ := sel.X.(*ast.Ident)
				if pkg == nil || !((pkg.Name == "errors" && sel.Sel.Name == "New") || (pkg.Name == "fmt" && sel.Sel.Name == "Errorf")) {
					return true
				}
				lit, ok := x.Args[0].(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					return true
				}
				s, _ := strconv.Unquote(lit.Value)
				msgs++
				checkCopy(t, fset.Position(lit.Pos()).String(), s, banned)
			}
			return true
		})
	}
	t.Logf("%d error strings and %d next steps audited", msgs, nexts)
}

func checkCopy(t *testing.T, at, s string, banned []string) {
	t.Helper()
	if strings.ContainsAny(s, "!—") {
		t.Errorf("%s: %q has an exclamation mark or an em dash", at, s)
	}
	low := " " + strings.ToLower(s) + " "
	for _, b := range banned {
		if strings.Contains(low, " "+b+" ") || strings.Contains(low, " "+b+".") {
			t.Errorf("%s: %q uses the banned word %q", at, s, b)
		}
	}
}

func loadBanned(t *testing.T) []string {
	b, err := os.ReadFile("../../scripts/banned_words.txt")
	if err != nil {
		t.Skip(err)
	}
	var out []string
	for _, l := range bytes.Split(b, []byte("\n")) {
		s := strings.TrimSpace(string(l))
		if s != "" && !strings.HasPrefix(s, "#") {
			out = append(out, strings.ToLower(s))
		}
	}
	return out
}
