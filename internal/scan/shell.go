package scan

import (
	"encoding/json"
	"fmt"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// What an agent can run in a shell (FEAT-05). Most public agent data-loss reports came through the
// shell, not through a database tool: an ORM's reset command, a migration downgrade. This part reads
// Claude Code's settings files (permissions.allow, ask, deny and defaultMode), finds the database
// CLIs in the dependency files (the catalog's database_cli entries) and works out, for each way an
// agent might spell a destructive command, whether Claude Code would run it without a prompt. It
// also flags database URL variables in .env files that no Read deny rule covers.
//
// It is string matching over files, in the way Claude Code's permissions page describes its own
// matching: it never runs a command, never reads a .env value and never prints a rule it can't show
// safely (a rule saved from a prompt can hold a whole command line).

// The pages the matching and the mode rules follow (checked 2026-10-18).
const (
	permDocs = "https://code.claude.com/docs/en/permissions"
	modeDocs = "https://code.claude.com/docs/en/permission-modes"
)

// ClaudeSettings is one Claude Code settings file the scan read: rule counts only, never the rules.
type ClaudeSettings struct {
	Path        string `json:"path"`
	Scope       string `json:"scope"` // project · local · user
	DefaultMode string `json:"default_mode,omitempty"`
	Allow       int    `json:"allow"`
	Ask         int    `json:"ask"`
	Deny        int    `json:"deny"`
	Error       string `json:"error,omitempty"`
}

// permSource is one settings file's permission block, kept in memory only.
type permSource struct {
	label, scope string
	root         string // the project directory it governs, relative to --dir ("" for user settings)
	anchor       string // the absolute directory a "/path" rule starts from
	mode         string
	noBypass     bool // permissions.disableBypassPermissionsMode is "disable"
	noAuto       bool // permissions.disableAutoMode is "disable"
	preBash      bool // a hooks.PreToolUse entry whose matcher covers Bash (none, "*", ".*" or one naming Bash)
	allow, ask   []string
	deny         []string
}

// script is one package.json script; its body never leaves memory.
type script struct{ file, name, body string }

var knownModes = map[string]string{"default": "default", "manual": "default", "acceptEdits": "acceptEdits", "plan": "plan",
	"auto": "auto", "dontAsk": "dontAsk", "bypassPermissions": "bypassPermissions"}

func readSettings(label, scope, root, anchor string, b []byte) (*permSource, ClaudeSettings) {
	src := &permSource{label: label, scope: scope, root: root, anchor: anchor}
	cs := ClaudeSettings{Path: label, Scope: scope}
	var s struct {
		Permissions json.RawMessage `json:"permissions"`
		Hooks       json.RawMessage `json:"hooks"`
	}
	if err := json.Unmarshal(b, &s); err != nil {
		cs.Error = "not valid JSON"
		return src, cs
	}
	src.preBash = preToolUseBash(s.Hooks)
	// Each key is read on its own, so one value of the wrong type costs only that key, and the
	// file is still listed with what was read.
	p := map[string]json.RawMessage{}
	if len(s.Permissions) > 0 && string(s.Permissions) != "null" && json.Unmarshal(s.Permissions, &p) != nil {
		cs.Error = "permissions isn't an object; its rules weren't read"
		return src, cs
	}
	var bad []string
	field := func(k string, v any) {
		if raw, ok := p[k]; ok && json.Unmarshal(raw, v) != nil {
			bad = append(bad, k)
		}
	}
	var mode, noBypass, noAuto string
	field("allow", &src.allow)
	field("ask", &src.ask)
	field("deny", &src.deny)
	field("defaultMode", &mode)
	field("disableBypassPermissionsMode", &noBypass)
	field("disableAutoMode", &noAuto)
	if len(bad) > 0 {
		cs.Error = "permissions." + strings.Join(bad, ", permissions.") + " not in the shape Claude Code reads; left out"
	}
	src.noBypass, src.noAuto = noBypass == "disable", noAuto == "disable"
	if mode != "" {
		src.mode = knownModes[mode]
		cs.DefaultMode = src.mode
		if src.mode == "" {
			cs.DefaultMode = "unknown"
		}
	}
	cs.Allow, cs.Ask, cs.Deny = len(src.allow), len(src.ask), len(src.deny)
	return src, cs
}

// --- rules ---

var (
	reRule      = regexp.MustCompile(`^\s*([A-Za-z0-9_*?]+)\s*(?:\((.*)\))?\s*$`)
	reParamRule = regexp.MustCompile(`^[a-z_]+\s*:\S*$`) // Bash(run_in_background:true): a parameter rule
	reToken     = regexp.MustCompile(`[A-Za-z0-9_-]{24,}`)
)

func splitRule(r string) (tool, spec string, ok bool) {
	m := reRule.FindStringSubmatch(r)
	if m == nil {
		return "", "", false
	}
	return m[1], strings.TrimSpace(m[2]), true
}

// toolIs: deny and ask rules accept a glob in the tool name ("*"); allow rules don't.
func toolIs(pattern, tool string, glob bool) bool {
	if pattern == tool {
		return true
	}
	m, _ := path.Match(pattern, tool)
	return glob && m
}

// bashRule reports whether a Bash rule matches one command: `*` stands for any text, a trailing
// ` *` (or `:*`) also matches the bare command, and no `*` means the exact command.
func bashRule(r, cmd string, glob bool) bool {
	tool, spec, ok := splitRule(r)
	if !ok || !toolIs(tool, "Bash", glob) {
		return false
	}
	if tool != "Bash" || spec == "" || spec == "*" {
		return tool == "Bash" || spec == ""
	}
	if s, ok := strings.CutSuffix(spec, ":*"); ok {
		spec = s + " *"
	} else if reParamRule.MatchString(spec) {
		return false
	}
	if !strings.Contains(spec, "*") {
		return spec == cmd
	}
	if strings.Count(spec, "*") == 1 && strings.HasSuffix(spec, " *") && cmd == strings.TrimSuffix(spec, " *") {
		return true
	}
	parts := strings.Split(spec, "*")
	if !strings.HasPrefix(cmd, parts[0]) {
		return false
	}
	rest := cmd[len(parts[0]):]
	for _, p := range parts[1 : len(parts)-1] {
		i := strings.Index(rest, p)
		if i < 0 {
			return false
		}
		rest = rest[i+len(p):]
	}
	return strings.HasSuffix(rest, parts[len(parts)-1])
}

// broadAllow: the allow rules auto mode drops (a blanket Bash, interpreters, package-manager runs).
func broadAllow(r string) bool {
	_, spec, _ := splitRule(r)
	spec = strings.TrimSuffix(spec, ":*")
	before, _, _ := strings.Cut(spec, "*")
	switch strings.TrimSpace(before) {
	case "", "npm run", "npm exec", "npx", "pnpm", "pnpm run", "pnpm exec", "pnpm dlx", "yarn", "yarn run", "bun run", "bunx",
		"uv run", "poetry run", "python", "python3", "node", "bundle exec", "sh", "bash":
		return true
	}
	return false
}

// quotable: a rule the report may print. One saved from a prompt can carry a URL, a password in an
// assignment or a token; those are named by file only.
func quotable(r string) bool {
	return len(r) <= 80 && !strings.ContainsAny(r, "=@$`'\"") && !strings.Contains(r, "://") && !reToken.MatchString(r)
}

const (
	oNone = iota
	oAsks
	oDenied
	oRuns
)

type verdict struct {
	o    int
	src  *permSource
	rule string // the matching rule; "" when the mode decided
}

type modeInfo struct {
	mode string // "" is the built-in default: auto on recent versions, Manual on others
	src  *permSource
}

// effectiveMode follows the permission-modes page: local, then project, then user settings; "auto"
// in a project or local file falls back to the built-in default and "bypassPermissions" there starts
// in Manual; disableBypassPermissionsMode in any file turns bypass off.
func effectiveMode(srcs []*permSource) modeInfo {
	m, found := modeInfo{}, false
	for _, scope := range []string{"local", "project"} {
		for _, s := range srcs {
			if found || s.scope != scope || s.mode == "" {
				continue
			}
			found = true
			switch s.mode {
			case "auto":
			case "bypassPermissions":
				m = modeInfo{"default", s}
			default:
				m = modeInfo{s.mode, s}
			}
		}
	}
	for _, s := range srcs {
		if !found && s.scope == "user" && s.mode != "" {
			m, found = modeInfo{s.mode, s}, true
		}
	}
	for _, s := range srcs {
		if s.noBypass && m.mode == "bypassPermissions" {
			m = modeInfo{"default", s}
		}
		// disableAutoMode in any file: the built-in default is Manual, and auto starts in Manual.
		if s.noAuto && (m.mode == "auto" || m.mode == "") {
			m = modeInfo{"default", s}
		}
	}
	return m
}

// autoOff names the settings file that turns auto mode off, or "".
func autoOff(srcs []*permSource) string {
	for _, s := range srcs {
		if s.noAuto {
			return s.label
		}
	}
	return ""
}

// decide: deny, then ask, then allow, the first match wins; deny and ask hold in every mode.
func decide(cmd string, srcs []*permSource, m modeInfo) verdict {
	for _, s := range srcs {
		for _, r := range s.deny {
			if bashRule(r, cmd, true) {
				return verdict{oDenied, s, r}
			}
		}
	}
	for _, s := range srcs {
		for _, r := range s.ask {
			if bashRule(r, cmd, true) {
				return verdict{oAsks, s, r}
			}
		}
	}
	if m.mode == "bypassPermissions" {
		return verdict{oRuns, m.src, ""}
	}
	for _, s := range srcs {
		for _, r := range s.allow {
			if bashRule(r, cmd, false) {
				return verdict{oRuns, s, r}
			}
		}
	}
	if m.mode == "dontAsk" {
		return verdict{oDenied, m.src, ""}
	}
	return verdict{}
}

// readDenied reports whether a Read deny rule covers the file. Patterns follow gitignore: a bare
// name matches at any depth, `/` starts at the settings source, `~/` at home, `//` at the root.
func readDenied(srcs []*permSource, abs, cwd, home string) bool {
	for _, s := range srcs {
		for _, r := range s.deny {
			tool, spec, ok := splitRule(r)
			if !ok || !toolIs(tool, "Read", true) {
				continue
			}
			if spec == "" || (tool == "Read" && readRule(spec, s.anchor, abs, cwd, home)) {
				return true
			}
		}
	}
	return false
}

func readRule(spec, anchor, abs, cwd, home string) bool {
	if strings.HasPrefix(spec, "!") {
		return false // a carve-out never adds cover
	}
	base, pat, anchored := cwd, spec, false
	switch {
	case strings.HasPrefix(spec, "//"):
		base, pat, anchored = "/", spec[2:], true
	case strings.HasPrefix(spec, "~/"):
		base, pat, anchored = home, spec[2:], true
	case strings.HasPrefix(spec, "/"):
		base, pat, anchored = anchor, spec[1:], true
	case strings.HasPrefix(spec, "./"):
		// The permissions page reads Read(./.env) as the .env in the current directory; the scan
		// takes it at that word and never at any depth, so it can't claim cover that isn't there.
		pat, anchored = spec[2:], true
	}
	if base == "" {
		return false
	}
	rel, err := filepath.Rel(base, abs)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return false
	}
	segs := strings.Split(filepath.ToSlash(rel), "/")
	pat = strings.TrimSuffix(pat, "/")
	if pat == "" || pat == "**" {
		return true
	}
	if !anchored && !strings.Contains(pat, "/") {
		for _, s := range segs {
			if m, _ := path.Match(pat, s); m {
				return true
			}
		}
		return false
	}
	if dir, ok := strings.CutSuffix(pat, "/**"); ok && !anchored && !strings.Contains(dir, "/") {
		for _, s := range segs[:len(segs)-1] {
			if m, _ := path.Match(dir, s); m {
				return true
			}
		}
	}
	ps := strings.Split(pat, "/")
	for n := len(segs); n >= 1; n-- {
		if globSegs(ps, segs[:n]) {
			return true
		}
	}
	return false
}

