package main

import (
	"context"
	"flag"

	"github.com/deedglass/deedglass/internal/config"
	"github.com/deedglass/deedglass/internal/identity"
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

var groupNames = [...]string{"Setup", "Agents", "Policies", "Actions", "Records", "Connectors", "MCP", "Insights", "Dev"}

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
	RawArgs bool                                                   // Run parses its own flags (they may follow paths)
	Detail  string                                                 // help text after Usage: what it does, flags, examples (S14)
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
	{Path: p("init"), Group: Setup, Summary: "Set up Deedglass in this directory",
		Usage: "[--yes] [--dir .] [--client claude-code|claude-desktop|cursor|vscode|windsurf|sdk-python|sdk-typescript|none] [--listen 127.0.0.1:8080] [--pg-port 55432] [--no-open] [--no-smoke] [--stop] [--json]",
		Flags: initFlags, Run: runInit,
		Detail: "Postgres 18.6 in Docker, deedglass serve, keys in .deedglass/ (0700, files 0600), an org Local, an agent quickstart-agent with a dg_test_ key (shown once, saved to .deedglass/quickstart.key), the Acme demo database with a Postgres connector ready for Proven checks, the postgres-safe policy enforced, then three smoke actions: a read, a blocked DROP TABLE and an UPDATE checked at Proven (L2). Run it again: Nothing to change.\n\nExamples:\n  deedglass init\n  deedglass init --yes --client none\n  deedglass init --listen 127.0.0.1:8090 --pg-port 55440\n  deedglass init --stop"},
	{Path: p("doctor"), Group: Setup, Summary: "Check config, database and keys",
		Usage: "[--only <ids>] [--json]", Flags: doctorFlags, Run: runDoctor,
		Detail: "Sixteen read-only checks, each with what it saw and, when it isn't OK, the exact fix: config.valid, state.dir, db.reachable, db.role, db.migrations, seal.key, vault.key, server.health, clock.skew, ledger.chain, connectors.postgres, decoder.slots, wal.level, disk.space, version.drift, policy.active. In a directory where you ran deedglass init, it reads .deedglass/ to find the local stack.\n\nExit codes: 0 all OK (warnings allowed) · 1 a problem · 2 doctor itself couldn't run · 3 the server refused the API key."},
	{Path: p("scan"), Group: Setup, Summary: "Show what your agent setup can change",
		Usage: "[--dir .] [--postgres-dsn-env VAR] [--format md,html,json,sarif] [--score] [--badge] [--share] [--ci] [--fail-under A|B|C|D] [--full [--no-seal]] [--json]",
		Flags: scanFlags, Run: runScan,
		Detail: "A local, read-only report: MCP client configs, agent and database dependencies, database URL variable names (never values) and tools that can write, then, with --postgres-dsn-env, the tables your agent's role can change, ranked. It opens with the Agent Blast-Radius Score (A to F). Nothing is sent anywhere.\n\n--full also writes deedglass-full-report.html, the full scan report, on this machine (Pro or the $29 full scan report; only --full reads the licence). With a Deedglass server configured it is sealed there as a record; --no-seal skips that.\n\nExit codes: 0 the report was written · 1 the grade is below --fail-under, the Postgres part failed (the file part was written), or --full has no active licence · 2 usage, or the DSN variable is unset.\n\nExamples:\n  deedglass scan\n  deedglass scan --dir ~/code/my-agent --postgres-dsn-env DATABASE_URL\n  deedglass scan --ci --format sarif --fail-under C"},
	{Path: p("serve"), Group: Setup, Summary: "Run the API, the rules and the Console",
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
		Usage: "[--json] [--check [--pre]]", Flags: versionFlags, Run: runVersion,
		Detail: "With --check, one request to GitHub's releases API (3 s, only a User-Agent) says whether a newer release exists. It never runs on its own.\n\nExit codes: 0 the check ran (also when there are no public releases yet) · 2 it couldn't tell (offline, or an unreadable answer)."},
	{Path: p("keys", "gen"), Group: Setup, Summary: "Generate a seal key",
		Usage: "[--dir <dir>] [--purpose seal|licence] [--json]", Flags: keysGenFlags, Run: runKeysGen,
		Detail: "--purpose seal (the default) writes a deed seal key. --purpose licence writes an lk_ key that signs licence tokens and never deeds: the private JWK (0600) for the site's signing secret and the public licence-keys.json to commit."},
	{Path: p("keys", "rotate"), Group: Setup, Summary: "Rotate the active seal key",
		Usage: "[--overlap 5m] [--dir <dir>] [--json]", Flags: keysRotateFlags, Needs: config.NeedDatabase, Run: runKeysRotate},
	{Path: p("keys", "list"), Group: Setup, Summary: "List seal keys",
		Usage: "[--server <url>] [--json]", Run: runKeysList},
	{Path: p("licence", "activate"), Group: Setup, Summary: "Turn on Pro with the key from Dodo",
		Usage: "<key> [--json]", Run: runLicenceActivate,
		Detail: "Registers this machine with Dodo Payments (under a hash of its name, never the name), then asks deedglass.com for a signed licence. Writes .deedglass/licence.json and .deedglass/licence.key (both 0600; DEEDGLASS_LICENCE_FILE moves them). The licence checks in at most once a day, only when a Pro feature runs, and keeps working 14 days offline. Free commands never read it.\n\nExample:\n  deedglass licence activate DG-7Q2M-…"},
	{Path: p("licence", "show"), Group: Setup, Summary: "Show this machine's licence, offline",
		Usage: "[--json]", Run: runLicenceShow},
	{Path: p("licence", "refresh"), Group: Setup, Summary: "Check the licence with Dodo now",
		Usage: "[--json]", Run: runLicenceRefresh},
	{Path: p("licence", "deactivate"), Group: Setup, Summary: "Remove the licence from this machine",
		Usage: "[--json]", Run: runLicenceDeactivate},
	{Path: p("vault", "status"), Group: Setup, Summary: "Show the credential vault's keys",
		Usage: "[--json]", Needs: config.NeedDatabase | config.NeedVault, Run: runVaultStatus},
	{Path: p("vault", "rotate"), Group: Setup, Summary: "Re-encrypt credentials with a new key",
		Usage: "[--json]", Needs: config.NeedDatabase | config.NeedVault | config.NeedSealKey, Run: runVaultRotate},
	{Path: p("help"), Group: Setup, Summary: "Show help for deedglass or one command",
		Usage: "[<command>] [--json]", Hidden: true}, // Run is set in init: help reads this table,
	{Path: p("completion", "zsh"), Group: Setup, Summary: "Print zsh completion",
		Detail: "Prints a zsh completion script for every command, flag and closed flag value in this build. It never runs deedglass while you type.\n\nExample:\n  deedglass completion zsh > ~/.zfunc/_deedglass  (with fpath+=(~/.zfunc) before compinit in ~/.zshrc)"},
	{Path: p("completion", "bash"), Group: Setup, Summary: "Print bash completion",
		Detail: "Prints a bash completion script for every command, flag and closed flag value in this build. It never runs deedglass while you type.\n\nExample:\n  deedglass completion bash > ~/.local/share/bash-completion/completions/deedglass"},
	{Path: p("completion", "fish"), Group: Setup, Summary: "Print fish completion",
		Detail: "Prints a fish completion script for every command, flag and closed flag value in this build. It never runs deedglass while you type.\n\nExample:\n  deedglass completion fish > ~/.config/fish/completions/deedglass.fish"},

	// Agents.
	{Path: p("agent", "create"), Group: Agents, Summary: "Register an agent",
		Usage: "<name> [--description …] [--label env=prod]... [--calls-per-minute n] [--calls-per-day n] [--spend-per-day minor --currency usd] [--owner usr_…] [--json]",
		Flags: agentCreateFlags, Run: runAgentCreate, RawArgs: true},
	{Path: p("agent", "list"), Group: Agents, Summary: "List agents",
		Usage: "[--status active|suspended|stopped] [--label env:prod] [--json]", Flags: agentListFlags, Run: runAgentList},
	{Path: p("agent", "show"), Group: Agents, Summary: "Show one agent", Usage: "<name|agt_…> [--json]", Run: runAgentShow},
	{Path: p("agent", "suspend"), Group: Agents, Summary: "Suspend an agent",
		Usage: "<name|agt_…> [--reason …] [--json]", Flags: reasonFlags, Run: statusCmd(identity.OpSuspend), RawArgs: true},
	{Path: p("agent", "resume"), Group: Agents, Summary: "Resume a suspended agent",
		Usage: "<name|agt_…> [--reason …] [--json]", Flags: reasonFlags, Run: statusCmd(identity.OpResume), RawArgs: true},
	{Path: p("agent", "stop"), Group: Agents, Summary: "Stop an agent now",
		Usage: "<name|agt_…> [--reason …] [--json]", Flags: reasonFlags, Run: statusCmd(identity.OpStop), RawArgs: true},
	{Path: p("agent", "start"), Group: Agents, Summary: "Start a stopped agent",
		Usage: `<name|agt_…> --reason "why it's safe now" [--json]`, Flags: reasonFlags, Run: statusCmd(identity.OpStart), RawArgs: true},
	{Path: p("agent", "keys", "create"), Group: Agents, Summary: "Create an API key for an agent",
		Usage: `<name|agt_…> [--name "prod runner"] [--mode live|test] [--scope …]... [--json]`, Flags: keyCreateFlags, Run: runAgentKeysCreate, RawArgs: true},
	{Path: p("agent", "keys", "list"), Group: Agents, Summary: "List an agent's API keys", Usage: "<name|agt_…> [--json]", Run: runAgentKeysList},
	{Path: p("agent", "keys", "roll"), Group: Agents, Summary: "Replace an API key, with an overlap",
		Usage: "<key_…> [--overlap 24h] [--json]", Flags: rollFlags, Run: runAgentKeysRoll, RawArgs: true},
	{Path: p("agent", "keys", "revoke"), Group: Agents, Summary: "Revoke an API key",
		Usage: "<key_…> [--reason …] [--json]", Flags: reasonFlags, Run: runAgentKeysRevoke, RawArgs: true},
	{Path: p("agent", "intent", "keygen"), Group: Agents, Summary: "Generate a signed-intent key pair",
		Usage: "<name> [--dir .deedglass/intent] [--force] [--json]", Flags: intentKeygenFlags, Run: runIntentKeygen, RawArgs: true},
	{Path: p("agent", "intent", "add"), Group: Agents, Summary: "Register an intent public key",
		Usage: "<name|agt_…> --public-key ed25519:… [--json]", Flags: intentAddFlags, Run: runIntentAdd, RawArgs: true},
	{Path: p("agent", "intent", "list"), Group: Agents, Summary: "List an agent's intent keys", Usage: "<name|agt_…> [--json]", Run: runIntentList},
	{Path: p("agent", "intent", "remove"), Group: Agents, Summary: "Remove an intent key", Usage: "<name|agt_…> <kid> [--json]", Run: runIntentRemove},
	{Path: p("agent", "intent", "mode"), Group: Agents, Summary: "Require, allow or ignore signed intents",
		Usage: "<name|agt_…> off|optional|required [--json]", Run: runIntentMode},
	{Path: p("org", "create"), Group: Agents, Summary: "Create an org and its owner",
		Usage: `--name "Acme" --owner-email priya@acme.com [--slug acme] [--print-link] [--admin-key] [--json]`, Flags: orgCreateFlags, Run: runOrgCreate},
	{Path: p("org", "login-link"), Group: Agents, Summary: "Print a one-time sign-in link",
		Usage: "--email sam@acme.com [--org acme] [--json]", Flags: loginLinkFlags, Run: runOrgLoginLink},
	{Path: p("org", "keys", "create"), Group: Agents, Summary: "Create an org admin key",
		Usage: "[--org acme] [--name ci] [--mode live|test] [--json]", Flags: orgKeyFlags, Run: runOrgKeysCreate},
	{Path: p("org", "keys", "list"), Group: Agents, Summary: "List org admin keys", Usage: "[--org acme] [--json]", Run: runOrgKeysList},
	{Path: p("org", "keys", "revoke"), Group: Agents, Summary: "Revoke an org admin key",
		Usage: "<key_…> [--org acme] [--reason …] [--json]", Flags: reasonFlags, Run: runOrgKeysRevoke, RawArgs: true},
	{Path: p("org", "stop-all"), Group: Agents, Summary: "Stop every agent in the org",
		Usage: "[--reason …] [--yes] [--json]", Flags: stopAllFlags, Run: runOrgStopAll},
	{Path: p("org", "start-all"), Group: Agents, Summary: "Start every agent not stopped one by one",
		Usage: `[--org acme] --reason "why it's safe now" [--json]`, Flags: reasonFlags, Run: runOrgStartAll, RawArgs: true},

	// Policies.
	{Path: p("policy", "lint"), Group: Policies, Summary: "Check a policy file",
		Usage: "<file|dir|->... [--format text|json|github] [--voice] [--json]", Run: runPolicyLint, RawArgs: true},
	{Path: p("policy", "fmt"), Group: Policies, Summary: "Format a policy file",
		Usage: "[-w] [--check] <file|dir|->... [--json]", Run: runPolicyFmt, RawArgs: true},
	{Path: p("policy", "test"), Group: Policies, Summary: "Run a policy's test cases",
		Usage: "<policy.yaml|cases.yaml>... [--cases <file>] | --dir <dir> [--require-coverage] [--voice] [--run <regexp>] [--json]",
		Run:   runPolicyTest, RawArgs: true},
	{Path: p("policy", "push"), Group: Policies, Summary: "Upload a new policy version",
		Usage: `<policy.yaml> [--notes "…"] [--json]`, Run: runPolicyPush, RawArgs: true},
	{Path: p("policy", "activate"), Group: Policies, Summary: "Activate a policy version",
		Usage: `<name|pol_…> --version <n> --mode enforce|shadow --reason "…" [--yes] [--ack default_allow] | --clear --reason "…" [--json]`,
		Run:   runPolicyActivate, RawArgs: true},
	{Path: p("policy", "evaluate"), Group: Policies, Summary: "Evaluate one action against a policy",
		Usage: "--policy <file> | --server [--policy <name> --version <n>] --tool <name> [--sql …] [--args <json>] [--input <file>] [--agent …] [--label k=v] [--user …] [--time …] [--tz …] [--usage k=v] [--explain] [--json]",
		Run:   runPolicyEvaluate, RawArgs: true},
	{Path: p("policy", "replay"), Group: Policies, Summary: "Replay past actions against a version",
		Usage: `<name|pol_…> --version <n> [--from 7d] [--to now] [--baseline recorded|vN] [--wait] [--quiet] [--json]`, Run: runPolicyReplay, RawArgs: true},
	{Path: p("policy", "diff"), Group: Policies, Summary: "Compare two policy versions",
		Usage: "<old.yaml> <new.yaml> | <policy> <vA> <vB> [--context 3] [--json]", Run: runPolicyDiff, RawArgs: true},
	{Path: p("policy", "install"), Group: Policies, Summary: "Install a policy pack as files",
		Usage: "<pack>[@version] [--dir policies] [--from <dir|tar.gz>] [--latest] [--yes] [--json]", Run: runPolicyInstall, RawArgs: true,
		Detail: "Checks the pack against its signed index, compiles it, runs its cases, then shows the files and the diff and asks\n[y/N] before writing <dir>/<pack>.yaml and <pack>.cases.yaml. It never pushes or activates. By default it reads the copy\nbuilt into deedglass (offline); --from reads a local copy; --latest downloads the newest release over HTTPS.\nPro packs need a Pro licence; an installed pack keeps enforcing if the licence lapses."},
	{Path: p("policy", "packs", "list"), Group: Policies, Summary: "List the policy packs",
		Usage: "[--from <dir|tar.gz>] [--latest] [--json]", Run: runPolicyPacksList, RawArgs: true},
	{Path: p("policy", "packs", "verify"), Group: Policies, Summary: "Check the packs against their signed index",
		Usage: "[--from <dir|tar.gz>] [--latest] [--json]", Run: runPolicyPacksVerify, RawArgs: true},
	{Path: p("policy", "packs", "sign"), Group: Policies, Summary: "Sign a policy-packs index",
		Usage: "--key <pack key file> [--dir .]", Run: runPolicyPacksSign, RawArgs: true, Hidden: true},

	// Actions.
	{Path: p("act"), Group: Actions, Summary: "Send a test action through Deedglass",
		Usage: `<tool> [--args '{"k":"v"}'] [--mode gateway|proxy] [--wait 60s] [--idempotency-key K] [--json]`, Run: runAct, RawArgs: true},
	{Path: p("actions", "list"), Group: Actions, Summary: "List actions",
		Usage: "[--agent agt_…] [--tool stripe.*] [--state …] [--decision …] [--verdict MISMATCH] [--level L2] [--since 1h] [--q text] [--limit 25] [--cursor …] [--json] [--follow] [--connector orders]",
		Flags: actionsListFlags, Needs: config.NeedDatabase, Run: runActionsList},
	{Path: p("actions", "show"), Group: Actions, Summary: "Show one action, its events and its Proven diff",
		Usage: "<act_…> [--wait] [--diff] [--json]", Needs: config.NeedDatabase, Run: runActionsShow, RawArgs: true},
	{Path: p("actions", "cancel"), Group: Actions, Summary: "Cancel a pending action",
		Usage: "<act_…> [--json]", Run: runActionsCancel},
	{Path: p("approve"), Group: Actions, Summary: "Approve a pending action",
		Usage: `<apr_…> --as <email> [--reason …] [--json]`, Flags: decideFlags, Run: runApprove, RawArgs: true,
		Detail: "Dev only: countersign as the member named by --as, directly on the database (the server's worker then runs the action).\nIn prod, approvals need a person: this prints the Console link and exits 2."},
	{Path: p("deny"), Group: Actions, Summary: "Deny a pending action",
		Usage: `<apr_…> --as <email> --reason "…" [--json]`, Flags: decideFlags, Run: runDeny, RawArgs: true,
		Detail: "Dev only, like approve. A deny is final (a veto) and needs a reason; the agent sees it."},
	{Path: p("approvals", "list"), Group: Actions, Summary: "List approvals",
		Usage: "[--status pending] [--agent <name>] [--limit 25] [--json]", Flags: approvalsListFlags, Run: runApprovalsList},
	{Path: p("approvals", "show"), Group: Actions, Summary: "Show one approval", Usage: "<apr_…> [--json]", Run: runApprovalsShow},
	{Path: p("undo", "preview"), Group: Actions, Summary: "Show what an undo would change",
		Usage: "<act_…> [--allow-triggers] [--steps N] [--json]", Run: runUndoPreview, RawArgs: true,
		Detail: "Plans the undo from what Postgres committed and runs the twelve checks without a lock. Writes nothing.\nExit 0 when the undo is available, 1 when a check fails (each with its fix)."},
	{Path: p("undo", "run"), Group: Actions, Summary: "Undo an action",
		Usage: `<act_…> --plan <sha256:…> --reason "…" [--allow-triggers] [--wait 25s] [--json]`, Run: runUndoRun, RawArgs: true,
		Detail: "Sends the undo through Deedglass as the console system agent, on your behalf: policy, approval, one transaction, a Proven check and its own record.\nExit 0 executed and Matches, 1 refused, failed or Doesn't match, 2 waiting for a countersign."},
	{Path: p("observe", "run"), Group: Actions, Summary: "Watch a database while a command runs",
		Usage: `--connector <name> [--label "…"] [--agent <name>] -- <command …>`, Run: runObserveRun, RawArgs: true,
		Detail: "Opens a watched session, runs the command with this terminal, closes the session when it exits and prints what Postgres committed on the watched tables.\nThe command never sees DEEDGLASS_* variables. Its exit code is passed through (130 after Ctrl-C).\nObserved: nothing is compared with what the agent said, and another writer's changes in the window appear too."},
	{Path: p("observe", "start"), Group: Actions, Summary: "Open a watched session",
		Usage: `--connector <name> [--label "…"] [--agent <name>] [--json]`, Run: runObserveStart, RawArgs: true,
		Detail: "Needs witness.observe.enabled on the connector. --agent shows an agent beside the session; it attributes nothing."},
	{Path: p("observe", "stop"), Group: Actions, Summary: "Close a watched session",
		Usage: "<obs_…> [--wait=false] [--json]", Run: runObserveStop, RawArgs: true,
		Detail: "Closes the session, waits up to 45 s for the seal, then prints its tables and anything Deedglass caught."},
	{Path: p("observe", "list"), Group: Actions, Summary: "List watched sessions",
		Usage: "[--connector <name>] [--state sealed] [--caught] [--limit 20] [--json]", Run: runObserveList, RawArgs: true},
	{Path: p("observe", "show"), Group: Actions, Summary: "Show a session or print its undo SQL",
		Usage: "<obs_…> [--undo-sql] [--json]", Run: runObserveShow, RawArgs: true,
		Detail: "--undo-sql prints the undo as SQL to review and run yourself, free: one transaction, newest change first, each statement guarded by the values the session left.\nDeedglass never runs it. A TRUNCATE, rows past the image cap, sensitive columns or expired frames print why instead (exit 1)."},
	{Path: p("observe", "undo"), Group: Actions, Summary: "Undo a whole session in one step (Pro)",
		Usage: `<obs_…> --reason "…" [--allow-triggers] [--wait 25s] [--json]`, Run: runObserveUndo, RawArgs: true,
		Detail: "Pro. One countersign undoes every included transaction in one Postgres transaction, with conflict checks and its own record.\nWithout Pro: deedglass observe show <obs_…> --undo-sql (free). Exit 0 executed and Matches, 1 refused or failed, 2 waiting for a countersign."},
	{Path: p("observe", "exclude"), Group: Actions, Summary: "Leave a transaction out of an undo",
		Usage: "<obs_…> <dtx_…> [--include] [--json]", Run: runObserveExclude, RawArgs: true,
		Detail: "Only while the session is sealed and before its undo starts. Each change is sealed in the record."},
	{Path: p("webhooks", "create"), Group: Actions, Summary: "Add a webhook endpoint",
		Usage: "--url <url> --events approval.requested,approval.decided [--description …] [--json]", Flags: webhookCreateFlags, Run: runWebhooksCreate},
	{Path: p("webhooks", "list"), Group: Actions, Summary: "List webhook endpoints", Usage: "[--json]", Run: runWebhooksList},
	{Path: p("webhooks", "test"), Group: Actions, Summary: "Send a test event", Usage: "<whk_…> [--json]", Run: runWebhooksTest},
	{Path: p("webhooks", "delete"), Group: Actions, Summary: "Remove a webhook endpoint", Usage: "<whk_…> [--json]", Run: runWebhooksDelete},

	// Deeds.
	{Path: p("verify"), Group: Deeds, Summary: "Check records offline",
		Usage: "<path|->... [--keys <file|url>] [--pin sha256:…] [--anchor <seq>:sha256:…] [--file <path>] [--strict] [--all] [-q]",
		Flags: verifyFlags, Run: runVerify, RawArgs: true},
	{Path: p("deeds", "list"), Group: Deeds, Summary: "List records",
		Usage: "[--from-seq n] [--to-seq n] [--kind action,admin,amendment] [--action act_…] [--order asc|desc] [--limit 25] [--json]",
		Flags: deedsListFlags, Needs: config.NeedDatabase, Run: runDeedsList},
	{Path: p("deeds", "show"), Group: Deeds, Summary: "Show one record",
		Usage: "<deed_…|seq> [--json]", Needs: config.NeedDatabase, Run: runDeedsShow},
	{Path: p("deeds", "share"), Group: Deeds, Summary: "Make a link that checks one record",
		Usage: "<deed_…|seq> [--yes] [--json]", Flags: deedsShareFlags, Needs: config.NeedDatabase, Run: runDeedsShare,
		Detail: "Prints https://deedglass.com/verify#d=…&k=…: the record and the key that sealed it, in the part after #, which never\nreaches a server. The link holds ids, the agent name, the tool, the decision, the verdict and digests; never arguments or results.\nAsks first ([y/N]) unless --yes."},
	{Path: p("deeds", "export"), Group: Deeds, Summary: "Export records as a bundle",
		Usage: "[--out <file>|-] [--format tar.gz|jsonl] [--from-seq n] [--to-seq n] [--since 1h] [--json]",
		Flags: deedsExportFlags, Needs: config.NeedDatabase, Run: runDeedsExport},
	{Path: p("deeds", "seal-file"), Group: Deeds, Summary: "Seal a file's hash as a record",
		Usage: "<path> --label <text> [--media-type application/pdf|text/markdown|application/json|text/plain|text/html] [--out <path>.deed.json] [--json]",
		Run:   runDeedsSealFile, RawArgs: true,
		Detail: "Hashes the file here and sends only its sha256, size, media type and label to the server (an org admin key), which seals the admin record file.sealed. The file never leaves this machine. The record is written to <path>.deed.json; check both later, offline, with deedglass verify <path>.deed.json --file <path> --keys <keys.json>.\n\nIt seals the file's hash, size, type and label. It says nothing about whether the file's contents are right."},
	{Path: p("ledger", "check"), Group: Deeds, Summary: "Check the record chain",
		Usage: "[--from-seq n] [--to-seq n] [--json]", Flags: ledgerCheckFlags, Needs: config.NeedDatabase, Run: runLedgerCheck},
	{Path: p("ledger", "prune"), Group: Deeds, Summary: "Remove old payloads; records stay",
		Usage: "[--older-than 90d] [--dry-run] [--yes] [--json]", Flags: ledgerPruneFlags, Needs: config.NeedDatabase, Run: runLedgerPrune},

	// Connectors.
	{Path: p("connector", "create"), Group: Connectors, Summary: "Add a connector",
		Usage: "--config <file> [--type rest] [--name orders] [--description …] [--json]", Flags: connectorCreateFlags, Run: runConnectorCreate},
	{Path: p("connector", "list"), Group: Connectors, Summary: "List connectors", Usage: "[--json]", Run: runConnectorList},
	{Path: p("connector", "show"), Group: Connectors, Summary: "Show one connector", Usage: "<name|con_…> [--json]", Run: runConnectorShow},
	{Path: p("connector", "test"), Group: Connectors, Summary: "Test a connector's credentials", Usage: "<name|con_…> [--json]", Run: runConnectorTest},
	{Path: p("connector", "types"), Group: Connectors, Summary: "List connector types", Usage: "[--type stripe] [--json]",
		Flags: connectorTypesFlags, Run: runConnectorTypes,
		Detail: "The connector catalog: every type, its tools, levels and honest limits. --type shows one type with its setup steps."},
	{Path: p("connector", "credentials", "set"), Group: Connectors, Summary: "Store a connector's credentials",
		Usage: "<name|con_…> --from-env VAR | --stdin | --field KEY=env:VAR|file:PATH|stdin... [--kind bearer] [--json]",
		Run:   runConnectorCredentialsSet, RawArgs: true,
		Detail: "Reads the value from the environment, stdin or a file, never from a flag (flags land in shell history and ps).\nThe value is encrypted with a fresh data key; agents never see it."},
	{Path: p("connector", "credentials", "remove"), Group: Connectors, Summary: "Delete a connector's credentials",
		Usage: "<name|con_…> [--json]", Run: runConnectorCredentialsRemove},
	{Path: p("connector", "postgres", "init"), Group: Connectors, Summary: "Prepare a Postgres database",
		Usage: "--connector <name> [--admin-dsn-env DEEDGLASS_INIT_DSN] [--config <file>] [--apply] [--confirm <database>] [--rotate-passwords] [--dsn-host host:port] [--no-store] [--json]",
		Flags: pgInitFlags, Run: runPostgresInit,
		Detail: "Plans the roles, schema deedglass, grants, replica identity and publication the connector needs, writes the SQL to .deedglass/plans/ for review,\nand applies it in one transaction with --apply (local or throwaway databases; otherwise --confirm <database>). The admin DSN is read once from the environment and never stored."},
	{Path: p("connector", "postgres", "check"), Group: Connectors, Summary: "Check a prepared Postgres database",
		Usage: "--connector <name> [--json]  ·  --dsn-env VAR [--witness-dsn-env VAR] [--config <file>] [--probe] [--json]",
		Flags: pgCheckFlags, Run: runPostgresCheck,
		Detail: "Read-only: wal_level, slots, the schema, begin_action's pinned body, the agent and witness roles, watched tables, the publication and the slot.\n--probe (local mode) also runs begin_action in a transaction it rolls back."},
	{Path: p("connector", "gmail", "authorize"), Group: Connectors, Summary: "Authorize the Gmail connector",
		Usage: "<name> --client-file ./client_secret.json [--readonly] [--no-browser] [--json]", Flags: gmailAuthorizeFlags, Run: runGmailAuthorize,
		Detail: "Signs you in with Google through a loopback page on 127.0.0.1 (PKCE, 5 minutes) and stores the refresh token in the vault.\nDeedglass writes drafts only. Google apps in Testing need this again every 7 days."},
	{Path: p("decoder", "status"), Group: Connectors, Summary: "Show what the decoder reads from Postgres",
		Usage: "[--connector <name>] [--recent N] [--json]", Flags: decoderStatusFlags, Run: runDecoderStatus,
		Detail: "One line per checked connector: healthy, degraded, halted or connecting, with the lag and the next step.\n--connector shows one in detail: the slot, positions, watched tables, frame counts and the newest frames."},
	{Path: p("decoder", "drop"), Group: Connectors, Summary: "Drop a connector's replication slot",
		Usage: "--connector <name> [--confirm <name>] [--json]", Flags: decoderDropFlags, Run: runDecoderDrop,
		Detail: "Stops the decoder, drops the slot and seals decoder.slot_dropped. A new slot starts on the next sync: a new baseline,\nand actions in the gap are recorded as missing. Type the connector's name to confirm, or pass --confirm in scripts."},
	{Path: p("slack", "manifest"), Group: Connectors, Summary: "Print the Slack app manifest",
		Usage: "--base-url <https://host> [--name Deedglass] [--json]", Flags: slackManifestFlags, Run: runSlackManifest,
		Detail: "Prints the app manifest for Slack approvals with request_url set to <base-url>/v1/slack/interact.\nPaste it at api.slack.com/apps › Create New App › From a manifest. The docs say why each scope is needed."},

	// MCP.
	{Path: p("mcp", "wrap"), Group: MCP, Summary: "Run an MCP server behind Deedglass",
		Usage: "--connector <name> [--connector-type mcp] [--hide-denied=false] [--call-timeout 2m] [--result-mode full|digest] [--log-file <path>] -- <command> [args…]",
		Run:   runMCPWrap, RawArgs: true, Detail: mcpWrapHelp},
	{Path: p("mcp", "serve"), Group: MCP, Summary: "Serve Deedglass's native MCP tools", Usage: "[--connector native|<name>]",
		Flags: mcpServeFlags, Run: runMCPServe, Detail: mcpServeHelp},
	{Path: p("mcp", "snippet"), Group: MCP, Summary: "Print client config for an MCP client",
		Usage: "--client <claude-desktop|claude-code|cursor|windsurf|vscode> --connector <name> [--connector-type postgres] [--api-key-from-env] -- <command> [args…]  ·  --transport http --client <…> [--connector native|<name>]",
		Run:   runMCPSnippet, RawArgs: true, Detail: mcpSnippetHelp},
	{Path: p("mcp", "install-links"), Group: MCP, Summary: "Print every one-click install link", Usage: "[--json]",
		Run: runMCPInstallLinks, Detail: "Cursor, VS Code, LM Studio and Goose deeplinks, the Claude Code command, the Claude Desktop .mcpb downloads and the\nWindsurf config. Every one installs npx -y deedglass mcp serve; no link holds a key."},

	// Insights.
	{Path: p("insights", "stats"), Group: Insights, Summary: "Show decisions, approvals and verdicts by rule",
		Usage: "[--window 7d|30d | --from … --to …] [--by rule|tool|agent|none] [--agent agt_…] [--tool stripe.*] [--compare] [--json]",
		Run:   runInsightsStats, RawArgs: true},
	{Path: p("insights", "shadow"), Group: Insights, Summary: "Show where the shadow policy disagrees",
		Usage: "[--shadow-version pv_…] [--from …] [--to …] [--json]", Run: runInsightsShadow, RawArgs: true},
	{Path: p("insights", "anomalies"), Group: Insights, Summary: "List anomalies",
		Usage: "[--status open|acknowledged|resolved|all] [--agent agt_…] [--kind volume_spike] [--days 14] [--limit 50] [--json]",
		Run:   runInsightsAnomalies, RawArgs: true},
	{Path: p("insights", "detect"), Group: Insights, Summary: "Run anomaly detection now",
		Usage: "[--org org_…|slug] [--backfill 7d] [--json]", Needs: config.NeedDatabase, Run: runInsightsDetect, RawArgs: true},
	{Path: p("insights", "settings"), Group: Insights, Summary: "Show or change Insights settings",
		Usage: `[--anomaly-facts on|off] [--active-minutes 60] [--stale-minutes 15] [--llm-phrasing on|off] [--llm-cap-cents 200] [--digest owners|admins|off] [--reason "…"] [--json]`, Run: runInsightsSettings, RawArgs: true},
	{Path: p("insights", "suggestions", "list"), Group: Insights, Summary: "List suggestions",
		Usage: "[--status open|accepted|rejected|superseded|all] [--kind loosen|tighten|budget|explicit_rule|advisory] [--limit 25] [--json]",
		Run:   runSuggestionsList, RawArgs: true},
	{Path: p("insights", "suggestions", "show"), Group: Insights, Summary: "Show one suggestion",
		Usage: "sug_… [--no-diff] [--json]", Run: runSuggestionsShow, RawArgs: true},
	{Path: p("insights", "suggestions", "accept"), Group: Insights, Summary: "Accept a suggestion into shadow",
		Usage: `sug_… [--reason "…"] [--replace-shadow] [--json]`, Run: runSuggestionsAccept, RawArgs: true,
		Detail: "Creates a policy version from the suggestion and runs it in shadow next to the enforced one. Nothing is enforced:\npromote it later with deedglass policy activate <name> --version N --mode enforce."},
	{Path: p("insights", "suggestions", "reject"), Group: Insights, Summary: "Reject a suggestion",
		Usage: `sug_… --reason "…" [--json]`, Run: runSuggestionsReject, RawArgs: true},
	{Path: p("insights", "suggestions", "generate"), Group: Insights, Summary: "Generate suggestions now",
		Usage: "[--json]", Run: runSuggestionsGenerate, RawArgs: true},
	{Path: p("insights", "digest"), Group: Insights, Summary: "Preview the weekly digest",
		Usage: "--preview [--org org_…|slug] [--html /tmp/digest.html] [--json]", Needs: config.NeedDatabase, Run: runInsightsDigest, RawArgs: true},

	// Dev.
	{Path: p("dev", "seed"), Group: Dev, Summary: "Load demo data into a dev database", Usage: "[--demo [--keys-file <path>] [--approver <email>]] [--json]",
		Flags: devSeedFlags, Run: runDevSeed},
	{Path: p("dev", "fake-action"), Group: Dev, Summary: "Seal realistic fake actions (dev only)",
		Usage: "[--n 20] [--seed 42] [--kinds allow,deny,…] [--agents 3] [--batch 1] [--pace 0s] [--org-slug demo] [--quiet] [--json]",
		Flags: fakeActionFlags, Needs: config.NeedDatabase, Run: runFakeAction},
	{Path: p("dev", "simulate"), Group: Dev, Summary: "Simulate a week of agent traffic (dev only)",
		Usage: "[--scenario examples/sim/acme.yaml] [--days 7] [--end now|-2d] [--seed 7] [--org org_… | --org-slug acme-sim] [--append] [--pace 0s] [--quiet] [--json]",
		Flags: simulateFlags, Needs: config.NeedDatabase, Run: runDevSimulate},
	{Path: p("dev", "agent-script"), Group: Dev, Summary: "Run a scripted agent",
		Usage: "<script.yaml> [--step] [--live] [--only <scene>] [--keys-file examples/demo/.demo-keys] [--root .] [--var name=value] [--json]",
		Run:   runDevAgentScript, RawArgs: true,
		Detail: "Runs the scenes of a script through the real rules as the demo's agent and checks every outcome; the first unmet\nexpectation stops it (exit 1). No shell: allow-listed make targets and deedglass subcommands only; files change only under .tmp/demo/."},
	{Path: p("dev", "mail-preview"), Group: Dev, Summary: "Preview emails in the browser",
		Usage: "login|invite|approval|approval-foureyes [--out /tmp/login.html] [--json]", Flags: mailPreviewFlags, Run: runMailPreview},
	{Path: p("dev", "sign-intent"), Group: Dev, Summary: "Sign an intent with a local key",
		Usage: `--key <pem> --tool stripe.refund --args '{…}' [--org org_… --agent agt_…] [--obo …] [--session …] [--ttl 60s] [--explain] [--json]`,
		Flags: signIntentFlags, Run: runSignIntent, RawArgs: true},
	{Path: p("dev", "stripe-fixtures"), Group: Dev, Summary: "Create Stripe sandbox fixtures",
		Usage: "[--from-env STRIPE_TEST_KEY] [--connector stripe] [--out examples/demo/.stripe-fixtures.json] [--json]", Flags: stripeFixturesFlags, Run: runStripeFixtures,
		Detail: "Creates the demo customer, $149.00, $20.00 and ¥5,000 payments and a $29.00/month subscription in your Stripe sandbox,\nthrough the connector's own rails (sandbox keys only, the livemode breaker). A second run the same day creates nothing new."},
	{Path: p("dev", "id"), Group: Dev, Summary: "Print a fresh id for a prefix", Usage: "<prefix>",
		Run: runDevID, Hidden: true},
}

