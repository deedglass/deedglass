// Package shellguard reads a shell command line before an agent runs it and names the parts that
// would destroy database data: ORM resets and rollbacks, dropdb, and SQL with DROP, TRUNCATE or an
// UPDATE or DELETE that touches every row (ADR-104).
//
// It is a second check, not a sandbox. It reads the command as written and never runs or expands
// it. A value known only at run time (a variable, an alias, a make target, a file the command
// writes first) isn't seen. With Options.ReadFile it also reads, from the working directory, the
// package.json script an npm, pnpm, yarn or bun command runs, a shell script run by path or by sh
// or bash, and the SQL files psql, mysql, sqlite3 or prisma db execute read.
//
// Invariants:
//   - Pure apart from Options.ReadFile: no process, no network, no environment.
//   - Never panics and always ends: the reader is linear, nesting stops at maxDepth and each file
//     is read at most once per check.
//   - It only ever adds a finding. Nothing it reads turns a finding off, so a hook built on it
//     can ask or deny but never allow.
package shellguard

import (
	"encoding/json"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/deedglass/deedglass/internal/sqlguard"
)

// MaxBytes is the longest command (or file) Check reads; a longer command is a finding of its own
// (too-long), a longer file isn't read.
const MaxBytes = 1 << 20

// MaxFindings caps what one check returns, and maxFiles the files one check reads, so a huge
// command costs time in proportion to its length.
const (
	MaxFindings = 32
	maxFiles    = 64
)

// Finding is one part of a command the guard asks about.
type Finding struct {
	ID   string `json:"id"`   // the catalog entry
	What string `json:"what"` // a clause: "prisma migrate reset drops the database, …"
}

// Options configure Check.
type Options struct {
	Dir      string                       // the working directory files are read from
	ReadFile func(string) ([]byte, error) // nil: no file is read
}

// Check returns what in command the guard would ask about, in reading order, without duplicates.
func Check(command string, o Options) []Finding {
	k := &checker{o: o, dir: o.Dir}
	if len(command) > MaxBytes {
		k.add("too-long", "this command", "")
		return k.out
	}
	k.run(command, 0)
	return k.out
}

type checker struct {
	o     Options
	dir   string
	via   string            // "npm run db:reset" while a script or file is being read
	seen  map[string]bool   // scripts and files already checked: each once, so a loop ends
	files map[string][]byte // file contents by path, read once (nil: unreadable)
	out   []Finding
}

// add records a finding: "<label> <why>", or "<label> would run <sql>, which <why>".
func (k *checker) add(id, label, sql string) { k.addWhy(id, label, sql, entry(id).Why) }

func (k *checker) addWhy(id, label, sql, why string) {
	if k.via != "" {
		label += " (in " + k.via + ")"
	}
	what := label + " " + why
	if sql != "" {
		what = label + " would run " + sql + ", which " + why
	}
	for _, f := range k.out {
		if f.ID == id && f.What == what {
			return
		}
	}
	if len(k.out) < MaxFindings {
		k.out = append(k.out, Finding{ID: id, What: what})
	}
}

// run reads a command line and checks each simple command in it.
func (k *checker) run(s string, depth int) {
	cmds, deep := parse(s, depth)
	if deep {
		k.add("too-nested", "this command", "")
	}
	for _, c := range cmds {
		if len(c.args) > 1 && c.args[0] == "cd" && k.dir != "" && !strings.ContainsAny(c.args[1], "$`~") {
			if filepath.IsAbs(c.args[1]) {
				k.dir = c.args[1]
			} else {
				k.dir = filepath.Join(k.dir, c.args[1])
			}
		}
		k.dispatch(c.args, c, depth)
	}
}

var (
	shells    = set("sh bash zsh dash ksh fish")
	sqlProgs  = set("psql mysql mariadb sqlite3")
	launchers = set("sudo doas env nice nohup time timeout exec command xargs stdbuf caffeinate chronic unbuffer " +
		"npx pnpx pnpm yarn bunx bun npm deno uv uvx poetry pipenv pdm hatch rye conda py ruby php bundle " +
		"docker docker-compose podman nerdctl kubectl oc heroku railway fly flyctl dotenv dotenvx doppler op " +
		"infisical sail mise asdf direnv devbox ddev lando")
	pmBuiltin = set("add install i remove rm uninstall update up upgrade dlx exec x create init why info view " +
		"publish pack link unlink outdated audit config cache store patch rebuild prune list ls help login " +
		"logout import dedupe workspace workspaces global set version bin root node env run run-script")
)

