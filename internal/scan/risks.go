package scan

import (
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strings"
)

// Three risks the agent-permissions study (2,511 public configs, October 2026) found in configs the
// grade still marked A: a database MCP server with no read-only setting, a credential written into a
// client config, and blanket shell access with no before-a-command hook. Each is a finding that costs
// points, so the grade reflects it. Values never leave this file's functions: a finding names the
// server, the variable or the file, never the secret.

// reDatabaseName: a server whose name, package or command says it talks to a database.
var reDatabaseName = regexp.MustCompile(`(?i)postgres|\bpg\b|pgsql|mysql|mariadb|sqlite|mongo|supabase|redis|\bneon\b|prisma|mssql|sql-?server|clickhouse|snowflake|bigquery|dynamodb|firestore|planetscale|duckdb|oracle|cockroach|cassandra|timescale|turso|libsql|surreal|\bdb\b|-db\b|\bdb-|dbhub|database|\bsql\b`)

// reNotDatabase: tools with a database word in the name that don't connect to one.
var reNotDatabase = regexp.MustCompile(`(?i)mongo-?express|sqlfluff|sql-?formatter|sql-?lint`)

// reReadOnly: the usual ways a database server is told to only read.
var reReadOnly = regexp.MustCompile(`(?i)read[-_ ]?only|readonly|--ro\b|access[-_]mode[=:\s"]*restricted|allow[-_]?writes?[=:\s"]*false|--no-?write|safe[-_]?mode`)

// Credential shapes (vendor prefixes), a database URL with a password, and placeholder text.
var (
	reTokens = []*regexp.Regexp{
		regexp.MustCompile(`\b(ghp|gho|ghu|ghs|ghr)_[A-Za-z0-9]{30,}|\bgithub_pat_[A-Za-z0-9_]{40,}`),
		regexp.MustCompile(`\bsk-(ant-|proj-)?[A-Za-z0-9_\-]{24,}`),
		regexp.MustCompile(`\b(AKIA|ASIA)[A-Z0-9]{16}\b`),
		regexp.MustCompile(`\bAIza[0-9A-Za-z_\-]{35}\b`),
		regexp.MustCompile(`\bxox[abprs]-[A-Za-z0-9\-]{10,}`),
		regexp.MustCompile(`\bglpat-[A-Za-z0-9_\-]{20}`),
		regexp.MustCompile(`\b(sk|rk)_(live|test)_[A-Za-z0-9]{16,}`),
		regexp.MustCompile(`\beyJ[A-Za-z0-9_\-]{20,}\.eyJ[A-Za-z0-9_\-]{20,}\.[A-Za-z0-9_\-]{10,}`),
	}
	reURLPassword = regexp.MustCompile(`(?i)\b(postgres(?:ql)?|mysql|mariadb|mongodb(?:\+srv)?|rediss?|mssql|sqlserver)://[^:/\s@]+:([^@\s]+)@`)
	rePlaceholder = regexp.MustCompile(`(?i)your|xxx|\.\.\.|<|>|\$\{|\{\{|changeme|example|placeholder|replace|insert|dummy|sample|todo|redacted|\*\*\*|env:|process\.env|my[-_]|test[-_]?key|here|abc123|123456`)
	reSecretName  = regexp.MustCompile(`(?i)(KEY|TOKEN|SECRET|PASSWORD|PASSWD|PWD|PASS|CREDENTIAL|AUTH|PAT)$|API_?KEY|ACCESS_?KEY|PRIVATE`)
	reURLKeyParam = regexp.MustCompile(`(?i)[?&](api[_-]?key|token|key|access_token)=([^&]+)`)
)

// trivialPasswords are local-development passwords, not secrets anyone needs to rotate.
var trivialPasswords = map[string]bool{"postgres": true, "password": true, "root": true, "admin": true, "user": true, "pass": true, "secret": true, "test": true}

func entropy(s string) float64 {
	if s == "" {
		return 0
	}
	n := float64(len(s))
	counts := map[rune]float64{}
	for _, r := range s {
		counts[r]++
	}
	h := 0.0
	for _, c := range counts {
		h -= c / n * math.Log2(c/n)
	}
	return h
}

