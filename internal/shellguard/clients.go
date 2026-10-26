package shellguard

import "strings"

// One table for every coding agent the shell guard runs in (TY-08, ADR-104). Each row says where
// the client reads hook config, which event and tool carry a shell command, what the client can
// answer, and the exact config Deedglass prints. The decision format comes from the row's format;
// the snippet is Config. Every key in every snippet is one the client's own hooks page documents,
// on the date in Checked. A client whose shell tool or input isn't documented has no snippet: it
// gets Unsupported, and the MCP path (deedglass mcp wrap) instead.

// HookFormat is the JSON shape a client sends and expects back.
type HookFormat int

const (
	// FormatClaude: {"hook_event_name","tool_name","tool_input":{"command"},"cwd"} in,
	// {"hookSpecificOutput":{"hookEventName","permissionDecision","permissionDecisionReason"}} out.
	FormatClaude HookFormat = iota
	// FormatGemini: the same input, {"decision","reason","systemMessage"} out; "{}" for nothing.
	FormatGemini
	// FormatCopilot: {"toolName","toolArgs","cwd"} in, toolArgs a JSON string or an object;
	// {"permissionDecision","permissionDecisionReason"} out.
	FormatCopilot
	// FormatCursor: {"hook_event_name","command","cwd"} in, {"permission","user_message","agent_message"} out.
	FormatCursor
)

// ClientHook is one row of the table.
type ClientHook struct {
	Name   string     // the --client value
	Title  string     // the client's name
	Format HookFormat // what it sends and reads back
	Event  string     // the hook event the snippet registers ("" for an unsupported client)
	Shell  string     // the tool name that carries a shell command; "" when the event is shell-only
	Ask    bool       // the client honours ask; when false the guard answers deny
	// ErrorBlocks: the client denies the tool call when the hook exits non-zero, so the guard exits
	// 0 even when it can't read its input.
	ErrorBlocks bool
	File        string   // where the snippet goes, from the project root
	Config      string   // the snippet, exactly as printed (golden: testdata/clients/<name>.*)
	Docs        []string // the pages the row was checked against
	Checked     string
	Limits      []string // what the client can't do, written down
	Unsupported string   // why there is no hook snippet; the MCP path still works
}