// lines is `deedglass help`: one row per command or family, in contract order.
var lines = []Line{
	{Name: "init", Group: Setup, Summary: "Set up Deedglass in this directory", Paths: ps("init")},
	{Name: "doctor", Group: Setup, Summary: "Check config, database and keys", Paths: ps("doctor")},
	{Name: "scan", Group: Setup, Summary: "Show what your agent setup can change", Paths: ps("scan")},
	{Name: "serve", Group: Setup, Summary: "Run the API, the rules and the Console", Paths: ps("serve")},
	{Name: "migrate", Group: Setup, Summary: "Apply or check database migrations", Subs: subs("up status verify"), Paths: ps("migrate up", "migrate status", "migrate verify")},
	{Name: "config print", Group: Setup, Summary: "Show the effective config, secrets hidden", Paths: ps("config print")},
	{Name: "version", Group: Setup, Summary: "Show the client and server versions", Paths: ps("version")},
	{Name: "keys", Group: Setup, Summary: "Manage seal keys", Subs: subs("gen rotate list"), Paths: ps("keys gen", "keys rotate", "keys list")},
	{Name: "licence", Group: Setup, Summary: "Turn on Pro, or check this machine's licence", Subs: subs("activate show refresh deactivate"), Paths: ps("licence activate", "licence show", "licence refresh", "licence deactivate")},
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

	{Name: "policy", Group: Policies, Summary: "Write, test and activate policies", Subs: subs("lint fmt test push activate evaluate replay diff install"),
		Paths: ps("policy lint", "policy fmt", "policy test", "policy push", "policy activate", "policy evaluate", "policy replay", "policy diff", "policy install")},
	{Name: "policy packs", Group: Policies, Summary: "List or check the policy packs", Subs: subs("list verify"),
		Paths: ps("policy packs list", "policy packs verify")},

	{Name: "act", Group: Actions, Summary: "Send a test action through Deedglass", Paths: ps("act")},
	{Name: "actions", Group: Actions, Summary: "Inspect actions", Subs: subs("list show cancel"), Paths: ps("actions list", "actions show", "actions cancel")},
	{Name: "approve · deny", Group: Actions, Summary: "Decide a pending approval", Paths: ps("approve", "deny")},
	{Name: "approvals", Group: Actions, Summary: "List pending approvals", Subs: subs("list show"), Paths: ps("approvals list", "approvals show")},
	{Name: "undo", Group: Actions, Summary: "Preview or run an undo", Subs: subs("preview run"), Paths: ps("undo preview", "undo run")},
	{Name: "observe", Group: Actions, Summary: "Watch what changed while an agent ran", Subs: subs("run start stop list show undo exclude"),
		Paths: ps("observe run", "observe start", "observe stop", "observe list", "observe show", "observe undo", "observe exclude")},
	{Name: "webhooks", Group: Actions, Summary: "Send events to your endpoints", Subs: subs("create list test delete"),
		Paths: ps("webhooks create", "webhooks list", "webhooks test", "webhooks delete")},

	{Name: "verify", Group: Deeds, Summary: "Check records offline", Paths: ps("verify")},
	{Name: "deeds", Group: Deeds, Summary: "Browse and export records", Subs: subs("list show share export seal-file"), Paths: ps("deeds list", "deeds show", "deeds share", "deeds export", "deeds seal-file")},
	{Name: "ledger", Group: Deeds, Summary: "Check or prune the Ledger", Subs: subs("check prune"), Paths: ps("ledger check", "ledger prune")},

	{Name: "connector", Group: Connectors, Summary: "Connect tools", Subs: subs("create list show test types credentials"),
		Paths: ps("connector create", "connector list", "connector show", "connector test", "connector types", "connector credentials set", "connector credentials remove")},
	{Name: "connector postgres", Group: Connectors, Summary: "Prepare a Postgres database", Subs: subs("init check"),
		Paths: ps("connector postgres init", "connector postgres check")},
	{Name: "connector gmail", Group: Connectors, Summary: "Authorize the Gmail connector", Subs: subs("authorize"), Paths: ps("connector gmail authorize")},
	{Name: "decoder", Group: Connectors, Summary: "Inspect Postgres change readers", Subs: subs("status drop"), Paths: ps("decoder status", "decoder drop")},
	{Name: "slack manifest", Group: Connectors, Summary: "Print the Slack app manifest", Paths: ps("slack manifest")},

	{Name: "mcp", Group: MCP, Summary: "Put Deedglass in front of MCP servers", Subs: subs("wrap serve snippet install-links"), Paths: ps("mcp wrap", "mcp serve", "mcp snippet", "mcp install-links")},

	{Name: "insights", Group: Insights, Summary: "Suggestions, shadow results and anomalies", Subs: subs("stats shadow anomalies detect settings suggestions digest"),
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
	"verifier run":     "serve: checks run inside it (S19 to S21)",
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