// looksLikeSecret reports whether a literal value (under name, when it has one) is a real-looking
// credential rather than a reference, a placeholder or a local-development default.
func looksLikeSecret(name, v string) bool {
	if len(v) < 8 || strings.HasPrefix(v, "$") || strings.HasPrefix(v, "%") {
		return false
	}
	for _, re := range reTokens {
		if m := re.FindString(v); m != "" && !rePlaceholder.MatchString(m) && entropy(m) > 3.3 {
			return true
		}
	}
	if m := reURLPassword.FindStringSubmatch(v); m != nil {
		pw := m[2]
		if len(pw) >= 6 && !trivialPasswords[strings.ToLower(pw)] && !rePlaceholder.MatchString(pw) {
			return true
		}
	}
	if name != "" && reSecretName.MatchString(name) && len(v) >= 20 && !strings.ContainsAny(v, " /~") &&
		!strings.HasPrefix(v, "http") && !strings.HasPrefix(v, ".") && !rePlaceholder.MatchString(v) &&
		strings.ContainsAny(v, "0123456789") && entropy(v) > 3.6 {
		return true
	}
	return false
}

// serverRisks fills Database, ReadOnly and PlaintextSecrets for one raw server entry.
func serverRisks(out *Server, s rawServer) {
	if e := entryByID(out.CatalogID); e != nil && e.Database {
		out.Database = true
	}
	hay := strings.Join([]string{out.Name, out.Package, out.Command}, " ")
	if !out.Database && reDatabaseName.MatchString(hay) && !reNotDatabase.MatchString(hay) && out.Package != "deedglass" {
		out.Database = true
	}
	// Read-only markers can sit in the arguments, the environment (names or values), the URL or
	// a header.
	var blob []string
	blob = append(blob, s.Args...)
	envNames := make([]string, 0, len(s.Env))
	for k, v := range s.Env {
		envNames = append(envNames, k)
		if str, ok := v.(string); ok {
			blob = append(blob, k+"="+str)
		} else {
			b, _ := json.Marshal(v)
			blob = append(blob, k+"="+string(b))
		}
	}
	blob = append(blob, s.URL)
	for k, v := range s.Headers {
		blob = append(blob, k+": "+v)
	}
	out.ReadOnly = reReadOnly.MatchString(strings.Join(blob, "\n"))

	// Credentials written into the file: env values, header values, arguments and the URL. A
	// Postgres URL in the arguments already has its own finding (database_url_in_client_args).
	var where []string
	sort.Strings(envNames)
	for _, k := range envNames {
		if v, ok := s.Env[k].(string); ok && looksLikeSecret(k, v) {
			where = append(where, "env "+k)
		}
	}
	hnames := make([]string, 0, len(s.Headers))
	for k := range s.Headers {
		hnames = append(hnames, k)
	}
	sort.Strings(hnames)
	for _, k := range hnames {
		v := s.Headers[k]
		if b, ok := strings.CutPrefix(v, "Bearer "); ok {
			v = b
			if looksLikeSecret("AUTH_TOKEN", v) {
				where = append(where, "header "+k)
				continue
			}
		}
		if looksLikeSecret(k, v) {
			where = append(where, "header "+k)
		}
	}
	for i, a := range s.Args {
		if reDBURL.MatchString(a) {
			continue
		}
		name := ""
		if k, v, ok := strings.Cut(a, "="); ok && strings.HasPrefix(k, "--") {
			name, a = strings.ToUpper(strings.ReplaceAll(strings.TrimLeft(k, "-"), "-", "_")), v
		} else if i > 0 && strings.HasPrefix(s.Args[i-1], "--") {
			name = strings.ToUpper(strings.ReplaceAll(strings.TrimLeft(s.Args[i-1], "-"), "-", "_"))
		}
		if looksLikeSecret(name, a) {
			where = append(where, fmt.Sprintf("argument %d", i+1))
		}
	}
	if s.URL != "" {
		u := s.URL
		if m := reURLKeyParam.FindStringSubmatch(u); m != nil && looksLikeSecret("API_KEY", m[2]) || looksLikeSecret("", u) {
			where = append(where, "url")
		}
	}
	out.PlaintextSecrets = uniq(where)
}

