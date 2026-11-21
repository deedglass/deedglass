// Package sections holds approval context sections (S40, Ward Light): clinical_preview, what a
// clinician reads before countersigning an ambient AI scribe or EHR write. Pure: it reads the action's
// arguments and policy facts, never the network, and stays strictly under approve.MaxSectionBytes (8 KiB).
package sections

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/deedglass/deedglass/internal/approve"
	"github.com/deedglass/deedglass/internal/gate"
)

// ClinicalPreviewVersion is the section's version.
const ClinicalPreviewVersion = 1

// MaxTranscriptBytes is how much of the scribe transcript text is carried.
const MaxTranscriptBytes = 2 << 10

// ClinicalPreview is the "clinical_preview" section for the Ward Light Clinician Elevation Card.
type ClinicalPreview struct {
	V                    int               `json:"v"`
	ResourceType         string            `json:"resource_type"`
	ResourceID           string            `json:"resource_id,omitempty"`
	ActionLabel          string            `json:"action_label"`
	Status               string            `json:"status,omitempty"`
	Intent               string            `json:"intent,omitempty"`
	PatientRef           string            `json:"patient_ref,omitempty"`
	PatientDisplay       string            `json:"patient_display,omitempty"`
	EHRDeepLink          string            `json:"ehr_deep_link,omitempty"`
	TranscriptAnchor     string            `json:"transcript_anchor,omitempty"`
	ScribeVendor         string            `json:"scribe_vendor,omitempty"`
	Fields               []ClinicalField   `json:"fields"`
	IsBreakGlass         bool              `json:"is_break_glass,omitempty"`
	EmergencyReason      string            `json:"emergency_reason,omitempty"`
	AuditNotice          string            `json:"audit_notice,omitempty"`
	CollisionWarning     string            `json:"collision_warning,omitempty"`   // CH-32
	UngroundedNotice     string            `json:"ungrounded_notice,omitempty"`   // CH-34
	Environment          string            `json:"environment,omitempty"`         // CH-35
	PreliminaryWarning   string            `json:"preliminary_warning,omitempty"` // CH-36
	SessionTimeoutSec    int               `json:"session_timeout_sec,omitempty"` // CH-37
	PractitionerRole     string            `json:"practitioner_role,omitempty"`   // CH-37
	IsPriorAuth          bool              `json:"is_prior_auth,omitempty"`
	PriorAuthDetails     *PriorAuthPreview `json:"prior_auth_details,omitempty"`
	AIProposedDigest     string            `json:"ai_proposed_digest,omitempty"`     // CH-10
	ClinicianFinalDigest string            `json:"clinician_final_digest,omitempty"` // CH-10
	EdgeQueueConflict    string            `json:"edge_queue_conflict,omitempty"`    // CH-38
	PromptInjectionAlert string            `json:"prompt_injection_alert,omitempty"` // CH-39
	CohortExportAlert    string            `json:"cohort_export_alert,omitempty"`    // CH-40
	BulkExportAlert      string            `json:"bulk_export_alert,omitempty"`      // CH-40
	CarePlanConflict     string            `json:"care_plan_conflict,omitempty"`     // CH-41
	Summary              string            `json:"summary"`
}

// PriorAuthPreview captures Da Vinci PAS / CMS-0057-F pre-authorization metadata.
type PriorAuthPreview struct {
	PayerName       string   `json:"payer_name,omitempty"`
	ProviderNPI     string   `json:"provider_npi,omitempty"`
	TotalAmount     string   `json:"total_amount,omitempty"`
	ServiceType     string   `json:"service_type,omitempty"`
	DiagnosisCodes  []string `json:"diagnosis_codes,omitempty"`
	ServiceRequests []string `json:"service_requests,omitempty"`
	Urgency         string   `json:"urgency,omitempty"`
}

// ClinicalField is one structured field displayed on the Ward Light chart card.
type ClinicalField struct {
	Key      string `json:"key"`
	Label    string `json:"label"`
	Value    string `json:"value"`
	Prior    string `json:"prior,omitempty"`
	Editable bool   `json:"editable"`
}

