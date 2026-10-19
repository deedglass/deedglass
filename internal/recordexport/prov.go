package recordexport

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// W3C PROV-JSON (Member Submission, 2013-04-24): one document for all the records read.
//
//   - The agent is a prov:SoftwareAgent; the person it acted for (on_behalf_of) a prov:Person,
//     linked by actedOnBehalfOf for that activity.
//   - Each action is a prov:Activity (the tool call), wasAssociatedWith the agent under the policy
//     version as its prov:Plan; it used the request (an entity named by its digest) and generated
//     the result (named by its digest).
//   - Each record is an entity, wasAttributedTo Deedglass and wasInfluencedBy the activity it is
//     about. An amendment wasDerivedFrom the record it amends (prov:Revision).
//   - An admin record's change is an activity associated with its actor.
//
// Ids are qualified names under the prefix dg (urn:deedglass:). docs/exports/prov-json.md has the
// mapping and what it loses.

const provNS = "urn:deedglass:"

type provWriter struct {
	w   io.Writer
	opt Options
	st  Stats
	doc map[string]map[string]any // section ("entity", "used", …) → id → record
	n   map[string]int            // relation counters, for _:<prefix><n> ids
}

func newPROVWriter(w io.Writer, opt Options) *provWriter {
	return &provWriter{w: w, opt: opt, doc: map[string]map[string]any{}, n: map[string]int{}}
}

func (x *provWriter) Stats() Stats { return x.st }

// put adds a record to a section; an id already present keeps its first attributes (the same agent
// or activity appears in many records).
func (x *provWriter) put(section, id string, attrs map[string]any) {
	m := x.doc[section]
	if m == nil {
		m = map[string]any{}
		x.doc[section] = m
	}
	if _, ok := m[id]; !ok {
		m[id] = attrs
	}
}

// rel adds a relation with a fresh blank-node id.
func (x *provWriter) rel(section, prefix string, attrs map[string]any) {
	x.n[prefix]++
	x.put(section, fmt.Sprintf("_:%s%d", prefix, x.n[prefix]), attrs)
}

func (x *provWriter) Write(raw []byte) error {
	x.st.Records++
	e, err := parse(raw)
	if err != nil {
		return err
	}
	b := &e.Body
	x.put("agent", "dg:deedglass", map[string]any{"prov:type": qn("prov:SoftwareAgent"), "prov:label": "Deedglass"})

	rec := qid(b.DeedID)
	ra := map[string]any{
		"prov:type":      qn("dg:Record"),
		"dg:kind":        b.Kind,
		"dg:seq":         b.Seq,
		"dg:org":         b.Org,
		"dg:issuedAt":    dateTime(b.IssuedAt),
		"dg:prevHash":    b.PrevHash,
		"dg:bodyHash":    e.BodyHash,
		"dg:seal":        e.Seal,
		"dg:sealKeyId":   b.SealKeyID,
		"dg:specVersion": b.V,
	}
	if w := b.Witness; w != nil {
		ra["dg:checkLevel"], ra["dg:checkVerdict"], ra["dg:checkMethod"] = w.Level, w.Verdict, w.Method
		if w.DetailDigest != nil {
			ra["dg:checkDetailDigest"] = *w.DetailDigest
		}
	}
	x.put("entity", rec, ra)
	x.rel("wasAttributedTo", "att", map[string]any{"prov:entity": rec, "prov:agent": "dg:deedglass"})

	switch b.Kind {
	case "admin":
		x.admin(b, rec)
	case "amendment":
		act := qid(b.Action.ID)
		x.agent(b)
		x.rel("wasInfluencedBy", "inf", map[string]any{"prov:influencee": rec, "prov:influencer": act})
		x.rel("wasDerivedFrom", "der", map[string]any{"prov:generatedEntity": rec, "prov:usedEntity": qid(*b.Amends), "prov:type": qn("prov:Revision")})
	default:
		x.action(b, rec)
	}
	x.st.Written++
	return nil
}

func (x *provWriter) agent(b *body) string {
	id := qid(b.Agent.ID)
	x.put("agent", id, map[string]any{"prov:type": qn("prov:SoftwareAgent"), "prov:label": b.Agent.Name})
	return id
}

