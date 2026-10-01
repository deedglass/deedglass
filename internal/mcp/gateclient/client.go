// Package gateclient is the MCP surfaces' typed client for the Gate (S14): submit a proxy-mode
// action, report its result, resolve tool visibility, read an action and whoami. Every answer the
// wrapper doesn't recognise is Unreachable, so the caller fails closed.
package gateclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/deedglass/deedglass/internal/deed"
	"github.com/deedglass/deedglass/internal/gate"
	"github.com/deedglass/deedglass/internal/ids"
	"github.com/deedglass/deedglass/internal/ledger"
	"github.com/deedglass/deedglass/pkg/deedglass"
)

// Kind is what the Gate decided about one call.
type Kind int

// Decision kinds.
const (
	Unreachable Kind = iota // no answer, a 5xx, or an answer the client can't read: never forward
	Authorized              // 200 status authorized: run it at the edge, then Report
	Denied                  // a recorded refusal (policy, budget, Stop, unknown tool): Action is sealed
	Pending                 // 202 pending_approval
	BadKey                  // 401: the key is missing, wrong, revoked or expired
	Rejected                // a refusal the Gate didn't record (400, intent errors, too many waits)
)

func (k Kind) String() string {
	return [...]string{"unreachable", "authorized", "denied", "pending_approval", "bad_key", "rejected"}[k]
}

// Decision is the Gate's answer to Submit.
type Decision struct {
	Kind    Kind
	Action  *ledger.Action // Authorized, Denied, Pending
	Message string         // the API error's message (Denied, Rejected, BadKey)
	Code    string         // the API error code
	Status  int            // HTTP status; 0 when no answer
	Err     error          // the cause, for logs (never shown to the model)
}

// Options configures a Client.
type Options struct {
	Server        string // http(s)://host:port
	APIKey        string // never logged
	Session       string // Deedglass-Session
	OnBehalfOf    string // Deedglass-On-Behalf-Of
	UserAgent     string
	HTTP          *http.Client
	SubmitTimeout time.Duration       // per attempt; default 10 s
	ReportTimeout time.Duration       // per attempt; default 5 s
	Sleep         func(time.Duration) // tests
}

// Client talks to one Deedglass server as one agent.
type Client struct {
	o   Options
	api *deedglass.Client
}

// New returns a Client.
func New(o Options) *Client {
	if o.SubmitTimeout <= 0 {
		o.SubmitTimeout = 10 * time.Second
	}
	if o.ReportTimeout <= 0 {
		o.ReportTimeout = 5 * time.Second
	}
	if o.Sleep == nil {
		o.Sleep = time.Sleep
	}
	hc := o.HTTP
	if hc == nil {
		hc = &http.Client{Transport: &http.Transport{Proxy: nil, MaxIdleConnsPerHost: 32, IdleConnTimeout: 90 * time.Second}}
	}
	return &Client{o: o, api: &deedglass.Client{BaseURL: o.Server, APIKey: o.APIKey, HTTP: hc, UserAgent: o.UserAgent}}
}

// Server is the server URL.
func (c *Client) Server() string { return c.o.Server }

func (c *Client) headers() []deedglass.Option {
	var out []deedglass.Option
	if c.o.Session != "" {
		out = append(out, header("Deedglass-Session", c.o.Session))
	}
	if c.o.OnBehalfOf != "" {
		out = append(out, header("Deedglass-On-Behalf-Of", c.o.OnBehalfOf))
	}
	return out
}

func header(k, v string) deedglass.Option { return func(r *http.Request) { r.Header.Set(k, v) } }

// NewIdempotencyKey is "mcpw_" plus a ULID.
func NewIdempotencyKey() string {
	return "mcpw_" + strings.TrimPrefix(ids.New[ids.IdempotencyKind]().String(), "idk_")
}

// SubmitRequest is one tools/call as an action.
type SubmitRequest struct {
	Tool           string          // "<connector>.<name>"
	Args           json.RawMessage // the MCP arguments object
	ConnectorType  string          // "" = mcp
	ToolDigest     string          // the pinned definition digest
	ClientName     string          // the MCP client's clientInfo
	ClientVersion  string          //
	Transport      string          // mcp_stdio | mcp_http
	IdempotencyKey string          // the same on every retry
}

type submitBody struct {
	Tool          string              `json:"tool"`
	Args          json.RawMessage     `json:"args"`
	Mode          string              `json:"mode"`
	Wait          float64             `json:"wait"`
	ConnectorType string              `json:"connector_type,omitempty"`
	ToolDigest    string              `json:"tool_digest,omitempty"`
	Context       gate.RequestContext `json:"context"`
}

var reActionID = regexp.MustCompile(`^act_[0-9a-hjkmnp-tv-z]{26}$`)

