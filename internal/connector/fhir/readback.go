package fhir

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"net/url"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/deedglass/deedglass/internal/connector"
	"github.com/deedglass/deedglass/internal/connector/saas"
)

// Witness reason codes this connector's findings name (the Witness catalog holds their class and
// sentence).
const (
	ReasonConditionalMatched = "fhir_conditional_matched"
	ReasonNoNewVersion       = "fhir_no_new_version"
	ReasonFieldChanged       = "fhir_field_changed"
	ReasonHistoryNoMatch     = "fhir_history_no_match"
)

// MaxDiffs is the most changed fields one read-back reports.
const MaxDiffs = 32

// CanReadBackUnknown says the connector looks for a write after an unknown outcome (a 5xx or a
// timeout): the history since the call, for a version carrying the action's token.
func (c *Connector) CanReadBackUnknown(t connector.ToolSpec) bool { return t.L1 != "" }

// NativeReadBack implements connector.NativeReadBacker (ADR-105 §6): it reads back exactly the
// version the server reported and compares it with what the agent sent, after normalising what a
// server adds on its own. A missing version, a 5xx on the read or an unknown version is a gap,
// never a mismatch.
func (c *Connector) NativeReadBack(ctx context.Context, inst *connector.Instance, call connector.Call, res connector.Result) (connector.Observation, []connector.L1Finding, error) {
	st, err := parse(inst)
	if err != nil {
		return connector.Observation{}, nil, misconfigured(err)
	}
	r, err := st.build(call.Tool.Name, call.Args)
	if err != nil {
		return connector.Observation{}, nil, err
	}
	if !r.write() {
		return connector.Observation{}, nil, &connector.ErrReadBackSkipped{Why: "a read changes nothing"}
	}
	cl, done, err := c.client(ctx, inst, st, false)
	if err != nil {
		return connector.Observation{}, nil, err
	}
	defer done()
	ev := res.Evidence
	status, _ := strconv.Atoi(ev["fhir_status"])
	id, vid := ev["fhir_id"], ev["fhir_version"]
	if id == "" {
		id = r.id
	}
	obs := connector.Observation{Target: r.target()}
	switch {
	case status < 200 || status > 299:
		return c.fromHistory(ctx, cl, st, r, call, obs)
	case r.inter == "delete":
		resp, err := cl.Do(ctx, saas.Request{Path: mustPath(r.typ, r.id), Idempotent: true})
		if resp != nil {
			obs.Status = resp.Status
		}
		if ce := connector.AsError(err); ce != nil && ce.Kind != connector.KindNotFound {
			return obs, nil, err
		}
		gone := obs.Status == 410 || obs.Status == 404
		return obs, []connector.L1Finding{{Field: "read after delete", Expected: "410 Gone", Observed: strconv.Itoa(obs.Status), Match: gone}}, nil
	case r.inter == "create" && ev["fhir_conditional"] == "true" && status == 200:
		obs.Target = r.typ + "/" + id
		return obs, []connector.L1Finding{{Field: "created", Expected: "a new " + r.typ, Observed: obs.Target + " version " + vid,
			Code: ReasonConditionalMatched}}, nil
	case vid == "" || !reID.MatchString(id):
		return obs, nil, &connector.Error{Kind: connector.KindUpstream, Code: "fhir_version_unknown",
			AgentMessage: "The server's answer named no version to read back."}
	}
	obs.Target = r.typ + "/" + id + " version " + vid
	resp, err := cl.Do(ctx, saas.Request{Path: mustPath(r.typ, id, "_history", vid), Idempotent: true})
	if resp != nil {
		obs.Status, obs.LatencyMS = resp.Status, resp.Latency.Milliseconds()
	}
	if err != nil {
		return obs, nil, err
	}
	got, err := decodeResource(resp.Body, r.typ)
	if err != nil {
		return obs, nil, err
	}
	fs := c.compare(st, r, got)
	if prior := ev["fhir_prior_version"]; prior != "" && prior == vid && (r.inter == "update" || r.inter == "patch") {
		fs = append(fs, connector.L1Finding{Field: "meta.versionId", Expected: "a new version", Observed: vid, Match: true, Code: ReasonNoNewVersion})
	}
	return obs, fs, nil
}