func globSegs(p, s []string) bool {
	if len(p) == 0 {
		return len(s) == 0
	}
	if p[0] == "**" {
		for i := 0; i <= len(s); i++ {
			if globSegs(p[1:], s[i:]) {
				return true
			}
		}
		return false
	}
	if len(s) == 0 {
		return false
	}
	if m, _ := path.Match(p[0], s[0]); !m {
		return false
	}
	return globSegs(p[1:], s[1:])
}

// --- spellings ---

// runners are the ways an agent usually starts a project's CLI, per ecosystem.
var (
	runners = map[string][]string{
		"npm":  {"", "npx ", "pnpm ", "pnpm exec ", "yarn ", "bunx "},
		"pypi": {"", "uv run ", "poetry run ", "python -m ", "python3 -m "},
		"gem":  {"", "bundle exec "},
	}
	scriptRunners = []string{"npm run ", "pnpm ", "pnpm run ", "yarn ", "yarn run ", "bun run "}
)

type spelling struct {
	text   string // what an agent would type
	rule   string // the ask rule that covers it and every other runner
	cmd    int    // the catalog command
	script string // the package.json script it runs through, if any
}

func ecosystem(e *CatalogEntry) string {
	eco, _, _ := strings.Cut(e.Match[0], ":")
	return eco
}

