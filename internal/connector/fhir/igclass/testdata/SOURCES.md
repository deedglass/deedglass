# Sources of the fixtures in ndhm.in-6.5.0/

These are the published examples of the **FHIR Implementation Guide for ABDM, version 6.5.0**
(package `ndhm.in#6.5.0`, FHIR 4.0.1), published by the National Resource Center for EHR Standards
(NRCeS), C-DAC Pune, dated 2025-05-08. The guide carries both the ABDM health record profiles and
the NHCX profiles.

- Guide: https://nrces.in/ndhm/fhir/r4/index.html
- NHCX profiles: https://nrces.in/ndhm/fhir/r4/hcx-profile.html
- Each file: `https://nrces.in/ndhm/fhir/r4/<name>.json`, downloaded 2026-10-25.
- Licence: CC0-1.0, as the guide's own ImplementationGuide resource states
  (https://nrces.in/ndhm/fhir/r4/ImplementationGuide-ndhm.in.json, `"license": "CC0-1.0"`).
  The data in the examples is the guide's own illustrative data, not real patients.

Changes from the published files (CC0 allows them): attachment `data` strings longer than 512
characters are replaced by a short base64 placeholder, narrative `div`s longer than 4,096 characters
are replaced by a one-line div, and the JSON is written compactly. Nothing the classifier reads
(`resourceType`, `type`, `meta.profile`, `Composition.type` and `status`, `Claim.use` and `total`)
is changed. `sha256` below is of the file as published, so a fresh download can be compared.

A new guide version means: download its examples, trim them the same way into a new directory, point
the tests at it and update `igclass.IGVersion` and the profile tables.

| File | sha256 of the published file | Published bytes | Fixture bytes |
|---|---|---|---|
| `Bundle-ClaimBundle-enhancement-example-01.json` | `a77a9ff7792c4758779451bcbe8f2b627dd6aa6bcfc6e0fcf6e4eac01c775a4a` | 30808 | 14301 |
| `Bundle-ClaimBundle-preauthorization-example-01.json` | `3684ec8f60ac3818bbeb242510c8766cc82bcb6bf7ddfbda8859de8982603496` | 20541 | 11177 |
| `Bundle-ClaimBundle-predetermination-example-01.json` | `f001292df86a22fc28cf8e1aebc67815bf9090f732ca112184dd82b070d2677d` | 20541 | 11177 |
| `Bundle-ClaimBundle-settlement-example-01.json` | `7307c549035b1314d6e3d8104c1473e9deb40b03e62c185d89f8a191db950ec0` | 474983 | 18536 |
| `Bundle-ClaimResponseBundle-enhancement-example-01.json` | `cacbb2461f337c99c97ca093dd1e59859b12f75d46dbb04d2475ef3919f59efc` | 40486 | 17255 |
| `Bundle-ClaimResponseBundle-preauthorization-example-01.json` | `4a8805fc8f2b4e5fdf3ae5d2b65e11d074986609c5e87b2976f120c47ae28b43` | 130878 | 15107 |
| `Bundle-ClaimResponseBundle-predetermination-example-01.json` | `e3db785e4fed7374b1b4c06990a8962d615dddec8fa0c149e621f9eb1b942fd0` | 31807 | 14434 |
| `Bundle-ClaimResponseBundle-settlement-example-01.json` | `7028ed49cb59da4d5ef9e9356d630f9739eb46c43dcc64d981c1ca53ea616c58` | 584755 | 22203 |
| `Bundle-CoverageEligibilityRequestBundle-auth-requirement-example-01.json` | `bfa88194f536767d0578dd0e26155c39b1acea19d9cc37af1b6b401402e11e68` | 19405 | 15642 |
| `Bundle-CoverageEligibilityRequestBundle-benefits-example-01.json` | `ec4e3937eed3f8e5b0d105adba8d101daeaba641c24caeb613b04818eba7854e` | 19327 | 15556 |
| `Bundle-CoverageEligibilityRequestBundle-discovery-example-01.json` | `a8aaf1e406c0f374e4a1c70a358854b3fc98df1ef9c1a19876232ad6c621df3f` | 17265 | 13992 |
| `Bundle-CoverageEligibilityRequestBundle-validation-example-01.json` | `76501e44ff4273e1aa6d738e069e1672201b4e8ed128b9c7c6570f464e054090` | 17377 | 14110 |
| `Bundle-CoverageEligibilityResponseBundle-auth-requirements-example-01.json` | `6eb9eedf4ea96095099be62b311767608bcc41515f9b55695efb143bc9cc0320` | 23955 | 19442 |
| `Bundle-CoverageEligibilityResponseBundle-benefits-example-01.json` | `3cc322bdd5c12cefe37b227b023e477e3fb9da04906bc0649aead671a0639fa9` | 25577 | 20508 |
| `Bundle-CoverageEligibilityResponseBundle-discovery-example-01.json` | `137d7fe084ee862e3dc7245db50efc617e38111504101bcce690d747acaaa200` | 21783 | 17982 |
| `Bundle-CoverageEligibilityResponseBundle-validation-example-01.json` | `404787af814b869654e2615b1523652684e3d5fbea952d97195f8d029474e4b0` | 20996 | 17257 |
| `Bundle-DiagnosticReport-Imaging-DCM-example-01.json` | `fec4368afee76d9b7ac627d2063bf771542ff819e3a2baa08dbb59e2c1bb9c85` | 277013 | 20920 |
| `Bundle-DiagnosticReport-Imaging-Media-example-02.json` | `ab6458e53d8bca1fc164d9cb8ab7350eb6b5b10ed6d639ef07d0152bdf4203b5` | 273414 | 17957 |
| `Bundle-DiagnosticReport-Lab-example-03.json` | `eb81c626ab20ef2d5f1e7c28f2e9226c6566a0bd7d36ebd2979fb2b4237215b5` | 258181 | 24360 |
| `Bundle-DischargeSummary-example-04.json` | `1eda09978c498ac3f47511d0cc3ac39ccd3b9efeca8b25c3b7a4b1dc4e3e6003` | 572657 | 39318 |
| `Bundle-HealthDocumentRecord-example-01.json` | `2d606227936bf0f773ff8ce241a9e80934d5cafcdb967b4aec47768984646da8` | 426728 | 8101 |
| `Bundle-ImmunizationRecord-example-07.json` | `5139d398a30efebe8cb264b3309bd3e365ef7746e9768baa32701615da3e9bd5` | 435050 | 14781 |
| `Bundle-ImmunizationRecord-for-WHO-DDCC.json` | `cd91691d112e63ef476bc5d126eee7e43b5af3b866a3792c9f1de5f10f5b3055` | 439353 | 18276 |
| `Bundle-InsurancePlanBundle-example-01.json` | `ae5a8153c6183619d99fa409fd95653d8a45e9a09d3654ed121650310d4324a1` | 33103 | 11602 |
| `Bundle-InvoiceRecord-example-01.json` | `0659766017238a0a1be38da757c000c31698971edd833201745a194feb3cc451` | 48596 | 28158 |
| `Bundle-OPConsultNote-example-05.json` | `ee55812516a81fe9e0a1a735fbbc099ea94fdf998777e0c73c5a8b017acc5642` | 455839 | 30896 |
| `Bundle-Prescription-example-06.json` | `773f331773e8eb41a7e515770dbda1889ee55288886a061a2d9d04613b90f1dc` | 137424 | 13485 |
| `Bundle-TaskBundleForCommunicationRequest-example-01.json` | `53ff2b9bb8db3d1ce581115685128b6ee508f72b5f8ac92baa0817af6e24764f` | 26532 | 16195 |
| `Bundle-TaskBundleForCommunicationResponse-example-01.json` | `6ee309ef6ac31af6be9d6c28280381fac6195f811c69dca0eb5b09721e37291d` | 244450 | 17082 |
| `Bundle-TaskBundleForInsurancePlanRequest-example-01.json` | `06c0213611be80212c68cfcade5fff97c14d9cbb2bb9c3996033a513778d03a0` | 7718 | 6131 |
| `Bundle-TaskBundleForPaymentNoticeRequest-example-01.json` | `5bb3676cb72b7b75e757fbf50eb2466115d6c18beca72792d576923cede86642` | 16191 | 13070 |
| `Bundle-TaskBundleForPaymentNoticeResponse-example-01.json` | `b089ea551bd62fc7a822987fd1dbd94d3fdad829aae0872cbd00e411f6e6d247` | 7529 | 5998 |
| `Bundle-TaskBundleForReprocessRequest-example-01.json` | `2dc5c04902d4baaea79a5331369dcbd0237d329e99e8a78303cc6bc9ec150f9c` | 7639 | 6110 |
| `Bundle-TaskBundleForReprocessResponse-example-01.json` | `78d93f79c6696ae1331c0fed584b5a231a7699f25b0756c1d4e0bc40b9c1cb50` | 587557 | 24538 |
| `Bundle-TaskBundleForSearchRequest-example-01.json` | `11a78e9d94efc2165e3e20cfd1017cd0020d0829771ec1e0d288ed8fbff1f709` | 7179 | 5732 |
| `Bundle-TaskBundleForSearchResponse-example-01.json` | `aee9b4d1e18f5a5224fbe6e6d97f7a60fc389962d1a698e7f3915635a036cfab` | 587467 | 24514 |
| `Bundle-WellnessRecord-example-01.json` | `6894e364c728f02b49d5ac55ee03603d187966b5c18cf3dcb81f9f989b37c3fc` | 466789 | 50572 |
| `Claim-Claim-enhancement-example-01.json` | `d1db35e7a4295352e177d8941cc8225188cc843b5d83a54e21241699c8f74129` | 9245 | 2786 |
| `Claim-Claim-preauthorization-example-01.json` | `9e9fcbaa394ff29b9f81727c6d5c33d5dfdb06f504153e4cc74cc4cbaf2cb53c` | 9264 | 2783 |
| `Claim-Claim-predetermination-example-01.json` | `94702c952847228edd031d741aa77fbe1aa3433580ed6e9d206e5b0d96b82367` | 9264 | 2783 |
| `Claim-Claim-settlement-example-01.json` | `6e978a819754d02ceaa6b8364c0b8af07875cb76f0ad2fa4361d809397cfe8be` | 443227 | 4014 |
