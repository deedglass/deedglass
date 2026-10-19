package deed

import (
	"fmt"
	"sort"
)

// The closed schema, read from a strict-parsed tree (map[string]any, []any, string, int64, bool,
// nil). The standard library decoders are not used for verification: encoding/json matches keys
// case-insensitively and ignores missing ones, and either would let a deed say more than its
// schema allows.

// Key lists, in spec order. Missing keys are reported in this order; unknown keys alphabetically.
var (
	envelopeKeys   = []string{"object", "id", "body", "body_hash", "seal", "seal_key_id"}
	envelopeKeysV2 = []string{"object", "id", "body", "body_hash", "seals"}
	bodyKeys       = []string{"v", "kind", "deed_id", "org", "seq", "prev_hash", "issued_at", "agent", "on_behalf_of", "session", "action", "policy", "approval", "execution", "witness", "cost", "admin", "amends", "seal_key_id"}
	bodyKeysV2     = []string{"v", "kind", "deed_id", "org", "seq", "prev_hash", "issued_at", "agent", "on_behalf_of", "session", "action", "policy", "approval", "execution", "witness", "cost", "admin", "amends", "seal_keys"}
	sealKeyRefKeys = []string{"alg", "id"}
	sealValueKeys  = []string{"alg", "seal", "seal_key_id"}
	agentKeys      = []string{"id", "name"}
	actionKeys     = []string{"id", "tool", "connector", "mode", "request_digest", "intent_hash", "intent_signed"}
	policyKeys     = []string{"id", "version_id", "version_hash", "mode", "decision", "rule_id"}
	approvalKeys   = []string{"id", "outcome", "decided_by", "decided_at", "deed"}
	executionKeys  = []string{"status", "started_at", "finished_at", "latency_ms", "result_digest", "error_code"}
	witnessKeys    = []string{"level", "verdict", "method", "detail_digest"}
	costKeys       = []string{"amount", "currency"}
	adminKeys      = []string{"event", "action", "actor", "target", "reason", "data_digest"}
	refKeys        = []string{"type", "id"}
)

// obj is one JSON object of the tree with its dotted path.
type obj struct {
	m    map[string]any
	path string
}

func (o obj) at(k string) string {
	if o.path == "" {
		return k
	}
	return o.path + "." + k
}

func asObj(v any, path string) (obj, *VerifyError) {
	m, ok := v.(map[string]any)
	if !ok {
		f := path
		if f == "" {
			f = "(top level)"
		}
		return obj{}, errSchemaField(f, "must be an object")
	}
	return obj{m: m, path: path}, nil
}

// closed refuses unknown keys (first in sorted order) and then missing keys (in spec order).
func (o obj) closed(keys []string) *VerifyError {
	allowed := make(map[string]bool, len(keys))
	for _, k := range keys {
		allowed[k] = true
	}
	var unknown []string
	for k := range o.m {
		if !allowed[k] {
			unknown = append(unknown, k)
		}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		return errSchemaField(o.at(unknown[0]), "is not part of the deed schema")
	}
	for _, k := range keys {
		if _, ok := o.m[k]; !ok {
			return errSchemaField(o.at(k), "is missing")
		}
	}
	return nil
}

func (o obj) str(k string) (string, *VerifyError) {
	s, ok := o.m[k].(string)
	if !ok {
		return "", errSchemaField(o.at(k), "must be a string")
	}
	return s, nil
}

func (o obj) nstr(k string) (*string, *VerifyError) {
	if o.m[k] == nil {
		return nil, nil
	}
	s, ok := o.m[k].(string)
	if !ok {
		return nil, errSchemaField(o.at(k), "must be a string or null")
	}
	return &s, nil
}

func (o obj) int(k string) (int64, *VerifyError) {
	n, ok := o.m[k].(int64)
	if !ok {
		return 0, errSchemaField(o.at(k), "must be an integer")
	}
	return n, nil
}

func (o obj) boolean(k string) (bool, *VerifyError) {
	b, ok := o.m[k].(bool)
	if !ok {
		return false, errSchemaField(o.at(k), "must be true or false")
	}
	return b, nil
}

// nobj returns the nested object at k (closed to keys), or ok=false for null.
func (o obj) nobj(k string, keys []string) (obj, bool, *VerifyError) {
	if o.m[k] == nil {
		return obj{}, false, nil
	}
	c, err := asObj(o.m[k], o.at(k))
	if err != nil {
		return obj{}, false, errSchemaField(o.at(k), "must be an object or null")
	}
	if err := c.closed(keys); err != nil {
		return obj{}, false, err
	}
	return c, true, nil
}

