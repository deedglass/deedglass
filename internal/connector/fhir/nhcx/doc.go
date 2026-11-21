// Package nhcx provides pre-submission verification and unbilled consumable
// reconciliation for India National Health Claims Exchange (NHCX) and ABDM FHIR R4
// claim bundles (NRCeS IG 6.5.0, ndhm.in#6.5.0).
//
// In accordance with Deedglass CLINICAL_SAFETY_BOUNDARY.md and Non-SaMD guarantees,
// this package performs purely administrative, structural, and financial validation.
// It never alters clinical diagnoses, scores clinical efficacy, or processes plaintext PHI.
package nhcx