// Submit asks the Gate about one call in proxy mode. Connection errors and 5xx are retried twice
// with the same Idempotency-Key, so a retry never makes a second action.
func (c *Client) Submit(ctx context.Context, r SubmitRequest) Decision {
	args := r.Args
	if len(args) == 0 || string(args) == "null" {
		args = json.RawMessage("{}")
	}
	b := submitBody{Tool: r.Tool, Args: args, Mode: "proxy", ConnectorType: r.ConnectorType, ToolDigest: r.ToolDigest,
		Context: gate.RequestContext{Transport: r.Transport}}
	if r.ClientName != "" || r.ClientVersion != "" {
		b.Context.Client = &gate.ClientInfo{Name: clip(r.ClientName, 64), Version: clip(r.ClientVersion, 64)}
	}
	key := r.IdempotencyKey
	if key == "" {
		key = NewIdempotencyKey()
	}
	opts := append(c.headers(), deedglass.WithIdempotencyKey(key))
	body, err := encodeSubmit(b)
	if err != nil {
		return Decision{Kind: Unreachable, Err: err}
	}
	var last Decision
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			c.o.Sleep(time.Duration(attempt) * 200 * time.Millisecond)
		}
		actx, cancel := context.WithTimeout(ctx, c.o.SubmitTimeout)
		var a ledger.Action
		res, err := c.api.Do(actx, http.MethodPost, "/v1/actions", body, &a, opts...)
		cancel()
		last = classify(res, &a, err)
		if last.Kind != Unreachable || ctx.Err() != nil || !retryable(res, err) {
			return last
		}
	}
	return last
}

// encodeSubmit writes the body once, with args as sent (no re-encoding of a large object).
func encodeSubmit(b submitBody) (json.RawMessage, error) {
	args := b.Args
	b.Args = json.RawMessage(`0`)
	head, err := json.Marshal(b)
	if err != nil {
		return nil, err
	}
	i := bytes.Index(head, []byte(`"args":0`))
	if i < 0 || !json.Valid(args) {
		return nil, errors.New("gateclient: the arguments aren't valid JSON")
	}
	out := make([]byte, 0, len(head)+len(args))
	out = append(out, head[:i+len(`"args":`)]...)
	out = append(out, args...)
	return append(out, head[i+len(`"args":0`):]...), nil
}

func retryable(res *deedglass.Response, err error) bool {
	var de *deedglass.Error
	if errors.As(err, &de) {
		return de.Status >= 500
	}
	return err != nil && res == nil // no answer at all
}

// classify maps S13's status table onto a Decision.
func classify(res *deedglass.Response, a *ledger.Action, err error) Decision {
	var de *deedglass.Error
	if errors.As(err, &de) {
		d := Decision{Status: de.Status, Message: de.Message, Code: de.Code, Err: err}
		switch {
		case de.Status == http.StatusUnauthorized:
			d.Kind = BadKey
		case len(de.Action) > 0:
			var x ledger.Action
			if json.Unmarshal(de.Action, &x) != nil || !reActionID.MatchString(x.ID) || x.Status != ledger.StatusDenied {
				d.Kind = Unreachable
				d.Err = fmt.Errorf("the refusal's action isn't one the wrapper can read: %w", err)
				return d
			}
			d.Kind, d.Action = Denied, &x
		case de.Status >= 500 || de.Status == 0:
			d.Kind = Unreachable
		case de.Status == http.StatusBadRequest || de.Status == http.StatusForbidden || de.Status == http.StatusConflict ||
			de.Status == http.StatusUnprocessableEntity || de.Status == http.StatusTooManyRequests || de.Status == http.StatusNotFound:
			if de.Message == "" {
				d.Kind = Unreachable
				return d
			}
			d.Kind = Rejected
		default:
			d.Kind = Unreachable
		}
		return d
	}
	if err != nil || res == nil {
		return Decision{Kind: Unreachable, Err: err}
	}
	d := Decision{Status: res.Status, Action: a}
	switch {
	case !reActionID.MatchString(a.ID):
		d.Kind, d.Action, d.Err = Unreachable, nil, errors.New("the answer has no action id")
	case res.Status == http.StatusOK && a.Status == ledger.StatusAuthorized && a.NextAction != nil && a.NextAction.Type == "execute_at_edge":
		d.Kind = Authorized
	case res.Status == http.StatusAccepted && a.Status == ledger.StatusPendingApproval && a.Approval != nil && strings.HasPrefix(a.Approval.ID, "apr_"):
		d.Kind = Pending
	default:
		d.Kind, d.Action, d.Err = Unreachable, nil, fmt.Errorf("unexpected answer: HTTP %d, status %q", res.Status, a.Status)
	}
	return d
}

// ErrNotProxyable means the Gate refused the report for a reason a retry can't fix.
var ErrNotProxyable = errors.New("gateclient: the Gate refused the report")