func (o obj) strs(k string) ([]string, *VerifyError) {
	a, ok := o.m[k].([]any)
	if !ok {
		return nil, errSchemaField(o.at(k), "must be an array")
	}
	out := make([]string, 0, len(a))
	for i, v := range a {
		s, ok := v.(string)
		if !ok {
			return nil, errSchemaField(fmt.Sprintf("%s[%d]", o.at(k), i), "must be a string")
		}
		out = append(out, s)
	}
	return out, nil
}

// decoder accumulates the first error, so the field reads below stay linear.
type decoder struct{ err *VerifyError }

func (d *decoder) s(o obj, k string) string {
	if d.err != nil {
		return ""
	}
	v, err := o.str(k)
	d.err = err
	return v
}

func (d *decoder) ns(o obj, k string) *string {
	if d.err != nil {
		return nil
	}
	v, err := o.nstr(k)
	d.err = err
	return v
}

func (d *decoder) i(o obj, k string) int64 {
	if d.err != nil {
		return 0
	}
	v, err := o.int(k)
	d.err = err
	return v
}

func (d *decoder) b(o obj, k string) bool {
	if d.err != nil {
		return false
	}
	v, err := o.boolean(k)
	d.err = err
	return v
}

func (d *decoder) obj(o obj, k string, keys []string) (obj, bool) {
	if d.err != nil {
		return obj{}, false
	}
	c, ok, err := o.nobj(k, keys)
	d.err = err
	return c, ok
}

// bodyFromTree reads a body from the strict tree: types, closed key sets and nullability only.
// Body.Validate checks the values.
func bodyFromTree(v any) (*Body, *VerifyError) {
	o, err := asObj(v, "")
	if err != nil {
		return nil, errSchemaField("body", "must be an object")
	}
	o.path = ""
	// The version is checked before the schema, so a v2 deed with new fields reads as
	// unsupported_version rather than schema_invalid.
	n, _ := o.m["v"].(int64)
	if _, ok := o.m["v"].(int64); ok && n != Version && n != VersionV2 {
		return nil, errUnsupported(n)
	}
	keys := bodyKeys
	if n == VersionV2 {
		keys = bodyKeysV2
	}
	if err := o.closed(keys); err != nil {
		return nil, err
	}
	d := &decoder{}
	b := &Body{
		V:          d.i(o, "v"),
		Kind:       Kind(d.s(o, "kind")),
		DeedID:     d.s(o, "deed_id"),
		Org:        d.s(o, "org"),
		Seq:        d.i(o, "seq"),
		PrevHash:   d.s(o, "prev_hash"),
		IssuedAt:   d.s(o, "issued_at"),
		OnBehalfOf: d.ns(o, "on_behalf_of"),
		Session:    d.ns(o, "session"),
		Amends:     d.ns(o, "amends"),
	}
	if n == VersionV2 {
		if d.err == nil {
			b.SealKeys, d.err = sealKeyRefs(o.m["seal_keys"])
		}
		if len(b.SealKeys) > 0 {
			b.SealKeyID = b.SealKeys[0].ID
		}
	} else {
		b.SealKeyID = d.s(o, "seal_key_id")
	}
	if a, ok := d.obj(o, "agent", agentKeys); ok {
		b.Agent = &AgentRef{ID: d.s(a, "id"), Name: d.s(a, "name")}
	}
	if a, ok := d.obj(o, "action", actionKeys); ok {
		b.Action = &ActionSection{ID: d.s(a, "id"), Tool: d.s(a, "tool"), Connector: d.ns(a, "connector"),
			Mode: d.s(a, "mode"), RequestDigest: d.s(a, "request_digest"), IntentHash: d.ns(a, "intent_hash"),
			IntentSigned: d.b(a, "intent_signed")}
	}
	if p, ok := d.obj(o, "policy", policyKeys); ok {
		b.Policy = &PolicySection{ID: d.ns(p, "id"), VersionID: d.ns(p, "version_id"), VersionHash: d.ns(p, "version_hash"),
			Mode: d.s(p, "mode"), Decision: d.s(p, "decision"), RuleID: d.ns(p, "rule_id")}
	}
	if a, ok := d.obj(o, "approval", approvalKeys); ok {
		b.Approval = &ApprovalSection{ID: d.s(a, "id"), Outcome: d.s(a, "outcome"), DecidedAt: d.ns(a, "decided_at"), Deed: d.ns(a, "deed")}
		if d.err == nil {
			b.Approval.DecidedBy, d.err = a.strs("decided_by")
		}
	}
	if e, ok := d.obj(o, "execution", executionKeys); ok {
		b.Execution = &ExecutionSection{Status: d.s(e, "status"), StartedAt: d.s(e, "started_at"), FinishedAt: d.s(e, "finished_at"),
			LatencyMS: d.i(e, "latency_ms"), ResultDigest: d.ns(e, "result_digest"), ErrorCode: d.ns(e, "error_code")}
	}
	if w, ok := d.obj(o, "witness", witnessKeys); ok {
		b.Witness = &WitnessSection{Level: d.s(w, "level"), Verdict: d.s(w, "verdict"), Method: d.s(w, "method"), DetailDigest: d.ns(w, "detail_digest")}
	}
	if c, ok := d.obj(o, "cost", costKeys); ok {
		b.Cost = &Cost{Amount: d.i(c, "amount"), Currency: d.s(c, "currency")}
	}
	if a, ok := d.obj(o, "admin", adminKeys); ok {
		b.Admin = &AdminSection{Event: d.s(a, "event"), Action: d.s(a, "action"), Reason: d.ns(a, "reason"), DataDigest: d.ns(a, "data_digest")}
		if d.err == nil {
			if actor, err := asObj(a.m["actor"], a.at("actor")); err != nil {
				d.err = err
			} else if d.err = actor.closed(refKeys); d.err == nil {
				b.Admin.Actor = Ref{Type: d.s(actor, "type"), ID: d.s(actor, "id")}
			}
		}
		if t, ok := d.obj(a, "target", refKeys); ok {
			b.Admin.Target = &Ref{Type: d.s(t, "type"), ID: d.s(t, "id")}
		}
	}
	if d.err != nil {
		return nil, d.err
	}
	return b, nil
}

