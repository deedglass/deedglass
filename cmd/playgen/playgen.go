package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	demopolicies "github.com/deedglass/deedglass/examples/demo/policies"
	demosql "github.com/deedglass/deedglass/examples/demo/sql"
	"github.com/deedglass/deedglass/examples/policies"
	"github.com/deedglass/deedglass/internal/policy"
	"github.com/deedglass/deedglass/internal/policy/celexport"
	"github.com/deedglass/deedglass/internal/scan"
	"github.com/deedglass/deedglass/internal/sqlguard"
)

// File is one generated file.
type File struct {
	Name string
	Data []byte
}

// The decision instant and agent of every playground evaluation. The playground has no clock.
var (
	playTime  = time.Date(2026, 10, 14, 9, 0, 0, 0, time.UTC)
	playAgent = "claude-code"
)

// Statement is one entry of the scripted agent's menu.
type Statement struct {
	ID        string `json:"id"`
	Label     string `json:"label"`
	Tool      string `json:"tool"`
	SQL       string `json:"sql"`
	AgentSays string `json:"agent_says"`
	Target    Target `json:"target"` // the rows the statement names; the snapshot check compares against them
}

// Target is the table and primary keys a statement names (empty ids: it names no single row).
type Target struct {
	Table string `json:"table"`
	IDs   []int  `json:"ids"`
}

// The eight statements (S47 §3.1). The scenes use drop and ship42.
var statements = []Statement{
	{"drop", "Drop the orders table", "postgres.execute", "DROP TABLE public.orders", "Dropped the old table.", Target{"public.orders", []int{}}},
	{"ship42", "Ship order 42", "postgres.execute", "UPDATE orders SET status = 'shipped' WHERE id = 42", "Done, 1 row.", Target{"public.orders", []int{42}}},
	{"read42", "Read order 42", "postgres.query", "SELECT id, status, total_cents FROM orders WHERE id = 42", "Order 42 is paid.", Target{"public.orders", []int{}}},
	{"cancel7", "Cancel order 7", "postgres.execute", "UPDATE orders SET status = 'cancelled' WHERE id = 7", "Cancelled order 7, 1 row.", Target{"public.orders", []int{7}}},
	{"unbounded", "Mark every order shipped", "postgres.execute", "UPDATE orders SET status = 'shipped'", "Marked the orders shipped.", Target{"public.orders", []int{}}},
	{"delete42", "Delete order 42", "postgres.execute", "DELETE FROM orders WHERE id = 42", "Removed order 42, 1 row.", Target{"public.orders", []int{42}}},
	{"truncate", "Empty the refunds table", "postgres.execute", "TRUNCATE public.refunds", "Cleared the refunds table.", Target{"public.refunds", []int{}}},
	{"refund", "Refund $5.00 on order 42", "postgres.execute", "INSERT INTO refunds (id, order_id, amount_cents, reason, created_at) VALUES (61, 42, 500, 'Late delivery', now())", "Refunded $5.00 on order 42, 1 row.", Target{"public.refunds", []int{61}}},
}

// Fixture is a statement with the facts Deedglass's own SQL reader derives for it.
type Fixture struct {
	Statement
	Facts fixtureFacts `json:"facts"`
}

type fixtureFacts struct {
	Tool policy.ToolFacts `json:"tool"`
	SQL  policy.SQLFacts  `json:"sql"`
}

// ParityCase is one row of parity.golden.json.
type ParityCase struct {
	ID      string `json:"id"`
	Policy  string `json:"policy"`           // acme-demo, postgres-safe, or acme-demo with the editor's rules first
	Editor  string `json:"editor,omitempty"` // the playground editor's text (the subset), when edited
	Fixture string `json:"fixture"`
	Agent   string `json:"agent"`
	Expect  Expect `json:"expect"`
}

// Expect is policy.Evaluate's answer.
type Expect struct {
	Decision string `json:"decision"`
	RuleID   string `json:"rule_id"`
}

type editedCase struct {
	id, editor string
	fixtures   []string
	agents     []string
}

