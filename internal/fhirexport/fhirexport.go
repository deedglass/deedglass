// Package fhirexport turns sealed Deedglass records into FHIR R4 resources (HC-P10, DL-109): one
// AuditEvent per FHIR interaction, shaped to the IHE Basic Audit Log Patterns (BALP 1.1.x)
// profiles, and one Provenance per successful write, shaped to the HL7 AI Transparency on FHIR
// ballot's AI Provenance profile.
//
// It is an exporter, a view: the sealed record stays the primary form and its format knows nothing
// about FHIR. The package is pure (standard library only) and holds digests and ids, never
// clinical content: what the export can say about a call is what the record and the action row's
// ids say. docs/EXPORTS.md has the mapping table and what each mapping loses.
package fhirexport

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

// Formats are the export formats, as `deeds export --format` and GET /v1/deeds/export?format=
// name them. Each writes FHIR NDJSON (one resource per line, as FHIR Bulk Data does).
const (
	FormatAuditEvent = "fhir-auditevent"
	FormatProvenance = "fhir-provenance"
	// ContentType is the NDJSON media type FHIR Bulk Data uses.
	ContentType = "application/fhir+ndjson"
)

// Canonical URLs of the profiles the resources declare.
const (
	balpBase        = "https://profiles.ihe.net/ITI/BALP/StructureDefinition/IHE.BasicAudit."
	aiProvenance    = "http://hl7.org/fhir/uv/aitransparency/StructureDefinition/AI-Provenance"
	aiDevice        = "http://hl7.org/fhir/uv/aitransparency/StructureDefinition/AI-Device"
	sysAuditType    = "http://terminology.hl7.org/CodeSystem/audit-event-type"
	sysInteraction  = "http://hl7.org/fhir/restful-interaction"
	sysDCM          = "http://dicom.nema.org/resources/ontology/DCM"
	sysParticipant  = "http://terminology.hl7.org/CodeSystem/v3-ParticipationType"
	sysSourceType   = "http://terminology.hl7.org/CodeSystem/security-source-type"
	sysEntityType   = "http://terminology.hl7.org/CodeSystem/audit-entity-type"
	sysObjectRole   = "http://terminology.hl7.org/CodeSystem/object-role"
	sysBALPEntity   = "https://profiles.ihe.net/ITI/BALP/CodeSystem/BasicAuditEntityType"
	sysProvAgent    = "http://terminology.hl7.org/CodeSystem/provenance-participant-type"
	sysDataOp       = "http://terminology.hl7.org/CodeSystem/v3-DataOperation"
	sysAIProv       = "http://hl7.org/fhir/uv/aitransparency/CodeSystem/AddedProvenanceCS"
	sysAIDeviceType = "http://hl7.org/fhir/uv/aitransparency/CodeSystem/AIdeviceTypeCS"
	sysSignature    = "urn:iso-astm:E1762-95:2013"
	// Deedglass's own identifier systems: URNs, so they resolve nowhere and promise nothing.
	sysDeed      = "urn:deedglass:deed"
	sysAction    = "urn:deedglass:action"
	sysAgent     = "urn:deedglass:agent"
	sysConnector = "urn:deedglass:connector"
	sysGateway   = "urn:deedglass:gateway"
	sysDigest    = "urn:deedglass:digest"
)

// Call is what the server knows about a record's FHIR call beyond the record itself: the ids in
// the action row and the connector. Nil for a record read from a bundle alone.
type Call struct {
	ResourceType string // the FHIR type
	ID           string // the resource id ("" for a create that failed, a search, a type's history)
	VersionID    string // the version the server reported
	Patient      string // "Patient/<id>" when the call names one in the clear; "" otherwise (PHI mode)
	Server       string // the FHIR base URL
	ClientID     string // the SMART Backend Services client_id the server saw; "" for bearer or none
	Connector    string // the connector's name
}

// Record is one sealed record (its stored envelope) and, when the server has it, its call.
type Record struct {
	Envelope []byte
	Call     *Call
}

// Options are the export's own facts.
type Options struct {
	// Gateway is the Deedglass server's base URL (DEEDGLASS_BASE_URL): the AuditEvent's
	// source.observer, the client's network address and the Provenance assembler.
	Gateway string
}