// fromHistory looks for the write after an unknown outcome: the resource's history (or the type's,
// for a create) since one second before the action started, for exactly one version whose
// meta.source carries the action's token (HAPI stores it as <uri>#<request id> when it keeps
// request ids). Anything else is the gap fhir_history_no_match.
func (c *Connector) fromHistory(ctx context.Context, cl *saas.Client, st *state, r *req, call connector.Call, obs connector.Observation) (connector.Observation, []connector.L1Finding, error) {
	tok := token(call.ActionID)
	parts := []string{r.typ, "_history"}
	if r.id != "" {
		parts = []string{r.typ, r.id, "_history"}
	}
	since := call.ActionID.Time().Add(-1e9).UTC().Format("2006-01-02T15:04:05Z")
	resp, err := cl.Do(ctx, saas.Request{Path: mustPath(parts...), Query: url.Values{"_since": {since}, "_count": {"100"}}, Idempotent: true})
	if resp != nil {
		obs.Status = resp.Status
	}
	if err != nil {
		return obs, nil, err
	}
	var b struct {
		Entry []struct {
			Resource json.RawMessage `json:"resource"`
		} `json:"entry"`
	}
	_ = json.Unmarshal(resp.Body, &b)
	var match []map[string]any
	for _, e := range b.Entry {
		m, err := decodeResource(e.Resource, r.typ)
		if err != nil {
			continue
		}
		meta, _ := m["meta"].(map[string]any)
		if src, _ := meta["source"].(string); tok != "" && strings.HasSuffix(src, "#"+tok) {
			match = append(match, m)
		}
	}
	if len(match) != 1 || r.inter == "delete" {
		return obs, []connector.L1Finding{{Field: "history", Expected: "one version carrying the action's token", Observed: fmt.Sprintf("%d", len(match)),
			Code: ReasonHistoryNoMatch}}, nil
	}
	id, _ := match[0]["id"].(string)
	meta, _ := match[0]["meta"].(map[string]any)
	vid, _ := meta["versionId"].(string)
	obs.Target = r.typ + "/" + id + " version " + vid
	return obs, c.compare(st, r, match[0]), nil
}

func decodeResource(b []byte, typ string) (map[string]any, error) {
	var m map[string]any
	if err := decodeJSON(b, &m); err != nil || m["resourceType"] != typ {
		return nil, &connector.Error{Kind: connector.KindUpstream, Code: connector.CodeUpstreamError,
			AgentMessage: "The read-back's answer isn't a " + typ + " resource."}
	}
	return m, nil
}

func decodeJSON(b []byte, v any) error {
	r := &saas.Response{Body: b}
	return r.JSON(v)
}

// normalise drops what a server writes on its own: meta.versionId, meta.lastUpdated, meta.source,
// the meta.tag and meta.security codings of the configured server systems that the agent didn't
// send, a generated narrative, and (create) the server-assigned id.
func normalise(m, sent map[string]any, servers []string, create bool) {
	if create {
		delete(m, "id")
	}
	if t, _ := m["text"].(map[string]any); t != nil && t["status"] == "generated" {
		delete(m, "text")
	}
	meta, _ := m["meta"].(map[string]any)
	if meta == nil {
		return
	}
	for _, k := range []string{"versionId", "lastUpdated", "source"} {
		delete(meta, k)
	}
	var sentMeta map[string]any
	if sent != nil {
		sentMeta, _ = sent["meta"].(map[string]any)
	}
	for _, k := range []string{"tag", "security"} {
		list, _ := meta[k].([]any)
		var keep []any
		for _, e := range list {
			c, _ := e.(map[string]any)
			sys, _ := c["system"].(string)
			if slices.Contains(servers, sys) && !containsJSON(sentMeta[k], e) {
				continue
			}
			keep = append(keep, e)
		}
		if len(keep) == 0 {
			delete(meta, k)
		} else {
			meta[k] = keep
		}
	}
	if len(meta) == 0 {
		delete(m, "meta")
	}
}

func containsJSON(list, v any) bool {
	l, _ := list.([]any)
	for _, e := range l {
		if equalJSON(e, v) {
			return true
		}
	}
	return false
}

