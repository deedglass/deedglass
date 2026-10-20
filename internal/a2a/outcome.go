package a2a

import (
	"bytes"
	"encoding/json"
	"regexp"
	"strings"

	"github.com/deedglass/deedglass/internal/gate"
)

// Task states (A2A §4.1.3, ProtoJSON enum names).
const (
	StateSubmitted     = "TASK_STATE_SUBMITTED"
	StateWorking       = "TASK_STATE_WORKING"
	StateCompleted     = "TASK_STATE_COMPLETED"
	StateFailed        = "TASK_STATE_FAILED"
	StateCanceled      = "TASK_STATE_CANCELED"
	StateInputRequired = "TASK_STATE_INPUT_REQUIRED"
	StateRejected      = "TASK_STATE_REJECTED"
	StateAuthRequired  = "TASK_STATE_AUTH_REQUIRED"
)

var reState = regexp.MustCompile(`^TASK_STATE_[A-Z][A-Z_]{0,40}$`)

// errorNames maps JSON-RPC and A2A error codes (A2A §5.4, §9.5) to the error_code reported.
var errorNames = map[int]string{
	-32700: "json_parse_error", -32600: "invalid_request", -32601: "method_not_found", -32602: "invalid_params",
	-32001: "task_not_found", -32002: "task_not_cancelable", -32003: "push_notification_not_supported",
	-32004: "unsupported_operation", -32005: "content_type_not_supported", -32006: "invalid_agent_response",
	-32007: "extended_agent_card_not_configured", -32008: "extension_support_required", -32009: "version_not_supported",
}

// Outcome is what the peer said about one acting call, as the Gate records it.
type Outcome struct {
	Status   gate.ExecStatus
	Code     string          // error_code; "" on success
	Message  string          // error_message, the peer's words clipped
	Recorded json.RawMessage // the JSON-RPC result or error object, as the peer sent it
}

type rpcAnswer struct {
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

type stateOnly struct {
	Status *struct {
		State string `json:"state"`
	} `json:"status"`
}

// streamOrSend is the members of SendMessageResponse and StreamResponse this package reads.
type streamOrSend struct {
	Task         *stateOnly      `json:"task"`
	Message      json.RawMessage `json:"message"`
	StatusUpdate *stateOnly      `json:"statusUpdate"`
}

// Classify reads one JSON-RPC answer (a whole body, or one SSE event's data) for method. A JSON-RPC
// error is the peer saying it didn't act (failed), except an internal error, which may have come
// part way (unknown). A task left short of a terminal state is unknown: the peer is still working,
// or waits for input, and Deedglass doesn't follow it.
func Classify(method string, body []byte) Outcome {
	var a rpcAnswer
	if json.Unmarshal(bytes.TrimSpace(body), &a) != nil || (a.Error == nil && len(a.Result) == 0) {
		return Outcome{Status: gate.ExecUnknown, Code: "peer_answer_unreadable"}
	}
	if a.Error != nil {
		raw, _ := json.Marshal(a.Error)
		o := Outcome{Status: gate.ExecFailed, Code: "peer_error", Message: clip(a.Error.Message, 200), Recorded: raw}
		if n, ok := errorNames[a.Error.Code]; ok {
			o.Code = n
		}
		if a.Error.Code == -32603 {
			o.Status, o.Code = gate.ExecUnknown, "peer_internal_error"
		}
		return o
	}
	o := Outcome{Recorded: a.Result}
	switch method {
	case MethodCancelTask:
		var t stateOnly
		_ = json.Unmarshal(a.Result, &t)
		o.Status, o.Code = fromState(stateOf(&t), true)
	case MethodSendMessage, MethodSendStreamingMessage:
		var r streamOrSend
		if json.Unmarshal(a.Result, &r) != nil {
			return Outcome{Status: gate.ExecUnknown, Code: "peer_answer_unreadable", Recorded: a.Result}
		}
		switch {
		case r.Task != nil:
			o.Status, o.Code = fromState(stateOf(r.Task), false)
		case r.StatusUpdate != nil:
			o.Status, o.Code = fromState(stateOf(r.StatusUpdate), false)
		case len(r.Message) > 0 && string(r.Message) != "null":
			o.Status = gate.ExecSucceeded // the agent answered with a message and made no task
		default:
			o.Status, o.Code = gate.ExecUnknown, "task_state_unknown" // an artifact update alone
		}
	default:
		o.Status = gate.ExecSucceeded // the push notification config writes
	}
	return o
}

func stateOf(t *stateOnly) string {
	if t == nil || t.Status == nil {
		return ""
	}
	return t.Status.State
}

// fromState maps a task state to an execution status. For CancelTask, CANCELED is the success.
func fromState(s string, cancel bool) (gate.ExecStatus, string) {
	switch {
	case cancel && s == StateCanceled:
		return gate.ExecSucceeded, ""
	case !cancel && s == StateCompleted:
		return gate.ExecSucceeded, ""
	case s == StateFailed, s == StateRejected, !cancel && s == StateCanceled:
		return gate.ExecFailed, "task_" + strings.ToLower(strings.TrimPrefix(s, "TASK_STATE_"))
	case reState.MatchString(s):
		return gate.ExecUnknown, "task_" + strings.ToLower(strings.TrimPrefix(s, "TASK_STATE_"))
	default:
		return gate.ExecUnknown, "task_state_unknown"
	}
}