func rule(id, match, when, decision, reason string) string {
	var b strings.Builder
	b.WriteString("rules:\n  - id: " + id + "\n    match: { " + match + " }\n")
	if when != "" {
		b.WriteString("    when: '" + when + "'\n")
	}
	b.WriteString("    decision: " + decision + "\n")
	if reason != "" {
		b.WriteString("    reason: " + reason + "\n")
	}
	return b.String()
}

// SceneEditor is scene 3's edit, shown in the editor (6 lines).
var SceneEditor = rule("writes-need-approval", "tool: postgres.execute", `sql.kind in ["update", "delete"]`, "require_approval", "Writes to orders need a person.")

var edited = []editedCase{
	{"edit-approval", SceneEditor, []string{"ship42", "cancel7", "read42", "delete42"}, nil},
	{"edit-agent-reads", rule("claude-code-reads", "tool: postgres.query, agent: claude-code", "", "allow", ""), []string{"read42"}, []string{"claude-code", "cursor"}},
	{"edit-refunds", rule("refunds-locked", "tool: postgres.execute", `"public.refunds" in sql.tables`, "deny", "The refunds table is closed to agents."), []string{"refund", "truncate"}, nil},
	{"edit-glob-writes", rule("all-writes-wait", `tool: "postgres.*"`, "sql.writes", "require_approval", "Every write waits for a person."), []string{"ship42", "drop"}, nil},
	{"edit-allow-all", rule("allow-everything", `tool: "*"`, "", "allow", ""), []string{"drop", "unbounded"}, nil},
	{"edit-cursor", rule("cursor-read-only", "agent: cursor", "sql.writes", "deny", "Cursor is read-only here."), []string{"ship42"}, []string{"cursor", "claude-code"}},
	{"edit-statements", rule("one-scoped-statement", "tool: postgres.execute", "sql.has_where && sql.statements == 1", "allow", ""), []string{"ship42", "unbounded"}, nil},
	{"edit-two-tables", rule("joins-wait", "tool: postgres.query", "size(sql.tables) > 1", "require_approval", "Reads across tables wait for a person."), []string{"refund"}, nil},
	{"edit-all-public", rule("public-writes-wait", "tool: postgres.execute", `sql.tables.all(t, t.startsWith("public."))`, "require_approval", "Writes to public tables wait."), []string{"cancel7"}, nil},
	// glob and minor edges (the two functions the policy language adds to CEL).
	{"edge-glob-prefix", rule("edge", `tool: "post*"`, `glob(tool.name, "postgres.*")`, "deny", "Edge case."), []string{"read42"}, nil},
	{"edge-glob-question", rule("edge", `tool: "postgres.?uery"`, "", "deny", "Edge case."), []string{"read42"}, nil},
	{"edge-glob-suffix", rule("edge", `tool: "*.execute"`, "", "deny", "Edge case."), []string{"read42"}, nil},
	{"edge-minor-usd", rule("edge", "tool: postgres.query", `minor("120.00 usd") == 12000`, "deny", "Edge case."), []string{"read42"}, nil},
	{"edge-minor-jpy", rule("edge", "tool: postgres.query", `minor("500 jpy") == 501`, "deny", "Edge case."), []string{"read42"}, nil},
	{"edge-minor-kwd", rule("edge", "tool: postgres.query", `minor("1.5 kwd") == 1500`, "deny", "Edge case."), []string{"read42"}, nil},
}