// riskFindings: database_server_not_read_only and plaintext_secret_in_config for the MCP configs,
// and shell_no_guard_hook for the Claude Code settings.
func riskFindings(r *Report, d *discovered, prior []Finding) []Finding {
	var out []Finding
	dbSev := "high"
	checked := ""
	if r.Postgres != nil {
		checked = checkedRole(r.Postgres)
		if !roleCanWrite(r.Postgres) {
			dbSev = "low"
		}
	}
	for _, c := range r.Configs {
		for _, s := range c.Servers {
			if s.Database && !s.ReadOnly && !s.Routed && s.Package != ArchivedPostgres {
				out = append(out, Finding{ID: "database_server_not_read_only", Severity: dbSev,
					Title:   "A database MCP server has no read-only setting",
					Detail:  fmt.Sprintf("Server %q connects to a database, and nothing in its entry asks for read-only. If its database user can write, the agent can write.", s.Name),
					Checked: checked,
					Fix: []string{"Give the server a database user that can only read, or turn on its read-only option.",
						"Or route it through Deedglass: deedglass mcp wrap --connector <name> -- <the server's command>."},
					Paths: []string{c.Path}, Sources: []string{}})
			}
			if len(s.PlaintextSecrets) > 0 {
				out = append(out, Finding{ID: "plaintext_secret_in_config", Severity: "high",
					Title:  "A credential sits in plain text in a client config",
					Detail: fmt.Sprintf("Server %q has what looks like a real credential in its %s. Anyone who can read or copy the file has it, and so does every agent that can read files here.", s.Name, joinAnd(s.PlaintextSecrets)),
					Fix: []string{"Rotate the credential, then have the client read it from an environment variable (for example \"${GITHUB_TOKEN}\") instead of the file.",
						"Keep the file out of version control until it holds no secret."},
					Paths: []string{c.Path}, Sources: []string{}})
			}
		}
	}
	unprompted := map[string]bool{}
	for _, f := range prior {
		if f.ID == "shell_destructive_unprompted" {
			for _, p := range f.Paths {
				unprompted[p] = true
			}
		}
	}
	roots := map[string]bool{}
	var order []string
	for _, s := range d.sources {
		k := s.root
		if s.scope == "user" {
			k = "."
		}
		if !roots[k] {
			roots[k] = true
			order = append(order, k)
		}
	}
	sort.Strings(order)
	for _, root := range order {
		srcs := sourcesFor(d.sources, root)
		var open *permSource
		var openRule string
		guarded, denied := false, false
		for _, s := range srcs {
			if s.preBash {
				guarded = true
			}
			// An ask or deny rule on Bash means someone chose which commands stop for a person
			// (the scan's own shell findings suggest ask rules), so this finding stays quiet.
			for _, rule := range append(append([]string{}, s.deny...), s.ask...) {
				if t, _, ok := splitRule(rule); ok && toolIs(t, "Bash", true) {
					denied = true
				}
			}
		}
		m := effectiveMode(srcs)
		if m.mode == "bypassPermissions" && m.src != nil {
			open = m.src
		}
		if open == nil {
			for _, s := range srcs {
				for _, rule := range s.allow {
					if t, _, ok := splitRule(rule); ok && t == "Bash" && broadAllow(rule) {
						open, openRule = s, rule
						break
					}
				}
				if open != nil {
					break
				}
			}
		}
		if open == nil || guarded || denied || unprompted[open.label] {
			continue
		}
		why := fmt.Sprintf("defaultMode bypassPermissions in %s runs every command without a prompt.", open.label)
		if openRule != "" {
			if quotable(openRule) {
				why = fmt.Sprintf("The allow rule %s in %s runs a whole class of commands without a prompt.", code(openRule), open.label)
			} else {
				why = fmt.Sprintf("A broad allow rule in %s runs a whole class of commands without a prompt.", open.label)
			}
		}
		out = append(out, Finding{ID: "shell_no_guard_hook", Severity: "high",
			Title:   "Shell commands run without a prompt and with no PreToolUse hook",
			Detail:  why + " No PreToolUse hook checks a Bash command first, and no ask or deny rule covers Bash, so nothing stands between the agent and a destructive command.",
			Checked: checkedLine(srcs),
			Fix: []string{"Add a PreToolUse hook for Bash: deedglass protect adds the shell guard, which asks before a command that destroys a database, a volume or infrastructure.",
				"Or narrow the allow rule, or add deny rules for the commands that must never run."},
			Paths: []string{open.label}, Sources: []string{permDocs, "https://code.claude.com/docs/en/hooks"}})
	}
	return out
}
