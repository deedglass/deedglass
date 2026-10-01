package gate

import (
	"fmt"
	"strings"
	"time"

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
	reasons := []string{}
	if a.Witness != nil && a.Witness.Reasons != nil {
		reasons = *a.Witness.Reasons
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

// UnreachableMessage is what an MCP surface says when the Deedglass server can't be reached.
func UnreachableMessage(server string) string {
	return fmt.Sprintf("Deedglass at %s can't be reached, so this tool call was not run. Check that deedglass serve is running.", clip(server, 120))
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
