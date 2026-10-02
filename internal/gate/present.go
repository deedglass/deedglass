package gate

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/deedglass/deedglass/internal/httpx"
	"github.com/deedglass/deedglass/internal/intent"
	"github.com/deedglass/deedglass/internal/ledger"
)

// present.go is the Gate's one catalog of user-facing strings (S13 §5). API error codes live in
// internal/httpx/errors.go; the decision sentences come from S11 (policy.Decision.Message) and S08
// (Stop) and are never rewritten here, only framed. S14 and S15 reuse the framing functions for
// MCP results, so they take an action, not a surface.

// --- validation (400 validation_failed, not recorded) ---

const (
	msgToolEmpty            = `tool is empty. Name a tool as "connector.tool", for example "fake.echo".`
	msgToolTooLong          = `tool is longer than 200 bytes. Name a tool as "connector.tool", for example "fake.echo".`
	msgToolNotUTF8          = `tool isn't valid UTF-8. Name a tool as "connector.tool", for example "fake.echo".`
	msgArgsMissing          = "args is missing. Send the tool's arguments as a JSON object, {} when it takes none."
	msgArgsNotObject        = "args must be a JSON object. Send the tool's arguments as an object, {} when it takes none."
	msgConnectorTypeGateway = "connector_type is for proxy mode, where the edge runs the tool. In gateway mode Deedglass knows the connector; leave connector_type out."
	msgToolDigest           = `tool_digest must be "sha256:" followed by 64 lowercase hex characters, or left out.`
	msgTransport            = "context.transport must be a short lowercase name, such as rest, sdk, mcp_stdio or mcp_http."
	msgClient               = "context.client.name and context.client.version must be text of at most 64 characters each."
	msgWait                 = "wait must be a number of seconds from 0 to 25."
)

func msgMode(got string) string {
	return fmt.Sprintf("mode must be gateway or proxy (got %q). Leave it out for gateway, where Deedglass runs the tool.", clip(got, 40))
}

func msgToolGrammar(tool string) string {
	return fmt.Sprintf(`%q isn't a tool name. Tools look like "connector.tool" in lowercase, for example "fake.echo".`, clip(tool, 80))
}

func msgConnectorTypeUnknown(t string) string {
	return fmt.Sprintf("connector_type %q isn't a connector type. Use one of postgres, rest, mcp, mcp_http, sdk, github, slack, stripe, gmail, notion or fake.", clip(t, 40))
}

func kib(n int) int { return (n + 1023) / 1024 }

func msgArgsTooLarge(n, max int) string {
	return fmt.Sprintf("args is %d KiB; the limit is %d KiB. Send a reference to the data instead of the data itself.", kib(n), max/1024)
}

func msgArgsInvalid(err error) string {
	switch intent.CodeOf(err) {
	case intent.CodeIntegerRange:
		return "args contains an integer larger than 9007199254740991, which JSON can't carry exactly. Send it as a string."
	case intent.CodeTooDeep:
		return "args is nested more than 32 levels deep. Flatten it, or send a reference to the data."
	case intent.CodeDuplicateKey:
		return "args has a key that appears twice in one object. Send each key once."
	case intent.CodeLoneSurrogate:
		return "args has a string with a lone UTF-16 surrogate. Send valid Unicode."
	case intent.CodeNumberRange:
		return "args has a number beyond the range of a double. Send it as a string."
	}
	return "args isn't valid JSON. Send the tool's arguments as a JSON object."
}

// --- refusals (recorded, sealed) ---

// ToolUnknownMessage is tool_unknown's message.
func ToolUnknownMessage(tool string) string {
	return fmt.Sprintf(`No tool named %q. Tools look like "connector.tool", for example "fake.echo".`, clip(tool, 80))
}

// ConnectorUnavailableMessage is connector_unavailable's message.
func ConnectorUnavailableMessage(connector string) string {
	return fmt.Sprintf("The %q connector is not available on this server. Check Console › Connectors.", clip(connector, 40))
}

// --- conflicts ---

