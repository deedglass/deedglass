// Licensed under ee/LICENSE (FSL-1.1-ALv2).
package scribeeval

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
)

var (
	ErrNilEvent                    = errors.New("scribe-eval: nil event")
	ErrDisallowedField             = errors.New("scribe-eval: disallowed field or free-text present")
	ErrPHISentinelMatch            = errors.New("scribe-eval: PHI regex sentinel triggered")
	ErrLengthExceeded              = errors.New("scribe-eval: string field exceeds character limit")
	ErrPromptInjectionDetected     = errors.New("scribe-eval: indirect prompt injection detected (CH-26)")
	ErrInvalidISOWeek              = errors.New("scribe-eval: invalid ISO week format (expected YYYY-Www)")
	ErrInvalidEnum                 = errors.New("scribe-eval: invalid enum field value")
	ErrUnknownFields               = errors.New("scribe-eval: closed schema violation (unknown properties present)")
	ErrIncompleteTransactionBundle = errors.New("scribe-eval: composite bundle atomicity violation (CH-20)")
	ErrPromptInjectionQuarantine   = errors.New("scribe-eval: prompt injection quarantine triggered (CH-39)")
	ErrEdgeQueueConflict           = errors.New("scribe-eval: edge queue split-brain conflict detected (CH-38)")
)

// Regex sentinels for detecting identifiers across all incoming fields.
var (
	reAadhaar      = regexp.MustCompile(`(?:^|\b)[2-9]{1}[0-9]{3}\s?[0-9]{4}\s?[0-9]{4}(?:\b|$)`)
	reABHA         = regexp.MustCompile(`(?:^|\b)[0-9]{2}-[0-9]{4}-[0-9]{4}-[0-9]{4}(?:\b|$)`)
	reNHS          = regexp.MustCompile(`(?:^|\b)[0-9]{3}\s?[0-9]{3}\s?[0-9]{4}(?:\b|$)`)
	reSSN          = regexp.MustCompile(`(?:^|\b)[0-9]{3}-[0-9]{2}-[0-9]{4}(?:\b|$)`)
	reEmail        = regexp.MustCompile(`[a-zA-Z0-9._%+-]+@[a-zA-Z0-9.-]+\.[a-zA-Z]{2,}`)
	rePhone        = regexp.MustCompile(`(?:\+?91[-.\s]?)?[6-9]\d{4}[-.\s]?\d{5}|(?:\+?\d{1,3}[-.\s]?)?\(?\d{3}\)?[-.\s]?\d{3}[-.\s]?\d{4}`)
	reMRN          = regexp.MustCompile(`(?i)\b(?:mrn|patient[_-]?id|abha[_-]?id|ssn|aadhaar)\b[:=\s]*[a-zA-Z0-9_-]+`)
	reISOWeek      = regexp.MustCompile(`^[0-9]{4}-W(0[1-9]|[1-4][0-9]|5[0-3])$`)
	rePromptInject = regexp.MustCompile(`(?i)(?:ignore\s+previous\s+instructions|system\s+prompt|override\s+system|<script>|<\/script>|<system>|eval\(|drop\s+table|delete\s+from|discard\s+rules)`)

	// Secondary prompt injection patterns in multi-agent handoffs (CH-39)
	reDelimiterEscape   = regexp.MustCompile("(?i)(?:\\[INST\\]|\\[/INST\\]|<\\|im_start\\|>|<\\|im_end\\|>|<system>|</system>|\\x60{3}system|\\x60{3}override)")
	reSystemOverride    = regexp.MustCompile(`(?i)(?:ignore\s+(?:previous|all)\s+instructions|system\s+prompt|override\s+system|disregard\s+(?:previous|above|all)|you\s+are\s+now\s+in\s+maintenance)`)
	reUngroundedCommand = regexp.MustCompile(`(?i)(?:ADMIN_OVERRIDE:|EXEC_TOOL:|<script>|</script>|eval\(|drop\s+table|delete\s+from)`)
)

