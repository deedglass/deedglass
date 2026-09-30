package main

import (
	"context"
	"flag"

	"github.com/deedglass/deedglass/internal/config"
)

// Group orders help output (contract §10).
type Group int

const (
	Setup Group = iota
	Agents
	Policies
	Actions
	Deeds
	Connectors
	MCP
	Insights
	Dev
)

var groupNames = [...]string{"Setup", "Agents", "Policies", "Actions", "Deeds", "Connectors", "MCP", "Insights", "Dev"}

func (g Group) String() string { return groupNames[g] }

// Command is one leaf of the contract §10 tree. An unbuilt command names its owner session; the
// session that builds it sets Run and clears Owner.
type Command struct {
	Path    []string                                               // {"agent", "keys", "create"}
	Group   Group                                                  // help section
	Summary string                                                 // sentence case, ≤ 48 chars, no final period
	Usage   string                                                 // after "deedglass <path>"
	Flags   func(*flag.FlagSet)                                    // command flags; globals are parsed first
	Needs   config.Need                                            // config loads lazily; help and version never load it
	Run     func(ctx context.Context, e *Env, args []string) error // nil while unbuilt
	Owner   string                                                 // "S04" while unbuilt; "" once built
	Hidden  bool                                                   // left out of help
}

// Built reports whether the command runs in this build.
func (c *Command) Built() bool { return c.Run != nil }

// Line is one row of `deedglass help`: a command, or a family of subcommands.
type Line struct {
	Name    string   // "agent keys", "approve · deny"
	Group   Group    //
	Summary string   //
	Subs    []string // subcommand words shown after the status
	Paths   []string // the command paths this row stands for
}