const (
	MsgIdempotencyConflict = "This Idempotency-Key was used with a different request in the last 24 hours. Use a new key for a new request."
	MsgCancelExecuting     = "This action is already executing, so it can't be cancelled."
	MsgCancelApproved      = "A person already approved this action, so it can't be cancelled. It runs now."
	MsgCancelDone          = "This action has already finished, so there is nothing to cancel."
	MsgResultReported      = "A result was already reported for this action."
	MsgResultLate          = "This action was already sealed as UNVERIFIABLE because its result arrived after the deadline."
	MsgResultNotProxy      = "This action runs in gateway mode, so Deedglass reported its result itself. Only proxy-mode actions take a result."
	MsgResultNotAuthorized = "This action hasn't been authorized to run yet, so it has no result to report. Wait for its approval: GET /v1/actions/{id}?wait=25."
	MsgResultDigest        = "result_digest doesn't match result. Send the digest of the exact result (sha256 of its RFC 8785 form), or leave it out."
	MsgResultMissing       = "Send result or result_digest, so the deed can name what the tool returned."
	MsgResultStatus        = "status must be succeeded, failed or unknown."
	MsgResultTimes         = "finished_at must not be before started_at, and both must be RFC 3339 times near now."
	MsgResultTooLarge      = "result is larger than the limit. Send result_digest alone, with the digest of the full result."
	MsgResultErrorCode     = "error_code must be a short snake_case code, such as tool_error."
	MsgWaitTooMany         = "This agent already has 20 open waits. Close one, or poll without wait."
)

// --- framing for the CLI and MCP surfaces (S14, S15) ---

// NothingRan is appended to refusals on the CLI and MCP surfaces (never in the API message).
const NothingRan = "Nothing was run."

// DenyMessage frames a refusal for an agent or a person: the decision's own sentence, then that
// nothing ran.
func DenyMessage(a *Action) string {
	msg := ""
	if a.Decision != nil && a.Decision.Message != nil {
		msg = *a.Decision.Message
	}
	if msg == "" && a.refusal != nil {
		msg = a.refusal.Message
	}
	if msg == "" {
		msg = "Deedglass refused this action."
	}
	return strings.TrimSpace(msg + " " + NothingRan)
}

// PendingMessage frames a pending approval: the decision's sentence and when it expires.
func PendingMessage(a *Action, loc *time.Location) string {
	msg := "Waiting for a person to approve this action."
	if a.Decision != nil && a.Decision.Message != nil && *a.Decision.Message != "" {
		msg = *a.Decision.Message
	}
	if a.Approval != nil {
		if loc == nil {
			loc = time.UTC
		}
		exp := time.Time(a.Approval.ExpiresAt).In(loc)
		msg = strings.TrimSuffix(msg, ".") + ". Expires " + exp.Format("15:04") + " " + zoneName(exp) + "."
	}
	return msg
}

func zoneName(t time.Time) string {
	if z, _ := t.Zone(); z != "" {
		return z
	}
	return "UTC"
}

// UnknownOutcomeMessage is the honest sentence for an action whose outcome Deedglass can't know.
func UnknownOutcomeMessage(a *Action, executeTimeout time.Duration) string {
	reasons := []string{a.Witness.Cause()}
	if a.Witness != nil && a.Witness.Reasons != nil {
		reasons = append(reasons, a.Witness.Codes()...)
	}
	for _, r := range reasons {
		switch r {
		case ReasonTimeout:
			return fmt.Sprintf("The tool did not answer within %s. It may have run.", humanSeconds(executeTimeout))
		case ReasonEdgeLost:
			return "The edge never reported a result. The tool may have run."
		}
	}
	return "Deedglass could not learn whether the tool ran. It may have run."
}

// UnreachableMessage is what an MCP surface says when the Deedglass server can't be reached (or
// answers something it can't read): fail closed, and say how to fix it.
func UnreachableMessage(server string) string {
	return fmt.Sprintf(`Deedglass could not be reached at %s, so this was not run. Start it with "deedglass serve", then try again.`, clip(server, 120))
}