// ClinicalMatches returns true when an action targets FHIR resources or clinical workflows.
func ClinicalMatches(tool, connectorType string) bool {
	if connectorType == "fhir" || strings.HasPrefix(tool, "fhir.") || strings.HasPrefix(tool, "health.") {
		return true
	}
	lower := strings.ToLower(tool)
	return strings.Contains(lower, "fhir") ||
		strings.Contains(lower, "observation") ||
		strings.Contains(lower, "medication") ||
		strings.Contains(lower, "allergy") ||
		strings.Contains(lower, "claim") ||
		strings.Contains(lower, "scribe")
}

// BuildClinical constructs the ClinicalPreview section for gate.ApprovalInput.
func BuildClinical(in gate.ApprovalInput) (json.RawMessage, error) {
	var raw map[string]any
	if len(in.Args) > 0 && json.Valid(in.Args) {
		_ = json.Unmarshal(in.Args, &raw)
	}
	if raw == nil {
		raw = make(map[string]any)
	}

	res := extractResource(raw)

	resType := in.Request.FHIR.ResourceType
	if resType == "" {
		resType = stringVal(raw["resource_type"])
	}
	if resType == "" {
		resType = stringVal(raw["resourceType"])
	}
	if resType == "" {
		resType = stringVal(res["resourceType"])
	}
	if resType == "" {
		resType = "Resource"
	}

	resID := in.Request.FHIR.ID
	if resID == "" {
		resID = stringVal(res["id"])
	}
	if resID == "" {
		if idObj, ok := res["identifier"].(map[string]any); ok {
			resID = stringVal(idObj["value"])
		}
	}

	patientRef := extractPatientRef(raw, res)
	patientDisplay := patientRef
	if strings.HasPrefix(patientRef, "Patient/") {
		patientDisplay = "Patient " + strings.TrimPrefix(patientRef, "Patient/")
	}

	// CH-05: EHR Deep Link so clinician can inspect in primary EHR before deciding.
	var ehrLink string
	if patientRef != "" {
		pID := strings.TrimPrefix(patientRef, "Patient/")
		if resID != "" && resType != "Resource" {
			ehrLink = fmt.Sprintf("/ehr/patient/%s/%s/%s", pID, resType, resID)
		} else {
			ehrLink = fmt.Sprintf("/ehr/patient/%s", pID)
		}
	}

	// Transcript source anchor (AI scribe utterance).
	transcript := extractTranscript(raw, res)
	if len(transcript) > MaxTranscriptBytes {
		transcript = clipRunes(transcript, MaxTranscriptBytes)
	}

	scribeVendor := stringVal(raw["scribe_vendor"])
	if scribeVendor == "" {
		scribeVendor = stringVal(res["scribe_vendor"])
	}

	// CH-07: Detect emergency break-glass.
	isBreakGlass, emerReason := detectBreakGlass(in, raw)
	var auditNotice string
	if isBreakGlass {
		auditNotice = "EMERGENCY OVERRIDE: Action initiated under break-glass override. Bypasses standard delay; logged to weekly CMO safety audit (CH-07)."
	}

	// CH-32: Concurrency collision detection.
	var collisionWarning string
	if boolVal(raw["concurrency_collision"]) || boolVal(res["concurrency_collision"]) || boolVal(raw["version_conflict"]) || boolVal(res["version_conflict"]) {
		collisionWarning = "CONCURRENCY CONFLICT: Resource modified by concurrent clinical staff; review prior version before commit (CH-32)."
	}

	// CH-34: Detect ungrounded ambient order drafts lacking transcript grounding.
	var ungroundedNotice string
	if boolVal(raw["ungrounded_draft"]) || boolVal(res["ungrounded_draft"]) ||
		((resType == "MedicationRequest" || resType == "ServiceRequest" || resType == "Observation" || resType == "Claim") &&
			(transcript == "" && (boolVal(raw["ambient_scribe"]) || scribeVendor != ""))) {
		ungroundedNotice = "UNGROUNDED DRAFT: Ambient order lacks transcript quote anchor; attending clinician must confirm verbal grounding before sign-off (CH-34)."
	}

	// CH-35: Runtime environment isolation tagging.
	env := stringVal(raw["environment"])
	if env == "" {
		env = stringVal(res["environment"])
	}
	if env == "" {
		env = stringVal(raw["tenant_environment"])
	}
	if env == "" {
		env = "production"
	}

	// CH-36: Preliminary draft cascade detection.
	var prelimWarning string
	resStatus := strings.ToLower(stringVal(res["status"]))
	verStatus := strings.ToLower(stringVal(res["verificationStatus"]))
	if resStatus == "preliminary" || resStatus == "draft" || verStatus == "unconfirmed" || boolVal(raw["preliminary_cascade"]) || boolVal(res["preliminary_cascade"]) {
		prelimWarning = "PRELIMINARY / UNCONFIRMED DRAFT: Upstream record is tentative/unconfirmed; verify clinical status before countersigning (CH-36)."
	}

	// CH-37: Clinician credential & role session timeout.
	sessionTimeout := 300 // 5 minutes default
	if to, ok := raw["session_timeout_sec"].(float64); ok && to > 0 {
		sessionTimeout = int(to)
	}
	practitionerRole := stringVal(raw["practitioner_role"])
	if practitionerRole == "" {
		practitionerRole = stringVal(res["practitioner_role"])
	}
	if practitionerRole == "" {
		practitionerRole = "Attending Clinician"
	}

	// Prior Authorization metadata (Da Vinci PAS / CMS-0057-F / NHCX)
	isPA, paDetails := extractPriorAuth(resType, raw, res)

	fields := buildClinicalFields(resType, raw, res)
	aiDigest := computeFieldsDigest(fields)
	clinicianDigest := aiDigest

	// CH-10: If explicit clinician edits were supplied (Edit & Commit)
	if edits, ok := raw["clinician_edits"].(map[string]any); ok && len(edits) > 0 {
		for i := range fields {
			if edVal, found := edits[fields[i].Key]; found {
				fields[i].Prior = fields[i].Value
				fields[i].Value = stringVal(edVal)
			}
		}
		clinicianDigest = computeFieldsDigest(fields)
	}

	actionLabel := describeAction(resType, in.Request.FHIR.Interaction, isBreakGlass, isPA)

	status := stringVal(res["status"])
	intent := stringVal(res["intent"])

	// CH-38: Offline-first edge gateway write queue split-brain conflict detection.
	var edgeQueueConflict string
	if boolVal(raw["edge_queue_conflict"]) || boolVal(res["edge_queue_conflict"]) ||
		boolVal(raw["offline_sync_conflict"]) || boolVal(res["offline_sync_conflict"]) ||
		strings.EqualFold(stringVal(raw["edge_sync_status"]), "conflict") ||
		strings.EqualFold(stringVal(res["edge_sync_status"]), "conflict") {
		edgeQueueConflict = "OFFLINE SPLIT-BRAIN CONFLICT: Edge write queue version diverges from hospital central server; review queued diff before flush (CH-38)."
	}

	// CH-39: Multi-agent secondary prompt injection detection.
	var promptInjectAlert string
	if boolVal(raw["prompt_injection_risk"]) || boolVal(res["prompt_injection_risk"]) ||
		hasSecondaryPromptInjection(raw, res) {
		promptInjectAlert = "UNVERIFIED INJECTION RISK: Secondary prompt override pattern detected in payload text; routed to mandatory clinician review (CH-39)."
	}

	// CH-40: FHIR Bulk Data Export ($export) unreviewed cohort widening detection.
	var bulkExportAlert string
	if boolVal(raw["bulk_export_unreviewed"]) || boolVal(res["bulk_export_unreviewed"]) ||
		boolVal(raw["unreviewed_cohort_widening"]) || boolVal(res["unreviewed_cohort_widening"]) ||
		in.Request.FHIR.Interaction == "export" ||
		strings.Contains(stringVal(raw["path"]), "$export") || strings.Contains(stringVal(res["path"]), "$export") ||
		strings.Contains(stringVal(raw["http_path"]), "$export") || strings.Contains(stringVal(res["http_path"]), "$export") ||
		(resType == "Group" && strings.EqualFold(stringVal(raw["export_scope"]), "unbounded")) {
		bulkExportAlert = "UNREVIEWED BULK EXPORT: FHIR $export requested without approved patient cohort scope; institutional data officer authorization required (CH-40)."
	}

	// CH-41: Multi-agent clinical goal misalignment or conflicting care-plan synthesis.
	var carePlanConflict string
	if boolVal(raw["care_plan_conflict"]) || boolVal(res["care_plan_conflict"]) ||
		boolVal(raw["multi_agent_conflict"]) || boolVal(res["multi_agent_conflict"]) ||
		((resType == "CarePlan" || resType == "NutritionOrder" || resType == "Goal") &&
			(boolVal(raw["uncoordinated_agents"]) || boolVal(res["uncoordinated_agents"]) ||
				strings.EqualFold(stringVal(raw["plan_conflict_status"]), "misaligned") ||
				strings.EqualFold(stringVal(res["plan_conflict_status"]), "misaligned"))) {
		carePlanConflict = "CARE PLAN CONFLICT: Multiple agents proposed contradictory care-plan interventions; attending clinician harmonization required (CH-41)."
	}

	p := ClinicalPreview{
		V:                    ClinicalPreviewVersion,
		ResourceType:         resType,
		ResourceID:           resID,
		ActionLabel:          actionLabel,
		Status:               status,
		Intent:               intent,
		PatientRef:           patientRef,
		PatientDisplay:       patientDisplay,
		EHRDeepLink:          ehrLink,
		TranscriptAnchor:     transcript,
		ScribeVendor:         scribeVendor,
		Fields:               fields,
		IsBreakGlass:         isBreakGlass,
		EmergencyReason:      emerReason,
		AuditNotice:          auditNotice,
		CollisionWarning:     collisionWarning,
		UngroundedNotice:     ungroundedNotice,
		Environment:          env,
		PreliminaryWarning:   prelimWarning,
		SessionTimeoutSec:    sessionTimeout,
		PractitionerRole:     practitionerRole,
		IsPriorAuth:          isPA,
		PriorAuthDetails:     paDetails,
		AIProposedDigest:     aiDigest,
		ClinicianFinalDigest: clinicianDigest,
		EdgeQueueConflict:    edgeQueueConflict,
		PromptInjectionAlert: promptInjectAlert,
		CohortExportAlert:    bulkExportAlert,
		BulkExportAlert:      bulkExportAlert,
		CarePlanConflict:     carePlanConflict,
	}

	p.Summary = clinicalSummary(p)
	return json.Marshal(p)
}

