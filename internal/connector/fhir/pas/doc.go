// Package pas provides pre-submission verification and clinical order reconciliation
// for US Da Vinci Prior Authorization Support (PAS) and CMS-0057-F FHIR R4 request bundles
// (http://hl7.org/fhir/us/davinci-pas/StructureDefinition/).
//
// In accordance with Deedglass CLINICAL_SAFETY_BOUNDARY.md and Non-SaMD guarantees,
// this package performs purely administrative, structural, and financial validation.
// It never alters clinical diagnoses, scores clinical efficacy, or processes plaintext PHI.
package pas
