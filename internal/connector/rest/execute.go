package rest

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/deedglass/deedglass/internal/connector"
	"github.com/deedglass/deedglass/internal/connector/egress"
	"github.com/deedglass/deedglass/internal/vault"
)

// Retries (S16 §3.4 step 3): idempotent calls only, at most 2 more attempts, on connect errors,
// 429, 502, 503 and 504, with 200 ms·2ⁿ ±20% backoff, honouring Retry-After up to 5 s, and never
// past the deadline.
const (
	MaxAttempts   = 3
	baseBackoff   = 200 * time.Millisecond
	maxRetryAfter = 5 * time.Second
	maxTextBody   = 64 << 10
)

var rePathValue = regexp.MustCompile(`^[A-Za-z0-9._~-]{1,256}$`)

// built is one tool call turned into an HTTP request (without auth).
type built struct {
	method   string
	templ    string // the configured path, "/v1/orders/{id}"
	path     string // the filled path, under the base URL's path
	query    url.Values
	body     []byte
	headers  http.Header
	host     string
	full     *url.URL
	secrets  []string // sensitive arg values, for the call's redactor
	toolName string
}

func decodeArgs(raw json.RawMessage) (map[string]any, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var m map[string]any
	if err := dec.Decode(&m); err != nil || m == nil {
		return nil, connector.ErrValidation("args", "must be a JSON object")
	}
	return m, nil
}

func scalar(v any) (string, bool) {
	switch x := v.(type) {
	case string:
		return x, true
	case json.Number:
		return x.String(), true
	case bool:
		return strconv.FormatBool(x), true
	}
	return "", false
}

