# Export: Tabular CSV Audit Trail

`deedglass export --format csv` writes an RFC 4180 tabular CSV file summarizing sealed records.
Designed for clinical governance committees, hospital quality directors, and NABH / HIPAA §164.312(b)
audit trail inspections without requiring SIEM software. Overview: [EXPORTS.md](../EXPORTS.md).

## Columns

| Column | Type | Description |
|---|---|---|
| `seq` | integer | Monotonic sequence number in the org's ledger |
| `issued_at` | RFC 3339 string | ISO timestamp when the record was sealed |
| `deed_id` | string | Unique deed identifier (`ded_...`) |
| `kind` | string | Record type (`action`, `admin`, `amendment`) |
| `org` | string | Organization identifier (`org_...`) |
| `agent_id` | string | Agent identifier (`agt_...`) |
| `agent_name` | string | Agent display name |
| `tool` | string | Invoked tool (`connector.operation`) |
| `decision` | string | Policy decision (`allow`, `deny`, `require_approval`) |
| `rule_id` | string | Matching policy rule identifier |
| `status` | string | Execution outcome (`succeeded`, `failed`, or empty if blocked) |
| `error_code` | string | Error code when execution failed |
| `latency_ms` | integer | Execution duration in milliseconds |
| `witness_level` | string | Physical verification tier (`L1`, `L2`, `NONE`) |
| `witness_verdict` | string | Verification result (`MATCH`, `MISMATCH`, `UNVERIFIABLE`) |
| `cost_currency` | string | ISO currency code (e.g. `INR`, `USD`) |
| `cost_amount` | integer | Associated action cost in minor currency units |
| `on_behalf_of` | string | Delegating user identity |
| `session` | string | Agent session / workflow ID |
| `amends` | string | Prior deed ID when amending a record |
| `seal_key_id` | string | Ed25519 signing key identifier |

Zero plaintext PHI is emitted in the CSV export: only identifiers, rule IDs, and verification statuses.