// forms are the command with each of the entry's program names: "prog rest" and its args.
func forms(e *CatalogEntry, c CatalogCommand) [][2]string {
	first, rest, _ := strings.Cut(c.Run, " ")
	progs := e.Programs
	if len(progs) == 0 {
		progs = []string{first}
	}
	var out [][2]string
	for _, p := range progs {
		out = append(out, [2]string{strings.TrimSpace(p + " " + rest), c.Args})
	}
	return out
}

func canon(c CatalogCommand) string { return strings.TrimSpace(c.Run + " " + c.Args) }

// askRule is one ask rule for every spelling of a command: the leading `*` matches any runner, and
// starts before the subcommand when the program has several names (rails, bin/rails, rake).
func askRule(e *CatalogEntry, c CatalogCommand) string {
	prog, rest, _ := strings.Cut(c.Run, " ")
	if len(e.Programs) > 1 {
		prog = ""
	}
	return "Bash(*" + prog + " " + rest + "*)"
}

type shellGroup struct {
	entry     *CatalogEntry
	root      string
	files     map[string]bool
	spellings []spelling
	seen      map[string]bool
}

func (g *shellGroup) add(sp spelling) {
	if !g.seen[sp.text] {
		g.seen[sp.text] = true
		g.spellings = append(g.spellings, sp)
	}
}

