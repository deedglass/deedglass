// Package sections holds approval context sections (S40, Ward Light): clinical_preview, what a
// clinician reads before countersigning an ambient AI scribe or EHR write. Pure: it reads the action's
// arguments and policy facts, never the network, and stays strictly under approve.MaxSectionBytes (8 KiB).
package sections

import (
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
	V                int             `json:"v"`
	ResourceType     string          `json:"resource_type"`
	ResourceID       string          `json:"resource_id,omitempty"`
	ActionLabel      string          `json:"action_label"`
	Status           string          `json:"status,omitempty"`
	Intent           string          `json:"intent,omitempty"`
	PatientRef       string          `json:"patient_ref,omitempty"`
	PatientDisplay   string          `json:"patient_display,omitempty"`
	EHRDeepLink      string          `json:"ehr_deep_link,omitempty"`
	TranscriptAnchor string          `json:"transcript_anchor,omitempty"`
	ScribeVendor     string          `json:"scribe_vendor,omitempty"`
	Fields           []ClinicalField `json:"fields"`
	IsBreakGlass     bool            `json:"is_break_glass,omitempty"`
	EmergencyReason  string          `json:"emergency_reason,omitempty"`
	AuditNotice      string          `json:"audit_notice,omitempty"`
	Summary          string          `json:"summary"`
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

	fields := buildClinicalFields(resType, raw, res)

	actionLabel := describeAction(resType, in.Request.FHIR.Interaction, isBreakGlass)

	status := stringVal(res["status"])
	intent := stringVal(res["intent"])

	p := ClinicalPreview{
		V:                ClinicalPreviewVersion,
		ResourceType:     resType,
		ResourceID:       resID,
		ActionLabel:      actionLabel,
		Status:           status,
		Intent:           intent,
		PatientRef:       patientRef,
		PatientDisplay:   patientDisplay,
		EHRDeepLink:      ehrLink,
		TranscriptAnchor: transcript,
		ScribeVendor:     scribeVendor,
		Fields:           fields,
		IsBreakGlass:     isBreakGlass,
		EmergencyReason:  emerReason,
		AuditNotice:      auditNotice,
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

func describeAction(resType, inter string, breakGlass bool) string {
	prefix := ""
	if breakGlass {
		prefix = "Emergency Break-Glass "
	}
	switch resType {
	case "MedicationRequest":
		return prefix + "Medication Order Proposal"
	case "Observation":
		return prefix + "Record Clinical Observation"
	case "AllergyIntolerance":
		return prefix + "Record Allergy Intolerance"
	case "Claim":
		return prefix + "NHCX Claim Bundle Submission"
	default:
		if inter != "" {
			return prefix + "Write " + resType + " (" + inter + ")"
		}
		return prefix + "Write " + resType
	}
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

var clinicalOnce sync.Once

// RegisterClinical registers the clinical_preview context section once per process.
func RegisterClinical() {
	clinicalOnce.Do(func() { approve.RegisterSection("clinical_preview", ClinicalMatches, BuildClinical) })
}