func set(words string) map[string]bool {
	m := map[string]bool{}
	for _, w := range strings.Fields(words) {
		m[w] = true
	}
	return m
}

// progName is a word as a program name: no directory, no .exe, no npm: prefix, no @version.
func progName(w string) string {
	if i := strings.LastIndexAny(w, `/\`); i >= 0 {
		w = w[i+1:]
	}
	w = strings.TrimPrefix(strings.TrimSuffix(strings.TrimSuffix(strings.ToLower(w), ".exe"), ".cmd"), "npm:")
	if i := strings.LastIndexByte(w, '@'); i > 0 {
		w = w[:i]
	}
	return w
}

func isPython(n string) bool { return strings.HasPrefix(n, "python") }

// known reports whether a program name is one the guard reads.
func known(n string) bool {
	return progEntries[n] != nil || sqlProgs[n] || launchers[n] || shells[n] || isPython(n) || n == "ssh" || n == "eval" || n == "watch" || n == "find"
}

func (k *checker) dispatch(args []string, c *cmd, depth int) {
	if len(args) == 0 {
		return
	}
	prog, rest := progName(args[0]), args[1:]
	switch {
	case shells[prog]:
		if s, ok := shellString(rest); ok {
			k.run(s, depth+1)
		} else if nf := nonflag(rest); len(nf) == 0 {
			k.run(strings.Join(stdinText(c), "\n"), depth+1)
			for _, f := range pipedFiles(c) {
				k.script(f, depth)
			}
		} else {
			k.script(nf[0], depth)
		}
		return
	case strings.HasSuffix(prog, ".sh") && strings.ContainsAny(args[0], "/\\"):
		k.script(args[0], depth)
		return
	case prog == "eval":
		k.run(strings.Join(rest, " "), depth+1)
		return
	case prog == "ssh":
		if remote := sshCommand(rest); len(remote) > 0 {
			k.run(strings.Join(remote, " "), depth+1)
		} else {
			k.run(strings.Join(stdinText(c), "\n"), depth+1)
		}
		return
	case prog == "command" && (hasFlag(rest, "-v") || hasFlag(rest, "-V")):
		return
	case prog == "watch":
		if s := watchCommand(rest); s != "" {
			k.run(s, depth+1)
		}
		return
	case prog == "find":
		for _, sub := range findExecs(rest) {
			k.dispatch(sub, &cmd{}, depth)
		}
		return
	}
	for _, e := range progEntries[prog] {
		if label, ok := e.match(prog, rest, c.env); ok {
			k.add(e.ID, label, "")
		}
	}
	if sqlProgs[prog] || prog == "prisma" {
		k.sql(prog, rest, c)
	}
	if launchers[prog] || isPython(prog) {
		for i, a := range rest {
			if known(progName(a)) {
				k.dispatch(rest[i:], c, depth)
				return
			}
		}
		if prog == "npm" || prog == "pnpm" || prog == "yarn" || prog == "bun" {
			k.pkgScript(prog, rest, depth)
		}
	}
}

// pkgScript reads the package.json script that npm run (or pnpm, yarn, bun) would run.
func (k *checker) pkgScript(prog string, rest []string, depth int) {
	nf := nonflag(rest)
	name := ""
	switch {
	case len(nf) > 1 && (nf[0] == "run" || nf[0] == "run-script"):
		name = nf[1]
	case prog == "npm" && len(nf) > 0 && npmScripts[nf[0]] != "":
		name = npmScripts[nf[0]]
	case prog != "npm" && len(nf) > 0 && !pmBuiltin[nf[0]]:
		name = nf[0]
	}
	if name == "" {
		return
	}
	b, ok := k.file("package.json", "script "+name)
	if !ok {
		return
	}
	var pj struct {
		Scripts map[string]string `json:"scripts"`
	}
	if json.Unmarshal(b, &pj) != nil || pj.Scripts[name] == "" {
		return
	}
	k.nested(prog+" run "+name, pj.Scripts[name], depth)
}

// script reads a shell script the command runs (sh x.sh, bash -e x.sh, ./x.sh).
func (k *checker) script(name string, depth int) {
	if b, ok := k.file(name, ""); ok {
		k.nested(name, string(b), depth)
	}
}

// nested checks text read from a file as a command line, naming where it came from.
func (k *checker) nested(via, text string, depth int) {
	outer := k.via
	if outer == "" {
		k.via = via
	}
	k.run(text, depth+1)
	k.via = outer
}

// file returns the contents of name (relative to the working directory), read at most once per
// check, the first time it is asked for with this part; part tells apart uses of the same file
// (two package.json scripts). It reads nothing without
// Options.ReadFile, a working directory for a relative name, or a name the reader can't know.
func (k *checker) file(name, part string) ([]byte, bool) {
	if k.o.ReadFile == nil || name == "" || name == "-" || strings.ContainsAny(name, "$`~*?") {
		return nil, false
	}
	if !filepath.IsAbs(name) {
		if k.dir == "" {
			return nil, false
		}
		name = filepath.Join(k.dir, name)
	}
	key := name + "\x00" + part
	if k.seen[key] {
		return nil, false
	}
	if k.seen == nil {
		k.seen, k.files = map[string]bool{}, map[string][]byte{}
	}
	k.seen[key] = true
	b, ok := k.files[name]
	if !ok && len(k.files) < maxFiles {
		var err error
		if b, err = k.o.ReadFile(name); err != nil || len(b) > MaxBytes {
			b = nil
		}
		k.files[name] = b
	}
	return b, b != nil
}

