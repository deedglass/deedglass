package shellguard

import (
	"encoding/json"
	"errors"
	"io"
)

// The Claude Code PreToolUse hook contract, as documented at https://code.claude.com/docs/en/hooks
// (checked 2026-10-18): the hook reads one JSON object on stdin and, to ask or deny, writes
// {"hookSpecificOutput": {"hookEventName": "PreToolUse", "permissionDecision": "ask"|"deny",
// "permissionDecisionReason": "…"}} and exits 0. For "ask" the reason is shown to the person in
// the permission prompt; for "deny" it is shown to Claude. Writing nothing leaves the decision to
// Claude Code's own permission rules, which still apply whatever a hook returns.

// HookInput is the part of a PreToolUse input the guard reads.
type HookInput struct {
	HookEventName string `json:"hook_event_name"`
	ToolName      string `json:"tool_name"`
	CWD           string `json:"cwd"`
	ToolInput     struct {
		Command string `json:"command"`
	} `json:"tool_input"`
}

type hookDecision struct {
	HookEventName            string `json:"hookEventName"`
	PermissionDecision       string `json:"permissionDecision"`
	PermissionDecisionReason string `json:"permissionDecisionReason"`
}

// ErrHookInput means stdin wasn't a PreToolUse input the guard could read.
var ErrHookInput = errors.New("it isn't a Claude Code PreToolUse JSON object")

// Hook reads one PreToolUse input from r and checks a Bash command. When something matches it
// writes an "ask" decision (or "deny" when deny is true) with one sentence to w; otherwise it
// writes nothing. It never answers "allow". Inputs for other tools or events are ignored.
func Hook(r io.Reader, w io.Writer, deny bool, o Options) ([]Finding, error) {
	var in HookInput
	if err := json.NewDecoder(io.LimitReader(r, 4*MaxBytes)).Decode(&in); err != nil {
		return nil, ErrHookInput
	}
	if in.ToolName != "Bash" || (in.HookEventName != "" && in.HookEventName != "PreToolUse") {
		return nil, nil
	}
	if o.Dir == "" {
		o.Dir = in.CWD
	}
	fs := Check(in.ToolInput.Command, o)
	if len(fs) == 0 {
		return nil, nil
	}
	d := hookDecision{HookEventName: "PreToolUse", PermissionDecision: "ask", PermissionDecisionReason: Reason(fs, deny)}
	if deny {
		d.PermissionDecision = "deny"
	}
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	return fs, enc.Encode(struct {
		HookSpecificOutput hookDecision `json:"hookSpecificOutput"`
	}{d})
}
