package deedglass

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// The S31 parity surface with @deedglass/sdk: Act (gateway or proxy), GetAction (a long poll),
// ReportResult (proxy mode) and Tools (GET /v1/tools, every page). The shapes follow contract §8;
// Raw keeps the whole answer for fields this struct doesn't name.

// MaxWait is the longest server-side wait on POST /v1/actions and GET /v1/actions/{id}.
const MaxWait = 25 * time.Second

// ActRequest is one POST /v1/actions.
type ActRequest struct {
	Tool string
	Args json.RawMessage // a JSON object; nil is {}
	// Mode is "gateway" (Deedglass runs the connector; the default) or "proxy" (you run the tool and
	// report the result).
	Mode string
	// Wait is the server-side wait for the outcome (0 to 25 s). Nil uses the server's default.
	Wait *time.Duration
	// ConnectorType is the edge's declared type in proxy mode ("sdk" for SDK edges, S31).
	ConnectorType string
	// Client names the calling software in the action's context.
	Client string
	// Version is the client's version.
	Version string
}

// Action is the action object (contract §8), with the fields most callers read.
type Action struct {
	Object     string          `json:"object"`
	ID         string          `json:"id"`
	Tool       string          `json:"tool"`
	Mode       string          `json:"mode"`
	State      string          `json:"state"`
	Status     string          `json:"status"`
	Deed       *string         `json:"deed"`
	Decision   *ActionDecision `json:"decision"`
	Approval   *ActionApproval `json:"approval"`
	NextAction *NextAction     `json:"next_action"`
	Execution  *struct {
		Status       string          `json:"status"`
		ErrorCode    *string         `json:"error_code"`
		ErrorMessage *string         `json:"error_message"`
		Result       json.RawMessage `json:"result,omitempty"`
	} `json:"execution"`
	Witness *struct {
		Level   string  `json:"level"`
		Verdict *string `json:"verdict"`
	} `json:"witness"`
	// Raw is the whole answer.
	Raw json.RawMessage `json:"-"`
}

// ActionDecision is the policy decision.
type ActionDecision struct {
	Outcome string  `json:"outcome"`
	RuleID  *string `json:"rule_id"`
	Message *string `json:"message"`
}

// ActionApproval is the approval an action waits for.
type ActionApproval struct {
	ID        string `json:"id"`
	Status    string `json:"status"`
	ExpiresAt string `json:"expires_at"`
}

// NextAction says what happens next: await_approval or execute_at_edge.
type NextAction struct {
	Type      string `json:"type"`
	Approval  string `json:"approval,omitempty"`
	ExpiresAt string `json:"expires_at,omitempty"`
	ReportURL string `json:"report_url,omitempty"`
	Deadline  string `json:"deadline,omitempty"`
}

// EdgeResult is POST /v1/actions/{id}/result: what the edge saw when it ran the tool.
type EdgeResult struct {
	Status       string          `json:"status"` // succeeded | failed | unknown
	StartedAt    time.Time       `json:"started_at"`
	FinishedAt   time.Time       `json:"finished_at"`
	Result       json.RawMessage `json:"result,omitempty"`
	ResultDigest string          `json:"result_digest,omitempty"`
	ErrorCode    string          `json:"error_code,omitempty"`
	ErrorMessage string          `json:"error_message,omitempty"`
}

// Tool is one tool of GET /v1/tools.
type Tool struct {
	Object      string          `json:"object"`
	Name        string          `json:"name"`
	Connector   string          `json:"connector"`
	Title       string          `json:"title"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema"`
	SideEffect  string          `json:"side_effect"`
	Visibility  string          `json:"visibility"`
	Levels      []string        `json:"levels"`
	ReadBack    string          `json:"read_back"`
}

func decodeAction(raw json.RawMessage) (*Action, error) {
	var a Action
	if err := json.Unmarshal(raw, &a); err != nil {
		return nil, fmt.Errorf("deedglass: the action isn't the JSON this client expects: %w", err)
	}
	a.Raw = raw
	return &a, nil
}

// Act sends POST /v1/actions. A refusal (a denial, Stop, a budget) is an *Error whose Action holds
// the sealed action. A tool that ran and failed is not an error: Status is "failed".
func (c *Client) Act(ctx context.Context, r ActRequest, opts ...Option) (*Action, *Response, error) {
	if r.Tool == "" {
		return nil, nil, errors.New("deedglass: Act needs a tool")
	}
	body := map[string]any{"tool": r.Tool, "args": json.RawMessage(`{}`)}
	if len(r.Args) > 0 {
		body["args"] = r.Args
	}
	if r.Mode != "" {
		body["mode"] = r.Mode
	}
	if r.Wait != nil {
		w := min(max(*r.Wait, 0), MaxWait)
		body["wait"] = w.Seconds()
	}
	if r.ConnectorType != "" {
		body["connector_type"] = r.ConnectorType
	}
	name := r.Client
	if name == "" {
		name = "deedglass-go"
	}
	body["context"] = map[string]any{"client": map[string]string{"name": name, "version": r.Version}, "transport": "sdk"}
	var raw json.RawMessage
	resp, err := c.Do(ctx, http.MethodPost, "/v1/actions", body, &raw, opts...)
	if err != nil {
		return nil, resp, err
	}
	a, err := decodeAction(raw)
	return a, resp, err
}

// GetAction reads one action, waiting up to wait (at most 25 s) for it to change.
func (c *Client) GetAction(ctx context.Context, id string, wait time.Duration) (*Action, error) {
	p := "/v1/actions/" + url.PathEscape(id)
	if wait > 0 {
		p += "?wait=" + strconv.FormatFloat(min(wait, MaxWait).Seconds(), 'f', -1, 64)
	}
	var raw json.RawMessage
	if _, err := c.Do(ctx, http.MethodGet, p, nil, &raw); err != nil {
		return nil, err
	}
	return decodeAction(raw)
}

// ReportResult sends POST /v1/actions/{id}/result for a proxy-mode action that ran at the edge. Pass
// WithIdempotencyKey to make a retry safe; a 409 means it was already reported.
func (c *Client) ReportResult(ctx context.Context, id string, r EdgeResult, opts ...Option) (*Action, error) {
	var raw json.RawMessage
	if _, err := c.Do(ctx, http.MethodPost, "/v1/actions/"+url.PathEscape(id)+"/result", r, &raw, opts...); err != nil {
		return nil, err
	}
	return decodeAction(raw)
}

// Tools lists GET /v1/tools, following next_cursor (at most 100 pages).
func (c *Client) Tools(ctx context.Context) ([]Tool, error) {
	var out []Tool
	cursor := ""
	for range 100 {
		p := "/v1/tools"
		if cursor != "" {
			p += "?cursor=" + url.QueryEscape(cursor)
		}
		var l struct {
			Data       []Tool  `json:"data"`
			HasMore    bool    `json:"has_more"`
			NextCursor *string `json:"next_cursor"`
		}
		if _, err := c.Do(ctx, http.MethodGet, p, nil, &l); err != nil {
			return nil, err
		}
		out = append(out, l.Data...)
		if !l.HasMore || l.NextCursor == nil || *l.NextCursor == "" {
			break
		}
		cursor = *l.NextCursor
	}
	return out, nil
}