// Allowed enumeration values.
var (
	validRegions = map[string]bool{
		"IN": true, "US": true, "UK": true, "SG": true, "UAE": true,
	}
	validCareSettings = map[string]bool{
		"emergency": true, "outpatient": true, "inpatient": true, "dental_os": true,
	}
	validRoleBuckets = map[string]bool{
		"attending": true, "resident": true, "nurse": true, "unspecified": true,
	}
	validCodeSystems = map[string]bool{
		"RxNorm": true, "SNOMED-CT": true, "ICD-10": true, "CPT": true, "LOINC": true, "NHCX-Local": true,
	}
	validOrigins = map[Origin]bool{
		OriginSyntheticStudy1: true, OriginIECShadowStudy2: true,
	}
	validVerdicts = map[WitnessVerdict]bool{
		VerdictL2ProvenMatch: true, VerdictL1CheckedMatch: true,
		VerdictMismatch: true, VerdictUnverifiable: true,
	}
	validDeltaClasses = map[DeltaClass]bool{
		DeltaNoChange: true, DeltaDoseEdit: true, DeltaFrequencyEdit: true,
		DeltaRouteEdit: true, DeltaCodeEdit: true, DeltaAllergyAdded: true,
		DeltaConsumableAdded: true, DeltaRejectedDraft: true, DeltaSilentDropCaught: true,
		DeltaAudioTruncated: true, DeltaUngroundedOrder: true, DeltaNegationCorrection: true,
		DeltaEdgeQueueConflict: true, DeltaPromptInjectionFlag: true,
	}
	validEdgeQueueActions = map[string]bool{
		"QUEUED_FOR_CLINICIAN_DIFF": true,
		"REJECTED_STALE_SEQUENCE":   true,
		"COMMITTED_L1_CHECKED":      true,
	}
	validPromptInjectionActions = map[string]bool{
		"QUARANTINED_FOR_CLINICIAN_REVIEW": true,
		"PASSED_CLEAN":                     true,
	}
	validPatternClassifications = map[string]bool{
		"":                   true,
		"NONE":               true,
		"DELIMITER_ESCAPE":   true,
		"SYSTEM_OVERRIDE":    true,
		"UNGROUNDED_COMMAND": true,
	}
	validRoleCategories = map[string]bool{
		"attending": true, "resident": true, "fellow": true,
		"triage_nurse": true, "respiratory_therapist": true, "unspecified": true,
	}
	validUCUMUnits = map[string]bool{
		"mg": true, "g": true, "kg": true, "ug": true, "mcg": true,
		"mL": true, "L": true, "mg/dL": true, "mmol/L": true,
		"tab": true, "cap": true, "puff": true, "drop": true, "{dose}": true,
	}
)

// Sanitizer validates and cleanses raw candidate telemetry into an immutable EvalEvent.
type Sanitizer interface {
	ValidateAndSanitize(candidate *EvalEvent) error
	ValidateJSONClosed(raw []byte) (*EvalEvent, error)
}

// DefaultSanitizer implements the standard Safe-Harbor sanitization pipeline.
type DefaultSanitizer struct{}