// commands is the whole contract §10 tree (revised 2026-10-01). Keep it in contract order.
var commands = []*Command{
	// Setup.
	{Path: p("init"), Group: Setup, Summary: "Set up Deedglass in this directory", Owner: "S32"},
	{Path: p("doctor"), Group: Setup, Summary: "Check config, database and keys", Owner: "S32"},
	{Path: p("serve"), Group: Setup, Summary: "Run the API, the Gate and the Console",
		Usage: "[--migrate]", Flags: serveFlags, Run: runServe},
	{Path: p("migrate", "up"), Group: Setup, Summary: "Apply database migrations",
		Usage: "[--database-url <url>] [--dry-run] [--json]", Flags: migrateFlags(true), Run: runMigrateUp},
	{Path: p("migrate", "status"), Group: Setup, Summary: "Show applied and pending migrations",
		Usage: "[--database-url <url>] [--json]", Flags: migrateFlags(false), Run: runMigrateStatus},
	{Path: p("migrate", "verify"), Group: Setup, Summary: "Check applied migrations against this build",
		Usage: "[--database-url <url>] [--json]", Flags: migrateFlags(false), Run: runMigrateVerify},
	{Path: p("config", "print"), Group: Setup, Summary: "Show the effective config, secrets hidden",
		Usage: "[--json]", Run: runConfigPrint},
	{Path: p("version"), Group: Setup, Summary: "Show the client and server versions",
		Usage: "[--json] [--check]", Flags: versionFlags, Run: runVersion},
	{Path: p("keys", "gen"), Group: Setup, Summary: "Generate a seal key", Owner: "S04"},
	{Path: p("keys", "rotate"), Group: Setup, Summary: "Rotate the active seal key", Owner: "S04"},
	{Path: p("keys", "list"), Group: Setup, Summary: "List seal keys", Owner: "S04"},
	{Path: p("vault", "status"), Group: Setup, Summary: "Show the credential vault's keys", Owner: "S16"},
	{Path: p("vault", "rotate"), Group: Setup, Summary: "Re-encrypt credentials with a new key", Owner: "S16"},
	{Path: p("help"), Group: Setup, Summary: "Show help for deedglass or one command",
		Usage: "[<command>] [--json]", Hidden: true}, // Run is set in init: help reads this table,
	{Path: p("completion", "zsh"), Group: Setup, Summary: "Print zsh completion", Owner: "S32"},
	{Path: p("completion", "bash"), Group: Setup, Summary: "Print bash completion", Owner: "S32"},
	{Path: p("completion", "fish"), Group: Setup, Summary: "Print fish completion", Owner: "S32"},

	// Agents.
	{Path: p("agent", "create"), Group: Agents, Summary: "Register an agent", Owner: "S08"},
	{Path: p("agent", "list"), Group: Agents, Summary: "List agents", Owner: "S08"},
	{Path: p("agent", "show"), Group: Agents, Summary: "Show one agent", Owner: "S08"},
	{Path: p("agent", "suspend"), Group: Agents, Summary: "Suspend an agent", Owner: "S08"},
	{Path: p("agent", "resume"), Group: Agents, Summary: "Resume a suspended agent", Owner: "S08"},
	{Path: p("agent", "stop"), Group: Agents, Summary: "Stop an agent now", Owner: "S08"},
	{Path: p("agent", "start"), Group: Agents, Summary: "Start a stopped agent", Owner: "S08"},
	{Path: p("agent", "keys", "create"), Group: Agents, Summary: "Create an API key for an agent", Owner: "S08"},
	{Path: p("agent", "keys", "list"), Group: Agents, Summary: "List an agent's API keys", Owner: "S08"},
	{Path: p("agent", "keys", "roll"), Group: Agents, Summary: "Replace an API key, with an overlap", Owner: "S08"},
	{Path: p("agent", "keys", "revoke"), Group: Agents, Summary: "Revoke an API key", Owner: "S08"},
	{Path: p("agent", "intent", "keygen"), Group: Agents, Summary: "Generate a signed-intent key pair", Owner: "S09"},
	{Path: p("agent", "intent", "add"), Group: Agents, Summary: "Register an intent public key", Owner: "S09"},
	{Path: p("agent", "intent", "list"), Group: Agents, Summary: "List an agent's intent keys", Owner: "S09"},
	{Path: p("agent", "intent", "remove"), Group: Agents, Summary: "Remove an intent key", Owner: "S09"},
	{Path: p("agent", "intent", "mode"), Group: Agents, Summary: "Require, allow or ignore signed intents", Owner: "S09"},
	{Path: p("org", "create"), Group: Agents, Summary: "Create an org and its owner", Owner: "S07"},
	{Path: p("org", "login-link"), Group: Agents, Summary: "Print a one-time sign-in link", Owner: "S07"},
	{Path: p("org", "keys", "create"), Group: Agents, Summary: "Create an org admin key", Owner: "S08"},
	{Path: p("org", "keys", "list"), Group: Agents, Summary: "List org admin keys", Owner: "S08"},
	{Path: p("org", "keys", "revoke"), Group: Agents, Summary: "Revoke an org admin key", Owner: "S08"},
	{Path: p("org", "stop-all"), Group: Agents, Summary: "Stop every agent in the org", Owner: "S08"},
	{Path: p("org", "start-all"), Group: Agents, Summary: "Start every stopped agent", Owner: "S08"},

	// Policies.
	{Path: p("policy", "lint"), Group: Policies, Summary: "Check a policy file", Owner: "S10"},
	{Path: p("policy", "fmt"), Group: Policies, Summary: "Format a policy file", Owner: "S10"},
	{Path: p("policy", "test"), Group: Policies, Summary: "Run a policy's test cases", Owner: "S12"},
	{Path: p("policy", "push"), Group: Policies, Summary: "Upload a new policy version", Owner: "S12"},
	{Path: p("policy", "activate"), Group: Policies, Summary: "Activate a policy version", Owner: "S12"},
	{Path: p("policy", "evaluate"), Group: Policies, Summary: "Evaluate one action against a policy", Owner: "S11"},
	{Path: p("policy", "replay"), Group: Policies, Summary: "Replay past actions against a version", Owner: "S27"},
	{Path: p("policy", "diff"), Group: Policies, Summary: "Compare two policy versions", Owner: "S12"},

	// Actions.
	{Path: p("act"), Group: Actions, Summary: "Send a test action through the Gate", Owner: "S13"},
	{Path: p("actions", "list"), Group: Actions, Summary: "List actions", Owner: "S06"},
	{Path: p("actions", "show"), Group: Actions, Summary: "Show one action and its events", Owner: "S06"},
	{Path: p("actions", "cancel"), Group: Actions, Summary: "Cancel a pending action", Owner: "S13"},
	{Path: p("approve"), Group: Actions, Summary: "Approve a pending action", Owner: "S22"},
	{Path: p("deny"), Group: Actions, Summary: "Deny a pending action", Owner: "S22"},
	{Path: p("approvals", "list"), Group: Actions, Summary: "List approvals", Owner: "S22"},
	{Path: p("approvals", "show"), Group: Actions, Summary: "Show one approval", Owner: "S22"},
	{Path: p("undo", "preview"), Group: Actions, Summary: "Show what an undo would change", Owner: "S23"},
	{Path: p("undo", "run"), Group: Actions, Summary: "Undo an action", Owner: "S23"},
	{Path: p("webhooks", "create"), Group: Actions, Summary: "Add a webhook endpoint", Owner: "S22"},
	{Path: p("webhooks", "list"), Group: Actions, Summary: "List webhook endpoints", Owner: "S22"},
	{Path: p("webhooks", "test"), Group: Actions, Summary: "Send a test event", Owner: "S22"},
	{Path: p("webhooks", "delete"), Group: Actions, Summary: "Remove a webhook endpoint", Owner: "S22"},

	// Deeds.
	{Path: p("verify"), Group: Deeds, Summary: "Check deeds offline", Owner: "S05"},
	{Path: p("deeds", "list"), Group: Deeds, Summary: "List deeds", Owner: "S06"},
	{Path: p("deeds", "show"), Group: Deeds, Summary: "Show one deed", Owner: "S06"},
	{Path: p("deeds", "export"), Group: Deeds, Summary: "Export deeds as a bundle", Owner: "S06"},
	{Path: p("ledger", "check"), Group: Deeds, Summary: "Check the deed chain", Owner: "S06"},
	{Path: p("ledger", "prune"), Group: Deeds, Summary: "Remove events past retention", Owner: "S06"},

	// Connectors.
	{Path: p("connector", "create"), Group: Connectors, Summary: "Add a connector", Owner: "S16"},
	{Path: p("connector", "list"), Group: Connectors, Summary: "List connectors", Owner: "S16"},
	{Path: p("connector", "show"), Group: Connectors, Summary: "Show one connector", Owner: "S16"},
	{Path: p("connector", "test"), Group: Connectors, Summary: "Test a connector's credentials", Owner: "S16"},
	{Path: p("connector", "types"), Group: Connectors, Summary: "List connector types", Owner: "S16"},
	{Path: p("connector", "credentials", "set"), Group: Connectors, Summary: "Store a connector's credentials", Owner: "S16"},
	{Path: p("connector", "credentials", "remove"), Group: Connectors, Summary: "Delete a connector's credentials", Owner: "S16"},
	{Path: p("connector", "postgres", "init"), Group: Connectors, Summary: "Prepare a Postgres database", Owner: "S17"},
	{Path: p("connector", "postgres", "check"), Group: Connectors, Summary: "Check a prepared Postgres database", Owner: "S17"},
	{Path: p("connector", "gmail", "authorize"), Group: Connectors, Summary: "Authorize the Gmail connector", Owner: "S30"},
	{Path: p("decoder", "status"), Group: Connectors, Summary: "Show Postgres change readers", Owner: "S20"},
	{Path: p("decoder", "drop"), Group: Connectors, Summary: "Remove a replication slot", Owner: "S20"},
	{Path: p("slack", "manifest"), Group: Connectors, Summary: "Print the Slack app manifest", Owner: "S29"},

	// MCP.
	{Path: p("mcp", "wrap"), Group: MCP, Summary: "Run an MCP server behind the Gate", Owner: "S14"},
	{Path: p("mcp", "serve"), Group: MCP, Summary: "Serve the Gate's native MCP tools", Owner: "S15"},
	{Path: p("mcp", "snippet"), Group: MCP, Summary: "Print client config for an MCP client", Owner: "S15"},

	// Insights.
	{Path: p("insights", "stats"), Group: Insights, Summary: "Show usage per agent and tool", Owner: "S27"},
	{Path: p("insights", "shadow"), Group: Insights, Summary: "Show shadow-policy results", Owner: "S27"},
	{Path: p("insights", "anomalies"), Group: Insights, Summary: "List anomalies", Owner: "S27"},
	{Path: p("insights", "detect"), Group: Insights, Summary: "Run anomaly detection now", Owner: "S27"},
	{Path: p("insights", "settings"), Group: Insights, Summary: "Show or change Insights settings", Owner: "S27"},
	{Path: p("insights", "suggestions", "list"), Group: Insights, Summary: "List suggestions", Owner: "S28"},
	{Path: p("insights", "suggestions", "show"), Group: Insights, Summary: "Show one suggestion", Owner: "S28"},
	{Path: p("insights", "suggestions", "accept"), Group: Insights, Summary: "Accept a suggestion", Owner: "S28"},
	{Path: p("insights", "suggestions", "reject"), Group: Insights, Summary: "Reject a suggestion", Owner: "S28"},
	{Path: p("insights", "suggestions", "generate"), Group: Insights, Summary: "Generate suggestions now", Owner: "S28"},
	{Path: p("insights", "digest"), Group: Insights, Summary: "Preview the weekly digest", Owner: "S28"},

	// Dev.
	{Path: p("dev", "seed"), Group: Dev, Summary: "Load demo data into a dev database", Usage: "[--json]", Run: runDevSeed},
	{Path: p("dev", "fake-action"), Group: Dev, Summary: "Send realistic actions for demos", Owner: "S06"},
	{Path: p("dev", "simulate"), Group: Dev, Summary: "Simulate agent traffic", Owner: "S27"},
	{Path: p("dev", "agent-script"), Group: Dev, Summary: "Run a scripted agent", Owner: "S37"},
	{Path: p("dev", "mail-preview"), Group: Dev, Summary: "Preview emails in the browser", Owner: "S07"},
	{Path: p("dev", "sign-intent"), Group: Dev, Summary: "Sign an intent with a local key", Owner: "S09"},
	{Path: p("dev", "stripe-fixtures"), Group: Dev, Summary: "Create Stripe test-mode fixtures", Owner: "S30"},
	{Path: p("dev", "id"), Group: Dev, Summary: "Print a fresh id for a prefix", Usage: "<prefix>",
		Run: runDevID, Hidden: true},
}