func extractResource(raw map[string]any) map[string]any {
	if nested, ok := raw["resource"].(map[string]any); ok && len(nested) > 0 {
		return nested
	}
	return raw
}

func extractPatientRef(raw, res map[string]any) string {
	for _, m := range []map[string]any{res, raw} {
		if sub, ok := m["subject"].(map[string]any); ok {
			if ref := stringVal(sub["reference"]); ref != "" {
				return ref
			}
		}
		if pat, ok := m["patient"].(map[string]any); ok {
			if ref := stringVal(pat["reference"]); ref != "" {
				return ref
			}
		}
		if ref := stringVal(m["subject"]); ref != "" {
			return ref
		}
		if ref := stringVal(m["patient"]); ref != "" {
			return ref
		}
	}
	return ""
}

func extractTranscript(raw, res map[string]any) string {
	keys := []string{"transcript_anchor", "transcript_quote", "transcript", "source_transcript", "ambient_utterance"}
	for _, k := range keys {
		if s := stringVal(raw[k]); s != "" {
			return s
		}
		if s := stringVal(res[k]); s != "" {
			return s
		}
	}
	// Check note text if available.
	if notes, ok := res["note"].([]any); ok && len(notes) > 0 {
		if n0, ok := notes[0].(map[string]any); ok {
			if t := stringVal(n0["text"]); t != "" {
				return t
			}
		}
	}
	return ""
}