// compare checks the version read back against what the call sent. Fields only the server has are
// allowed (server_added); a field the agent set with another value, or missing, is
// fhir_field_changed with its path.
func (c *Connector) compare(st *state, r *req, got map[string]any) []connector.L1Finding {
	if r.inter == "patch" {
		return patchFindings(r.patch, got)
	}
	var sent map[string]any
	b, _ := json.Marshal(r.resource)
	_ = decodeJSON(b, &sent)
	normalise(got, sent, st.cfg.ServerTagSystems, r.inter == "create")
	normalise(sent, nil, nil, r.inter == "create")
	var fs []connector.L1Finding
	diff("", sent, got, &fs)
	if len(fs) > 0 {
		return fs
	}
	keys := make([]string, 0, len(sent))
	for k := range sent {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fs = append(fs, connector.L1Finding{Field: k, Match: true})
	}
	return fs
}

// diff walks what the agent sent: objects field by field, anything else as a whole JSON value.
func diff(path string, exp, got any, fs *[]connector.L1Finding) {
	em, eok := exp.(map[string]any)
	gm, gok := got.(map[string]any)
	if eok && gok {
		keys := make([]string, 0, len(em))
		for k := range em {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			p := k
			if path != "" {
				p = path + "." + k
			}
			diff(p, em[k], gm[k], fs)
		}
		return
	}
	if !equalJSON(exp, got) && len(*fs) < MaxDiffs {
		*fs = append(*fs, connector.L1Finding{Field: path, Expected: value(exp), Observed: value(got), Code: ReasonFieldChanged})
	}
}

// patchFindings checks a JSON Patch's add, replace and remove paths on the version read back.
func patchFindings(ops []any, got map[string]any) []connector.L1Finding {
	fs := []connector.L1Finding{{Field: "version", Match: true}}
	for _, o := range ops {
		m, _ := o.(map[string]any)
		op, _ := m["op"].(string)
		p, _ := m["path"].(string)
		v, found := pointer(got, p)
		field := strings.ReplaceAll(strings.TrimPrefix(p, "/"), "/", ".")
		switch op {
		case "add", "replace":
			if strings.HasSuffix(p, "/-") {
				continue // appended to an array: its index isn't known
			}
			if !equalJSON(m["value"], v) {
				fs = append(fs, connector.L1Finding{Field: field, Expected: value(m["value"]), Observed: value(v), Code: ReasonFieldChanged})
			}
		case "remove":
			if found {
				fs = append(fs, connector.L1Finding{Field: field, Expected: nil, Observed: value(v), Code: ReasonFieldChanged})
			}
		}
	}
	return fs
}

// pointer resolves an RFC 6901 JSON Pointer.
func pointer(doc any, p string) (any, bool) {
	if p == "" {
		return doc, true
	}
	cur := doc
	for _, tok := range strings.Split(strings.TrimPrefix(p, "/"), "/") {
		tok = strings.ReplaceAll(strings.ReplaceAll(tok, "~1", "/"), "~0", "~")
		switch x := cur.(type) {
		case map[string]any:
			v, ok := x[tok]
			if !ok {
				return nil, false
			}
			cur = v
		case []any:
			i, err := strconv.Atoi(tok)
			if err != nil || i < 0 || i >= len(x) {
				return nil, false
			}
			cur = x[i]
		default:
			return nil, false
		}
	}
	return cur, true
}

// equalJSON compares two decoded JSON values; numbers by value (72 equals 72.0).
func equalJSON(a, b any) bool {
	switch x := a.(type) {
	case json.Number:
		y, ok := b.(json.Number)
		if !ok {
			return false
		}
		fx, okx := new(big.Float).SetString(x.String())
		fy, oky := new(big.Float).SetString(y.String())
		return okx && oky && fx.Cmp(fy) == 0
	case map[string]any:
		y, ok := b.(map[string]any)
		if !ok || len(x) != len(y) {
			return false
		}
		for k, v := range x {
			if w, ok := y[k]; !ok || !equalJSON(v, w) {
				return false
			}
		}
		return true
	case []any:
		y, ok := b.([]any)
		if !ok || len(x) != len(y) {
			return false
		}
		for i := range x {
			if !equalJSON(x[i], y[i]) {
				return false
			}
		}
		return true
	}
	return a == b
}

// value is how a finding shows a value: short strings and scalars as they are; anything longer
// than 256 bytes as JSON becomes "sha256:<hex>".
func value(v any) any {
	b, _ := json.Marshal(v)
	if len(b) <= connector.MaxFindingRunes {
		return v
	}
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:])
}