// Stats counts what an export wrote and what it left out.
type Stats struct {
	Written int `json:"written"` // resources written
	Records int `json:"records"` // records read
	// Skipped are records with no resource in this format: admin records, amendments, tools that
	// aren't FHIR interactions, and (Provenance) anything but a successful write.
	Skipped int `json:"skipped"`
}

// Interactions are the FHIR connector's tools (ADR-105 §3): a tool `<connector>.<op>` is a FHIR
// interaction when op is one of them.
var interactions = map[string]struct {
	action  string // AuditEvent.action
	profile string // the BALP profile family; "" for history (no BALP profile claims it)
	write   string // v3-DataOperation for Provenance.activity; "" for reads
}{
	"create":  {"C", "Create", "CREATE"},
	"read":    {"R", "Read", ""},
	"vread":   {"R", "Read", ""},
	"update":  {"U", "Update", "UPDATE"},
	"patch":   {"U", "Update", "UPDATE"},
	"delete":  {"D", "Delete", "DELETE"},
	"search":  {"E", "Query", ""},
	"history": {"E", "", ""},
}

// body is the part of a deed body (docs/DEED_SPEC.md §4) the export reads.
type body struct {
	Kind     string `json:"kind"`
	DeedID   string `json:"deed_id"`
	Org      string `json:"org"`
	Seq      int64  `json:"seq"`
	IssuedAt string `json:"issued_at"`
	Agent    *struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"agent"`
	Action *struct {
		ID            string  `json:"id"`
		Tool          string  `json:"tool"`
		Connector     *string `json:"connector"`
		RequestDigest string  `json:"request_digest"`
	} `json:"action"`
	Policy *struct {
		VersionID *string `json:"version_id"`
		Mode      string  `json:"mode"`
		Decision  string  `json:"decision"`
		RuleID    *string `json:"rule_id"`
	} `json:"policy"`
	Approval *struct {
		Outcome string `json:"outcome"`
	} `json:"approval"`
	Execution *struct {
		Status       string  `json:"status"`
		StartedAt    string  `json:"started_at"`
		FinishedAt   string  `json:"finished_at"`
		ResultDigest *string `json:"result_digest"`
		ErrorCode    *string `json:"error_code"`
	} `json:"execution"`
	Witness *struct {
		Level   string `json:"level"`
		Verdict string `json:"verdict"`
		Method  string `json:"method"`
	} `json:"witness"`
}

type envelope struct {
	ID        string `json:"id"`
	Body      body   `json:"body"`
	Seal      string `json:"seal"`
	SealKeyID string `json:"seal_key_id"`
}

// ErrFormat is returned for a format this package doesn't write.
var ErrFormat = errors.New("fhirexport: the format must be fhir-auditevent or fhir-provenance")

// Writer writes one format as NDJSON.
type Writer struct {
	w      io.Writer
	format string
	opt    Options
	Stats  Stats
}

// NewWriter returns a Writer for format.
func NewWriter(w io.Writer, format string, opt Options) (*Writer, error) {
	if format != FormatAuditEvent && format != FormatProvenance {
		return nil, ErrFormat
	}
	return &Writer{w: w, format: format, opt: opt}, nil
}

// Write converts one record and writes its resource, if it has one in this format.
func (x *Writer) Write(rec Record) error {
	x.Stats.Records++
	var (
		res map[string]any
		ok  bool
		err error
	)
	if x.format == FormatAuditEvent {
		res, ok, err = AuditEvent(rec, x.opt)
	} else {
		res, ok, err = Provenance(rec, x.opt)
	}
	if err != nil {
		return err
	}
	if !ok {
		x.Stats.Skipped++
		return nil
	}
	b, err := json.Marshal(res)
	if err != nil {
		return err
	}
	if _, err := x.w.Write(append(b, '\n')); err != nil {
		return err
	}
	x.Stats.Written++
	return nil
}

// parse reads an action record and its interaction; ok is false for anything else.
func parse(rec Record) (env envelope, op string, ok bool, err error) {
	if err = json.Unmarshal(rec.Envelope, &env); err != nil {
		return env, "", false, fmt.Errorf("fhirexport: a record isn't a deed envelope: %w", err)
	}
	b := env.Body
	if b.Kind != "action" || b.Action == nil || b.Agent == nil || b.Policy == nil {
		return env, "", false, nil
	}
	_, op, found := strings.Cut(b.Action.Tool, ".")
	if _, known := interactions[op]; !found || !known {
		return env, "", false, nil
	}
	return env, op, true, nil
}

// resourceID is a FHIR id from a deed id (FHIR ids allow no underscore).
func resourceID(deedID string) string { return strings.ReplaceAll(deedID, "_", "-") }

func coding(system, code, display string) map[string]any {
	c := map[string]any{"system": system, "code": code}
	if display != "" {
		c["display"] = display
	}
	return c
}

func ident(system, value string) map[string]any {
	return map[string]any{"system": system, "value": value}
}

// target is the reference to the resource a call touched: the version when it is known, else the
// resource, else an identifier naming the action (a record read without its call).
func target(c *Call, actionID string) map[string]any {
	switch {
	case c != nil && c.ResourceType != "" && c.ID != "" && c.VersionID != "":
		return map[string]any{"reference": c.ResourceType + "/" + c.ID + "/_history/" + c.VersionID}
	case c != nil && c.ResourceType != "" && c.ID != "":
		return map[string]any{"reference": c.ResourceType + "/" + c.ID}
	case c != nil && c.ResourceType != "":
		return map[string]any{"type": c.ResourceType, "identifier": ident(sysAction, actionID)}
	}
	return map[string]any{"identifier": ident(sysAction, actionID)}
}

// gateway is the Deedglass server as a Device reference.
func gateway(opt Options, org string) map[string]any {
	v := opt.Gateway
	if v == "" {
		v = org
	}
	return map[string]any{"type": "Device", "identifier": ident(sysGateway, v), "display": "Deedglass"}
}

// outcome is AuditEvent.outcome and outcomeDesc: what the record says happened, with the rule and
// the check, and no clinical content.
func outcome(b body) (code, desc string) {
	parts := []string{"Policy " + b.Policy.Decision}
	if b.Policy.RuleID != nil {
		parts[0] += " by rule " + *b.Policy.RuleID
	}
	if b.Policy.Mode == "shadow" {
		parts[0] += " (shadow)"
	}
	if b.Approval != nil {
		parts = append(parts, "approval "+b.Approval.Outcome)
	}
	code = "4" // minor failure: denied, expired, cancelled, failed
	switch {
	case b.Execution == nil:
		parts = append(parts, "not executed")
	case b.Execution.Status == "succeeded":
		code = "0"
		parts = append(parts, "executed")
	case b.Execution.Status == "unknown":
		parts = append(parts, "outcome unknown")
	default:
		parts = append(parts, "failed")
	}
	if b.Execution != nil && b.Execution.ErrorCode != nil {
		parts[len(parts)-1] += " (" + *b.Execution.ErrorCode + ")"
	}
	if b.Witness != nil {
		parts = append(parts, fmt.Sprintf("check %s at %s (%s)", b.Witness.Verdict, b.Witness.Level, b.Witness.Method))
	}
	return code, "Deedglass: " + strings.Join(parts, "; ") + "."
}

func detail(typ, v string) map[string]any { return map[string]any{"type": typ, "valueString": v} }

// AuditEvent is the record's AuditEvent, and false when the record isn't a FHIR interaction.
// A successful interaction declares its BALP profile (the Patient variant when the call names a
// patient in the clear); a denied, failed or unknown one, and a history call, declare none, because
// BALP's profiles fix outcome to success and don't cover history.
func AuditEvent(rec Record, opt Options) (map[string]any, bool, error) {
	env, op, ok, err := parse(rec)
	if !ok || err != nil {
		return nil, false, err
	}
	b, c, in := env.Body, rec.Call, interactions[op]
	code, desc := outcome(b)
	recorded := b.IssuedAt
	if b.Execution != nil && b.Execution.FinishedAt != "" {
		recorded = b.Execution.FinishedAt
	}
	subtype := op
	if op == "history" {
		subtype = "history-type"
		if c != nil && c.ID != "" {
			subtype = "history-instance"
		}
	}
	ev := map[string]any{
		"resourceType": "AuditEvent",
		"id":           resourceID(b.DeedID),
		"type":         coding(sysAuditType, "rest", "Restful Operation"),
		"subtype":      []any{coding(sysInteraction, subtype, subtype)},
		"action":       in.action,
		"recorded":     recorded,
		"outcome":      code,
		"outcomeDesc":  desc,
	}
	patient := c != nil && strings.HasPrefix(c.Patient, "Patient/")
	if code == "0" && in.profile != "" {
		name := in.profile
		if patient {
			name = "Patient" + in.profile
		}
		ev["meta"] = map[string]any{"profile": []any{balpBase + name}}
	}

	// Agents: the client the FHIR server saw (the connector, by its OAuth client_id when it has
	// one), the FHIR server, and the AI agent as the user who asked.
	client := map[string]any{"type": "Device", "display": "Deedglass FHIR connector"}
	if b.Action.Connector != nil {
		client["identifier"] = ident(sysConnector, *b.Action.Connector)
	}
	if c != nil && c.Connector != "" {
		client["display"] = "Deedglass FHIR connector " + c.Connector
	}
	if c != nil && c.ClientID != "" {
		client["identifier"] = map[string]any{"type": map[string]any{"text": "OAuth 2.0 client_id"}, "value": c.ClientID}
	}
	gw := opt.Gateway
	if gw == "" {
		gw = "urn:deedglass:org:" + b.Org
	}
	server := map[string]any{"type": "Device", "display": "FHIR server"}
	serverAddr := "unknown"
	if c != nil && c.Server != "" {
		server["identifier"] = ident("urn:ietf:rfc:3986", c.Server)
		serverAddr = c.Server
	}
	ev["agent"] = []any{
		map[string]any{"type": map[string]any{"coding": []any{coding(sysDCM, "110153", "Source Role ID")}},
			"who": client, "requestor": false, "network": map[string]any{"address": gw, "type": "5"}},
		map[string]any{"type": map[string]any{"coding": []any{coding(sysDCM, "110152", "Destination Role ID")}},
			"who": server, "requestor": false, "network": map[string]any{"address": serverAddr, "type": "5"}},
		map[string]any{"type": map[string]any{"coding": []any{coding(sysParticipant, "AUT", "author (originator)")}},
			"who":       map[string]any{"type": "Device", "identifier": ident(sysAgent, b.Agent.ID), "display": b.Agent.Name},
			"requestor": true},
	}
	ev["source"] = map[string]any{"observer": gateway(opt, b.Org), "type": []any{coding(sysSourceType, "4", "Application Server")}}

	// Entities: what the call touched (or, for a search, the query as a digest), the patient when
	// the call names one, the request id the connector sent, and the sealed record with its digests.
	var ents []any
	if in.action == "E" {
		q := "digest " + b.Action.RequestDigest
		ents = append(ents, map[string]any{"type": coding(sysEntityType, "2", "System Object"), "role": coding(sysObjectRole, "24", "Query"),
			"description": "The search parameters are kept as a digest, never in the clear.",
			"query":       base64.StdEncoding.EncodeToString([]byte(q))})
	} else {
		ents = append(ents, map[string]any{"what": target(c, b.Action.ID), "type": coding(sysEntityType, "2", "System Object"),
			"role": coding(sysObjectRole, "4", "Domain Resource")})
	}
	if patient {
		ents = append(ents, map[string]any{"what": map[string]any{"reference": c.Patient}, "type": coding(sysEntityType, "1", "Person"),
			"role": coding(sysObjectRole, "1", "Patient")})
	}
	if c != nil && len(b.Action.ID) >= 16 && b.Execution != nil {
		// The binding token: the last 16 characters of the action id (ADR-105 §3).
		ents = append(ents, map[string]any{"what": map[string]any{"identifier": map[string]any{"value": b.Action.ID[len(b.Action.ID)-16:]}},
			"type": coding(sysBALPEntity, "XrequestId", "")})
	}
	details := []any{detail("deed", b.DeedID), detail("action", b.Action.ID), detail("request_digest", b.Action.RequestDigest)}
	if b.Execution != nil && b.Execution.ResultDigest != nil {
		details = append(details, detail("result_digest", *b.Execution.ResultDigest))
	}
	if b.Witness != nil {
		details = append(details, detail("witness", b.Witness.Level+" "+b.Witness.Verdict+" "+b.Witness.Method))
	}
	ents = append(ents, map[string]any{"what": map[string]any{"identifier": ident(sysDeed, b.DeedID)},
		"type": coding(sysEntityType, "4", "Other"), "description": "The sealed Deedglass record of this call.", "detail": details})
	ev["entity"] = ents
	return ev, true, nil
}

// Provenance is the record's Provenance, and false unless the record is a FHIR write that ran and
// succeeded. The AI agent is the author (a contained AI Device), Deedglass is the assembler and,
// when the check passed, the verifier; the entity is the digest of what the agent sent; the
// signature is the record's seal.
func Provenance(rec Record, opt Options) (map[string]any, bool, error) {
	env, op, ok, err := parse(rec)
	if !ok || err != nil {
		return nil, false, err
	}
	b, c, in := env.Body, rec.Call, interactions[op]
	if in.write == "" || b.Execution == nil || b.Execution.Status != "succeeded" {
		return nil, false, nil
	}
	sig, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(env.Seal, "ed25519:"))
	if err != nil || !strings.HasPrefix(env.Seal, "ed25519:") {
		return nil, false, fmt.Errorf("fhirexport: %s has a malformed seal", b.DeedID)
	}
	device := map[string]any{
		"resourceType": "Device",
		"id":           "ai-agent",
		"meta":         map[string]any{"profile": []any{aiDevice}},
		"identifier":   []any{ident(sysAgent, b.Agent.ID)},
		"deviceName":   []any{map[string]any{"name": b.Agent.Name, "type": "user-friendly-name"}},
		"type":         map[string]any{"coding": []any{coding(sysAIDeviceType, "Artificial-Intelligence", "")}},
	}
	agents := []any{
		map[string]any{"type": map[string]any{"coding": []any{coding(sysProvAgent, "author", "Author")}},
			"who": map[string]any{"reference": "#ai-agent", "display": b.Agent.Name}},
		map[string]any{"type": map[string]any{"coding": []any{coding(sysProvAgent, "assembler", "Assembler")}}, "who": gateway(opt, b.Org)},
	}
	if b.Witness != nil && b.Witness.Verdict == "VERIFIED" && b.Witness.Level != "L0" {
		agents = append(agents, map[string]any{"type": map[string]any{"coding": []any{coding(sysProvAgent, "verifier", "Verifier")}},
			"who": gateway(opt, b.Org)})
	}
	pr := map[string]any{
		"resourceType": "Provenance",
		"id":           resourceID(b.DeedID),
		"meta":         map[string]any{"profile": []any{aiProvenance}},
		"contained":    []any{device},
		"target":       []any{target(c, b.Action.ID)},
		"recorded":     b.IssuedAt,
		"reason":       []any{map[string]any{"coding": []any{coding(sysAIProv, "AIRPT", "AI Reported")}}},
		"activity":     map[string]any{"coding": []any{coding(sysDataOp, in.write, "")}},
		"agent":        agents,
		"entity": []any{map[string]any{"role": "source",
			"what": map[string]any{"identifier": ident(sysDigest, b.Action.RequestDigest), "display": "What the agent sent (the request digest)"}}},
		"signature": []any{map[string]any{
			"type": []any{coding(sysSignature, "1.2.840.10065.1.12.1.14", "Source Signature")},
			"when": b.IssuedAt,
			"who":  gateway(opt, b.Org),
			// The record's Ed25519 seal over its canonical body; check it with deedglass verify
			// and the seal key named in the record (docs/DEED_SPEC.md §7).
			"targetFormat": "application/json",
			"data":         base64.StdEncoding.EncodeToString(sig),
		}},
	}
	if b.Execution.StartedAt != "" && b.Execution.FinishedAt != "" {
		pr["occurredPeriod"] = map[string]any{"start": b.Execution.StartedAt, "end": b.Execution.FinishedAt}
	}
	if b.Policy.VersionID != nil && b.Policy.RuleID != nil {
		pr["policy"] = []any{"urn:deedglass:policy-version:" + *b.Policy.VersionID + "#" + *b.Policy.RuleID}
	}
	return pr, true, nil
}