func detectBreakGlass(in gate.ApprovalInput, raw map[string]any) (bool, string) {
	rule := strings.ToLower(in.PolicyID + " " + in.PolicyName + " " + in.AgentName)
	if strings.Contains(rule, "break-glass") || strings.Contains(rule, "break_glass") || strings.Contains(rule, "emergency") {
		return true, "Policy rule requires break-glass audit"
	}
	if b, ok := raw["break_glass"].(bool); ok && b {
		return true, stringVal(raw["emergency_reason"])
	}
	if b, ok := raw["emergency"].(bool); ok && b {
		return true, stringVal(raw["emergency_reason"])
	}
	return false, ""
}

func buildClinicalFields(resType string, raw, res map[string]any) []ClinicalField {
	var fields []ClinicalField

	switch resType {
	case "MedicationRequest":
		// Medicine
		med := extractMedication(res, raw)
		fields = append(fields, ClinicalField{Key: "medicine", Label: "Medicine", Value: med, Editable: true})

		// Dosage, Route, Frequency, Duration
		dose, route, freq, dur := extractDosage(res)
		if dose != "" {
			fields = append(fields, ClinicalField{Key: "dose", Label: "Dose", Value: dose, Editable: true})
		}
		if route != "" {
			fields = append(fields, ClinicalField{Key: "route", Label: "Route", Value: route, Editable: true})
		}
		if freq != "" {
			fields = append(fields, ClinicalField{Key: "frequency", Label: "Frequency", Value: freq, Editable: true})
		}
		if dur != "" {
			fields = append(fields, ClinicalField{Key: "duration", Label: "Duration", Value: dur, Editable: true})
		}
		if intent := stringVal(res["intent"]); intent != "" {
			fields = append(fields, ClinicalField{Key: "intent", Label: "Intent", Value: intent, Editable: false})
		}

	case "Observation":
		code := extractCode(res, raw)
		fields = append(fields, ClinicalField{Key: "observation", Label: "Observation", Value: code, Editable: false})

		val := extractValueQuantity(res)
		if val != "" {
			fields = append(fields, ClinicalField{Key: "value", Label: "Recorded Value", Value: val, Editable: true})
		}
		if status := stringVal(res["status"]); status != "" {
			fields = append(fields, ClinicalField{Key: "status", Label: "Status", Value: status, Editable: false})
		}

	case "AllergyIntolerance":
		substance := extractCode(res, raw)
		fields = append(fields, ClinicalField{Key: "substance", Label: "Substance / Allergen", Value: substance, Editable: false})

		if crit := stringVal(res["criticality"]); crit != "" {
			fields = append(fields, ClinicalField{Key: "criticality", Label: "Criticality", Value: crit, Editable: true})
		}
		if clin := extractClinicalStatus(res); clin != "" {
			fields = append(fields, ClinicalField{Key: "clinical_status", Label: "Clinical Status", Value: clin, Editable: false})
		}

	case "Claim":
		use := stringVal(res["use"])
		if use == "" {
			use = "institutional claim"
		}
		fields = append(fields, ClinicalField{Key: "claim_use", Label: "Claim Purpose", Value: use, Editable: false})

		if tot, ok := res["total"].(map[string]any); ok {
			val := stringVal(tot["value"])
			cur := stringVal(tot["currency"])
			fields = append(fields, ClinicalField{Key: "amount", Label: "Total Amount", Value: val + " " + cur, Editable: false})
		}
		if prov, ok := res["provider"].(map[string]any); ok {
			if d := stringVal(prov["display"]); d != "" {
				fields = append(fields, ClinicalField{Key: "provider", Label: "Provider", Value: d, Editable: false})
			}
		}

	case "ServiceRequest":
		code := extractCode(res, raw)
		fields = append(fields, ClinicalField{Key: "service", Label: "Service / Procedure", Value: code, Editable: false})
		if intent := stringVal(res["intent"]); intent != "" {
			fields = append(fields, ClinicalField{Key: "intent", Label: "Intent", Value: intent, Editable: false})
		}
		if priority := stringVal(res["priority"]); priority != "" {
			fields = append(fields, ClinicalField{Key: "priority", Label: "Priority", Value: priority, Editable: true})
		}
		if occ := stringVal(res["occurrenceDateTime"]); occ != "" {
			fields = append(fields, ClinicalField{Key: "occurrence", Label: "Occurrence", Value: occ, Editable: true})
		}
		if req, ok := res["requester"].(map[string]any); ok {
			if d := stringVal(req["display"]); d != "" {
				fields = append(fields, ClinicalField{Key: "requester", Label: "Requester", Value: d, Editable: false})
			}
		}

	case "Condition":
		code := extractCode(res, raw)
		fields = append(fields, ClinicalField{Key: "condition", Label: "Diagnosis / Condition", Value: code, Editable: false})
		if clin := extractClinicalStatus(res); clin != "" {
			fields = append(fields, ClinicalField{Key: "clinical_status", Label: "Clinical Status", Value: clin, Editable: false})
		}
		if ver := stringVal(res["verificationStatus"]); ver != "" {
			fields = append(fields, ClinicalField{Key: "verification_status", Label: "Verification Status", Value: ver, Editable: false})
		}
		if sev, ok := res["severity"].(map[string]any); ok {
			if disp := codeDisplay(sev); disp != "" {
				fields = append(fields, ClinicalField{Key: "severity", Label: "Severity", Value: disp, Editable: false})
			}
		}

	case "CarePlan":
		if title := stringVal(res["title"]); title != "" {
			fields = append(fields, ClinicalField{Key: "title", Label: "Care Plan Title", Value: title, Editable: true})
		}
		if desc := stringVal(res["description"]); desc != "" {
			fields = append(fields, ClinicalField{Key: "description", Label: "Plan Description", Value: desc, Editable: true})
		}
		if status := stringVal(res["status"]); status != "" {
			fields = append(fields, ClinicalField{Key: "status", Label: "Plan Status", Value: status, Editable: false})
		}
		if intent := stringVal(res["intent"]); intent != "" {
			fields = append(fields, ClinicalField{Key: "intent", Label: "Intent", Value: intent, Editable: false})
		}

	case "Group":
		if name := stringVal(res["name"]); name != "" {
			fields = append(fields, ClinicalField{Key: "group_name", Label: "Cohort Group", Value: name, Editable: false})
		}
		if qty, ok := res["quantity"].(float64); ok {
			fields = append(fields, ClinicalField{Key: "cohort_size", Label: "Cohort Size", Value: fmt.Sprintf("%d patients", int(qty)), Editable: false})
		}

	default:
		// Generic FHIR resource fallback.
		if code := extractCode(res, raw); code != "" {
			fields = append(fields, ClinicalField{Key: "concept", Label: "Concept", Value: code, Editable: false})
		}
		if status := stringVal(res["status"]); status != "" {
			fields = append(fields, ClinicalField{Key: "status", Label: "Status", Value: status, Editable: false})
		}
	}

	if len(fields) == 0 {
		fields = append(fields, ClinicalField{Key: "summary", Label: "Resource", Value: resType, Editable: false})
	}
	return fields
}

