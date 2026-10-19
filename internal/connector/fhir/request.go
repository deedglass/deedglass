package fhir

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"mime"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/deedglass/deedglass/internal/connector"
	"github.com/deedglass/deedglass/internal/connector/egress"
	"github.com/deedglass/deedglass/internal/connector/saas"
	"github.com/deedglass/deedglass/internal/ids"
)

var (
	reParam   = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_.:\-]{0,63}$`)
	rePatchOp = []string{"add", "remove", "replace", "move", "copy", "test"}
	historyQ  = []string{"_since", "_at", "_count"}
	reWord    = regexp.MustCompile(`^[a-z][a-z-]{0,31}$`)
)

// MaxParams is the most search parameters one call may send.
const MaxParams = 32

// req is one tool call turned into a FHIR request (without auth). Every refusal happens while it is
// built, before anything is sent.
type req struct {
	inter, typ, id, vid  string
	method, path         string
	query                url.Values
	body                 json.RawMessage
	ctype                string
	ifMatch, ifNoneExist string
	resource             map[string]any // create and update: the resource as sent
	patch                []any          // patch: JSON Patch operations
}

func (r *req) write() bool { return r.method != http.MethodGet }

// target names what the call reaches: "Observation/42", "Observation".
func (r *req) target() string {
	if r.id == "" {
		return r.typ
	}
	return r.typ + "/" + r.id
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

func line(v any, max int) (string, bool) {
	s, ok := v.(string)
	return s, ok && s != "" && len(s) <= max && utf8.ValidString(s) && !strings.ContainsAny(s, "\r\n\x00")
}

// build turns a tool call into a request: the type must be one of the allowed types, the id and
// version ids FHIR ids, and the path is made only of those (saas.Segments escapes each).
func (st *state) build(name string, raw json.RawMessage) (*req, error) {
	spec, ok := st.tools[name]
	if !ok {
		return nil, &connector.Error{Kind: connector.KindInvalidRequest, Code: "tool_unknown",
			AgentMessage: fmt.Sprintf("This connector has no tool named %s. Its tools are the interactions in allowed_interactions.", clip(name, 64))}
	}
	args, err := decodeArgs(raw)
	if err != nil {
		return nil, err
	}
	var allowed struct {
		Properties map[string]json.RawMessage `json:"properties"`
		Required   []string                   `json:"required"`
	}
	_ = json.Unmarshal(spec.InputSchema, &allowed)
	for k := range args {
		if _, ok := allowed.Properties[k]; !ok {
			return nil, connector.ErrValidation("args."+clip(k, 64), "isn't an argument this tool takes")
		}
	}
	for _, k := range allowed.Required {
		if _, ok := args[k]; !ok {
			return nil, connector.ErrValidation("args."+k, "is required")
		}
	}
	r := &req{inter: name, method: http.MethodGet, query: url.Values{}}
	r.typ, _ = args["resource_type"].(string)
	if !slices.Contains(st.cfg.AllowedResourceTypes, r.typ) {
		return nil, connector.ErrValidation("args.resource_type", "isn't one of this connector's allowed_resource_types ("+strings.Join(st.cfg.AllowedResourceTypes, ", ")+")")
	}
	for _, k := range []string{"id", "version_id", "if_match"} {
		v, set := args[k]
		if !set {
			continue
		}
		s, ok := v.(string)
		if !ok || !reID.MatchString(s) || s == "." || s == ".." {
			return nil, connector.ErrValidation("args."+k, "must be a FHIR id: 1 to 64 letters, digits, hyphens or dots")
		}
		switch k {
		case "id":
			r.id = s
		case "version_id":
			r.vid = s
		default:
			r.ifMatch = s
		}
	}
	if (name == "update" || name == "patch") && st.cfg.IfMatch == "required" && r.ifMatch == "" {
		return nil, connector.ErrValidation("args.if_match", "is required by this connector (if_match: required): send the version id you read")
	}
	switch name {
	case "search", "history":
		if err := r.params(args["params"], name == "history"); err != nil {
			return nil, err
		}
	case "create", "update":
		res, ok := args["resource"].(map[string]any)
		if !ok {
			return nil, connector.ErrValidation("args.resource", "must be a FHIR resource as a JSON object")
		}
		if rt, _ := res["resourceType"].(string); rt != r.typ {
			return nil, connector.ErrValidation("args.resource.resourceType", "must equal resource_type ("+r.typ+")")
		}
		if name == "update" {
			if id, set := res["id"]; set && id != r.id {
				return nil, connector.ErrValidation("args.resource.id", "must equal id ("+r.id+") when it is set")
			}
			res["id"] = r.id
			r.method = http.MethodPut
		} else {
			r.method = http.MethodPost
			if v, set := args["if_none_exist"]; set {
				s, ok := line(v, 1024)
				q, err := url.ParseQuery(s)
				if !ok || err != nil || len(q) == 0 {
					return nil, connector.ErrValidation("args.if_none_exist", "must be search parameters on one line, like identifier=http://example.org/mrn|12345")
				}
				r.ifNoneExist = s
			}
		}
		r.resource = res
		r.body, _ = json.Marshal(res)
		r.ctype = "application/fhir+json"
	case "patch":
		r.method = http.MethodPatch
		jp, hasJP := args["json_patch"]
		fp, hasFP := args["fhirpath_patch"]
		switch {
		case hasJP == hasFP:
			return nil, connector.ErrValidation("args", "need exactly one of json_patch and fhirpath_patch")
		case hasJP:
			ops, ok := jp.([]any)
			if !ok || len(ops) == 0 || len(ops) > 64 {
				return nil, connector.ErrValidation("args.json_patch", "must be 1 to 64 JSON Patch operations")
			}
			for i, o := range ops {
				m, _ := o.(map[string]any)
				op, _ := m["op"].(string)
				p, _ := m["path"].(string)
				if !slices.Contains(rePatchOp, op) || !strings.HasPrefix(p, "/") {
					return nil, connector.ErrValidation(fmt.Sprintf("args.json_patch[%d]", i), "must have an op (add, remove, replace, move, copy or test) and a path starting with /")
				}
			}
			r.patch = ops
			r.body, _ = json.Marshal(ops)
			r.ctype = "application/json-patch+json"
		default:
			m, ok := fp.(map[string]any)
			if !ok || m["resourceType"] != "Parameters" {
				return nil, connector.ErrValidation("args.fhirpath_patch", "must be a Parameters resource")
			}
			r.body, _ = json.Marshal(m)
			r.ctype = "application/fhir+json"
		}
	case "delete":
		r.method = http.MethodDelete
	}
	parts := []string{r.typ}
	switch {
	case r.vid != "":
		parts = append(parts, r.id, "_history", r.vid)
	case name == "history" && r.id != "":
		parts = append(parts, r.id, "_history")
	case name == "history":
		parts = append(parts, "_history")
	case r.id != "":
		parts = append(parts, r.id)
	}
	if r.path, err = saas.Segments(parts...); err != nil {
		return nil, connector.ErrValidation("args", "make a path this connector can't send")
	}
	return r, nil
}

// params fills the query from search parameters: names like code, subject.name or code:text, values
// strings or arrays of strings. history takes only _since, _at and _count.
func (r *req) params(v any, history bool) error {
	if v == nil {
		return nil
	}
	m, ok := v.(map[string]any)
	if !ok || len(m) > MaxParams {
		return connector.ErrValidation("args.params", fmt.Sprintf("must be an object of at most %d search parameters", MaxParams))
	}
	for k, val := range m {
		p := "args.params." + clip(k, 64)
		if !reParam.MatchString(k) || history && !slices.Contains(historyQ, k) {
			return connector.ErrValidation(p, "isn't a search parameter this tool sends")
		}
		vals, isArr := val.([]any)
		if !isArr {
			vals = []any{val}
		}
		for _, x := range vals {
			s, ok := line(x, 1024)
			if !ok {
				return connector.ErrValidation(p, "must be a string or an array of strings, each one line")
			}
			r.query.Add(k, s)
		}
	}
	return nil
}

// token is the action's binding token: the last 16 characters of its id, the random part of the
// ULID (HC-P05: HAPI keeps only 16 characters of X-Request-ID).
func token(a ids.ActionID) string {
	s := a.String()
	if len(s) < 16 {
		return ""
	}
	return s[len(s)-16:]
}

func (r *req) send(st *state, call connector.Call) saas.Request {
	h := http.Header{}
	if r.write() {
		h.Set("Prefer", "return=representation")
		if !call.ActionID.IsZero() {
			h.Set("Deedglass-Action-Id", call.ActionID.String())
			if st.cfg.RequestIDHeader != "none" {
				h.Set(st.cfg.RequestIDHeader, token(call.ActionID))
			}
		}
	}
	if r.ifMatch != "" {
		h.Set("If-Match", `W/"`+r.ifMatch+`"`)
	}
	if r.ifNoneExist != "" {
		h.Set("If-None-Exist", r.ifNoneExist)
	}
	out := saas.Request{Method: r.method, Path: r.path, Query: r.query, Header: h, Idempotent: !r.write(), ContentType: r.ctype}
	if r.body != nil {
		out.JSON = r.body
	}
	return out
}

// classify maps a FHIR server's answer (ADR-105 §3): 409 and 412 are a version conflict (failed,
// never retried); 404 and 410 not found; 400 and 422 keep the OperationOutcome's issue codes; the
// rest as connector.FromStatus (5xx: the call may have run).
func classify(resp *saas.Response) *connector.Error {
	if resp.Status < 300 {
		return nil
	}
	switch resp.Status {
	case 409, 412:
		return &connector.Error{Kind: connector.KindInvalidRequest, Code: CodeVersionConflict, HTTPStatus: resp.Status,
			AgentMessage: fmt.Sprintf("The server refused the write because the resource changed since the version you sent (HTTP %d). Nothing was written. Read it again and send the new version id.", resp.Status)}
	case 410:
		return &connector.Error{Kind: connector.KindNotFound, Code: connector.CodeUpstreamNotFound, HTTPStatus: 410,
			AgentMessage: "The resource was deleted (HTTP 410). Check the id."}
	}
	e := connector.FromStatus(resp.Status, resp.RetryAfter(0))
	if codes := issueCodes(resp.Body); codes != "" && resp.Status < 500 {
		e.AgentMessage = fmt.Sprintf("The FHIR server refused the request (HTTP %d, %s). Check the arguments.", resp.Status, codes)
	}
	return e
}

// CodeVersionConflict is a stale If-Match (409 or 412).
const CodeVersionConflict = "fhir_version_conflict"

// issueCodes lists an OperationOutcome's issue severities and codes ("error invalid, error
// required"), never its diagnostics, which may echo clinical values.
func issueCodes(body []byte) string {
	var oo struct {
		ResourceType string `json:"resourceType"`
		Issue        []struct {
			Severity, Code string
		} `json:"issue"`
	}
	if json.Unmarshal(body, &oo) != nil || oo.ResourceType != "OperationOutcome" {
		return ""
	}
	var out []string
	for i, is := range oo.Issue {
		if i < 4 && reWord.MatchString(is.Severity) && reWord.MatchString(is.Code) {
			out = append(out, is.Severity+" "+is.Code)
		}
	}
	return strings.Join(out, ", ")
}

func (st *state) clients(inst *connector.Instance) (read, write *http.Client) {
	st.clientOnce.Do(func() {
		pol := inst.Env.Egress
		if pol == nil {
			pol = &egress.Policy{Env: egress.EnvProd}
		}
		o := egress.ClientOptions{Timeout: msDur(st.cfg.TimeoutMS), MaxResponseBytes: st.cfg.MaxResponseBytes, Redirects: egress.Redirects(st.cfg.Redirects)}
		st.read = pol.Client(o)
		o.Redirects = egress.RedirectNone
		st.write = pol.Client(o)
	})
	return st.read, st.write
}

// client is one call's FHIR client, authenticated. done releases the credential.
func (c *Connector) client(ctx context.Context, inst *connector.Instance, st *state, write bool) (cl *saas.Client, done func(), err error) {
	read, wr := st.clients(inst)
	cl = &saas.Client{Base: st.base, HTTP: read, Headers: http.Header{"Accept": {"application/fhir+json"}}, Classify: classify, MaxBody: st.cfg.MaxResponseBytes}
	if write {
		cl.HTTP = wr
	}
	cl.Auth, done, err = c.auth(ctx, inst, st)
	return cl, done, err
}

// Execute implements connector.Connector.
func (c *Connector) Execute(ctx context.Context, inst *connector.Instance, call connector.Call) (connector.Result, error) {
	st, err := parse(inst)
	if err != nil {
		return connector.Result{}, misconfigured(err)
	}
	r, err := st.build(call.Tool.Name, call.Args)
	if err != nil {
		return connector.Result{}, err
	}
	cl, done, err := c.client(ctx, inst, st, r.write())
	if err != nil {
		return connector.Result{}, err
	}
	defer done()
	prior := r.ifMatch
	if prior == "" && (r.inter == "update" || r.inter == "patch") {
		// The version before the write, so an update the server keeps at the same version is
		// seen (HAPI suppresses no-change updates). Best effort: a failed read leaves it unknown.
		pc := *cl
		if read, _ := st.clients(inst); read != nil {
			pc.HTTP = read
		}
		if resp, err := pc.Do(ctx, saas.Request{Method: http.MethodGet, Path: mustPath(r.typ, r.id), Idempotent: true}); err == nil {
			_, prior = versionOf(resp, r)
		}
	}
	resp, err := cl.Do(ctx, r.send(st, call))
	clientID := ""
	if st.cfg.Auth.Kind == AuthSMART {
		clientID, _ = st.clientID.Load().(string)
	}
	res := result(resp, r, clientID)
	if res.Evidence != nil && prior != "" {
		res.Evidence["fhir_prior_version"] = prior
	}
	if !call.ActionID.IsZero() && st.cfg.RequestIDHeader != "none" {
		versionEvidence(res.Evidence, r, prior, token(call.ActionID))
	} else {
		versionEvidence(res.Evidence, r, prior, "")
	}
	if resp != nil && resp.Status == 401 && st.cfg.Auth.Kind == AuthSMART {
		c.forget(st)
	}
	return res, err
}

func mustPath(parts ...string) string {
	p, _ := saas.Segments(parts...)
	return p
}

// versionOf reads the id and version the server reported: Location …/<Type>/<id>/_history/<vid>,
// then ETag W/"<vid>", then the body's id and meta.versionId.
func versionOf(resp *saas.Response, r *req) (id, vid string) {
	if resp == nil {
		return "", ""
	}
	loc := resp.Header.Get("Location")
	if loc == "" && r.inter == "delete" {
		loc = resp.Header.Get("Content-Location") // HAPI names the deleted version here
	}
	if loc != "" {
		if u, err := url.Parse(loc); err == nil {
			seg := strings.Split(strings.Trim(u.Path, "/"), "/")
			for i := len(seg) - 4; i >= 0; i-- {
				if seg[i] == r.typ && seg[i+2] == "_history" && reID.MatchString(seg[i+1]) && reID.MatchString(seg[i+3]) {
					id, vid = seg[i+1], seg[i+3]
					break
				}
			}
		}
	}
	if vid == "" {
		et := strings.TrimPrefix(resp.Header.Get("ETag"), "W/")
		if v, err := strconv.Unquote(et); err == nil && reID.MatchString(v) {
			vid = v
		}
	}
	var body struct {
		ResourceType string `json:"resourceType"`
		ID           string `json:"id"`
		Meta         struct {
			VersionID string `json:"versionId"`
		} `json:"meta"`
	}
	if json.Unmarshal(resp.Body, &body) == nil && body.ResourceType == r.typ {
		if id == "" && reID.MatchString(body.ID) {
			id = body.ID
		}
		if vid == "" && reID.MatchString(body.Meta.VersionID) {
			vid = body.Meta.VersionID
		}
	}
	if id == "" {
		id = r.id
	}
	return id, vid
}

var keptHeaders = []string{"content-type", "etag", "last-modified", "location", "x-request-id", "retry-after"}

// result is the tool's output, {"status", "headers", "body"} as the REST connector shapes it, plus
// "client_id" (the SMART client the FHIR server saw, when auth.kind is smart_backend; the FHIR
// AuditEvent export names it), and the evidence the read-back needs (the witness only).
func result(resp *saas.Response, r *req, clientID string) connector.Result {
	ev := map[string]string{"fhir_interaction": r.inter}
	if r.ifNoneExist != "" {
		ev["fhir_conditional"] = "true"
	}
	res := connector.Result{Meta: map[string]any{"interaction": r.inter, "resource_type": r.typ}, Evidence: ev}
	if !r.write() {
		res.Evidence = nil
	}
	if resp == nil {
		return res
	}
	res.Meta["http_status"], res.Meta["attempts"], res.Latency = resp.Status, resp.Attempts, resp.Latency
	if res.Evidence != nil {
		ev["fhir_status"] = strconv.Itoa(resp.Status)
		if id, vid := versionOf(resp, r); resp.Status < 300 {
			ev["fhir_id"], ev["fhir_version"] = id, vid
		}
	}
	hs := map[string]string{}
	for _, k := range keptHeaders {
		if v := resp.Header.Get(k); v != "" {
			hs[k] = clip(v, 1024)
		}
	}
	out := map[string]any{"status": resp.Status, "headers": hs, "body": nil}
	if clientID != "" {
		out["client_id"] = clip(clientID, 256)
	}
	mt, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	switch {
	case len(resp.Body) == 0:
	case (mt == "application/json" || strings.HasSuffix(mt, "+json")) && json.Valid(resp.Body):
		out["body"] = json.RawMessage(resp.Body)
	default:
		sum := sha256.Sum256(resp.Body)
		out["body"] = map[string]any{"digest": "sha256:" + hex.EncodeToString(sum[:]), "bytes": len(resp.Body)}
	}
	res.Output, _ = json.Marshal(out)
	return res
}

// Enrich implements connector.Enricher: request.fhir. Values that don't build a request add
// nothing, so a rule that allows on request.fhir doesn't match them.
func (c *Connector) Enrich(_ context.Context, inst *connector.Instance, t connector.ToolSpec, raw json.RawMessage) (map[string]any, error) {
	st, err := parse(inst)
	if err != nil {
		return nil, err
	}
	r, err := st.build(t.Name, raw)
	if err != nil {
		return nil, err
	}
	f := map[string]any{"interaction": r.inter, "resource_type": r.typ, "id": r.id, "conditional": r.ifNoneExist != "",
		"if_match": r.ifMatch != "", "codes": codes(r.resource), "profiles": profiles(r.resource), "enriched": true}
	return map[string]any{"fhir": f}, nil
}

// codes are the system|code pairs of the resource's code and category, never free text.
func codes(res map[string]any) []string {
	out := []string{}
	var add func(v any)
	add = func(v any) {
		switch x := v.(type) {
		case []any:
			for _, e := range x {
				add(e)
			}
		case map[string]any:
			cs, _ := x["coding"].([]any)
			for _, c := range cs {
				m, _ := c.(map[string]any)
				sys, _ := m["system"].(string)
				code, _ := m["code"].(string)
				if code != "" && len(out) < 32 && len(sys)+len(code) < 512 {
					out = append(out, sys+"|"+code)
				}
			}
		}
	}
	add(res["code"])
	add(res["category"])
	return out
}

func profiles(res map[string]any) []string {
	out := []string{}
	meta, _ := res["meta"].(map[string]any)
	ps, _ := meta["profile"].([]any)
	for _, p := range ps {
		if s, ok := line(p, 512); ok && len(out) < 16 {
			out = append(out, s)
		}
	}
	return out
}

// PHIKeep implements connector.PHIKeeper (ADR-105 §8): the type, ids and versions are kept; resources,
// bundles, search parameters and codes become digests.
func (c *Connector) PHIKeep(*connector.Instance, connector.ToolSpec) connector.PHIKeep {
	return connector.PHIKeep{Args: []string{"id", "resource_type", "version_id"},
		Result: []string{"status", "client_id", "headers.etag", "headers.location", "headers.last-modified", "body.resourceType", "body.id", "body.meta.versionId"}}
}