// governing is the nearest directory at or above dir that holds Claude Code project settings.
func governing(roots map[string]bool, dir string) string {
	for {
		if roots[dir] {
			return dir
		}
		if dir == "." || dir == "/" || dir == "" {
			return "."
		}
		dir = path.Dir(dir)
	}
}

func sourcesFor(all []*permSource, root string) []*permSource {
	var out []*permSource
	for _, scope := range []string{"local", "project", "user"} {
		for _, s := range all {
			if s.scope == scope && (scope == "user" || s.root == root) {
				out = append(out, s)
			}
		}
	}
	return out
}

func checkedLine(srcs []*permSource) string {
	if len(srcs) == 0 {
		return "No Claude Code settings file was found for this directory, so its defaults apply. Managed settings and command-line flags weren't read."
	}
	var l []string
	for _, s := range srcs {
		l = append(l, s.label)
	}
	return "Read " + joinAnd(l) + ". Managed settings and command-line flags weren't read and can change this."
}

func joinAnd(l []string) string {
	if len(l) < 2 {
		return strings.Join(l, "")
	}
	return strings.Join(l[:len(l)-1], ", ") + " and " + l[len(l)-1]
}

func code(s string) string { return "`" + s + "`" }

// askFix is the fix line: the rules to add, each quoted as it goes into the file.
func askFix(rules []string) string {
	var q []string
	for _, r := range uniq(rules) {
		q = append(q, `"`+r+`"`)
	}
	return "Add " + joinAnd(q) + " to permissions.ask. The leading * matches any runner, such as npx, pnpm or uv run, and an ask rule makes Claude Code ask first, even in auto mode."
}