// ValidateAndSanitize executes strict de-identification, sentinel scanning, and field bounding.
func (s *DefaultSanitizer) ValidateAndSanitize(e *EvalEvent) error {
	if e == nil {
		return ErrNilEvent
	}

	// Schema version must match exactly
	if e.SchemaVersion == "" {
		e.SchemaVersion = "scribe-eval-event/v1"
	} else if e.SchemaVersion != "scribe-eval-event/v1" {
		return fmt.Errorf("%w: unsupported schema_version %q", ErrInvalidEnum, e.SchemaVersion)
	}

	// Enforce character length limits
	if len(e.ConceptDisplayCanonical) > 64 {
		return fmt.Errorf("%w: concept_display_canonical exceeds 64 chars (%d)", ErrLengthExceeded, len(e.ConceptDisplayCanonical))
	}
	if len(e.ConceptCode) > 32 {
		return fmt.Errorf("%w: concept_code exceeds 32 chars (%d)", ErrLengthExceeded, len(e.ConceptCode))
	}
	if len(e.ScribeAdapterFamily) > 40 {
		return fmt.Errorf("%w: scribe_adapter_family exceeds 40 chars (%d)", ErrLengthExceeded, len(e.ScribeAdapterFamily))
	}
	if len(e.FHIRResourceType) > 32 {
		return fmt.Errorf("%w: fhir_resource_type exceeds 32 chars (%d)", ErrLengthExceeded, len(e.FHIRResourceType))
	}

	// Scan all string fields against PHI sentinels & prompt injection patterns
	stringsToScan := []string{
		e.ConceptCode,
		e.ConceptDisplayCanonical,
		e.ScribeAdapterFamily,
		e.FHIRResourceType,
		e.RegionJurisdiction,
		e.CareSetting,
		e.ClinicianRoleBucket,
	}
	if e.ScribeProposedStruct.DoseUnit != nil {
		stringsToScan = append(stringsToScan, *e.ScribeProposedStruct.DoseUnit)
	}
	if e.ClinicianFinalStruct.DoseUnit != nil {
		stringsToScan = append(stringsToScan, *e.ClinicianFinalStruct.DoseUnit)
	}
	if e.ScribeProposedStruct.FrequencyCode != nil {
		stringsToScan = append(stringsToScan, *e.ScribeProposedStruct.FrequencyCode)
	}
	if e.ClinicianFinalStruct.FrequencyCode != nil {
		stringsToScan = append(stringsToScan, *e.ClinicianFinalStruct.FrequencyCode)
	}
	if e.RoleAttribution != nil {
		if e.RoleAttribution.OrderingRoleCategory != "" {
			if len(e.RoleAttribution.OrderingRoleCategory) > 32 {
				return fmt.Errorf("%w: ordering_role_category exceeds 32 chars", ErrLengthExceeded)
			}
			if !validRoleCategories[e.RoleAttribution.OrderingRoleCategory] {
				return fmt.Errorf("%w: invalid ordering_role_category %q", ErrInvalidEnum, e.RoleAttribution.OrderingRoleCategory)
			}
			stringsToScan = append(stringsToScan, e.RoleAttribution.OrderingRoleCategory)
		}
		if e.RoleAttribution.ApprovingRoleCategory != "" {
			if len(e.RoleAttribution.ApprovingRoleCategory) > 32 {
				return fmt.Errorf("%w: approving_role_category exceeds 32 chars", ErrLengthExceeded)
			}
			if !validRoleCategories[e.RoleAttribution.ApprovingRoleCategory] {
				return fmt.Errorf("%w: invalid approving_role_category %q", ErrInvalidEnum, e.RoleAttribution.ApprovingRoleCategory)
			}
			stringsToScan = append(stringsToScan, e.RoleAttribution.ApprovingRoleCategory)
		}
	}

	for _, text := range stringsToScan {
		if text == "" {
			continue
		}
		// CH-26: Check for indirect prompt injection attempts
		if rePromptInject.MatchString(text) {
			e.PromptInjectionFlag = true
			return ErrPromptInjectionDetected
		}
		// Safe-Harbor PHI pattern scanning
		if reAadhaar.MatchString(text) ||
			reABHA.MatchString(text) ||
			reNHS.MatchString(text) ||
			reSSN.MatchString(text) ||
			reEmail.MatchString(text) ||
			rePhone.MatchString(text) ||
			reMRN.MatchString(text) {
			return ErrPHISentinelMatch
		}
	}

	// Validate enumeration invariants
	if !validRegions[e.RegionJurisdiction] {
		return fmt.Errorf("%w: unknown region_jurisdiction %q", ErrInvalidEnum, e.RegionJurisdiction)
	}
	if !validCareSettings[e.CareSetting] {
		return fmt.Errorf("%w: unknown care_setting %q", ErrInvalidEnum, e.CareSetting)
	}
	if !validRoleBuckets[e.ClinicianRoleBucket] {
		return fmt.Errorf("%w: unknown clinician_role_bucket %q", ErrInvalidEnum, e.ClinicianRoleBucket)
	}
	if !validCodeSystems[e.CodeSystem] {
		return fmt.Errorf("%w: unknown code_system %q", ErrInvalidEnum, e.CodeSystem)
	}
	if !validOrigins[e.DataOrigin] {
		return fmt.Errorf("%w: unknown data_origin %q", ErrInvalidEnum, e.DataOrigin)
	}
	if !validVerdicts[e.WitnessVerdict] {
		return fmt.Errorf("%w: unknown witness_verdict %q", ErrInvalidEnum, e.WitnessVerdict)
	}
	if !validDeltaClasses[e.DeltaClass] {
		return fmt.Errorf("%w: unknown delta_class %q", ErrInvalidEnum, e.DeltaClass)
	}

	// Validate ISO week format (YYYY-Www)
	if !reISOWeek.MatchString(e.ISOWeek) {
		return fmt.Errorf("%w: %q", ErrInvalidISOWeek, e.ISOWeek)
	}

	// Ensure cryptographically random UUIDv4 event identifier
	if e.EvalEventID == "" {
		uuid, err := generateUUIDv4()
		if err != nil {
			return err
		}
		e.EvalEventID = uuid
	} else {
		if err := validateUUIDv4(e.EvalEventID); err != nil {
			return fmt.Errorf("scribe-eval: invalid eval_event_id %q: %w", e.EvalEventID, err)
		}
	}

	// Hazard CH-27: Detect clinical negation flip/omission divergence
	if e.ScribeProposedStruct.NegationFlag != nil && e.ClinicianFinalStruct.NegationFlag != nil {
		if *e.ScribeProposedStruct.NegationFlag != *e.ClinicianFinalStruct.NegationFlag {
			e.NegationMismatchFlag = true
			if e.DeltaClass == DeltaNoChange {
				e.DeltaClass = DeltaSilentDropCaught
			}
			e.SilentDivergenceDetected = true
		}
	}

	// Hazard CH-28: UCUM unit syntax standardization & divergence check
	if e.ScribeProposedStruct.DoseUnit != nil && e.ClinicianFinalStruct.DoseUnit != nil {
		propUnit := strings.TrimSpace(*e.ScribeProposedStruct.DoseUnit)
		clinUnit := strings.TrimSpace(*e.ClinicianFinalStruct.DoseUnit)
		if propUnit != "" && !validUCUMUnits[propUnit] {
			e.UCUMMismatchFlag = true
		}
		if clinUnit != "" && !validUCUMUnits[clinUnit] {
			e.UCUMMismatchFlag = true
		}
		if propUnit != clinUnit {
			e.UCUMMismatchFlag = true
			if e.DeltaClass == DeltaNoChange {
				e.DeltaClass = DeltaDoseEdit
			}
		}
	}

	// Hazard CH-29: Audio truncation & dropout handling
	if e.AudioContinuity != nil {
		if e.AudioContinuity.AbruptTruncationDetected || e.AudioContinuity.ClosingTagMissing {
			if e.AudioContinuity.IncompleteDraftSuppressed {
				// Suppress draft slots to prevent partial order leakage
				e.ScribeProposedStruct = StructuredSlot{Present: false}
				e.ClinicianFinalStruct = StructuredSlot{Present: false}
			}
			if e.DeltaClass == DeltaNoChange {
				e.DeltaClass = DeltaAudioTruncated
			}
		}
	}

	// Hazard CH-30: Administrative coding drift & upcoding detection
	if e.CodingGrounding != nil {
		if e.CodingGrounding.UngroundedCodesCount > 0 {
			e.CodingGrounding.UpcodingRiskFlag = true
		}
	}

	// Hazard CH-31: Multi-speaker attending attribution & role alignment
	if e.RoleAttribution != nil {
		if e.RoleAttribution.OrderingRoleCategory != "" && e.RoleAttribution.ApprovingRoleCategory != "" {
			if e.RoleAttribution.OrderingRoleCategory == e.RoleAttribution.ApprovingRoleCategory {
				e.RoleAttribution.RoleAlignmentConfirmed = true
			}
		}
	}

	// Hazard CH-38: Offline-first edge queue split-brain & reconciliation handling
	if e.EdgeQueueReconciliation != nil {
		eq := e.EdgeQueueReconciliation
		if eq.VersionConflictDetected {
			if !eq.ClinicianDiffReviewed {
				eq.EnforcementAction = "QUEUED_FOR_CLINICIAN_DIFF"
				if e.DeltaClass == DeltaNoChange {
					e.DeltaClass = DeltaEdgeQueueConflict
				}
				e.SilentDivergenceDetected = true
			} else {
				eq.EnforcementAction = "COMMITTED_L1_CHECKED"
			}
		} else if eq.EnforcementAction == "" {
			eq.EnforcementAction = "COMMITTED_L1_CHECKED"
		}
		if !validEdgeQueueActions[eq.EnforcementAction] {
			return fmt.Errorf("%w: invalid edge_queue enforcement_action %q", ErrInvalidEnum, eq.EnforcementAction)
		}
	}

	// Hazard CH-39: Multi-agent secondary prompt injection detection & quarantine
	if e.PromptInjectionQuarantine != nil {
		pi := e.PromptInjectionQuarantine
		if !validPatternClassifications[pi.PatternClassification] {
			return fmt.Errorf("%w: invalid pattern_classification %q", ErrInvalidEnum, pi.PatternClassification)
		}
		if pi.QuarantineTriggered || (pi.PatternClassification != "" && pi.PatternClassification != "NONE") {
			pi.QuarantineTriggered = true
			pi.EnforcementAction = "QUARANTINED_FOR_CLINICIAN_REVIEW"
			e.PromptInjectionFlag = true
			if e.DeltaClass == DeltaNoChange {
				e.DeltaClass = DeltaPromptInjectionFlag
			}
		} else if pi.EnforcementAction == "" {
			pi.EnforcementAction = "PASSED_CLEAN"
		}
		if !validPromptInjectionActions[pi.EnforcementAction] {
			return fmt.Errorf("%w: invalid prompt_injection enforcement_action %q", ErrInvalidEnum, pi.EnforcementAction)
		}
	}

	return nil
}