// WitnessPhrase is the method of a verdict in plain words, so L0 never reads like an independent
// check: "the tool reported success".
func WitnessPhrase(level, verdict, method string, reasons []string) string {
	switch {
	case verdict == VerdictVerified && method == MethodEdgeReported:
		return "the edge reported success"
	case verdict == VerdictVerified && method == MethodReported:
		return "the tool reported success"
	case verdict == VerdictUnverifiable:
		for _, r := range reasons {
			switch r {
			case ReasonTimeout:
				return "the tool did not answer in time"
			case ReasonEdgeLost:
				return "the edge never reported"
			case ReasonWitnessGaveUp:
				return "the witness gave up"
			}
		}
		return "the outcome is unknown"
	}
	return method
}

// NextStep is the one next step the CLI prints for a status.
func NextStep(a *Action) string {
	switch a.Status {
	case ledger.StatusDenied:
		if a.Decision != nil && a.Decision.Source != nil && *a.Decision.Source == "stop" {
			return "Next: an admin can start the agent again in Console › Agents."
		}
		if a.Decision != nil && a.Decision.Code != nil && *a.Decision.Code == "budget_exceeded" {
			return "Next: wait for the budget to reset, or ask an admin to raise it in Console › Policies."
		}
		return "Next: change the rule in Console › Policies, or ask an admin to run it by hand."
	case ledger.StatusPendingApproval:
		return "Next: approve it in Console › Approvals, then run: deedglass actions show " + a.ID + " --wait"
	case ledger.StatusAuthorized:
		return "Next: run the tool at the edge, then report it: POST /v1/actions/" + a.ID + "/result"
	case ledger.StatusFailed:
		return "Next: fix the cause in the error, then send the action again with a new Idempotency-Key."
	case ledger.StatusProcessing:
		return "Next: deedglass actions show " + a.ID + " --wait"
	case ledger.StatusExpired:
		return "Next: send the action again to ask for a new approval."
	}
	if a.Witness != nil && a.Witness.Verdict == VerdictUnverifiable {
		return "Next: check the target system by hand, or raise gate.execute_timeout."
	}
	return ""
}

func humanSeconds(d time.Duration) string {
	if d%time.Second == 0 {
		return fmt.Sprintf("%d s", int(d.Seconds()))
	}
	return fmt.Sprintf("%.1f s", d.Seconds())
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := s[:n]
	for len(cut) > 0 && cut[len(cut)-1]&0xC0 == 0x80 {
		cut = cut[:len(cut)-1]
	}
	if len(cut) > 0 && cut[len(cut)-1] >= 0xC0 {
		cut = cut[:len(cut)-1]
	}
	return cut + "…"
}

// --- MCP surfaces (S14 stdio wrap, S15 HTTP gateway) ---
//
// The model reads these, then explains them to a person. Each says what happened, whether anything
// ran, the rule or cause, and one next step. They carry only rule ids, the policy author's reason
// and ids: never tool arguments or upstream text (S14 §7, deny message injection).

// MCP-facing constants.
const (
	MCPInstructions             = "Tool calls on this server pass through Deedglass. Some may be blocked by policy or wait for a person to approve them; the result will say so. Use deedglass.check_approval to continue after an approval."
	MCPBadKey                   = "Deedglass did not accept the API key for this MCP server, so nothing was run. Create a key in Console › Agents and update your MCP client config."
	MCPServerStopped            = "The MCP server stopped. Deedglass is restarting it."
	MCPKeepsCrashing            = "The MCP server stopped 5 times in a minute, so Deedglass stopped restarting it. Nothing was run. Check the server's log, then restart your MCP client."
	MCPTooLarge                 = "The server's response was larger than 4 MiB."
	MCPApprovalDenied           = "A person denied this action. Nothing was run."
	MCPApprovalIDBad            = "approval_id must be an approval id from a Waiting for approval message, for example apr_01j9zs4k7m2n3p5q6r7s8t9v0w."
	MCPCheckApprovalTitle       = "Check a Deedglass approval"
	MCPCheckApprovalDescription = "Checks whether a person has countersigned an action that Deedglass paused for approval. Call it with the approval_id from a 'Waiting for approval' message. It waits up to wait_seconds (0 to 20, default 10) for the decision, so one call is usually enough. If it was approved, Deedglass runs the original tool call and returns its result. Nothing runs while the approval is pending."
)