// does says what a spelling runs: the command itself, or the script that runs it.
func does(sp spelling, c CatalogCommand) string {
	if sp.script != "" {
		return fmt.Sprintf("The %s script runs %s, which %s.", sp.script, code(canon(c)), c.Does)
	}
	return fmt.Sprintf("%s %s.", code(canon(c)), c.Does)
}

// shellFindings builds the FEAT-05 findings: for each database CLI and project directory, the
// spellings that run without a prompt and the ones no rule covers; then the .env files no deny rule
// covers.
func shellFindings(r *Report, d *discovered, o Options) []Finding {
	groups := map[string]*shellGroup{}
	var order []string
	group := func(e *CatalogEntry, file string) *shellGroup {
		root := governing(d.roots, path.Dir(file))
		k := e.ID + "\x00" + root
		g := groups[k]
		if g == nil {
			g = &shellGroup{entry: e, root: root, files: map[string]bool{}, seen: map[string]bool{}}
			groups[k] = g
			order = append(order, k)
		}
		g.files[file] = true
		return g
	}
	for _, dep := range r.Dependencies {
		e := entryByID(dep.CatalogID)
		if e == nil || e.Kind != "database_cli" {
			continue
		}
		g := group(e, dep.File)
		for ci, c := range e.Commands {
			for _, run := range runners[ecosystem(e)] {
				for _, f := range forms(e, c) {
					g.add(spelling{text: strings.TrimSpace(run + f[0] + " " + f[1]), rule: askRule(e, c), cmd: ci})
				}
			}
		}
	}
	for _, sc := range d.scripts {
		for i := range catalog {
			e := &catalog[i]
			if e.Kind != "database_cli" || ecosystem(e) != "npm" {
				continue
			}
			for ci, c := range e.Commands {
				for _, f := range forms(e, c) {
					if !strings.Contains(sc.body, f[0]) || (c.NeedsArgs && !strings.Contains(sc.body, c.Args)) {
						continue
					}
					g := group(e, sc.file)
					for _, run := range scriptRunners {
						g.add(spelling{text: run + sc.name, rule: "Bash(* " + sc.name + "*)", cmd: ci, script: sc.name})
					}
				}
			}
		}
	}
	var out []Finding
	for _, k := range order {
		out = append(out, groupFindings(groups[k], sourcesFor(d.sources, groups[k].root))...)
	}
	if f := envFinding(r, d, o); f != nil {
		out = append(out, *f)
	}
	return out
}

