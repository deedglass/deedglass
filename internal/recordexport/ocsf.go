package recordexport

import "io"

// OCSF 1.9.0: one API Activity event (class 6003, category 6) per action or amendment record, as
// NDJSON, with the ai_operation profile (ai_agent, delegation) and the security_control profile
// (the policy's decision as action and disposition). Fields OCSF has no attribute for go under
// unmapped.deedglass. docs/exports/ocsf.md has the mapping and what it loses.

const (
	ocsfClassUID    = 6003
	ocsfCategoryUID = 6
)

type ocsfWriter struct {
	w   io.Writer
	opt Options
	st  Stats
}

func (x *ocsfWriter) Stats() Stats { return x.st }
func (x *ocsfWriter) Close() error { return nil }

func (x *ocsfWriter) Write(raw []byte) error {
	x.st.Records++
	e, err := parse(raw)
	if err != nil {
		return err
	}
	ev, ok := ocsfEvent(&e, x.opt)
	if !ok {
		x.st.Skipped++
		return nil
	}
	if err := writeLine(x.w, ev); err != nil {
		return err
	}
	x.st.Written++
	return nil
}

// ocsfActivity maps the tool's operation to an API Activity activity_id. Only the tool name is
// read, so a tool whose name doesn't say (postgres.execute) is Other, labelled with its operation.
func ocsfActivity(tool string) (int, string) {
	switch o := op(tool); o {
	case "create", "insert":
		return 1, "Create"
	case "read", "vread", "get", "list", "search", "history", "query", "select":
		return 2, "Read"
	case "update", "patch":
		return 3, "Update"
	case "delete", "remove":
		return 4, "Delete"
	default:
		return 99, o
	}
}

var ocsfSeverity = map[int]string{1: "Informational", 3: "Medium", 4: "High"}

func ocsfEvent(e *envelope, opt Options) (map[string]any, bool) {
	b := &e.Body
	if b.Kind == "admin" {
		return nil, false
	}
	actID, actName := ocsfActivity(b.Action.Tool)
	issued := mustTime(b.IssuedAt).UnixMilli()

	sev := 1
	switch {
	case b.Witness != nil && b.Witness.Verdict == "MISMATCH":
		sev = 4
	case b.Kind == "action" && (b.Execution == nil || b.Execution.Status != "succeeded"):
		sev = 3
	}

	ev := map[string]any{
		"class_uid":     ocsfClassUID,
		"class_name":    "API Activity",
		"category_uid":  ocsfCategoryUID,
		"category_name": "Application Activity",
		"activity_id":   actID,
		"activity_name": actName,
		"type_uid":      ocsfClassUID*100 + actID,
		"type_name":     "API Activity: " + actName,
		"time":          issued,
		"severity_id":   sev,
		"severity":      ocsfSeverity[sev],
		"message":       summary(b),
		"is_alert":      b.Witness != nil && b.Witness.Verdict == "MISMATCH",
		"metadata": map[string]any{
			"version":         OCSFVersion,
			"product":         map[string]any{"name": "Deedglass", "vendor_name": "Deedglass", "version": opt.Producer},
			"profiles":        []string{"ai_operation", "security_control"},
			"uid":             b.DeedID,
			"correlation_uid": b.Action.ID,
			"tenant_uid":      b.Org,
			"sequence":        b.Seq,
			"logged_time":     issued,
			"log_name":        "deedglass.records",
			"event_code":      b.Kind,
		},
		"actor":        ocsfActor(b),
		"api":          ocsfAPI(b),
		"src_endpoint": map[string]any{"uid": b.Agent.ID, "name": b.Agent.Name},
		"ai_agent":     map[string]any{"uid": b.Agent.ID, "name": b.Agent.Name, "type_id": 0, "type": "Unknown"},
		"unmapped":     map[string]any{"deedglass": ocsfUnmapped(e)},
	}
	if b.OnBehalfOf != nil {
		// Deedglass carries an opaque reference to the person, not a durable grant: the id is
		// derived from the pair, and the person is the issuer.
		ev["delegation"] = map[string]any{"uid": *b.OnBehalfOf + "/" + b.Agent.ID, "issuer_uid": *b.OnBehalfOf}
	}

	if b.Kind == "amendment" {
		ev["status_id"], ev["status"] = 0, "Unknown"
		ev["status_detail"] = "A later check of " + b.Action.ID + " (record " + str(b.Amends) + "); the call's own outcome is in that record's event."
		return ev, true
	}

	switch {
	case b.Execution == nil:
		ev["status_id"], ev["status"] = 2, "Failure"
		ev["status_detail"] = "Not run."
	case b.Execution.Status == "succeeded":
		ev["status_id"], ev["status"] = 1, "Success"
	case b.Execution.Status == "failed":
		ev["status_id"], ev["status"] = 2, "Failure"
		if b.Execution.ErrorCode != nil {
			ev["status_code"] = *b.Execution.ErrorCode
		}
	default:
		ev["status_id"], ev["status"] = 0, "Unknown"
		ev["status_detail"] = "Deedglass can't know whether the tool ran."
	}

	p := b.Policy
	pol := map[string]any{"name": "org default", "is_applied": p.Mode == "enforce"}
	if p.RuleID != nil {
		pol["name"] = *p.RuleID
	}
	if p.ID != nil {
		pol["uid"] = *p.ID
	}
	if p.VersionID != nil {
		pol["version"] = *p.VersionID
	}
	ev["policy"] = pol

	allowed := p.Decision == "allow" || (p.Decision == "require_approval" && b.Approval != nil && b.Approval.Outcome == "approved")
	switch {
	case p.Mode == "shadow":
		ev["action_id"], ev["action"] = 3, "Observed"
		ev["disposition_id"], ev["disposition"] = 17, "Logged"
	case allowed && p.Decision == "require_approval":
		ev["action_id"], ev["action"] = 1, "Allowed"
		ev["disposition_id"], ev["disposition"] = 8, "Approved"
	case allowed:
		ev["action_id"], ev["action"] = 1, "Allowed"
		ev["disposition_id"], ev["disposition"] = 1, "Allowed"
	default:
		ev["action_id"], ev["action"] = 2, "Denied"
		ev["disposition_id"], ev["disposition"] = 2, "Blocked"
	}
	return ev, true
}

