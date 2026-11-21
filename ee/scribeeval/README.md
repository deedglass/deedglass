# `ee/scribeeval`: Safe-Harbor Scribe Evaluation & Research Telemetry Pipeline

**License:** Functional Source License 1.1 (`ee/LICENSE`, `FSL-1.1-ALv2`)  
**Specification Anchor:** [`ops/healthcare/SCRIBE_EVAL_SAFE_HARBOR_SPEC.md`](../../../ops/healthcare/SCRIBE_EVAL_SAFE_HARBOR_SPEC.md)  
**Safety & Regulatory Boundary:** [`ops/company/CLINICAL_SAFETY_BOUNDARY.md`](../../../ops/company/CLINICAL_SAFETY_BOUNDARY.md)  

---

## 1. Overview & Multiple-Function Device Separation

`ee/scribeeval` is the research and evaluation telemetry pipeline designed for:
1. **Study 1 Synthetic Benchmarks:** Measuring divergence rates across ambient AI scribes (such as the MAHE/MIT AI Scribe) against physical storage witnesses on synthetic datasets (`OriginSyntheticStudy1`).
2. **Study 2 Institutional Ethics Committee (IEC) Shadow Audits:** Prospective non-interventional write-integrity observation on hospital campus servers (`OriginIECShadowStudy2`).

Strictly segregated outside the core Apache-2.0 runtime (`gate`, `witness`, `deed`), `ee/scribeeval` operates out-of-band and is disabled by default. It activates exclusively through explicit institutional research opt-in.

---

## 2. Statutory Non-SaMD & Safe-Harbor Boundaries

- **Zero Clinical Decision Support (Non-SaMD):** Captures only structural edit pairs (`scribe_proposed_struct` vs `clinician_final_struct`) and storage-witness verdicts. Never diagnoses, recommends clinical interventions, or ranks patient severity.
- **HIPAA Safe Harbor (45 CFR §164.514(b)(2)) & DPDP Rules 2025:**
  - Automated regex sentinel rejection for Aadhaar, ABHA, NHS Number, SSN, email, phone, and MRN patterns.
  - Strict length limits: concept codes $\le 32$ chars, canonical displays $\le 64$ chars.
  - Coarse time quantization: wall-clock timestamps are replaced with `iso_week` (`YYYY-Www`).
  - Zero free-text narratives or raw transcripts stored or exported.
- **Volatile RAM Ring Buffer:** Transient candidate events reside in an in-memory ring buffer (512 slots) and undergo memory zeroization (`memclr`) upon eviction, timeout (300s), or release.
- **Cohort Suppression Engine ($k \ge 11, c \ge 5$):** Aggregates research exports into `scribe-eval-cohort/v1` JSON Lines streams. Any cell with fewer than 11 events or 5 distinct clinicians is replaced with `[SUPPRESSED]` to eliminate re-identification risk.

---

## 3. Hazard Controls

- **`CH-20` Multi-Resource Bundle Atomicity:** Composite encounter bundles (e.g. SOAP notes creating multiple resources) must validate and verify atomically. If any member resource diverges (`MISMATCH` or `UNVERIFIABLE`), the entire bundle fails closed with `ErrIncompleteTransactionBundle`.
- **`CH-26` Indirect Prompt Injection:** String inputs are screened against prompt injection heuristics (`ignore previous instructions`, `system prompt`, `<script>`). Detected attempts fail closed with `ErrPromptInjectionDetected`.
- **`CH-27` Clinical Negation Flip / Omission:** Automatic comparison between proposed and final negation flags catches silent drops of allergy negations or clinical contraindications.
- **`CH-28` UCUM Unit Divergence:** Standardizes Unified Code for Units of Measure (UCUM) syntax and identifies unit discrepancies (e.g. `mg` to `mL`).