func extractMedication(res, raw map[string]any) string {
	for _, m := range []map[string]any{res, raw} {
		if med, ok := m["medicationCodeableConcept"].(map[string]any); ok {
			if s := codeDisplay(med); s != "" {
				return s
			}
		}
		if ref, ok := m["medicationReference"].(map[string]any); ok {
			if d := stringVal(ref["display"]); d != "" {
				return d
			}
			if r := stringVal(ref["reference"]); r != "" {
				return r
			}
		}
		if s := stringVal(m["medication"]); s != "" {
			return s
		}
		if s := stringVal(m["medicine"]); s != "" {
			return s
		}
	}
	return "Unspecified medication"
}

func extractDosage(res map[string]any) (dose, route, freq, dur string) {
	dosages, ok := res["dosageInstruction"].([]any)
	if !ok || len(dosages) == 0 {
		return
	}
	d0, ok := dosages[0].(map[string]any)
	if !ok {
		return
	}
	if t := stringVal(d0["text"]); t != "" {
		dose = t
	}
	if dar, ok := d0["doseAndRate"].([]any); ok && len(dar) > 0 {
		if dar0, ok := dar[0].(map[string]any); ok {
			if dq, ok := dar0["doseQuantity"].(map[string]any); ok {
				v := stringVal(dq["value"])
				u := stringVal(dq["unit"])
				if v != "" {
					dose = v + " " + u
				}
			}
		}
	}
	if r, ok := d0["route"].(map[string]any); ok {
		route = codeDisplay(r)
	}
	if timing, ok := d0["timing"].(map[string]any); ok {
		if tc, ok := timing["code"].(map[string]any); ok {
			freq = codeDisplay(tc)
		}
		if repeat, ok := timing["repeat"].(map[string]any); ok {
			if f := stringVal(repeat["frequency"]); f != "" {
				p := stringVal(repeat["period"])
				pu := stringVal(repeat["periodUnit"])
				freq = f + "x per " + p + pu
			}
			if bd, ok := repeat["boundsDuration"].(map[string]any); ok {
				dur = stringVal(bd["value"]) + " " + stringVal(bd["unit"])
			}
		}
	}
	return
}