// MCPDenyText frames a recorded refusal (policy, budget, Stop, unknown tool) for the model. fallback
// is the API error's message, used when the action carries no decision message.
func MCPDenyText(a *ledger.Action, fallback string) string {
	x := &Action{Action: *a}
	if (x.Decision == nil || x.Decision.Message == nil || *x.Decision.Message == "") && fallback != "" {
		x.refusal = &httpx.Error{Message: fallback}
	}
	msg := DenyMessage(x)
	src := ""
	if a.Decision != nil && a.Decision.Source != nil {
		src = *a.Decision.Source
	}
	if src == "rule" || src == "default" || src == "builtin" {
		if a.Deed != nil {
			msg += " Deedglass sealed a deed of this attempt (" + *a.Deed + ")."
		}
		msg += " If this should be allowed, a person can change the policy in Console › Policies."
	}
	return msg
}

// MCPRejectedText is a refusal the Gate didn't record (a malformed call, a signed-intent problem):
// the API's own sentence, then that nothing ran.
func MCPRejectedText(message string) string {
	m := strings.TrimSpace(message)
	if m == "" {
		m = "Deedglass refused this call."
	}
	if !strings.HasSuffix(m, ".") {
		m += "."
	}
	return m + " " + NothingRan
}

// MCPPendingText frames a pending approval and how to continue.
func MCPPendingText(a *ledger.Action, approvalID string, expires time.Time) string {
	msg := "Waiting for a person to approve this action."
	if a.Decision != nil && a.Decision.Message != nil && *a.Decision.Message != "" {
		msg = *a.Decision.Message
	}
	if !strings.HasSuffix(msg, ".") {
		msg += "."
	}
	return fmt.Sprintf(`%s Nothing has run yet. Approval %s expires at %s. To continue, call deedglass.check_approval with {"approval_id": "%s"}.`,
		msg, approvalID, clock(expires), approvalID)
}

// MCPWitnessText is the one line a tools/call result gets when its verdict isn't VERIFIED (S19):
// "Deedglass witness · L1 · MISMATCH · orders.update_status reported success, but …". "" for a
// VERIFIED verdict or none.
func MCPWitnessText(a *ledger.Action) string {
	w := a.Witness
	if w == nil || w.Verdict == VerdictVerified {
		return ""
	}
	line := "Deedglass witness · " + w.Level + " · " + w.Verdict
	if w.Summary != nil && *w.Summary != "" {
		line += " · " + *w.Summary
	}
	return line
}

// MCPWitnessPendingText is a tools/call result whose tool ran while its L2 verdict still waits for
// Postgres's change log when the call's wait ends (S21 §3.9).
const MCPWitnessPendingText = "Deedglass witness · pending · Postgres's change log hasn't shown this write yet. The verdict will appear in Console › Actions."

// MCPStillPendingText answers deedglass.check_approval while a person hasn't decided.
func MCPStillPendingText(approvalID string, expires time.Time) string {
	return fmt.Sprintf("Still waiting for approval %s. It expires at %s. Nothing has run yet. Check again in a minute.", approvalID, clock(expires))
}

// MCPApprovedRanText heads the original tool's result after a person approved it.
func MCPApprovedRanText(tool string) string {
	return "Approved by a person. Ran " + clip(tool, 160) + "."
}