// ocsfActor is the person the agent acted for when the record names one; otherwise the agent
// itself as a service account (OCSF's actor needs a user, process, session or application, and an
// AI agent is none of those: it is also in ai_agent).
func ocsfActor(b *body) map[string]any {
	if b.OnBehalfOf != nil {
		return map[string]any{"user": map[string]any{"uid": *b.OnBehalfOf, "type_id": 1, "type": "User"}}
	}
	return map[string]any{"user": map[string]any{"uid": b.Agent.ID, "name": b.Agent.Name, "type_id": 4, "type": "Service"}}
}

func ocsfAPI(b *body) map[string]any {
	svc := map[string]any{"name": service(b.Action.Tool)}
	if b.Action.Connector != nil {
		svc["uid"] = *b.Action.Connector
	}
	api := map[string]any{
		"operation": b.Action.Tool,
		"service":   svc,
		"request": map[string]any{"uid": b.Action.ID, "data": map[string]any{
			"request_digest": b.Action.RequestDigest, "intent_hash": b.Action.IntentHash, "intent_signed": b.Action.IntentSigned}},
	}
	if x := b.Execution; x != nil {
		resp := map[string]any{"message": x.Status}
		if x.ErrorCode != nil {
			resp["error"] = *x.ErrorCode
		}
		if x.ResultDigest != nil {
			resp["data"] = map[string]any{"result_digest": *x.ResultDigest}
		}
		api["response"] = resp
	}
	return api
}

// ocsfUnmapped keeps the record fields OCSF has no attribute for, by their record names.
func ocsfUnmapped(e *envelope) map[string]any {
	b := &e.Body
	u := map[string]any{
		"record": map[string]any{"deed_id": b.DeedID, "kind": b.Kind, "seq": b.Seq, "issued_at": b.IssuedAt,
			"prev_hash": b.PrevHash, "body_hash": e.BodyHash, "seal": e.Seal, "seal_key_id": b.SealKeyID},
		"action_mode": b.Action.Mode,
	}
	if b.Amends != nil {
		u["amends"] = *b.Amends
	}
	if b.Session != nil {
		u["session"] = *b.Session
	}
	if p := b.Policy; p != nil {
		u["policy"] = map[string]any{"decision": p.Decision, "mode": p.Mode, "version_hash": p.VersionHash}
	}
	if a := b.Approval; a != nil {
		u["approval"] = map[string]any{"id": a.ID, "outcome": a.Outcome, "decided_by": a.DecidedBy, "decided_at": a.DecidedAt, "deed": a.Deed}
	}
	if x := b.Execution; x != nil {
		u["execution"] = map[string]any{"status": x.Status, "started_at": x.StartedAt, "finished_at": x.FinishedAt, "latency_ms": x.LatencyMS}
	}
	if w := b.Witness; w != nil {
		u["check"] = map[string]any{"level": w.Level, "verdict": w.Verdict, "method": w.Method, "detail_digest": w.DetailDigest}
	}
	if c := b.Cost; c != nil {
		u["cost"] = map[string]any{"amount": c.Amount, "currency": c.Currency}
	}
	return u
}