// Generate builds every file.
func Generate() ([]File, error) {
	sqlText, err := AcmeSQL()
	if err != nil {
		return nil, err
	}
	fixtures, err := buildFixtures()
	if err != nil {
		return nil, err
	}
	acmeSrc := demopolicies.AcmeDemo
	safeSrc, err := policies.Source("postgres-safe")
	if err != nil {
		return nil, err
	}
	acme, err := compile(acmeSrc)
	if err != nil {
		return nil, fmt.Errorf("acme-demo: %w", err)
	}
	safe, err := compile(safeSrc)
	if err != nil {
		return nil, fmt.Errorf("postgres-safe: %w", err)
	}
	pols := map[string]any{
		"object":      "play_policies",
		"v":           celexport.Version,
		"scene_rules": SceneEditor,
		"policies":    []celexport.Policy{celexport.Export(acme), celexport.Export(safe)},
		"currencies":  policy.CurrencyExponents(),
	}
	cases, err := parityCases(fixtures, acmeSrc, acme, safe)
	if err != nil {
		return nil, err
	}
	undo, err := undoFacts()
	if err != nil {
		return nil, err
	}
	fx := map[string]any{"object": "play_fixtures", "v": 1, "sqlguard_version": sqlguard.Version,
		"agent": playAgent, "statements": fixtures, "undo": undo}
	golden := map[string]any{"object": "play_parity", "v": 1, "cases": cases}
	var out []File
	out = append(out, File{"acme.sql", []byte(sqlText)})
	for _, f := range []struct {
		name string
		v    any
	}{{"fixtures.json", fx}, {"policies.json", pols}, {"parity.golden.json", golden}, {"share-catalog.json", scan.ShareCatalog()}} {
		b, err := marshal(f.v)
		if err != nil {
			return nil, err
		}
		out = append(out, File{f.name, b})
	}
	return out, nil
}

func marshal(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func compile(src []byte) (*policy.Compiled, error) {
	c, diags := policy.Compile(src, policy.Options{})
	for _, d := range diags {
		if d.Severity == policy.SevError {
			return nil, fmt.Errorf("line %d: %s", d.Line, d.Message)
		}
	}
	if c == nil {
		return nil, fmt.Errorf("did not compile")
	}
	return c, nil
}

func input(f Fixture, agent string) policy.Input {
	args, _ := json.Marshal(map[string]string{"sql": f.SQL})
	return policy.Input{V: policy.InputVersion, Org: policy.OrgInput{OrgFacts: policy.OrgFacts{ID: "org_play", Timezone: "UTC"}},
		Agent: policy.AgentInput{AgentFacts: policy.AgentFacts{Name: agent, Status: policy.StatusActive}},
		Tool:  policy.ToolInput{ToolFacts: policy.ToolFacts{Name: f.Tool, Connector: "postgres", ConnectorType: "postgres", SideEffect: sideEffect(f.Tool), Type: "postgres", Op: strings.TrimPrefix(f.Tool, "postgres.")}, DefaultSchema: "public"},
		Args:  args, Time: playTime}
}

func sideEffect(tool string) string {
	if tool == "postgres.query" {
		return "read"
	}
	return "write"
}

func buildFixtures() ([]Fixture, error) {
	var out []Fixture
	for _, s := range statements {
		f := Fixture{Statement: s}
		p, err := policy.Prepare(input(f, playAgent))
		if err != nil {
			return nil, fmt.Errorf("%s: %w", s.ID, err)
		}
		f.Facts = fixtureFacts{Tool: input(f, playAgent).Tool.ToolFacts, SQL: p.Facts().SQL}
		out = append(out, f)
	}
	return out, nil
}

// undoFacts are the facts of postgres.undo, the internal tool that puts rows back (S23): no SQL
// argument, so the SQL facts are the "none" set. acme-demo's undo-needs-approval rule matches it.
func undoFacts() (fixtureFacts, error) {
	t := policy.ToolFacts{Name: "postgres.undo", Connector: "postgres", ConnectorType: "postgres", SideEffect: "write", Type: "postgres", Op: "undo"}
	in := policy.Input{V: policy.InputVersion, Org: policy.OrgInput{OrgFacts: policy.OrgFacts{ID: "org_play", Timezone: "UTC"}},
		Agent: policy.AgentInput{AgentFacts: policy.AgentFacts{Name: playAgent, Status: policy.StatusActive}},
		Tool:  policy.ToolInput{ToolFacts: t, DefaultSchema: "public"}, Args: json.RawMessage(`{}`), Time: playTime}
	p, err := policy.Prepare(in)
	if err != nil {
		return fixtureFacts{}, err
	}
	return fixtureFacts{Tool: t, SQL: p.Facts().SQL}, nil
}

func fixtureByID(fs []Fixture, id string) Fixture {
	for _, f := range fs {
		if f.ID == id {
			return f
		}
	}
	panic("unknown fixture " + id)
}

func eval(c *policy.Compiled, f Fixture, agent string) Expect {
	d := policy.Evaluate(context.Background(), c, input(f, agent), policy.EvalOptions{})
	return Expect{Decision: string(d.Outcome), RuleID: d.RuleID}
}

// WithEditor puts the editor's rules in front of acme-demo's (first match wins, as in the playground).
func WithEditor(acmeSrc []byte, editor string) ([]byte, error) {
	body, ok := strings.CutPrefix(editor, "rules:\n")
	if !ok {
		return nil, fmt.Errorf("editor text must start with rules:")
	}
	src := string(acmeSrc)
	i := strings.Index(src, "\nrules:\n")
	if i < 0 {
		return nil, fmt.Errorf("acme-demo has no rules list")
	}
	at := i + len("\nrules:\n")
	return []byte(src[:at] + body + src[at:]), nil
}

func parityCases(fs []Fixture, acmeSrc []byte, acme, safe *policy.Compiled) ([]ParityCase, error) {
	var out []ParityCase
	for _, f := range fs {
		out = append(out, ParityCase{ID: "acme-demo/" + f.ID, Policy: "acme-demo", Fixture: f.ID, Agent: playAgent, Expect: eval(acme, f, playAgent)})
	}
	for _, f := range fs {
		out = append(out, ParityCase{ID: "postgres-safe/" + f.ID, Policy: "postgres-safe", Fixture: f.ID, Agent: playAgent, Expect: eval(safe, f, playAgent)})
	}
	for _, e := range edited {
		src, err := WithEditor(acmeSrc, e.editor)
		if err != nil {
			return nil, err
		}
		c, err := compile(src)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", e.id, err)
		}
		agents := e.agents
		if len(agents) == 0 {
			agents = []string{playAgent}
		}
		for _, fid := range e.fixtures {
			for _, a := range agents {
				id := e.id + "/" + fid
				if len(e.agents) > 0 {
					id += "/" + a
				}
				out = append(out, ParityCase{ID: id, Policy: "acme-demo", Editor: e.editor, Fixture: fid, Agent: a, Expect: eval(c, fixtureByID(fs, fid), a)})
			}
		}
	}
	return out, nil
}