// MCPApprovalDeniedText is a person's denial, with their reason when they gave one (S22, DL-028:
// the reason reaches the MCP client). Agent-facing texts never name the person.
func MCPApprovalDeniedText(a *ledger.Action) string {
	if a == nil || a.Approval == nil || a.Approval.DecisionReason == nil {
		return MCPApprovalDenied
	}
	r := strings.Join(strings.Fields(*a.Approval.DecisionReason), " ")
	r = strings.ReplaceAll(clip(r, 280), `"`, "'")
	if r == "" {
		return MCPApprovalDenied
	}
	if !strings.HasSuffix(r, ".") && !strings.HasSuffix(r, "?") {
		r += "."
	}
	return MCPApprovalDenied + ` Reason: "` + r + `"`
}

// MCPApprovedStoppedText is an approval whose agent was stopped before it could run.
func MCPApprovedStoppedText(agent string) string {
	return fmt.Sprintf("A person approved this, but agent %q is stopped, so nothing ran.", clip(agent, 64))
}

// MCPApprovalElsewhereText answers, after a wrapper restart, for an approval this process didn't
// request (it found it through GET /v1/approvals/{id}): it never runs the earlier call.
func MCPApprovalElsewhereText(approvalID, status string) string {
	switch status {
	case "approved":
		return "A person approved " + approvalID + ", but the call was made before this MCP server restarted, so Deedglass won't run it here. Make the call again to run it."
	case "pending":
		return "Approval " + approvalID + " is still waiting for a person. It was requested before this MCP server restarted, so after it is approved, make the call again."
	}
	return "Approval " + approvalID + " is " + status + ". Nothing was run here. Make the call again if it is still needed."
}

// MCPAlreadyRanText answers a second check after the approved call ran.
func MCPAlreadyRanText(deedOrAction string) string {
	return "This action already ran (" + deedOrAction + "). Nothing new was run."
}

// MCPApprovalExpiredText and MCPApprovalCancelledText end an approval that didn't run.
func MCPApprovalExpiredText(approvalID string) string {
	return "Approval " + approvalID + " expired before a person decided. Nothing was run. Make the call again to ask for a new approval."
}

// MCPApprovalCancelledText is a cancelled approval.
func MCPApprovalCancelledText(approvalID string) string {
	return "Approval " + approvalID + " was cancelled. Nothing was run. Make the call again if it is still needed."
}

// MCPApprovalNotHereText answers for an approval this wrapper process didn't create (another
// session, or the MCP server restarted): it never runs someone else's call.
func MCPApprovalNotHereText(approvalID string) string {
	return "Approval " + approvalID + " wasn't requested in this MCP session, so Deedglass won't run it here. If the MCP server restarted since, make the call again."
}

// MCPApprovalFinishedText is an approval whose action already finished without this process.
func MCPApprovalFinishedText(approvalID, status string) string {
	return "The action behind approval " + approvalID + " has already finished (" + status + "). Nothing new was run."
}

// MCPToolChangedText is a call to a tool whose definition changed after it was first listed.
func MCPToolChangedText(tool string) string {
	return fmt.Sprintf("The server changed the definition of %s after it was first listed, so Deedglass hid it. Nothing was run. Restart the MCP server to accept the new definition after a person reviews it.", clip(tool, 160))
}

// MCPUnknownToolText is the JSON-RPC -32602 for a tool the server doesn't list.
func MCPUnknownToolText(tool string) string {
	return fmt.Sprintf("Unknown tool: %q. Call tools/list to see the tools this server offers.", clip(tool, 160))
}

// MCPCrashedText is a call the server died running: Deedglass can't know whether it applied.
func MCPCrashedText(tool, actionID string) string {
	return fmt.Sprintf("The MCP server stopped while running %s. It may have run. Deedglass recorded it as UNVERIFIABLE (%s).", clip(tool, 160), actionID)
}

// MCPTimeoutText is a call the server didn't answer within the wrapper's --call-timeout.
func MCPTimeoutText(tool string, d time.Duration, actionID string) string {
	return fmt.Sprintf("The MCP server didn't answer %s within %s. It may have run. Deedglass recorded it as UNVERIFIABLE (%s).", clip(tool, 160), humanSeconds(d), actionID)
}

