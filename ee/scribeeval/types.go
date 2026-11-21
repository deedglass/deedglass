// Package scribeeval implements the de-identified scribe evaluation and research
// telemetry pipeline for Study 1 synthetic benchmarks and IEC Study 2 shadow audits.
//
// It lives strictly outside the core Apache-2.0 runtime gate (Multiple-Function Device separation),
// enforces strict Safe-Harbor stripping with zero plaintext PHI retention, volatile RAM ring-buffer
// lifecycle with zeroization on release, k >= 11 and c >= 5 cohort cell suppression, and closed
// JSON schemas (scribe-eval-event/v1 and scribe-eval-cohort/v1).
//
// Licensed under ee/LICENSE (FSL-1.1-ALv2).
package scribeeval

// Origin designates whether the event derives from synthetic runs or approved shadow audits.
type Origin string

const (
	OriginSyntheticStudy1 Origin = "synthetic_study1"
	OriginIECShadowStudy2 Origin = "iec_shadow_study2"
)

// DeltaClass categorizes clinician modifications made during Ward Light review.
type DeltaClass string

const (
	DeltaNoChange            DeltaClass = "no_change"
	DeltaDoseEdit            DeltaClass = "dose_edit"
	DeltaFrequencyEdit       DeltaClass = "frequency_edit"
	DeltaRouteEdit           DeltaClass = "route_edit"
	DeltaCodeEdit            DeltaClass = "code_edit"
	DeltaAllergyAdded        DeltaClass = "allergy_added"
	DeltaConsumableAdded     DeltaClass = "consumable_added"
	DeltaRejectedDraft       DeltaClass = "rejected_draft"
	DeltaSilentDropCaught    DeltaClass = "silent_drop_caught"
	DeltaAudioTruncated      DeltaClass = "audio_truncated"
	DeltaUngroundedOrder     DeltaClass = "ungrounded_order_rejected"
	DeltaNegationCorrection  DeltaClass = "negation_correction"
	DeltaEdgeQueueConflict          DeltaClass = "edge_queue_conflict"
	DeltaPromptInjectionFlag        DeltaClass = "prompt_injection_flagged"
	DeltaBulkExportQuarantined      DeltaClass = "bulk_export_quarantined"
	DeltaCarePlanConflictHarmonized DeltaClass = "care_plan_conflict_harmonized"
)

// WitnessVerdict records physical persistence verification outcome.
type WitnessVerdict string

const (
	VerdictL2ProvenMatch  WitnessVerdict = "L2_PROVEN_MATCH"
	VerdictL1CheckedMatch WitnessVerdict = "L1_CHECKED_MATCH"
	VerdictMismatch       WitnessVerdict = "MISMATCH"
	VerdictUnverifiable   WitnessVerdict = "UNVERIFIABLE"
)

// AudioContinuityTelemetry tracks ambient recording continuity and abrupt dropouts (CH-29).
type AudioContinuityTelemetry struct {
	SessionDurationSec        uint32 `json:"session_duration_sec"`
	AbruptTruncationDetected  bool   `json:"abrupt_truncation_detected"`
	ClosingTagMissing         bool   `json:"closing_tag_missing"`
	IncompleteDraftSuppressed bool   `json:"incomplete_draft_suppressed"`
}

// CodingGroundingTelemetry tracks ungrounded code synthesis and upcoding risk (CH-30).
type CodingGroundingTelemetry struct {
	TotalSuggestedCodes  uint16 `json:"total_suggested_codes"`
	GroundedCodesCount   uint16 `json:"grounded_codes_count"`
	UngroundedCodesCount uint16 `json:"ungrounded_codes_count"`
	UpcodingRiskFlag     bool   `json:"upcoding_risk_flag"`
}

// RoleAttributionTelemetry tracks clinician role categorization and review latency (CH-31).
type RoleAttributionTelemetry struct {
	OrderingRoleCategory   string `json:"ordering_role_category"`
	ApprovingRoleCategory  string `json:"approving_role_category"`
	RoleAlignmentConfirmed bool   `json:"role_alignment_confirmed"`
	AttributionReviewMs    uint32 `json:"attribution_review_ms"`
}

// EdgeQueueReconciliationTelemetry tracks edge write queue split-brain and sync duration (CH-38).
type EdgeQueueReconciliationTelemetry struct {
	PartitionDurationSeconds uint32 `json:"partition_duration_seconds"`
	BufferedResourceCount    uint16 `json:"buffered_resource_count"`
	VersionConflictDetected  bool   `json:"version_conflict_detected"`
	EnforcementAction        string `json:"enforcement_action"` // "QUEUED_FOR_CLINICIAN_DIFF", "REJECTED_STALE_SEQUENCE", "COMMITTED_L1_CHECKED"
	ReconciliationLatencyMs  uint32 `json:"reconciliation_latency_ms"`
	ClinicianDiffReviewed    bool   `json:"clinician_diff_reviewed"`
}