func extractCode(res, raw map[string]any) string {
	for _, m := range []map[string]any{res, raw} {
		if c, ok := m["code"].(map[string]any); ok {
			if s := codeDisplay(c); s != "" {
				return s
			}
		}
	}
	return ""
}

func codeDisplay(m map[string]any) string {
	if text := stringVal(m["text"]); text != "" {
		return text
	}
	if codings, ok := m["coding"].([]any); ok && len(codings) > 0 {
		if c0, ok := codings[0].(map[string]any); ok {
			if disp := stringVal(c0["display"]); disp != "" {
				return disp
			}
			if code := stringVal(c0["code"]); code != "" {
				return code
			}
		}
	}
	return ""
}

func extractValueQuantity(res map[string]any) string {
	if vq, ok := res["valueQuantity"].(map[string]any); ok {
		val := stringVal(vq["value"])
		unit := stringVal(vq["unit"])
		if unit == "" {
			unit = stringVal(vq["code"])
		}
		if val != "" {
			if unit != "" {
				return val + " " + unit
			}
			return val
		}
	}
	if vs := stringVal(res["valueString"]); vs != "" {
		return vs
	}
	return ""
}

func extractClinicalStatus(res map[string]any) string {
	if cs, ok := res["clinicalStatus"].(map[string]any); ok {
		return codeDisplay(cs)
	}
	return stringVal(res["clinicalStatus"])
}