// groupFindings: shell_destructive_unprompted for the spellings an allow rule or bypass mode runs,
// then shell_destructive_no_rule for the commands with a spelling that only the mode decides.
func groupFindings(g *shellGroup, srcs []*permSource) []Finding {
	e := g.entry
	m := effectiveMode(srcs)
	type run struct {
		sp spelling
		v  verdict
	}
	var runs []run
	open := map[int][]spelling{}  // spellings no rule matches, per command
	coveredBy := map[int]string{} // a spelling an ask or deny rule matches, per command
	ran := map[int]bool{}
	for _, sp := range g.spellings {
		v := decide(sp.text, srcs, m)
		switch {
		case v.o == oRuns:
			runs = append(runs, run{sp, v})
			ran[sp.cmd] = true
		case v.rule != "":
			if coveredBy[sp.cmd] == "" {
				coveredBy[sp.cmd] = sp.text
			}
		case v.o == oNone:
			open[sp.cmd] = append(open[sp.cmd], sp)
		}
	}
	var files []string
	for f := range g.files {
		files = append(files, f)
	}
	sort.Strings(files)
	var sources []string
	covers := func(ci int, text string) string {
		if coveredBy[ci] == "" {
			return ""
		}
		return fmt.Sprintf("A rule covers %s, not %s: a rule matches the text of a command, not the program.", code(coveredBy[ci]), code(text))
	}
	finish := func(f Finding) Finding {
		sources = append(sources, e.Incidents...)
		f.Sources = uniq(append(sources, permDocs))
		f.Checked = checkedLine(srcs)
		sources = nil
		return f
	}
	var out []Finding
	if len(runs) > 0 {
		first := runs[0]
		c := e.Commands[first.sp.cmd]
		var why string
		switch {
		case first.v.rule == "":
			why = fmt.Sprintf("defaultMode bypassPermissions in %s skips prompts, and no ask or deny rule matches it.", first.v.src.label)
		case quotable(first.v.rule):
			why = fmt.Sprintf("The allow rule %s in %s matches it, and no ask or deny rule does.", code(first.v.rule), first.v.src.label)
		default:
			why = fmt.Sprintf("An allow rule in %s matches it, and no ask or deny rule does.", first.v.src.label)
		}
		detail := []string{why, does(first.sp, c)}
		if c.Note != "" {
			detail = append(detail, c.Note)
		}
		if s := covers(first.sp.cmd, first.sp.text); s != "" {
			detail = append(detail, s)
		}
		if first.v.rule != "" && broadAllow(first.v.rule) {
			detail = append(detail, "In auto mode, Claude Code drops broad allow rules like this one and a classifier reviews the command instead.")
		}
		var also, rules []string
		for i, x := range runs {
			rules = append(rules, x.sp.rule)
			if i > 0 && len(also) < 3 {
				also = append(also, code(x.sp.text))
			}
			sources = append(sources, e.Commands[x.sp.cmd].Source)
		}
		for ci := range e.Commands {
			for _, sp := range open[ci] {
				if ran[ci] { // the same command, spelled in a way only the mode decides
					rules = append(rules, sp.rule)
				}
			}
		}
		if len(also) > 0 {
			s := "Also without a prompt: " + strings.Join(also, ", ")
			if n := len(runs) - 1 - len(also); n > 0 {
				s += fmt.Sprintf(" and %d more", n)
			}
			detail = append(detail, s+".")
		}
		fix := []string{askFix(rules)}
		if first.v.rule != "" {
			fix = append(fix, "Or narrow the allow rule so it no longer matches.")
		}
		out = append(out, finish(Finding{ID: "shell_destructive_unprompted", Severity: "high",
			Title:  fmt.Sprintf("An agent can run %s here without a prompt", code(first.sp.text)),
			Detail: strings.Join(detail, " "), Fix: fix, Paths: uniq(append(files, first.v.src.label))}))
	}
	if m.mode == "dontAsk" {
		return out
	}
	var cmds []int
	for ci := range e.Commands {
		if len(open[ci]) > 0 && !ran[ci] {
			cmds = append(cmds, ci)
		}
	}
	if len(cmds) == 0 {
		return out
	}
	first := open[cmds[0]][0]
	c := e.Commands[cmds[0]]
	sev, paths := "low", files
	modeLine := "Claude Code asks first in Manual mode. In auto mode, its default on recent versions, a classifier decides instead of you."
	if off := autoOff(srcs); off != "" {
		modeLine = fmt.Sprintf("disableAutoMode in %s turns auto mode off, so Claude Code asks first.", off)
	} else if m.mode == "auto" {
		sev, paths = "medium", uniq(append(files, m.src.label))
		modeLine = fmt.Sprintf("defaultMode auto in %s sends it to a classifier, which decides instead of you.", m.src.label)
	}
	detail := []string{does(first, c)}
	if c.Note != "" {
		detail = append(detail, c.Note)
	}
	if s := covers(cmds[0], first.text); s != "" {
		detail = append(detail, s)
	}
	detail = append(detail, modeLine)
	var also, rules []string
	for i, ci := range cmds {
		if i > 0 && len(also) < 3 {
			also = append(also, code(open[ci][0].text))
		}
		for _, sp := range open[ci] {
			rules = append(rules, sp.rule)
		}
		sources = append(sources, e.Commands[ci].Source)
	}
	if len(also) > 0 {
		detail = append(detail, "No rule covers "+joinAnd(also)+" either.")
	}
	sources = append(sources, modeDocs)
	return append(out, finish(Finding{ID: "shell_destructive_no_rule", Severity: sev,
		Title:  fmt.Sprintf("No ask or deny rule covers %s", code(first.text)),
		Detail: strings.Join(detail, " "), Fix: []string{askFix(rules)}, Paths: paths}))
}

