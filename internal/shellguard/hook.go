package shellguard

import (
	"encoding/json"
	"errors"
	"io"
)

// The hook contracts. Claude Code's is documented at https://code.claude.com/docs/en/hooks (checked
// 2026-10-18): the hook reads one JSON object on stdin and, to ask or deny, writes
// {"hookSpecificOutput": {"hookEventName": "PreToolUse", "permissionDecision": "ask"|"deny",
// "permissionDecisionReason": "…"}} and exits 0. For "ask" the reason is shown to the person in
// the permission prompt; for "deny" it is shown to Claude. Writing nothing leaves the decision to
// Claude Code's own permission rules, which still apply whatever a hook returns. The other clients'
// contracts are rows of Clients (clients.go), each with the pages it was checked against.

// HookInput is the part of a hook input the guard reads, across every client's shape.
type HookInput struct {
	HookEventName string `json:"hook_event_name"`
	ToolName      string `json:"tool_name"`
	CWD           string `json:"cwd"`
	ToolInput     struct {
		Command string `json:"command"`
	} `json:"tool_input"`
	// GitHub Copilot's camelCase preToolUse: toolArgs is a JSON-encoded string in GitHub's own
	// example ("{\"command\":\"ls\"}"); an object is read too.
	CopilotToolName string          `json:"toolName"`
	CopilotToolArgs json.RawMessage `json:"toolArgs"`
	// Cursor's beforeShellExecution carries the command at the top level.
	Command string `json:"command"`
}

type hookDecision struct {
	HookEventName            string `json:"hookEventName"`
	PermissionDecision       string `json:"permissionDecision"`
	PermissionDecisionReason string `json:"permissionDecisionReason"`
}

// ErrHookInput means stdin wasn't a hook input the guard could read.
var ErrHookInput = errors.New("it isn't a hook's JSON object")

// Hook reads one Claude Code PreToolUse input from r and checks a Bash command. When something
// matches it writes an "ask" decision (or "deny" when deny is true) with one sentence to w;
// otherwise it writes nothing. It never answers "allow". Inputs for other tools or events are ignored.
func Hook(r io.Reader, w io.Writer, deny bool, o Options) ([]Finding, error) {
	return Clients[0].Hook(r, w, deny, o)
}

// Hook reads one hook input in c's shape and answers in c's shape. It never answers allow. A
// client that can't ask gets deny. When nothing matches, it writes what c reads as no decision.
func (c ClientHook) Hook(r io.Reader, w io.Writer, deny bool, o Options) ([]Finding, error) {
	var in HookInput
	if err := json.NewDecoder(io.LimitReader(r, 4*MaxBytes)).Decode(&in); err != nil {
		return nil, ErrHookInput
	}
	cmd, ok := c.command(in)
	var fs []Finding
	if ok {
		if o.Dir == "" {
			o.Dir = in.CWD
		}
		fs = Check(cmd, o)
	}
	deny = deny || !c.Ask
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	if len(fs) == 0 {
		if c.Format == FormatGemini {
			// Gemini CLI parses stdout as JSON after exit 0; an empty object carries no decision.
			return nil, enc.Encode(struct{}{})
		}
		return nil, nil
	}
	reason, decision := Reason(fs, deny), "ask"
	if deny {
		decision = "deny"
	}
	var out any
	switch c.Format {
	case FormatGemini:
		out = struct {
			Decision      string `json:"decision"`
			Reason        string `json:"reason"`
			SystemMessage string `json:"systemMessage"`
		}{decision, reason, reason}
	case FormatCopilot:
		out = struct {
			PermissionDecision       string `json:"permissionDecision"`
			PermissionDecisionReason string `json:"permissionDecisionReason"`
		}{decision, reason}
	case FormatCursor:
		out = struct {
			Permission   string `json:"permission"`
			UserMessage  string `json:"user_message"`
			AgentMessage string `json:"agent_message"`
		}{decision, reason, reason}
	default:
		out = struct {
			HookSpecificOutput hookDecision `json:"hookSpecificOutput"`
		}{hookDecision{HookEventName: c.Event, PermissionDecision: decision, PermissionDecisionReason: reason}}
	}
	return fs, enc.Encode(out)
}

// command returns the shell command in in, when in is c's shell event.
func (c ClientHook) command(in HookInput) (string, bool) {
	if c.Event == "" {
		return "", false
	}
	switch c.Format {
	case FormatCopilot:
		if in.CopilotToolName != c.Shell {
			return "", false
		}
		args := []byte(in.CopilotToolArgs)
		var s string
		if json.Unmarshal(args, &s) == nil {
			args = []byte(s)
		}
		var a struct {
			Command string `json:"command"`
		}
		if json.Unmarshal(args, &a) != nil {
			return "", false
		}
		return a.Command, true
	case FormatCursor:
		if in.HookEventName != "" && in.HookEventName != c.Event {
			return "", false
		}
		return in.Command, true
	default:
		if in.ToolName != c.Shell || (in.HookEventName != "" && in.HookEventName != c.Event) {
			return "", false
		}
		return in.ToolInput.Command, true
	}
}