// npmScripts are the npm commands that run a script without "run".
var npmScripts = map[string]string{"test": "test", "t": "test", "tst": "test", "start": "start", "stop": "stop", "restart": "restart"}

// match reports whether args (after the program) have this entry's shape, and how to name it.
func (e *Entry) match(prog string, args, env []string) (string, bool) {
	nf := nonflag(args)
	if hasFlag(args, "--help") || len(nf) > 0 && nf[0] == "help" {
		return "", false
	}
	label := prog
	if e.custom != nil {
		got, ok := e.custom(args)
		if !ok {
			return "", false
		}
		return label + " " + got, true
	}
	if e.task {
		w := ""
		for _, a := range nf {
			for _, t := range e.words {
				if w == "" && (a == t || strings.HasPrefix(a, t+":")) {
					w = a
				}
			}
		}
		if w == "" {
			return "", false
		}
		label += " " + w
	} else if len(e.words) > 0 {
		at, got := seq(nf, e.words)
		if got == nil || e.first && at != 0 {
			return "", false
		}
		label += " " + strings.Join(got, " ")
		if len(e.then) > 0 {
			kind := ""
			for _, a := range nf[at+len(got):] {
				for _, k := range e.then {
					if kind == "" && (a == k || strings.HasPrefix(a, k+"/") || slices.Contains(strings.Split(a, ","), k)) {
						kind = a
					}
				}
			}
			if kind == "" {
				return "", false
			}
			label += " " + kind
		}
	}
	for _, g := range e.flags {
		f := ""
		for _, name := range g {
			if f == "" && hasFlag(args, name) {
				f = name
			}
		}
		if f == "" {
			return "", false
		}
		label += " " + f
	}
	for _, n := range e.not {
		if hasFlag(args, n) {
			return "", false
		}
	}
	if e.arg != "" {
		if !contains(args, e.arg) && !contains(env, e.arg) {
			return "", false
		}
		label += " " + e.arg
	}
	return label, true
}

// seq finds words in nf, consecutively ("*" is any word, "a|b" either), and returns where and what
// it matched.
func seq(nf, words []string) (int, []string) {
	for i := range nf {
		if i+len(words) > len(nf) {
			return -1, nil
		}
		ok := true
		for j, w := range words {
			if w != "*" && !slices.Contains(strings.Split(w, "|"), nf[i+j]) {
				ok = false
				break
			}
		}
		if ok {
			return i, nf[i : i+len(words)]
		}
	}
	return -1, nil
}