func (x *provWriter) action(b *body, rec string) {
	agent := x.agent(b)
	act := qid(b.Action.ID)
	aa := map[string]any{
		"prov:type":         qn("dg:ToolCall"),
		"prov:label":        b.Action.Tool,
		"dg:tool":           b.Action.Tool,
		"dg:mode":           b.Action.Mode,
		"dg:policyDecision": b.Policy.Decision,
		"dg:policyMode":     b.Policy.Mode,
		"dg:intentSigned":   b.Action.IntentSigned,
	}
	if b.Action.Connector != nil {
		aa["dg:connector"] = *b.Action.Connector
	}
	if b.Policy.RuleID != nil {
		aa["dg:policyRule"] = *b.Policy.RuleID
	}
	if b.Approval != nil {
		aa["dg:approval"] = b.Approval.ID
		aa["dg:approvalOutcome"] = b.Approval.Outcome
	}
	if b.Execution != nil {
		aa["prov:startTime"] = b.Execution.StartedAt
		aa["prov:endTime"] = b.Execution.FinishedAt
		aa["dg:executionStatus"] = b.Execution.Status
		if b.Execution.ErrorCode != nil {
			aa["dg:errorCode"] = *b.Execution.ErrorCode
		}
	} else {
		aa["dg:executionStatus"] = "not_run"
	}
	if b.Cost != nil {
		aa["dg:costAmount"], aa["dg:costCurrency"] = b.Cost.Amount, b.Cost.Currency
	}
	if b.Session != nil {
		aa["dg:session"] = *b.Session
	}
	x.put("activity", act, aa)

	assoc := map[string]any{"prov:activity": act, "prov:agent": agent}
	if v := b.Policy.VersionID; v != nil {
		plan := qid(*v)
		pa := map[string]any{"prov:type": qn("prov:Plan")}
		if b.Policy.VersionHash != nil {
			pa["dg:versionHash"] = *b.Policy.VersionHash
		}
		if b.Policy.ID != nil {
			pa["dg:policy"] = *b.Policy.ID
		}
		x.put("entity", plan, pa)
		assoc["prov:plan"] = plan
	}
	x.rel("wasAssociatedWith", "assoc", assoc)

	if b.OnBehalfOf != nil {
		person := "dg:obo-" + escapeLocal(*b.OnBehalfOf)
		x.put("agent", person, map[string]any{"prov:type": qn("prov:Person"), "dg:reference": *b.OnBehalfOf})
		x.rel("actedOnBehalfOf", "obo", map[string]any{"prov:delegate": agent, "prov:responsible": person, "prov:activity": act})
	}
	if a := b.Approval; a != nil {
		for _, u := range a.DecidedBy {
			person := qid(u)
			x.put("agent", person, map[string]any{"prov:type": qn("prov:Person")})
			x.rel("wasInfluencedBy", "inf", map[string]any{"prov:influencee": act, "prov:influencer": person, "dg:role": "approver"})
		}
	}

	req := digestID(b.Action.RequestDigest)
	x.put("entity", req, map[string]any{"prov:type": qn("dg:Request"), "dg:digest": b.Action.RequestDigest})
	x.rel("used", "u", map[string]any{"prov:activity": act, "prov:entity": req, "prov:role": qn("dg:request")})
	if ex := b.Execution; ex != nil && ex.ResultDigest != nil {
		res := digestID(*ex.ResultDigest)
		x.put("entity", res, map[string]any{"prov:type": qn("dg:Result"), "dg:digest": *ex.ResultDigest})
		x.rel("wasGeneratedBy", "g", map[string]any{"prov:entity": res, "prov:activity": act, "prov:time": ex.FinishedAt})
	}
	x.rel("wasInfluencedBy", "inf", map[string]any{"prov:influencee": rec, "prov:influencer": act})
}

func (x *provWriter) admin(b *body, rec string) {
	a := b.Admin
	act := qid(a.Event)
	aa := map[string]any{
		"prov:type":      qn("dg:AdminChange"),
		"prov:label":     a.Action,
		"dg:action":      a.Action,
		"prov:startTime": b.IssuedAt,
		"prov:endTime":   b.IssuedAt,
	}
	if a.Target != nil {
		aa["dg:targetType"], aa["dg:target"] = a.Target.Type, a.Target.ID
	}
	if a.DataDigest != nil {
		aa["dg:dataDigest"] = *a.DataDigest
	}
	x.put("activity", act, aa)
	actor := "dg:" + escapeLocal(a.Actor.Type) + "-" + escapeLocal(a.Actor.ID)
	kind := "prov:Agent"
	switch a.Actor.Type {
	case "user":
		kind = "prov:Person"
	case "agent", "system", "api_key":
		kind = "prov:SoftwareAgent"
	}
	x.put("agent", actor, map[string]any{"prov:type": qn(kind), "dg:refType": a.Actor.Type})
	x.rel("wasAssociatedWith", "assoc", map[string]any{"prov:activity": act, "prov:agent": actor})
	x.rel("wasInfluencedBy", "inf", map[string]any{"prov:influencee": rec, "prov:influencer": act})
}

// Close writes the document. Nothing is written before Close: a PROV-JSON document is one object.
func (x *provWriter) Close() error {
	doc := map[string]any{"prefix": map[string]any{"dg": provNS}}
	for k, v := range x.doc {
		doc[k] = v
	}
	b, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	_, err = x.w.Write(append(b, '\n'))
	return err
}

// qid is the qualified name of a Deedglass id.
func qid(id string) string { return "dg:" + escapeLocal(id) }

// digestID names an entity by its content digest: dg:sha256-<hex>.
func digestID(d string) string { return "dg:" + strings.Replace(d, ":", "-", 1) }

// escapeLocal percent-escapes everything a PROV-N local name can't hold as is.
func escapeLocal(s string) string {
	var out strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-' || c == '.' {
			out.WriteByte(c)
			continue
		}
		fmt.Fprintf(&out, "%%%02X", c)
	}
	return out.String()
}

// qn is a qualified-name value (PROV-JSON typed literal).
func qn(q string) map[string]any { return map[string]any{"$": q, "type": "xsd:QName"} }

func dateTime(s string) map[string]any { return map[string]any{"$": s, "type": "xsd:dateTime"} }