// build fills the path, the query, the body and the header args. Every refusal happens here,
// before anything is sent.
func (st *state) build(t Tool, args map[string]any) (*built, error) {
	b := &built{method: t.Method, templ: t.Path, query: url.Values{}, headers: http.Header{}, toolName: t.Name}
	for name := range args {
		if _, ok := t.Args[name]; !ok {
			return nil, connector.ErrValidation("args."+clip(name), "isn't an argument this tool takes")
		}
	}
	var missing error
	filled := rePlaceholder.ReplaceAllStringFunc(t.Path, func(m string) string {
		name := m[1 : len(m)-1]
		s, ok := scalar(args[name])
		if !ok || !rePathValue.MatchString(s) || s == "." || s == ".." {
			if missing == nil {
				missing = connector.ErrValidation("args."+name, "must be 1 to 256 letters, digits, dots, hyphens, underscores or tildes, and not . or ..")
			}
			return "x"
		}
		return url.PathEscape(s)
	})
	if missing != nil {
		return nil, missing
	}
	if path.Clean(filled) != filled || !underPrefix(filled, st.cfg.AllowedPathPrefixes) {
		return nil, connector.ErrValidation("args", "make a path outside the connector's allowed paths")
	}
	body := map[string]any{}
	names := make([]string, 0, len(t.Args))
	for n := range t.Args {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, name := range names {
		a := t.Args[name]
		v, set := args[name]
		if !set {
			continue
		}
		if a.Sensitive {
			if s, ok := scalar(v); ok {
				b.secrets = append(b.secrets, s)
			} else if raw, err := json.Marshal(v); err == nil {
				b.secrets = append(b.secrets, string(raw))
			}
		}
		switch a.In {
		case "query":
			if arr, ok := v.([]any); ok {
				for _, x := range arr {
					s, ok := scalar(x)
					if !ok {
						return nil, connector.ErrValidation("args."+name, "must hold only strings, numbers or booleans")
					}
					b.query.Add(name, s)
				}
				continue
			}
			s, ok := scalar(v)
			if !ok {
				return nil, connector.ErrValidation("args."+name, "must be a string, a number or a boolean")
			}
			b.query.Set(name, s)
		case "body":
			body[name] = v
		case "header":
			s, ok := scalar(v)
			if !ok || strings.ContainsAny(s, "\r\n\x00") || len(s) > maxHeaderValueBytes || !utf8.ValidString(s) {
				return nil, connector.ErrValidation("args."+name, "must be one line of text, at most 1 KiB")
			}
			b.headers.Set(a.Header, s)
		}
	}
	if len(body) > 0 {
		raw, err := json.Marshal(body)
		if err != nil {
			return nil, connector.ErrValidation("args", "can't be sent as JSON")
		}
		b.body = raw
	}
	full := *st.base
	full.Path = strings.TrimSuffix(st.base.Path, "/") + filled
	full.RawPath = ""
	b.path, b.host, b.full = filled, st.base.Host, &full
	return b, nil
}

// Enrich implements connector.Enricher: request.http = {method, path, host}.
func (c *Connector) Enrich(_ context.Context, inst *connector.Instance, t connector.ToolSpec, raw json.RawMessage) (map[string]any, error) {
	st, err := parse(inst)
	if err != nil {
		return nil, err
	}
	tool, ok := st.tools[t.Name]
	if !ok {
		return nil, connector.ErrValidation("tool", "isn't a tool of this connector")
	}
	args, err := decodeArgs(raw)
	if err != nil {
		return nil, err
	}
	b, err := st.build(tool, args)
	if err != nil {
		return nil, err
	}
	return map[string]any{"http": map[string]string{"method": b.method, "path": strings.TrimSuffix(st.base.Path, "/") + b.path, "host": b.host}}, nil
}

// Execute implements connector.Connector.
func (c *Connector) Execute(ctx context.Context, inst *connector.Instance, call connector.Call) (connector.Result, error) {
	st, err := parse(inst)
	if err != nil {
		return connector.Result{}, &connector.Error{Kind: connector.KindConfig, Code: "connector_misconfigured",
			AgentMessage: "This connector's config can't be read. An admin can check Console › Connectors.", Err: err}
	}
	t, ok := st.tools[call.Tool.Name]
	if !ok {
		return connector.Result{}, &connector.Error{Kind: connector.KindInvalidRequest, Code: "tool_unknown",
			AgentMessage: fmt.Sprintf("The connector %s has no tool named %s.", inst.Name, clip(call.Tool.Name))}
	}
	args, err := decodeArgs(call.Args)
	if err != nil {
		return connector.Result{}, err
	}
	b, err := st.build(t, args)
	if err != nil {
		return connector.Result{}, err
	}
	red := vault.RedactorFrom(ctx)
	red.Add(b.secrets...)
	var sec *vault.Secret
	if st.cfg.Auth.Kind != "none" {
		if sec, err = inst.Secret(ctx); err != nil {
			return connector.Result{}, err
		}
		defer sec.Destroy()
	}
	read, write := st.clients(inst)
	client := read
	if t.sideOf() != connector.SideEffectRead {
		client = write
	}
	return st.do(ctx, client, t, b, sec, call)
}

func (st *state) do(ctx context.Context, client *http.Client, t Tool, b *built, sec *vault.Secret, call connector.Call) (connector.Result, error) {
	deadline, hasDeadline := ctx.Deadline()
	start := time.Now()
	for attempt := 1; ; attempt++ {
		res, retry, err := st.attempt(ctx, client, b, sec, call, attempt)
		res.Latency = time.Since(start)
		if err == nil || !t.idempotent() || !retry || attempt == MaxAttempts {
			return res, err
		}
		wait := backoff(attempt)
		if ce := connector.AsError(err); ce != nil && ce.RetryAfter > 0 {
			if ce.RetryAfter > maxRetryAfter {
				return res, err
			}
			wait = ce.RetryAfter
		}
		if hasDeadline && time.Now().Add(wait).After(deadline) {
			return res, err
		}
		tm := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			tm.Stop()
			return res, err
		case <-tm.C:
		}
	}
}

// unitFloat is a uniform float in [0, 1) from crypto/rand (lint G5: no math/rand).
func unitFloat() float64 {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return 0.5
	}
	return float64(binary.LittleEndian.Uint64(b[:])>>11) / (1 << 53)
}