// terraformDestroy matches terraform destroy and terraform apply -destroy (destroy is "a
// convenience alias" for apply -destroy); plan -destroy only shows a plan.
func terraformDestroy(args []string) (string, bool) {
	nf := nonflag(args)
	for _, a := range args {
		if a == "-help" || a == "-h" {
			return "", false
		}
	}
	for _, w := range nf {
		switch {
		case w == "destroy":
			return "destroy", true
		case w == "apply" && (hasFlag(args, "-destroy") || hasFlag(args, "--destroy")):
			return "apply -destroy", true
		}
		if strings.Contains(w, "=") { // -chdir=dir and other values never stand alone
			continue
		}
		return "", false
	}
	return "", false
}

// railwayMutation is a delete mutation in Railway's public GraphQL API (docs.railway.com guides:
// volumeDelete in Manage Volumes, projectDelete in Manage Projects).
var railwayMutation = regexp.MustCompile(`\b(volumeDelete|projectDelete)\b`)

// railwayAPIDelete matches an HTTP client sending volumeDelete or projectDelete to Railway's
// GraphQL endpoint (backboard.railway.com; backboard.railway.app in Railway's PocketOS write-up).
func railwayAPIDelete(args []string) (string, bool) {
	host, mutation := false, ""
	for _, a := range args {
		if strings.Contains(a, "backboard.railway.app") || strings.Contains(a, "backboard.railway.com") {
			host = true
		}
		if m := railwayMutation.FindString(a); m != "" && mutation == "" {
			mutation = m
		}
	}
	if !host || mutation == "" {
		return "", false
	}
	return "… " + mutation + " (Railway API)", true
}

func nonflag(args []string) []string {
	var out []string
	for _, a := range args {
		if !strings.HasPrefix(a, "-") || a == "-" {
			out = append(out, a)
		}
	}
	return out
}

func hasFlag(args []string, name string) bool {
	for _, a := range args {
		if a == "--" {
			return false
		}
		if a == name || strings.HasPrefix(a, name+"=") {
			return true
		}
	}
	return false
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// shellString returns the string sh -c (or bash -lc, …) would run.
func shellString(args []string) (string, bool) {
	c := false
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--":
			i++
			if c && i < len(args) {
				return args[i], true
			}
			return "", false
		case a == "-o" || a == "+o" || a == "-O" || a == "+O":
			i++
		case len(a) > 1 && a[0] == '-' && a[1] != '-' && strings.IndexByte(a, 'c') > 0:
			c = true
		case strings.HasPrefix(a, "-") || strings.HasPrefix(a, "+"):
		case c:
			return a, true
		default:
			return "", false // a script file
		}
	}
	return "", false
}

// sshCommand returns the remote command words of an ssh invocation.
func sshCommand(args []string) []string {
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			if i+2 <= len(args) {
				return args[i+2:]
			}
			return nil
		}
		if len(a) > 1 && a[0] == '-' {
			for j := 1; j < len(a); j++ {
				if strings.IndexByte("BbcDEeFIiJLlmOoPpQRSWw", a[j]) >= 0 {
					if j == len(a)-1 {
						i++
					}
					break
				}
			}
			continue
		}
		return args[i+1:]
	}
	return nil
}

// stdinText is the text a command reads on stdin when the command line holds it: heredocs,
// here-strings, and the words of an echo or printf (or a cat's heredoc) piped into it.
func stdinText(c *cmd) []string {
	out := append([]string(nil), c.stdin...)
	if f := c.from; f != nil && len(f.args) > 0 {
		switch progName(f.args[0]) {
		case "echo":
			w := f.args[1:]
			for len(w) > 0 && strings.HasPrefix(w[0], "-") {
				w = w[1:]
			}
			out = append(out, strings.Join(w, " "))
		case "printf":
			out = append(out, strings.Join(f.args[1:], " "))
			if len(f.args) > 2 {
				out = append(out, strings.Join(f.args[2:], " "))
			}
		case "cat":
			out = append(out, f.stdin...)
		}
	}
	return out
}