// PromptInjectionQuarantineTelemetry tracks prompt injection detection in FHIR payloads (CH-39).
type PromptInjectionQuarantineTelemetry struct {
	SourceAgentFamily     string `json:"source_agent_family"`  // e.g. "triage-intake-v1"
	TargetResourceType    string `json:"target_resource_type"` // e.g. "Observation", "DocumentReference"
	QuarantineTriggered   bool   `json:"quarantine_triggered"`
	PatternClassification string `json:"pattern_classification"` // "DELIMITER_ESCAPE", "SYSTEM_OVERRIDE", "UNGROUNDED_COMMAND"
	EnforcementAction     string `json:"enforcement_action"`     // "QUARANTINED_FOR_CLINICIAN_REVIEW", "PASSED_CLEAN"
	QuarantineLatencyUs   uint32 `json:"quarantine_latency_us"`
}

// BulkExportTelemetry tracks FHIR Bulk Data Export scope and institutional authorization (CH-40).
type BulkExportTelemetry struct {
	ExportOperationType    string   `json:"export_operation_type"`    // "GROUP_EXPORT", "PATIENT_EXPORT", "SYSTEM_EXPORT"
	TargetResourceTypes    []string `json:"target_resource_types"`  // e.g. ["Condition", "MedicationRequest"]
	CohortPatientCount     uint32   `json:"cohort_patient_count"`     // bounded count; suppressed if < 11
	InstitutionalDPAHash   string   `json:"institutional_dpa_hash"`   // SHA-256 digest of signed data processing agreement
	EnforcementAction      string   `json:"enforcement_action"`       // "AUTHORIZED_DISPATCH", "BLOCKED_UNSCOPED_COHORT", "PENDING_CISO_APPROVAL"
	ExportEvaluationTimeUs uint32   `json:"export_evaluation_time_us"`
}

// CarePlanConflictTelemetry tracks multi-agent care plan conflict and harmonization (CH-41).
type CarePlanConflictTelemetry struct {
	EncounterIDDigest        string `json:"encounter_id_digest"`        // SHA-256 digest of encounter identifier
	ConcurrentAgentCount     uint8  `json:"concurrent_agent_count"`     // count of agents modifying same plan
	ConflictingGoalDetected  bool   `json:"conflicting_goal_detected"`
	EnforcementAction        string `json:"enforcement_action"`         // "ROUTED_TO_WARD_LIGHT_HARMONIZATION", "COMMITTED_SEQUENTIAL", "REJECTED_STALE_LOCK"
	ClinicianHarmonizeTimeMs uint32 `json:"clinician_harmonize_time_ms"`
	FinalConsolidatedAction  string `json:"final_consolidated_action"`  // "ATTENDING_ACCEPTED_AGENT_A", "ATTENDING_ACCEPTED_AGENT_B", "ATTENDING_MANUAL_SYNTHESIS"
}

// StructuredSlot holds de-identified normalized clinical parameters (zero free text).
type StructuredSlot struct {
	Present           bool     `json:"present"`
	DoseValue         *float64 `json:"dose_value,omitempty"`
	DoseUnit          *string  `json:"dose_unit,omitempty"`
	FrequencyCode     *string  `json:"frequency_code,omitempty"`
	RouteCode         *string  `json:"route_code,omitempty"`
	Quantity          *float64 `json:"quantity,omitempty"`
	StatusCode        *string  `json:"status_code,omitempty"`
	NegationFlag      *bool    `json:"negation_flag,omitempty"`
	UCUMUnitCanonical *string  `json:"ucum_unit_canonical,omitempty"`
}