// DetectSecondaryPromptInjection inspects text for indirect prompt injection markers (CH-39).
func DetectSecondaryPromptInjection(text string) (bool, string) {
	if text == "" {
		return false, ""
	}
	if reDelimiterEscape.MatchString(text) {
		return true, "DELIMITER_ESCAPE"
	}
	if reSystemOverride.MatchString(text) {
		return true, "SYSTEM_OVERRIDE"
	}
	if reUngroundedCommand.MatchString(text) {
		return true, "UNGROUNDED_COMMAND"
	}
	return false, ""
}

// ValidateJSONClosed parses a raw JSON payload with strict unknown field rejection.
func (s *DefaultSanitizer) ValidateJSONClosed(raw []byte) (*EvalEvent, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()

	var event EvalEvent
	if err := dec.Decode(&event); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnknownFields, err)
	}
	if dec.More() {
		return nil, fmt.Errorf("%w: trailing characters", ErrUnknownFields)
	}
	if err := s.ValidateAndSanitize(&event); err != nil {
		return nil, err
	}
	return &event, nil
}

// generateUUIDv4 generates an RFC 4122 version 4 UUID using crypto/rand.
func generateUUIDv4() (string, error) {
	var buf [16]byte
	if _, err := io.ReadFull(rand.Reader, buf[:]); err != nil {
		return "", fmt.Errorf("scribe-eval: crypto rand error: %w", err)
	}
	buf[6] = (buf[6] & 0x0f) | 0x40 // Version 4
	buf[8] = (buf[8] & 0x3f) | 0x80 // Variant RFC 4122
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		buf[0:4], buf[4:6], buf[6:8], buf[8:10], buf[10:16]), nil
}

// validateUUIDv4 ensures an existing event ID conforms to RFC 4122 UUIDv4 structure.
func validateUUIDv4(id string) error {
	if len(id) != 36 {
		return errors.New("must be 36 characters")
	}
	if id[8] != '-' || id[13] != '-' || id[18] != '-' || id[23] != '-' {
		return errors.New("invalid hyphen separator positions")
	}
	if id[14] != '4' {
		return errors.New("not version 4")
	}
	var b8 byte
	switch id[19] {
	case '8', '9', 'a', 'b', 'A', 'B':
		b8 = id[19]
	default:
		return errors.New("invalid variant bits")
	}
	_ = b8
	return nil
}