// pipedFiles are the files a cat piped into this command reads (cat drop.sql | psql).
func pipedFiles(c *cmd) []string {
	if f := c.from; f != nil && len(f.args) > 1 && progName(f.args[0]) == "cat" {
		return nonflag(f.args[1:])
	}
	return nil
}

// watchCommand is the command line watch runs (it hands its words to sh -c), after its options.
func watchCommand(args []string) string {
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--":
			return strings.Join(args[i+1:], " ")
		case a == "-n" || a == "--interval" || a == "-q" || a == "--equexit":
			i++
		case strings.HasPrefix(a, "-"):
		default:
			return strings.Join(args[i:], " ")
		}
	}
	return ""
}

// findExecs are the commands find runs with -exec, -execdir, -ok or -okdir, each up to its ";" or "+".
func findExecs(args []string) [][]string {
	var out [][]string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-exec", "-execdir", "-ok", "-okdir":
			j := i + 1
			for j < len(args) && args[j] != ";" && args[j] != "+" {
				j++
			}
			if j > i+1 {
				out = append(out, args[i+1:j])
			}
			i = j
		}
	}
	return out
}

// sql checks the SQL a database client would run: from its options, its stdin, and the SQL
// files it reads.
func (k *checker) sql(prog string, rest []string, c *cmd) {
	var texts, files []string
	label, stdin := prog, true
	switch prog {
	case "psql":
		texts = optValues(rest, 'c', "--command", psqlOpts, "")
		files = optValues(rest, 'f', "--file", psqlOpts, "")
		if f := c.from; f != nil {
			for i, a := range f.args {
				if progName(a) == "pg_dump" && (hasFlag(f.args[i:], "-c") || hasFlag(f.args[i:], "--clean")) {
					k.add("pg-dump-clean-pipe", "pg_dump --clean | psql", "")
				}
			}
		}
	case "mysql", "mariadb":
		texts = optValues(rest, 'e', "--execute", "ehPuDS", "p")
	case "sqlite3":
		var nf []string
		for i := 0; i < len(rest); i++ {
			switch a := rest[i]; {
			case a == "-cmd" && i+1 < len(rest):
				texts = append(texts, rest[i+1])
				i++
			case a == "-init" || a == "-separator" || a == "-newline" || a == "-nullvalue" || a == "-vfs" || a == "-mmap":
				i++
			case strings.HasPrefix(a, "-"):
			default:
				nf = append(nf, a)
			}
		}
		if len(nf) > 1 {
			texts = append(texts, strings.Join(nf[1:], " "))
		}
	case "prisma":
		if _, got := seq(nonflag(rest), []string{"db", "execute"}); got == nil {
			return
		}
		label, stdin = "prisma db execute", hasFlag(rest, "--stdin")
		files = optValues(rest, 0, "--file", "", "")
	}
	if stdin {
		texts = append(texts, stdinText(c)...)
		files = append(files, c.files...)
		files = append(files, pipedFiles(c)...)
	}
	for _, t := range texts {
		k.statements(label, t)
	}
	for _, f := range files {
		if b, ok := k.file(f, ""); ok {
			k.statements(label+" reading "+f, string(b))
		}
	}
}

// psqlOpts are psql's short options that take a value.
const psqlOpts = "cdfhLopPRTUvF"

// optValues returns the values of a short option (getopt clusters: -Atc 'sql') and its long form.
// argOpts are the other short options that take a value; optional ones take it only when attached.
func optValues(args []string, short byte, long, argOpts, optional string) []string {
	var out []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--":
			return out
		case strings.HasPrefix(a, long+"="):
			out = append(out, a[len(long)+1:])
			continue
		case a == long:
			if i+1 < len(args) {
				out = append(out, args[i+1])
				i++
			}
			continue
		case len(a) < 2 || a[0] != '-' || a[1] == '-':
			continue
		}
		for j := 1; j < len(a); j++ {
			ch := a[j]
			if ch == short {
				if j+1 < len(a) {
					out = append(out, a[j+1:])
				} else if i+1 < len(args) {
					out = append(out, args[i+1])
					i++
				}
				break
			}
			if strings.IndexByte(optional, ch) >= 0 {
				break
			}
			if strings.IndexByte(argOpts, ch) >= 0 {
				if j == len(a)-1 {
					i++
				}
				break
			}
		}
	}
	return out
}

