package shellguard

import (
	"reflect"
	"strings"
	"testing"
)

type got struct {
	Env, Args, Stdin, Files []string
	Piped                   bool
}

func summarize(cs []*cmd) []got {
	var out []got
	for _, c := range cs {
		out = append(out, got{Env: c.env, Args: c.args, Stdin: c.stdin, Files: c.files, Piped: c.from != nil})
	}
	return out
}

func a(words ...string) got { return got{Args: words} }

// TestParse: the reader splits commands the way sh and bash do, far enough to find each one.
func TestParse(t *testing.T) {
	cases := []struct {
		in   string
		want []got
	}{
		{"", nil},
		{"ls -la", []got{a("ls", "-la")}},
		{"a && b || c; d & e", []got{a("a"), a("b"), a("c"), a("d"), a("e")}},
		{"a | b |& c", []got{a("a"), {Args: []string{"b"}, Piped: true}, {Args: []string{"c"}, Piped: true}}},
		{"a |\n b", []got{a("a"), {Args: []string{"b"}, Piped: true}}},
		{"(cd x && y)", []got{a("cd", "x"), a("y")}},
		{"{ a; b; }", []got{a("a"), a("b")}},
		{"FOO=1 BAR='x y' cmd arg", []got{{Env: []string{"FOO=1", "BAR=x y"}, Args: []string{"cmd", "arg"}}}},
		{"cmd FOO=1", []got{a("cmd", "FOO=1")}},
		{`echo 'a b' "c $d" e\ f`, []got{a("echo", "a b", "c $d", "e f")}},
		{`echo "a \"q\" \$x \\ \n"`, []got{a("echo", `a "q" $x \ \n`)}},
		{`echo $'a\tb'`, []got{a("echo", "a\tb")}},
		{"echo a\\\nb", []got{a("echo", "ab")}},
		{"x=$(a b) c", []got{a("a", "b"), {Env: []string{"x=$(a b)"}, Args: []string{"c"}}}},
		{"echo `a b`", []got{a("a", "b"), a("echo", "`a b`")}},
		{`echo "$(a "b c")"`, []got{a("a", "b c"), a("echo", `$(a "b c")`)}},
		{"diff <(a) >(b)", []got{a("a"), a("b"), a("diff")}},
		{"echo ${X:-$(a)}", []got{a("a"), a("echo", "${X:-$(a)}")}},
		{"echo $((1 + 2))", []got{a("echo", "$((1 + 2))")}},
		{"(( i++ )); b", []got{a("b")}},
		{"((x)) && ( (y) )", []got{a("y")}},
		{"a 2>&1 >/dev/null b", []got{a("a", "b")}},
		{"a &> log", []got{a("a")}},
		{"psql < in.sql > out.txt", []got{{Args: []string{"psql"}, Files: []string{"in.sql"}}}},
		{"psql <<< 'SELECT 1'", []got{{Args: []string{"psql"}, Stdin: []string{"SELECT 1"}}}},
		{"psql <<EOF\nSELECT 1;\nEOF\nnext", []got{{Args: []string{"psql"}, Stdin: []string{"SELECT 1;\n"}}, a("next")}},
		{"psql <<-'EOF'\n\tSELECT 1;\n\tEOF", []got{{Args: []string{"psql"}, Stdin: []string{"SELECT 1;\n"}}}},
		{"a <<X; b <<Y\nx\nX\ny\nY", []got{{Args: []string{"a"}, Stdin: []string{"x\n"}}, {Args: []string{"b"}, Stdin: []string{"y\n"}}}},
		{"if a; then b; else c; fi", []got{a("a"), a("b"), a("c")}},
		{"while read l; do echo \"$l\"; done < f", []got{a("read", "l"), a("echo", "$l")}},
		{"for f in *.sql; do psql -f \"$f\"; done", []got{a("f", "in", "*.sql"), a("psql", "-f", "$f")}},
		{"! a", []got{a("a")}},
		{"f() { a; }", []got{a("a")}},
		{"case $x in y) a;; esac", []got{a("$x", "in", "y"), a("a")}},
		{"echo hi # dropdb x", []got{a("echo", "hi")}},
		{"echo 'unterminated", []got{a("echo", "unterminated")}},
		{"echo a)", []got{a("echo", "a")}},
		{"'if' x", []got{a("if", "x")}},
	}
	for _, c := range cases {
		cs, _ := parse(c.in, 0)
		if g := summarize(cs); !reflect.DeepEqual(g, c.want) {
			t.Errorf("parse(%q)\n got %+v\nwant %+v", c.in, g, c.want)
		}
	}
}

func TestParseDepth(t *testing.T) {
	in := strings.Repeat("( ", maxDepth+5) + "a" + strings.Repeat(" )", maxDepth+5)
	if _, deep := parse(in, 0); !deep {
		t.Fatal("subshells: want deep")
	}
	if _, deep := parse(strings.Repeat("`", 1)+"echo "+strings.Repeat("${X:-$(", maxDepth+2)+"a"+strings.Repeat(")}", maxDepth+2)+"`", 0); !deep {
		t.Fatal("expansions: want deep")
	}
	if _, deep := parse("( ( ( a ) ) )", 0); deep {
		t.Fatal("three levels: want not deep")
	}
}

func TestIsAssign(t *testing.T) {
	for w, want := range map[string]bool{"A=1": true, "_a1=": true, "A+=x": true, "1A=x": false, "=x": false, "a-b=c": false, "--flag=x": false} {
		if isAssign(w) != want {
			t.Errorf("isAssign(%q) = %v", w, !want)
		}
	}
}

func TestShellString(t *testing.T) {
	cases := map[string]string{
		"-c x":             "x",
		"-lc x":            "x",
		"-e -c x":          "x",
		"-o pipefail -c x": "x",
		"-c -- x":          "x",
		"script.sh":        "",
		"-e":               "",
	}
	for in, want := range cases {
		s, ok := shellString(strings.Fields(in))
		if s != want || ok != (want != "") {
			t.Errorf("shellString(%q) = %q, %v", in, s, ok)
		}
	}
}

func TestSSHCommand(t *testing.T) {
	cases := map[string]string{
		"host cmd a":                      "cmd a",
		"-p 22 -i key user@h c":           "c",
		"-tt h c":                         "c",
		"-o StrictHostKeyChecking=no h c": "c",
		"h":                               "",
	}
	for in, want := range cases {
		if g := strings.Join(sshCommand(strings.Fields(in)), " "); g != want {
			t.Errorf("sshCommand(%q) = %q, want %q", in, g, want)
		}
	}
}

func TestOptValues(t *testing.T) {
	cases := []struct {
		args string
		want string
	}{
		{"-c x", "x"},
		{"-cx", "x"},
		{"-Atc x", "x"},
		{"-U postgres -c x -c y", "x|y"},
		{"-d app -c x", "x"},
		{"-dc x", ""}, // -d takes "c" as its value
		{"--command=x", "x"},
		{"--command x", "x"},
		{"-- -c x", ""},
	}
	for _, c := range cases {
		g := strings.Join(optValues(strings.Fields(c.args), 'c', "--command", psqlOpts, ""), "|")
		if g != c.want {
			t.Errorf("optValues(%q) = %q, want %q", c.args, g, c.want)
		}
	}
}