// EvalEvent represents an immutable, Safe-Harbor stripped telemetry record.
type EvalEvent struct {
	SchemaVersion             string                              `json:"schema_version"` // "scribe-eval-event/v1"
	EvalEventID               string                              `json:"eval_event_id"`  // crypto/rand UUIDv4
	DataOrigin                Origin                              `json:"data_origin"`
	ISOWeek                   string                              `json:"iso_week"`              // YYYY-Www
	RegionJurisdiction        string                              `json:"region_jurisdiction"`   // IN, US, UK, SG, UAE
	CareSetting               string                              `json:"care_setting"`          // emergency, outpatient, inpatient, dental_os
	ClinicianRoleBucket       string                              `json:"clinician_role_bucket"` // attending, resident, nurse, unspecified
	ScribeAdapterFamily       string                              `json:"scribe_adapter_family"`
	FHIRResourceType          string                              `json:"fhir_resource_type"`
	CodeSystem                string                              `json:"code_system"` // RxNorm, SNOMED-CT, ICD-10, CPT, LOINC, NHCX-Local
	ConceptCode               string                              `json:"concept_code"`
	ConceptDisplayCanonical   string                              `json:"concept_display_canonical,omitempty"`
	ScribeProposedStruct      StructuredSlot                      `json:"scribe_proposed_struct"`
	ClinicianFinalStruct      StructuredSlot                      `json:"clinician_final_struct"`
	DeltaClass                DeltaClass                          `json:"delta_class"`
	WitnessVerdict            WitnessVerdict                      `json:"witness_verdict"`
	SilentDivergenceDetected  bool                                `json:"silent_divergence_detected"`
	ReviewLatencyMS           int64                               `json:"review_latency_ms"`
	PromptInjectionFlag       bool                                `json:"prompt_injection_flag,omitempty"`
	NegationMismatchFlag      bool                                `json:"negation_mismatch_flag,omitempty"`
	UCUMMismatchFlag          bool                                `json:"ucum_mismatch_flag,omitempty"`
	AudioContinuity           *AudioContinuityTelemetry           `json:"audio_continuity,omitempty"`
	CodingGrounding           *CodingGroundingTelemetry           `json:"coding_grounding,omitempty"`
	RoleAttribution           *RoleAttributionTelemetry           `json:"role_attribution,omitempty"`
	EdgeQueueReconciliation   *EdgeQueueReconciliationTelemetry   `json:"edge_queue_reconciliation,omitempty"`
	PromptInjectionQuarantine *PromptInjectionQuarantineTelemetry `json:"prompt_injection_quarantine,omitempty"`
	BulkExport                *BulkExportTelemetry                `json:"bulk_export,omitempty"`
	CarePlanConflict          *CarePlanConflictTelemetry          `json:"care_plan_conflict,omitempty"`
}

// CohortCellKey identifies an aggregation cell for research export.
type CohortCellKey struct {
	ScribeAdapterFamily string     `json:"scribe_adapter_family"`
	CareSetting         string     `json:"care_setting"`
	ConceptCode         string     `json:"concept_code"`
	DeltaClass          DeltaClass `json:"delta_class"`
	RegionJurisdiction  string     `json:"region_jurisdiction"`
	FHIRResourceType    string     `json:"fhir_resource_type"`
}

// CohortExportEvent represents an aggregated, k-suppressed cell record (scribe-eval-cohort/v1).
type CohortExportEvent struct {
	SchemaVersion         string   `json:"schema_version"` // "scribe-eval-cohort/v1"
	CohortWindowISOWeek   string   `json:"cohort_window_iso_week"`
	RegionJurisdiction    string   `json:"region_jurisdiction"`
	CareSetting           string   `json:"care_setting"`
	ScribeAdapterFamily   string   `json:"scribe_adapter_family"`
	FHIRResourceType      string   `json:"fhir_resource_type"`
	CodeSystem            string   `json:"code_system"`
	ConceptCode           string   `json:"concept_code"`
	DeltaClass            string   `json:"delta_class"`
	EventCount            int      `json:"event_count"`
	DistinctClinicians    int      `json:"distinct_clinicians"`
	SuppressionApplied    bool     `json:"suppression_applied"`
	MedianReviewLatencyMS *int64   `json:"median_review_latency_ms"`
	P95ReviewLatencyMS    *int64   `json:"p95_review_latency_ms"`
	SilentDivergenceRate  *float64 `json:"silent_divergence_rate"`
}

// SuppressionSummary records cell reporting and suppression metrics.
type SuppressionSummary struct {
	CellsReported   int `json:"cells_reported"`
	CellsSuppressed int `json:"cells_suppressed"`
	KThreshold      int `json:"k_threshold"`
	CThreshold      int `json:"c_threshold"`
}

// CohortExportManifest encapsulates an aggregated export batch with offline cryptographic provenance.
type CohortExportManifest struct {
	Schema                string             `json:"$schema"`
	ManifestVersion       string             `json:"manifest_version"`
	ExportID              string             `json:"export_id"`
	GeneratedAtUTC        string             `json:"generated_at_utc"`
	InstitutionCode       string             `json:"institution_code"`
	DataOrigin            string             `json:"data_origin"`
	ISOWeekWindow         string             `json:"iso_week_window"`
	TotalAggregatedCells  int                `json:"total_aggregated_cells"`
	TotalUnderlyingEvents int                `json:"total_underlying_events"`
	SuppressionSummary    SuppressionSummary `json:"suppression_summary"`
	PayloadSHA256         string             `json:"payload_sha256"`
	SigningKeyID          string             `json:"signing_key_id"`
	SignatureAlgorithm    string             `json:"signature_algorithm"`
	Signature             string             `json:"signature"`
}