// statements checks each SQL statement in text. A psql backslash command at the start of a
// statement runs to the end of its line and is skipped.
func (k *checker) statements(prog, text string) {
	toks, _ := sqlguard.Tokenize(text)
	for _, st := range sqlguard.Split(toks) {
		var sig []sqlguard.Token
		meta := false
		for _, t := range st.Tokens {
			switch {
			case t.Kind == sqlguard.Space || t.Kind == sqlguard.LineComment || t.Kind == sqlguard.BlockComment:
				if meta && strings.ContainsRune(t.Text, '\n') {
					meta = false
				}
			case meta:
			case len(sig) == 0 && t.Kind == sqlguard.Invalid && t.Text == `\`:
				meta = true
			default:
				sig = append(sig, t)
			}
		}
		if len(sig) == 0 {
			continue
		}
		kw := func(i int) string {
			if i < len(sig) && sig[i].Kind == sqlguard.Ident {
				return sig[i].Norm
			}
			return ""
		}
		switch kw(0) {
		case "drop":
			cascade := false
			for i := range sig {
				cascade = cascade || (kw(i) == "cascade" && sig[i].Depth == 0)
			}
			obj := kw(1)
			if obj == "materialized" {
				obj += " " + kw(2)
			}
			if why := dropWhy(obj, cascade); why != "" {
				k.addWhy("sql-drop", prog, "DROP "+strings.ToUpper(obj), why)
			}
		case "truncate":
			k.add("sql-truncate", prog, "TRUNCATE")
		case "alter":
			for i := 2; kw(1) == "table" && i < len(sig); i++ {
				if kw(i) == "drop" && sig[i].Depth == 0 {
					switch kw(i + 1) {
					case "constraint", "default", "not", "identity", "expression":
						continue
					}
					k.add("sql-drop-column", prog, "ALTER TABLE … DROP COLUMN")
					break
				}
			}
		case "do":
			if hasWord(text[sig[0].Start:sig[len(sig)-1].End], "drop", "truncate") {
				k.add("sql-do-drop", prog, "a DO block")
			}
		}
		stmt := text[sig[0].Start:sig[len(sig)-1].End]
		if f := sqlguard.Analyze(stmt, sqlguard.Options{}); f.UnboundedWrite {
			how := " with no WHERE clause"
			if f.WhereTrivial {
				how = " whose WHERE is always true"
			}
			for _, w := range f.WriteKinds {
				switch w {
				case "delete":
					k.add("sql-delete-all", prog, "a DELETE"+how)
				case "update":
					k.add("sql-update-all", prog, "an UPDATE"+how)
				}
			}
		}
	}
}

// dropWhy says what DROP <obj> deletes, or "" when the object holds no rows (an index, a view, a
// function): the guard asks only about data. Types, domains and extensions hold no rows
// themselves, but with CASCADE Postgres also drops the columns and tables that use them.
func dropWhy(obj string, cascade bool) string {
	switch obj {
	case "table":
		return "deletes the table and every row in it"
	case "schema", "database":
		return "deletes the " + obj + " and everything in it"
	case "owned":
		return "deletes every object the role owns"
	case "materialized view":
		return "deletes the materialized view and the rows it holds"
	case "type", "domain", "extension":
		if cascade {
			return "with CASCADE also deletes the columns and tables that depend on the " + obj
		}
	}
	return ""
}

// hasWord reports whether s holds one of words as a whole word, ignoring case.
func hasWord(s string, words ...string) bool {
	s = strings.ToLower(s)
	for _, w := range words {
		for i := 0; ; {
			j := strings.Index(s[i:], w)
			if j < 0 {
				break
			}
			j += i
			before := j == 0 || !isWordByte(s[j-1])
			after := j+len(w) == len(s) || !isWordByte(s[j+len(w)])
			if before && after {
				return true
			}
			i = j + 1
		}
	}
	return false
}

func isWordByte(c byte) bool { return c == '_' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' }

// Reason is the one sentence a hook shows: what Deedglass found and why it asks (or denies).
func Reason(fs []Finding, deny bool) string {
	if len(fs) == 0 {
		return ""
	}
	more := ""
	switch {
	case len(fs) >= MaxFindings:
		more = " (and at least " + strconv.Itoa(len(fs)-1) + " more in this command)"
	case len(fs) > 1:
		more = " (and " + strconv.Itoa(len(fs)-1) + " more in this command)"
	}
	if deny {
		return "Deedglass denied this command because " + fs[0].What + more + "; if it is intended, ask the user to run it themselves."
	}
	return "Deedglass asks first: " + fs[0].What + more + "."
}

// composeValueFlags are docker compose's global flags that take a value, so the word after them
// isn't a subcommand.
var composeValueFlags = set("-f --file -p --project-name --project-directory --env-file --profile --ansi --progress --parallel -c --context -H --host --config --log-level")

// composeDownVolumes matches docker compose down -v (and docker-compose, podman compose): -v or
// --volumes removes the named volumes in the Compose file and the containers' anonymous volumes
// (docs.docker.com compose down). The subcommand is the first word after compose that isn't a flag
// or a flag's value; for docker-compose, the first such word.
func composeDownVolumes(args []string) (string, bool) {
	i := 0 // docker-compose and podman-compose: from the start
	if at := slices.Index(args, "compose"); at >= 0 {
		i = at + 1 // docker, podman, nerdctl: after "compose"
	}
	sub := -1
	for j := i; j < len(args); j++ {
		a := args[j]
		if a == "--" {
			return "", false
		}
		if strings.HasPrefix(a, "-") {
			if composeValueFlags[a] {
				j++
			}
			continue
		}
		sub = j
		break
	}
	if sub < 0 || args[sub] != "down" {
		return "", false
	}
	for _, a := range args[sub+1:] {
		if a == "--" {
			break
		}
		if a == "-v" || a == "--volumes" || a == "--volumes=true" {
			label := "down " + a
			if slices.Contains(args, "compose") {
				label = "compose " + label
			}
			return label, true
		}
	}
	return "", false
}

// helmValueFlags are helm's global flags that take a value.
var helmValueFlags = set("-n --namespace --kube-context --kubeconfig --kube-apiserver --kube-as-user --kube-as-group --kube-ca-file --kube-token --kube-tls-server-name --registry-config --repository-cache --repository-config --burst-limit --qps")

// helmUninstall matches helm uninstall and its aliases del, delete and un as the subcommand, unless
// --dry-run only lists what it would remove (helm.sh helm_uninstall).
func helmUninstall(args []string) (string, bool) {
	for j := 0; j < len(args); j++ {
		a := args[j]
		if a == "--" {
			return "", false
		}
		if strings.HasPrefix(a, "-") {
			if helmValueFlags[a] {
				j++
			}
			continue
		}
		switch a {
		case "uninstall", "delete", "del", "un":
			if hasFlag(args, "--dry-run") {
				return "", false
			}
			return a, true
		}
		return "", false
	}
	return "", false
}

// mongoDropCall is dropDatabase() on any database handle, or drop() on a collection
// (db.users.drop(), db.getCollection("users").drop()).
var mongoDropCall = regexp.MustCompile(`\bdropDatabase\s*\(|\.\s*drop\s*\(`)

// mongoDrop matches mongosh or mongo with --eval code that drops a database or a collection
// (mongodb.com db.dropDatabase(), db.collection.drop()).
func mongoDrop(args []string) (string, bool) {
	for j, a := range args {
		code := ""
		switch {
		case (a == "--eval" || a == "-e") && j+1 < len(args):
			code = args[j+1]
		case strings.HasPrefix(a, "--eval="):
			code = strings.TrimPrefix(a, "--eval=")
		}
		if m := mongoDropCall.FindString(code); m != "" {
			if strings.Contains(m, "dropDatabase") {
				return "--eval dropDatabase()", true
			}
			return "--eval drop()", true
		}
	}
	return "", false
}