// sealKeyRefs reads a v2 body's seal_keys: an array of 1 to MaxSeals closed {alg, id} objects.
// Body.Validate checks the values.
func sealKeyRefs(v any) ([]SealKeyRef, *VerifyError) {
	list, ok := v.([]any)
	if !ok {
		return nil, errSchemaField("seal_keys", "must be an array")
	}
	if len(list) < 1 || len(list) > MaxSeals {
		return nil, errSchemaField("seal_keys", fmt.Sprintf("must hold 1 to %d keys", MaxSeals))
	}
	out := make([]SealKeyRef, 0, len(list))
	d := &decoder{}
	for i, e := range list {
		o, err := asObj(e, fmt.Sprintf("seal_keys[%d]", i))
		if err != nil {
			return nil, err
		}
		if err := o.closed(sealKeyRefKeys); err != nil {
			return nil, err
		}
		out = append(out, SealKeyRef{Alg: d.s(o, "alg"), ID: d.s(o, "id")})
	}
	if d.err != nil {
		return nil, d.err
	}
	return out, nil
}

// sealValues reads a v2 envelope's seals: an array of 1 to MaxSeals closed {alg, seal,
// seal_key_id} objects, each with a well-formed alg, an sk_ id and a seal of the shape its
// algorithm needs.
func sealValues(v any) ([]SealValue, *VerifyError) {
	list, ok := v.([]any)
	if !ok {
		return nil, errSchemaField("seals", "must be an array")
	}
	if len(list) < 1 || len(list) > MaxSeals {
		return nil, errSchemaField("seals", fmt.Sprintf("must hold 1 to %d seals", MaxSeals))
	}
	out := make([]SealValue, 0, len(list))
	d := &decoder{}
	for i, e := range list {
		o, err := asObj(e, fmt.Sprintf("seals[%d]", i))
		if err != nil {
			return nil, err
		}
		if err := o.closed(sealValueKeys); err != nil {
			return nil, err
		}
		sv := SealValue{Alg: d.s(o, "alg"), Seal: d.s(o, "seal"), SealKeyID: d.s(o, "seal_key_id")}
		if d.err != nil {
			return nil, d.err
		}
		switch {
		case !reAlg.MatchString(sv.Alg):
			return nil, errSchemaField(o.at("alg"), "must match ^[A-Za-z0-9][A-Za-z0-9-]{0,31}$")
		case !sealShapeOK(sv.Alg, sv.Seal):
			return nil, errSchemaField(o.at("seal"), "must be a "+sv.Alg+" seal")
		case !isID(pSealKey, sv.SealKeyID):
			return nil, errSchemaField(o.at("seal_key_id"), "must be an sk_ id")
		}
		out = append(out, sv)
	}
	return out, nil
}