// clock is HH:MM UTC.
func clock(t time.Time) string {
	if t.IsZero() {
		return "an unknown time"
	}
	u := t.UTC()
	return pad2(u.Hour()) + ":" + pad2(u.Minute()) + " UTC"
}

func pad2(n int) string {
	if n < 10 {
		return "0" + strconv.Itoa(n)
	}
	return strconv.Itoa(n)
}

// --- S15: the Streamable HTTP gateway and /mcp/native ---

// MCP HTTP sentences (S15 §5). One sentence plus a next step each.
const (
	MCPNeedKey            = `Deedglass needs an agent API key. Send it as "Authorization: Bearer dg_…".`
	MCPOriginRefused      = "This origin is not allowed to call Deedglass MCP endpoints. Add it to mcp.allowed_origins if it should be."
	MCPUnsupportedVersion = "Deedglass supports MCP 2026-07-28, 2025-11-25, 2025-06-18 and 2025-03-26. Retry with one of those."
	MCPNotAcceptable      = "Deedglass answers MCP requests as application/json. Send Accept: application/json, text/event-stream."
	MCPOneMessage         = "Send one JSON-RPC request or notification per POST. Batches and responses are not accepted."
	MCPTooManyInFlight    = "This agent already has 32 MCP requests in flight. Wait for one to finish, then try again."
	MCPServerError        = "Deedglass could not handle this request, so nothing was run. Try again in a moment."
)

// MCPHeaderMismatchText is the -32020 sentence when a mirrored header and the body disagree.
func MCPHeaderMismatchText(header, headerValue, field, bodyValue string) string {
	return fmt.Sprintf("Header mismatch: %s is %q but the body names %q. Send the same %s in both.",
		header, clip(headerValue, 80), clip(bodyValue, 80), field)
}

// MCPHeaderMissingText is the -32020 sentence for a required header that wasn't sent.
func MCPHeaderMissingText(header, bodyValue string) string {
	if bodyValue == "" {
		return fmt.Sprintf("Header mismatch: %s is missing. Send it with every request, as MCP 2026-07-28 requires.", header)
	}
	return fmt.Sprintf("Header mismatch: %s is missing, but the body names %q. Send it with the same value.", header, clip(bodyValue, 80))
}

// MCPHeaderInvalidText is the -32020 sentence for a header value that can't be read.
func MCPHeaderInvalidText(header string) string {
	return fmt.Sprintf("Header mismatch: %s isn't a valid value. Encode values outside plain ASCII as =?base64?…?=.", header)
}

// MCPUnknownConnectorText answers /mcp/{connector} for a name no upstream is configured under.
func MCPUnknownConnectorText(name string) string {
	return fmt.Sprintf("No MCP connector named %q. Check the URL; configured connectors are listed in Console › Connectors.", clip(name, 64))
}

// MCPUpstreamDownText is the tool result when the upstream MCP server can't be reached.
func MCPUpstreamDownText(slug string) string {
	return fmt.Sprintf("The %q MCP server did not answer, so nothing was run. Deedglass will keep trying; check that it is running.", clip(slug, 64))
}

// MCPStillRunningText answers a gateway call that is still running when the HTTP answer is due.
func MCPStillRunningText(actionID string) string {
	return "Still running. Deedglass will record the outcome (" + actionID + ")."
}

// MCPApprovalUnknownText answers deedglass.check_approval for an approval this agent doesn't have.
func MCPApprovalUnknownText(approvalID string) string {
	return "No approval " + approvalID + " for this agent. Use the approval_id from the Waiting for approval message."
}

// MCPToolFailedText frames a gateway call the tool refused, without its upstream text.
func MCPToolFailedText(tool, actionID string) string {
	return fmt.Sprintf("%s failed before it applied anything. Deedglass recorded it (%s).", clip(tool, 160), actionID)
}

// MCPMethodNotFoundText is -32601 for a method the Gate doesn't serve or pass through.
func MCPMethodNotFoundText(method string) string {
	return fmt.Sprintf("Deedglass doesn't serve %q on this endpoint. Tools are available through tools/list and tools/call.", clip(method, 64))
}