func backoff(attempt int) time.Duration {
	d := baseBackoff << (attempt - 1)
	j := 0.8 + 0.4*unitFloat() // ±20%
	return time.Duration(float64(d) * j)
}

// attempt sends one try. retry says whether this failure may be retried (for idempotent calls).
func (st *state) attempt(ctx context.Context, client *http.Client, b *built, sec *vault.Secret, call connector.Call, attempt int) (connector.Result, bool, error) {
	u := *b.full
	q := url.Values{}
	for k, vs := range b.query {
		q[k] = append([]string(nil), vs...)
	}
	var body io.Reader
	if b.body != nil {
		body = bytes.NewReader(b.body)
	}
	wrote := false
	trace := &httptrace.ClientTrace{WroteHeaders: func() { wrote = true }}
	rctx := httptrace.WithClientTrace(ctx, trace)
	// Auth on last, from the vault; the query form needs the URL.
	if sec != nil && st.cfg.Auth.Kind == "query" {
		q.Set(st.cfg.Auth.Param, sec.Field("value"))
	}
	u.RawQuery = q.Encode()
	req, err := http.NewRequestWithContext(rctx, b.method, u.String(), body)
	if err != nil {
		return connector.Result{}, false, &connector.Error{Kind: connector.KindConfig, Code: "connector_misconfigured",
			AgentMessage: "This connector's request can't be built. An admin can check Console › Connectors."}
	}
	for k, v := range st.cfg.Headers {
		req.Header.Set(k, v)
	}
	req.Header.Set("Accept", "application/json")
	if b.body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if *st.cfg.SendActionID && !call.ActionID.IsZero() {
		req.Header.Set("Deedglass-Action-Id", call.ActionID.String())
	}
	if hdr := toolIdemHeader(st, b); hdr != "" {
		key := call.IdempotencyKey
		if key == "" {
			key = call.ActionID.String()
		}
		req.Header.Set(hdr, key)
	}
	for k, vs := range b.headers {
		req.Header[k] = vs
	}
	if sec != nil {
		switch st.cfg.Auth.Kind {
		case "bearer":
			req.Header.Set("Authorization", "Bearer "+sec.Field("token"))
		case "header":
			req.Header.Set(st.cfg.Auth.Header, sec.Field("value"))
		case "basic":
			req.SetBasicAuth(sec.Field("username"), sec.Field("password"))
		}
	}
	resp, err := client.Do(req)
	meta := map[string]any{"method": b.method, "path": b.templ, "host": b.host, "attempts": attempt}
	if err != nil {
		cause := err
		var ue *url.Error
		if errors.As(err, &ue) {
			cause = ue.Err // the URL may hold a query credential: never keep it
		}
		if be, ok := egress.IsBlocked(cause); ok {
			return connector.Result{Meta: meta}, false, connector.ErrDestinationBlocked(be)
		}
		var re *egress.ResolveError
		timedOut := errors.Is(cause, context.DeadlineExceeded) || ue != nil && ue.Timeout()
		switch {
		case errors.As(cause, &re):
			return connector.Result{Meta: meta}, true, connector.ErrUnreachable(errors.New(re.Error()))
		case !wrote && timedOut:
			return connector.Result{Meta: meta}, false, &connector.Error{Kind: connector.KindConfig, Code: connector.CodeTimeout,
				AgentMessage: "The service didn't accept a connection in time, so nothing was sent. Try again later.", Err: errors.New(errText(cause))}
		case !wrote:
			return connector.Result{Meta: meta}, ctx.Err() == nil, connector.ErrUnreachable(errors.New(errText(cause)))
		case timedOut:
			return connector.Result{Meta: meta}, false, connector.ErrTimeout(errors.New(errText(cause)))
		case errors.Is(cause, egress.ErrResponseTooLarge):
			return connector.Result{Meta: meta}, false, connector.ErrResponseTooLarge(st.cfg.MaxResponseBytes)
		}
		var re2 *egress.RedirectError
		if errors.As(cause, &re2) {
			// Only read tools follow redirects (writes never do), so a refused redirect changed
			// nothing, and nothing was sent to the new address (S18, attack K02).
			return connector.Result{Meta: meta}, false, &connector.Error{Kind: connector.KindDestinationBlocked, Code: connector.CodeDestinationBlocked,
				AgentMessage: "The service redirected the call to another host. Deedglass follows same-host redirects only, so nothing was sent there.", AdminDetail: re2.Error()}
		}
		return connector.Result{Meta: meta}, false, &connector.Error{Kind: connector.KindUpstream, Code: connector.CodeUpstreamError,
			AgentMessage: "The connection to the service failed while the call was sent. The call may have run.", Err: errors.New(errText(cause))}
	}
	defer resp.Body.Close()
	meta["http_status"] = resp.StatusCode
	var raw []byte
	if b.method != "HEAD" {
		raw, err = egress.ReadBody(resp.Body, st.cfg.MaxResponseBytes)
		if errors.Is(err, egress.ErrResponseTooLarge) {
			return connector.Result{Meta: meta}, false, connector.ErrResponseTooLarge(st.cfg.MaxResponseBytes)
		}
		if err != nil {
			if errors.Is(err, context.DeadlineExceeded) || ctx.Err() != nil {
				return connector.Result{Meta: meta}, false, connector.ErrTimeout(errors.New(errText(err)))
			}
			return connector.Result{Meta: meta}, false, &connector.Error{Kind: connector.KindUpstream, Code: connector.CodeUpstreamError,
				AgentMessage: "The service's answer was cut off. The call may have run.", Err: errors.New(errText(err))}
		}
	}
	out, _ := json.Marshal(output(resp, raw))
	res := connector.Result{Output: out, Meta: meta}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return res, false, nil
	}
	ra := parseRetryAfter(resp.Header.Get("Retry-After"))
	e := connector.FromStatus(resp.StatusCode, ra)
	retry := resp.StatusCode == 429 || resp.StatusCode == 502 || resp.StatusCode == 503 || resp.StatusCode == 504
	return res, retry, e
}

