package present

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/deedglass/deedglass/internal/gate"
	"github.com/deedglass/deedglass/internal/ledger"
	"github.com/deedglass/deedglass/internal/mcp/jsonrpc"
)

var update = flag.Bool("update", false, "rewrite testdata/mcp/present")

func ptr(s string) *string { return &s }

// TestGoldens pins every synthetic result the wrapper sends, legacy and modern
// (testdata/mcp/present; go test ./internal/mcp/present -update).
func TestGoldens(t *testing.T) {
	exp := ledger.Time(time.Date(2026, 10, 5, 10, 42, 0, 0, time.UTC))
	denyMsg := `Blocked by policy "scratch-writes-only-in-allowed": Agents may only write inside /tmp/deedglass-demo/allowed.`
	denied := &ledger.Action{ID: "act_01j9zr0000000000000000000a", Status: ledger.StatusDenied, Deed: ptr("deed_01j9zr0000000000000000000a"),
		Decision: &ledger.ActionDecision{Outcome: "deny", Source: ptr("rule"), Code: ptr("policy_denied"), RuleID: ptr("scratch-writes-only-in-allowed"), Message: &denyMsg}}
	stopMsg := `Agent "claude-desktop" is stopped. Deedglass refuses its calls until an admin starts it in Console › Agents.`
	stopped := &ledger.Action{ID: "act_01j9zr0000000000000000000b", Status: ledger.StatusDenied, Deed: ptr("deed_01j9zr0000000000000000000b"),
		Decision: &ledger.ActionDecision{Outcome: "deny", Source: ptr("stop"), Code: ptr("agent_stopped"), Message: &stopMsg}}
	budgetMsg := `Budget "agent-rate" reached: 30 of 30 calls this minute. Try again in 20 s.`
	budget := &ledger.Action{ID: "act_01j9zr0000000000000000000c", Status: ledger.StatusDenied, Deed: ptr("deed_01j9zr0000000000000000000c"),
		Decision: &ledger.ActionDecision{Outcome: "deny", Source: ptr("budget"), Code: ptr("budget_exceeded"), Message: &budgetMsg}}
	pendMsg := `Waiting for approval under policy "writes-need-approval": Writes wait for a human.`
	pending := &ledger.Action{ID: "act_01j9zs0000000000000000000a", Status: ledger.StatusPendingApproval,
		Approval: &ledger.ActionApproval{ID: "apr_01j9zs4k7m2n3p5q6r7s8t9v0w", Status: "pending", ExpiresAt: exp},
		Decision: &ledger.ActionDecision{Outcome: "require_approval", Source: ptr("rule"), RuleID: ptr("writes-need-approval"), Message: &pendMsg}}
	cases := map[string]func(modern bool) json.RawMessage{
		"denied":        func(m bool) json.RawMessage { return Deny(denied, "", m) },
		"stopped":       func(m bool) json.RawMessage { return Deny(stopped, "", m) },
		"budget":        func(m bool) json.RawMessage { return Deny(budget, "", m) },
		"pending":       func(m bool) json.RawMessage { return Pending(pending, m) },
		"still_pending": func(m bool) json.RawMessage { return StillPending(pending, m) },
		"unreachable":   func(m bool) json.RawMessage { return Plain(gate.UnreachableMessage("http://127.0.0.1:8080"), m) },
		"bad_key":       func(m bool) json.RawMessage { return Plain(gate.MCPBadKey, m) },
		"rejected": func(m bool) json.RawMessage {
			return Plain(gate.MCPRejectedText("args has a key that appears twice in one object. Send each key once."), m)
		},
		"tool_changed": func(m bool) json.RawMessage { return Plain(gate.MCPToolChangedText("scratch.write_file"), m) },
		"crashed": func(m bool) json.RawMessage {
			return WithAction(gate.MCPCrashedText("scratch.write_file", "act_01j9zt0000000000000000000a"), "act_01j9zt0000000000000000000a", m)
		},
		"timeout": func(m bool) json.RawMessage {
			return WithAction(gate.MCPTimeoutText("scratch.slow", 30*time.Second, "act_01j9zt0000000000000000000b"), "act_01j9zt0000000000000000000b", m)
		},
		"keeps_crashing":  func(m bool) json.RawMessage { return Plain(gate.MCPKeepsCrashing, m) },
		"approval_denied": func(m bool) json.RawMessage { return Text(gate.MCPApprovalDenied, true, m, ActionMeta(pending), nil) },
		"approval_expired": func(m bool) json.RawMessage {
			return Plain(gate.MCPApprovalExpiredText("apr_01j9zs4k7m2n3p5q6r7s8t9v0w"), m)
		},
		"approval_not_here": func(m bool) json.RawMessage {
			return Plain(gate.MCPApprovalNotHereText("apr_01j9zs4k7m2n3p5q6r7s8t9v0w"), m)
		},
		"already_ran": func(m bool) json.RawMessage {
			return Text(gate.MCPAlreadyRanText("deed_01j9zs0000000000000000000a"), false, m, Meta{MetaAction: "act_01j9zs0000000000000000000a"}, nil)
		},
	}
	dir := filepath.Join("..", "..", "..", "testdata", "mcp", "present")
	for name, f := range cases {
		for _, era := range []string{"legacy", "modern"} {
			got := pretty(t, f(era == "modern"))
			p := filepath.Join(dir, name+"."+era+".json")
			if *update {
				if err := os.MkdirAll(dir, 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(p, got, 0o644); err != nil {
					t.Fatal(err)
				}
				continue
			}
			want, err := os.ReadFile(p)
			if err != nil || !bytes.Equal(want, got) {
				t.Errorf("%s differs (go test ./internal/mcp/present -update):\n%s", p, got)
			}
			var r struct {
				ResultType *string `json:"resultType"`
			}
			_ = json.Unmarshal(got, &r)
			if (era == "modern") != (r.ResultType != nil) {
				t.Errorf("%s: resultType only for modern", p)
			}
		}
	}
	tool := CheckApprovalTool()
	var def struct {
		Name        string
		InputSchema struct {
			Properties map[string]struct{ Pattern string }
		}
	}
	if err := json.Unmarshal(tool, &def); err != nil || def.Name != CheckApprovalName ||
		!regexp.MustCompile(def.InputSchema.Properties["approval_id"].Pattern).MatchString("apr_01j9zs4k7m2n3p5q6r7s8t9v0w") {
		t.Fatalf("%v %s", err, tool)
	}
}

func pretty(t *testing.T, raw json.RawMessage) []byte {
	t.Helper()
	if _, err := jsonrpc.Parse(jsonrpc.NewResponse(jsonrpc.Int(1), raw).Marshal()); err != nil {
		t.Fatal(err)
	}
	var b bytes.Buffer
	if err := json.Indent(&b, raw, "", "  "); err != nil {
		t.Fatal(err)
	}
	b.WriteByte('\n')
	return b.Bytes()
}

// TestCopy: every MCP sentence is calm (no exclamation marks, dashes or emoji) and says whether
// anything ran.
func TestCopy(t *testing.T) {
	exp := time.Date(2026, 10, 5, 10, 42, 0, 0, time.UTC)
	a := &ledger.Action{ID: "act_x"}
	msgs := []string{gate.MCPInstructions, gate.MCPBadKey, gate.MCPNoKey, gate.MCPServerStopped, gate.MCPKeepsCrashing, gate.MCPTooLarge,
		gate.MCPApprovalDenied, gate.MCPApprovalIDBad, gate.MCPCheckApprovalDescription, gate.UnreachableMessage("http://127.0.0.1:8080"),
		gate.MCPDenyText(a, "Blocked."), gate.MCPRejectedText("x"), gate.MCPPendingText(a, "apr_x", exp), gate.MCPStillPendingText("apr_x", exp),
		gate.MCPApprovedRanText("scratch.x"), gate.MCPAlreadyRanText("deed_x"), gate.MCPApprovalExpiredText("apr_x"),
		gate.MCPApprovalCancelledText("apr_x"), gate.MCPApprovalNotHereText("apr_x"), gate.MCPApprovalFinishedText("apr_x", "executed"),
		gate.MCPToolChangedText("scratch.x"), gate.MCPUnknownToolText("x"), gate.MCPCrashedText("scratch.x", "act_x"),
		gate.MCPTimeoutText("scratch.x", time.Second, "act_x")}
	bad := regexp.MustCompile(`[!—–]|\p{So}`)
	for _, m := range msgs {
		if bad.MatchString(m) || !strings.HasSuffix(m, ".") {
			t.Errorf("copy: %q", m)
		}
	}
	for _, m := range []string{gate.MCPDenyText(a, "Blocked."), gate.MCPRejectedText("x"), gate.MCPPendingText(a, "apr_x", exp),
		gate.MCPBadKey, gate.MCPNoKey, gate.UnreachableMessage("x"), gate.MCPToolChangedText("x"), gate.MCPKeepsCrashing} {
		if !strings.Contains(m, "run") && !strings.Contains(m, "ran") {
			t.Errorf("doesn't say whether anything ran: %q", m)
		}
	}
}
