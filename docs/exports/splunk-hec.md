# Export: Splunk HTTP Event Collector (HEC) JSON

`deedglass export --format splunk-hec` writes each sealed record as an NDJSON event formatted for
direct delivery to a Splunk HTTP Event Collector (`/services/collector/event`) endpoint or via
Splunk Universal Forwarder. Overview and other formats: [EXPORTS.md](../EXPORTS.md).

## Pinned version

| Attribute | Specification |
|---|---|
| Format | Splunk HEC Event JSON (NDJSON) |
| Sourcetype | `_json` |
| Source | `deedglass:records` |
| Index | `clinical_audit` (action/amendment), `admin_audit` (admin) |

## Event structure

Each line is a JSON object with Splunk envelope fields:
```json
{
  "time": 1731651163.123,
  "host": "deedglass",
  "source": "deedglass:records",
  "sourcetype": "_json",
  "index": "clinical_audit",
  "event": {
    "deed_id": "ded_...",
    "seq": 1,
    "issued_at": "2026-11-15T08:52:00.000Z",
    "org": "org_...",
    "kind": "action",
    "agent_id": "agt_...",
    "agent_name": "Ambient Scribe",
    "tool": "fhir.create",
    "decision": "allow",
    "rule_id": "orders-need-clinician",
    "status": "succeeded",
    "latency_ms": 42,
    "witness_level": "L2",
    "witness_verdict": "MATCH",
    "witness_method": "wal_tuple_correlation",
    "seal_key_id": "key_..."
  }
}
```

## Mapping

| Splunk Event Field | Record Source | Notes |
|---|---|---|
| `time` | `issued_at` | Fractional seconds since Unix epoch |
| `host` | `--gateway` or `deedglass` | Hostname of the Deedglass gateway |
| `source` | fixed | `deedglass:records` |
| `sourcetype` | fixed | `_json` |
| `index` | fixed | `clinical_audit` for clinical/tool calls, `admin_audit` for config events |
| `event.deed_id` | `deed_id` | Unique deed identifier |
| `event.seq` | `seq` | Monotonic ledger sequence number |
| `event.agent_id` | `agent.id` | Calling AI agent identifier |
| `event.decision` | `policy.decision` | Policy decision: `allow`, `deny`, `require_approval` |
| `event.witness_verdict` | `witness.verdict` | Cryptographic check verdict (`MATCH`, `MISMATCH`, `UNVERIFIABLE`) |