// lines is `deedglass help`: one row per command or family, in contract order.
var lines = []Line{
	{Name: "init", Group: Setup, Summary: "Set up Deedglass in this directory", Paths: ps("init")},
	{Name: "doctor", Group: Setup, Summary: "Check config, database and keys", Paths: ps("doctor")},
	{Name: "serve", Group: Setup, Summary: "Run the API, the Gate and the Console", Paths: ps("serve")},
	{Name: "migrate", Group: Setup, Summary: "Apply or check database migrations", Subs: subs("up status verify"), Paths: ps("migrate up", "migrate status", "migrate verify")},
	{Name: "config print", Group: Setup, Summary: "Show the effective config, secrets hidden", Paths: ps("config print")},
	{Name: "version", Group: Setup, Summary: "Show the client and server versions", Paths: ps("version")},
	{Name: "keys", Group: Setup, Summary: "Manage seal keys", Subs: subs("gen rotate list"), Paths: ps("keys gen", "keys rotate", "keys list")},
	{Name: "vault", Group: Setup, Summary: "Check or rotate the credential vault", Subs: subs("status rotate"), Paths: ps("vault status", "vault rotate")},
	{Name: "completion", Group: Setup, Summary: "Print shell completion", Subs: subs("zsh bash fish"), Paths: ps("completion zsh", "completion bash", "completion fish")},

	{Name: "agent", Group: Agents, Summary: "Register and control agents", Subs: subs("create list show suspend resume stop start"),
		Paths: ps("agent create", "agent list", "agent show", "agent suspend", "agent resume", "agent stop", "agent start")},
	{Name: "agent keys", Group: Agents, Summary: "Manage an agent's API keys", Subs: subs("create list roll revoke"),
		Paths: ps("agent keys create", "agent keys list", "agent keys roll", "agent keys revoke")},
	{Name: "agent intent", Group: Agents, Summary: "Manage an agent's signed-intent keys", Subs: subs("keygen add list remove mode"),
		Paths: ps("agent intent keygen", "agent intent add", "agent intent list", "agent intent remove", "agent intent mode")},
	{Name: "org", Group: Agents, Summary: "Create orgs and keys; stop or start all", Subs: subs("create login-link keys stop-all start-all"),
		Paths: ps("org create", "org login-link", "org keys create", "org keys list", "org keys revoke", "org stop-all", "org start-all")},

	{Name: "policy", Group: Policies, Summary: "Write, test and activate policies", Subs: subs("lint fmt test push activate evaluate replay diff"),
		Paths: ps("policy lint", "policy fmt", "policy test", "policy push", "policy activate", "policy evaluate", "policy replay", "policy diff")},

	{Name: "act", Group: Actions, Summary: "Send a test action through the Gate", Paths: ps("act")},
	{Name: "actions", Group: Actions, Summary: "Inspect actions", Subs: subs("list show cancel"), Paths: ps("actions list", "actions show", "actions cancel")},
	{Name: "approve · deny", Group: Actions, Summary: "Decide a pending approval", Paths: ps("approve", "deny")},
	{Name: "approvals", Group: Actions, Summary: "List pending approvals", Subs: subs("list show"), Paths: ps("approvals list", "approvals show")},
	{Name: "undo", Group: Actions, Summary: "Preview or run an undo", Subs: subs("preview run"), Paths: ps("undo preview", "undo run")},
	{Name: "webhooks", Group: Actions, Summary: "Send events to your endpoints", Subs: subs("create list test delete"),
		Paths: ps("webhooks create", "webhooks list", "webhooks test", "webhooks delete")},

	{Name: "verify", Group: Deeds, Summary: "Check deeds offline", Paths: ps("verify")},
	{Name: "deeds", Group: Deeds, Summary: "Browse and export deeds", Subs: subs("list show export"), Paths: ps("deeds list", "deeds show", "deeds export")},
	{Name: "ledger", Group: Deeds, Summary: "Check or prune the Ledger", Subs: subs("check prune"), Paths: ps("ledger check", "ledger prune")},

	{Name: "connector", Group: Connectors, Summary: "Connect tools", Subs: subs("create list show test types credentials"),
		Paths: ps("connector create", "connector list", "connector show", "connector test", "connector types", "connector credentials set", "connector credentials remove")},
	{Name: "connector postgres", Group: Connectors, Summary: "Prepare a Postgres database", Subs: subs("init check"),
		Paths: ps("connector postgres init", "connector postgres check")},
	{Name: "connector gmail", Group: Connectors, Summary: "Authorize the Gmail connector", Subs: subs("authorize"), Paths: ps("connector gmail authorize")},
	{Name: "decoder", Group: Connectors, Summary: "Inspect Postgres change readers", Subs: subs("status drop"), Paths: ps("decoder status", "decoder drop")},
	{Name: "slack manifest", Group: Connectors, Summary: "Print the Slack app manifest", Paths: ps("slack manifest")},

	{Name: "mcp", Group: MCP, Summary: "Put the Gate in front of MCP servers", Subs: subs("wrap serve snippet"), Paths: ps("mcp wrap", "mcp serve", "mcp snippet")},

	{Name: "insights", Group: Insights, Summary: "Usage, shadow results and anomalies", Subs: subs("stats shadow anomalies detect settings suggestions digest"),
		Paths: ps("insights stats", "insights shadow", "insights anomalies", "insights detect", "insights settings",
			"insights suggestions list", "insights suggestions show", "insights suggestions accept", "insights suggestions reject",
			"insights suggestions generate", "insights digest")},

	{Name: "dev", Group: Dev, Summary: "Local helpers", Subs: subs("seed fake-action simulate agent-script mail-preview sign-intent stripe-fixtures"),
		Paths: ps("dev seed", "dev fake-action", "dev simulate", "dev agent-script", "dev mail-preview", "dev sign-intent", "dev stripe-fixtures")},
}

// retired maps V0 commands to their successors (docs/NAMING.md).
var retired = map[string]string{
	"sign":             "dev sign-intent (S09)",
	"submit":           "act (S13)",
	"executor serve":   "serve (S03)",
	"verifier run":     "serve: the Witness runs inside it (S19–S21)",
	"statement verify": "verify (S05)",
	"evidence export":  "deeds export (S06)",
	"evidence verify":  "verify (S05)",
	"evidence report":  "deeds export (S06)",
	"install":          "connector postgres init (S17)",
	"audit":            "connector postgres check (S17)",
	"boundary":         "connector postgres check (S17)",
	"status":           "actions show (S06)",
	"chain":            "ledger check (S06)",
	"keygen":           "keys gen (S04)",
}

func p(parts ...string) []string { return parts }

func ps(paths ...string) []string { return paths }

func subs(s string) []string {
	var out []string
	for _, w := range splitWords(s) {
		out = append(out, w)
	}
	return out
}

func init() {
	c, _ := lookup([]string{"help"})
	c.Run = runHelp
}