func describeAction(resType, inter string, breakGlass, isPA bool) string {
	prefix := ""
	if breakGlass {
		prefix = "Emergency Break-Glass "
	}
	if isPA {
		return prefix + "Prior Authorization Request (Da Vinci PAS / CMS-0057-F)"
	}
	switch resType {
	case "MedicationRequest":
		return prefix + "Medication Order Proposal"
	case "Observation":
		return prefix + "Record Clinical Observation"
	case "AllergyIntolerance":
		return prefix + "Record Allergy Intolerance"
	case "ServiceRequest":
		return prefix + "Clinical Service Request"
	case "Condition":
		return prefix + "Record Clinical Condition"
	case "Claim":
		return prefix + "NHCX Claim Bundle Submission"
	default:
		if inter != "" {
			return prefix + "Write " + resType + " (" + inter + ")"
		}
		return prefix + "Write " + resType
	}
}

func computeFieldsDigest(fields []ClinicalField) string {
	var b strings.Builder
	for _, f := range fields {
		b.WriteString(f.Key)
		b.WriteString(":")
		b.WriteString(f.Value)
		b.WriteString("\n")
	}
	h := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(h[:])
}

func extractPriorAuth(resType string, raw, res map[string]any) (bool, *PriorAuthPreview) {
	isPA := boolVal(raw["prior_auth"]) || boolVal(res["prior_auth"]) ||
		strings.EqualFold(stringVal(res["use"]), "preauthorization") ||
		strings.EqualFold(stringVal(raw["use"]), "preauthorization") ||
		(strings.Contains(strings.ToLower(resType), "claim") && strings.Contains(strings.ToLower(stringVal(raw["rule"])), "prior-auth"))
	if !isPA && resType != "Claim" && resType != "ClaimResponse" {
		return false, nil
	}

	pa := &PriorAuthPreview{
		PayerName:   "Payer / Insurer",
		ServiceType: "Professional / Institutional",
		Urgency:     "standard",
	}

	if p := stringVal(raw["payer_name"]); p != "" {
		pa.PayerName = p
	} else if ins, ok := res["insurer"].(map[string]any); ok {
		if d := stringVal(ins["display"]); d != "" {
			pa.PayerName = d
		}
	}

	if npi := stringVal(raw["provider_npi"]); npi != "" {
		pa.ProviderNPI = npi
	} else if prov, ok := res["provider"].(map[string]any); ok {
		if idObj, ok := prov["identifier"].(map[string]any); ok {
			pa.ProviderNPI = stringVal(idObj["value"])
		}
	}

	if tot, ok := res["total"].(map[string]any); ok {
		pa.TotalAmount = stringVal(tot["value"]) + " " + stringVal(tot["currency"])
	} else if amt := stringVal(raw["total_amount"]); amt != "" {
		pa.TotalAmount = amt
	}

	if st := stringVal(raw["service_type"]); st != "" {
		pa.ServiceType = st
	}

	if urg := stringVal(raw["urgency"]); urg != "" {
		pa.Urgency = urg
	} else if prio, ok := res["priority"].(map[string]any); ok {
		if c := codeDisplay(prio); c != "" {
			pa.Urgency = strings.ToLower(c)
		}
	}

	// Diagnoses
	if diagList, ok := res["diagnosis"].([]any); ok {
		for _, d := range diagList {
			if dm, ok := d.(map[string]any); ok {
				if dcc, ok := dm["diagnosisCodeableConcept"].(map[string]any); ok {
					if c := codeDisplay(dcc); c != "" {
						pa.DiagnosisCodes = append(pa.DiagnosisCodes, c)
					}
				}
			}
		}
	}
	if len(pa.DiagnosisCodes) == 0 && raw["diagnosis_codes"] != nil {
		if dcs, ok := raw["diagnosis_codes"].([]any); ok {
			for _, d := range dcs {
				if ds := stringVal(d); ds != "" {
					pa.DiagnosisCodes = append(pa.DiagnosisCodes, ds)
				}
			}
		}
	}

	return isPA, pa
}