// reportBackoff is S14 §3.6 step 6.
var reportBackoff = []time.Duration{200 * time.Millisecond, 500 * time.Millisecond, time.Second, 2 * time.Second, 4 * time.Second}

// Report sends a proxy action's result, retrying connection errors and 5xx five times. A 409 whose
// action already holds the same result digest counts as done. A result the Gate refuses as too
// large or unreadable is sent again as its digest alone.
func (c *Client) Report(ctx context.Context, actionID string, r gate.EdgeResult) error {
	if len(r.Result) > 0 && r.ResultDigest == "" {
		if d, err := deed.DigestJSON(r.Result); err == nil {
			r.ResultDigest = d
		} else {
			r.Result = nil // unreadable JSON can't be digested by the Gate either
		}
	}
	if r.ResultDigest == "" {
		empty, _ := deed.DigestJSON([]byte("null"))
		r.ResultDigest = empty
	}
	path := "/v1/actions/" + url.PathEscape(actionID) + "/result"
	var err error
	for attempt := 0; attempt <= len(reportBackoff); attempt++ {
		if attempt > 0 {
			c.o.Sleep(reportBackoff[attempt-1])
		}
		actx, cancel := context.WithTimeout(ctx, c.o.ReportTimeout)
		var res *deedglass.Response
		variant := "full"
		if len(r.Result) == 0 {
			variant = "digest"
		}
		res, err = c.api.Do(actx, http.MethodPost, path, r, nil, append(c.headers(), deedglass.WithIdempotencyKey("mcpr_"+variant+"_"+actionID))...)
		cancel()
		if err == nil {
			return nil
		}
		var de *deedglass.Error
		if errors.As(err, &de) {
			switch {
			case de.Status == http.StatusBadRequest && len(r.Result) > 0:
				r.Result = nil // too large or not acceptable as a value: the digest alone
				continue
			case de.Status == http.StatusConflict:
				if a, gerr := c.GetAction(ctx, actionID); gerr == nil && a.ResultDigest != nil && *a.ResultDigest == r.ResultDigest {
					return nil
				}
				return fmt.Errorf("%w: %s", ErrNotProxyable, de.Message)
			case de.Status < 500:
				return fmt.Errorf("%w: %s", ErrNotProxyable, de.Message)
			}
		}
		if ctx.Err() != nil || (res != nil && res.Status < 500 && res.Status != 0) {
			return err
		}
	}
	return err
}

// GetAction reads one action (the caller's own).
func (c *Client) GetAction(ctx context.Context, id string) (*ledger.Action, error) {
	actx, cancel := context.WithTimeout(ctx, c.o.SubmitTimeout)
	defer cancel()
	var a ledger.Action
	if _, err := c.api.Do(actx, http.MethodGet, "/v1/actions/"+url.PathEscape(id), nil, &a); err != nil {
		return nil, err
	}
	return &a, nil
}

// Resolve asks which tools the policy always denies (POST /v1/tools/resolve).
func (c *Client) Resolve(ctx context.Context, connector, connectorType string, tools []gate.ToolRef) (*gate.ToolVisibilityList, error) {
	actx, cancel := context.WithTimeout(ctx, c.o.SubmitTimeout)
	defer cancel()
	var out gate.ToolVisibilityList
	_, err := c.api.Do(actx, http.MethodPost, "/v1/tools/resolve",
		gate.ResolveRequest{Connector: connector, ConnectorType: connectorType, Tools: tools}, &out, c.headers()...)
	if err != nil {
		return nil, err
	}
	if out.Object != "list" || len(out.Data) != len(tools) {
		return nil, errors.New("gateclient: the resolve answer doesn't match the request")
	}
	return &out, nil
}

// Whoami is the part of GET /v1/whoami the wrapper prints.
type Whoami struct {
	Org struct {
		ID, Name string
		Stopped  bool
	} `json:"org"`
	Agent *struct {
		ID, Name, Status string
	} `json:"agent"`
	Key struct {
		Mode string `json:"mode"`
	} `json:"key"`
}

// Whoami reads GET /v1/whoami.
func (c *Client) Whoami(ctx context.Context) (*Whoami, error) {
	actx, cancel := context.WithTimeout(ctx, c.o.SubmitTimeout)
	defer cancel()
	var w Whoami
	if _, err := c.api.Do(actx, http.MethodGet, "/v1/whoami", nil, &w); err != nil {
		return nil, err
	}
	return &w, nil
}

// IsAuthError reports a 401 from any call.
func IsAuthError(err error) bool {
	var de *deedglass.Error
	return errors.As(err, &de) && de.Status == http.StatusUnauthorized
}

// clip cuts s to at most n bytes on a UTF-8 boundary.
func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}