func toolIdemHeader(st *state, b *built) string {
	t := st.tools[b.toolName]
	return t.IdempotencyHeader
}

func errText(err error) string {
	s := err.Error()
	if len(s) > 300 {
		s = s[:300]
	}
	return s
}

func parseRetryAfter(v string) time.Duration {
	if v == "" {
		return 0
	}
	if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && n >= 0 {
		return time.Duration(n) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil {
		if d := time.Until(t); d > 0 {
			return d
		}
	}
	return 0
}

// allowedHeaders pass to the output; Set-Cookie never does.
var allowedHeaders = []string{"content-type", "etag", "last-modified", "location", "request-id", "x-request-id", "retry-after"}

// output is {"status":200,"headers":{…},"body":…}: JSON as is, text up to 64 KiB as {"text":…},
// anything else as {"digest":"sha256:…","bytes":N}.
func output(resp *http.Response, raw []byte) map[string]any {
	hs := map[string]string{}
	for k, vs := range resp.Header {
		l := strings.ToLower(k)
		if contains(allowedHeaders, l) || strings.HasPrefix(l, "x-ratelimit-") {
			hs[l] = strings.Join(vs, ", ")
		}
	}
	out := map[string]any{"status": resp.StatusCode, "headers": hs}
	switch {
	case len(raw) == 0:
		out["body"] = nil
	case isJSON(resp.Header.Get("Content-Type")) && json.Valid(raw):
		out["body"] = json.RawMessage(raw)
	case len(raw) <= maxTextBody && utf8.Valid(raw):
		out["body"] = map[string]any{"text": string(raw)}
	default:
		sum := sha256.Sum256(raw)
		out["body"] = map[string]any{"digest": "sha256:" + hex.EncodeToString(sum[:]), "bytes": len(raw)}
	}
	return out
}

func isJSON(ct string) bool {
	mt, _, err := mime.ParseMediaType(ct)
	if err != nil {
		return false
	}
	return mt == "application/json" || strings.HasSuffix(mt, "+json")
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

func bytesReader(b []byte) io.Reader { return bytes.NewReader(b) }