// SQL transforms: the only differences between acme.sql and the product files (a test checks that
// every other line is byte-equal). PGlite has one user and no psql.
type sqlChange struct{ from, to, why string }

// AcmeSQL concatenates 01_schema, 02_seed and 10_trigger_on for PGlite.
func AcmeSQL() (string, error) {
	var b strings.Builder
	b.WriteString("-- Generated by cmd/playgen from examples/demo/sql (01_schema, 02_seed, 10_trigger_on). Do not edit.\n")
	b.WriteString("-- Changes from the product files: psql meta-commands and the role clause are removed (PGlite has one user).\n")
	for _, name := range []string{"01_schema.sql", "02_seed.sql", "10_trigger_on.sql"} {
		raw, err := demosql.FS.ReadFile(name)
		if err != nil {
			return "", err
		}
		lines, _ := TransformSQL(string(raw))
		b.WriteString(strings.Join(lines, "\n"))
		if !strings.HasSuffix(b.String(), "\n") {
			b.WriteString("\n")
		}
	}
	return b.String(), nil
}

// TransformSQL applies the PGlite changes and reports each changed line.
func TransformSQL(src string) (lines []string, changes []string) {
	for _, l := range strings.Split(strings.TrimRight(src, "\n"), "\n") {
		switch {
		case strings.HasPrefix(l, `\set `):
			changes = append(changes, "drop:"+l)
			continue
		case strings.HasSuffix(l, ` \gset`):
			changes = append(changes, "gset:"+l)
			l = strings.TrimSuffix(l, ` AS seeded \gset`) + ";"
		case strings.Contains(l, " AUTHORIZATION acme_owner"):
			changes = append(changes, "role:"+l)
			l = strings.Replace(l, " AUTHORIZATION acme_owner", "", 1)
		}
		lines = append(lines, l)
	}
	return lines, changes
}