func boolVal(v any) bool {
	if b, ok := v.(bool); ok {
		return b
	}
	if s, ok := v.(string); ok {
		clean := strings.ToLower(strings.TrimSpace(s))
		return clean == "true" || clean == "1" || clean == "yes"
	}
	return false
}

func clinicalSummary(p ClinicalPreview) string {
	var b strings.Builder
	if p.IsBreakGlass {
		b.WriteString("Emergency Break-Glass: ")
	}
	b.WriteString(p.ResourceType)
	if len(p.Fields) > 0 && p.Fields[0].Value != "" {
		b.WriteString(" (")
		b.WriteString(p.Fields[0].Value)
		b.WriteString(")")
	}
	if p.PatientDisplay != "" {
		b.WriteString(" for ")
		b.WriteString(p.PatientDisplay)
	}
	return clipRunes(b.String(), 280)
}

func stringVal(v any) string {
	if v == nil {
		return ""
	}
	switch val := v.(type) {
	case string:
		return strings.TrimSpace(val)
	case float64:
		return strconv.FormatFloat(val, 'f', -1, 64)
	case int:
		return strconv.Itoa(val)
	case int64:
		return strconv.FormatInt(val, 10)
	case bool:
		return strconv.FormatBool(val)
	default:
		return ""
	}
}

func clipRunes(s string, max int) string {
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	r := []rune(s)
	return string(r[:max-1]) + "\u2026"
}

func hasSecondaryPromptInjection(raw, res map[string]any) bool {
	texts := []string{
		stringVal(raw["transcript_anchor"]),
		stringVal(raw["transcript"]),
		stringVal(res["transcript"]),
		stringVal(raw["note_text"]),
		stringVal(res["note_text"]),
		stringVal(raw["text"]),
		stringVal(res["text"]),
		stringVal(raw["comment"]),
		stringVal(res["comment"]),
	}
	if notes, ok := res["note"].([]any); ok {
		for _, n := range notes {
			if nm, ok := n.(map[string]any); ok {
				texts = append(texts, stringVal(nm["text"]))
			}
		}
	}
	if exts, ok := res["extension"].([]any); ok {
		for _, ext := range exts {
			if em, ok := ext.(map[string]any); ok {
				texts = append(texts, stringVal(em["valueString"]))
			}
		}
	}
	for _, t := range texts {
		if t == "" {
			continue
		}
		lower := strings.ToLower(t)
		if strings.Contains(lower, "[inst]") ||
			strings.Contains(lower, "[/inst]") ||
			strings.Contains(lower, "<|im_start|>") ||
			strings.Contains(lower, "<|im_end|>") ||
			strings.Contains(lower, "<system>") ||
			strings.Contains(lower, "```system") ||
			strings.Contains(lower, "system prompt") ||
			strings.Contains(lower, "ignore previous instructions") ||
			strings.Contains(lower, "ignore all instructions") ||
			strings.Contains(lower, "override system") ||
			strings.Contains(lower, "admin_override:") ||
			strings.Contains(lower, "<script>") ||
			strings.Contains(lower, "eval(") {
			return true
		}
	}
	return false
}

var clinicalOnce sync.Once

// RegisterClinical registers the clinical_preview context section once per process.
func RegisterClinical() {
	clinicalOnce.Do(func() { approve.RegisterSection("clinical_preview", ClinicalMatches, BuildClinical) })
}