// exampleEnv: files meant to be committed with placeholders (.env.example and the like).
func exampleEnv(file string) bool {
	for _, s := range []string{".example", ".sample", ".template", ".dist"} {
		if strings.HasSuffix(file, s) {
			return true
		}
	}
	return false
}

func envFinding(r *Report, d *discovered, o Options) *Finding {
	var files, list []string
	var srcsSeen []*permSource
	for _, e := range r.EnvNames {
		if exampleEnv(e.File) {
			continue
		}
		root := governing(d.roots, path.Dir(e.File))
		srcs := sourcesFor(d.sources, root)
		abs := filepath.Join(o.Dir, filepath.FromSlash(e.File))
		if readDenied(srcs, abs, filepath.Join(o.Dir, filepath.FromSlash(root)), o.Home) {
			continue
		}
		files = append(files, e.File)
		list = append(list, fmt.Sprintf("%s (%s)", code(e.File), strings.Join(e.Names, ", ")))
		for _, s := range srcs {
			dup := false
			for _, x := range srcsSeen {
				dup = dup || x == s
			}
			if !dup {
				srcsSeen = append(srcsSeen, s)
			}
		}
	}
	if len(files) == 0 {
		return nil
	}
	first := r.EnvNames[0]
	for _, e := range r.EnvNames {
		if e.File == files[0] {
			first = e
		}
	}
	return &Finding{ID: "env_database_url_readable", Severity: "medium",
		Title: fmt.Sprintf("An agent can read %s in %s without a prompt", first.Names[0], first.File),
		Detail: "Claude Code reads project files without a prompt, and no deny rule the scan read covers " + joinAnd(list) +
			". With the URL, an agent can point any command at that database.",
		Checked: checkedLine(srcsSeen),
		Fix: []string{`Add "Read(.env)" and "Read(.env.*)" to permissions.deny in .claude/settings.json: a bare file name matches at any depth.`,
			"That stops Claude Code's file tools and commands such as cat or head that name the file. A program that opens the file itself, such as prisma, still reads it."},
		Paths: files, Sources: []string{permDocs, "https://github.com/anthropics/claude-code/issues/80868"}}
}

// preToolUseBash reports whether a hooks block has a PreToolUse entry that runs before Bash: no
// matcher (or an empty one), "*", ".*", or a matcher naming Bash, with at least one hook command.
func preToolUseBash(raw json.RawMessage) bool {
	var h struct {
		PreToolUse []struct {
			Matcher string            `json:"matcher"`
			Hooks   []json.RawMessage `json:"hooks"`
		} `json:"PreToolUse"`
	}
	if len(raw) == 0 || json.Unmarshal(raw, &h) != nil {
		return false
	}
	for _, e := range h.PreToolUse {
		if len(e.Hooks) == 0 {
			continue
		}
		m := strings.TrimSpace(e.Matcher)
		if m == "" || m == "*" || m == ".*" || regexp.MustCompile(`(^|[^A-Za-z])Bash([^A-Za-z]|$)`).MatchString(m) {
			return true
		}
	}
	return false
}