// Clients is the table. Claude Code is first and is the default.
var Clients = []ClientHook{
	{
		Name: "claude-code", Title: "Claude Code", Format: FormatClaude, Event: "PreToolUse", Shell: "Bash", Ask: true,
		File: ".claude/settings.json (or ~/.claude/settings.json); the Deedglass plugin carries the same hook, on (ask) by default; DEEDGLASS_SHELL_GUARD=off turns it off",
		Config: `{
  "hooks": {
    "PreToolUse": [
      {
        "matcher": "Bash",
        "hooks": [{ "type": "command", "command": "deedglass guard shell", "timeout": 10 }]
      }
    ]
  }
}
`,
		Docs:    []string{"https://code.claude.com/docs/en/hooks"},
		Checked: "2026-10-18",
	},
	{
		Name: "codex", Title: "Codex CLI", Format: FormatClaude, Event: "PreToolUse", Shell: "Bash", Ask: false,
		File: ".codex/hooks.json (or ~/.codex/hooks.json; the same keys work as [[hooks.PreToolUse]] in config.toml)",
		Config: `{
  "hooks": {
    "PreToolUse": [
      {
        "matcher": "^Bash$",
        "hooks": [
          {
            "type": "command",
            "command": "deedglass guard shell --client codex",
            "timeout": 10,
            "statusMessage": "Deedglass shell guard"
          }
        ]
      }
    ]
  }
}
`,
		Docs:    []string{"https://developers.openai.com/codex/hooks"},
		Checked: "2026-10-20",
		Limits: []string{
			`Codex parses permissionDecision "ask" but doesn't support it yet: it marks the hook failed and runs the command. So with --client codex the guard answers deny, and the reason tells Codex to ask you to run the command yourself.`,
			"Codex runs a hook only after you trust it with /hooks, and loads a project's .codex/ hooks only when that project is trusted.",
		},
	},
	{
		Name: "gemini", Title: "Gemini CLI", Format: FormatGemini, Event: "BeforeTool", Shell: "run_shell_command", Ask: false,
		File: ".gemini/settings.json (or ~/.gemini/settings.json)",
		Config: `{
  "hooks": {
    "BeforeTool": [
      {
        "matcher": "^run_shell_command$",
        "hooks": [
          {
            "name": "deedglass-shell-guard",
            "type": "command",
            "command": "deedglass guard shell --client gemini",
            "timeout": 10000
          }
        ]
      }
    ]
  }
}
`,
		Docs:    []string{"https://geminicli.com/docs/hooks/", "https://geminicli.com/docs/hooks/reference/", "https://geminicli.com/docs/tools/shell/"},
		Checked: "2026-10-20",
		Limits: []string{
			`Gemini CLI's hook decisions are allow and deny; there is no ask. So with --client gemini the guard answers deny: Gemini reads the reason as a tool error, and you see it as a systemMessage.`,
			"Gemini CLI's docs say it was replaced by Antigravity CLI for unpaid and Google One users on 2026-06-18. Antigravity CLI's hooks are not checked here.",
		},
	},
	{
		Name: "copilot", Title: "GitHub Copilot (CLI and cloud agent)", Format: FormatCopilot, Event: "preToolUse", Shell: "bash", Ask: true, ErrorBlocks: true,
		File: ".github/hooks/deedglass.json (Copilot CLI also reads ~/.copilot/hooks/*.json)",
		Config: `{
  "version": 1,
  "hooks": {
    "preToolUse": [
      {
        "type": "command",
        "bash": "deedglass guard shell --client copilot || true",
        "timeoutSec": 10
      }
    ]
  }
}
`,
		Docs:    []string{"https://docs.github.com/en/copilot/reference/hooks-reference", "https://docs.github.com/en/copilot/concepts/agents/coding-agent/about-hooks"},
		Checked: "2026-10-20",
		Limits: []string{
			`The cloud agent, and Copilot CLI in autopilot, treat "ask" as "deny", because no one is there to answer. Copilot shows the reason to the agent.`,
			"Copilot denies the tool call when a preToolUse hook exits non-zero, so the snippet ends in || true: without a deedglass on PATH every command still runs, unchecked.",
			"Bash only: the snippet has no powershell command, so on Windows Copilot runs no guard.",
			"VS Code reads .github/hooks/*.json too, with its own payload and tool names; the guard writes nothing there (see vscode).",
		},
	},
	{
		Name: "cursor", Title: "Cursor", Format: FormatCursor, Event: "beforeShellExecution", Ask: true,
		File: ".cursor/hooks.json (or ~/.cursor/hooks.json)",
		Config: `{
  "version": 1,
  "hooks": {
    "beforeShellExecution": [
      {
        "command": "deedglass guard shell --client cursor",
        "timeout": 10
      }
    ]
  }
}
`,
		Docs:    []string{"https://cursor.com/docs/agent/hooks"},
		Checked: "2026-10-20",
		Limits: []string{
			"When nothing matches the guard writes nothing. Cursor counts no output as a hook failure and, with failClosed false (its default), lets the command go on. Don't set failClosed: true on this hook.",
			"Cursor's page doesn't say whether the Cursor CLI runs beforeShellExecution; checked in the editor's agent only.",
			"Cursor started from the Dock on macOS may not have deedglass on its PATH; then the hook fails and the command runs unchecked. Use the absolute path (command -v deedglass).",
		},
	},
	{
		Name: "vscode", Title: "VS Code (Copilot agent mode)",
		Docs:    []string{"https://code.visualstudio.com/docs/copilot/customization/hooks", "https://code.visualstudio.com/docs/agents/reference/hooks-reference"},
		Checked: "2026-10-20",
		Unsupported: "VS Code's hooks (Preview) take PreToolUse with hookSpecificOutput, but its docs don't name the terminal tool or its arguments " +
			`("Tool names and input schemas differ between harnesses"), so the guard can't find the command. ` +
			"Put your database MCP server behind deedglass mcp wrap instead: deedglass mcp snippet --client vscode.",
	},
}

// LookupClient returns the row for a --client value.
func LookupClient(name string) (ClientHook, bool) {
	for _, c := range Clients {
		if c.Name == name {
			return c, true
		}
	}
	return ClientHook{}, false
}

// ClientNames lists the --client values, comma-separated, for help and errors.
func ClientNames() string {
	ns := make([]string, len(Clients))
	for i, c := range Clients {
		ns[i] = c.Name
	}
	return strings.Join(ns, ", ")
}
