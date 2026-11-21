# Export: CEF (Common Event Format) for SIEM Ingestion

`deedglass export --format cef` writes each sealed record as an ArcSight Common Event Format (CEF)
log line, so enterprise SIEMs (Microsoft Sentinel, Splunk, IBM QRadar, Palo Alto Cortex) and
hospital Security Operations Centers (SOC) can ingest what agents attempted and what Deedglass
decided and witnessed. The output is a standard syslog-compatible line-delimited stream.
Overview and other formats: [EXPORTS.md](../EXPORTS.md).

## Pinned version

| Attribute | Specification |
|---|---|
| Format | Common Event Format (CEF) Version 0 |
| Vendor | `Deedglass` |
| Product | `Deedglass` |
| Transport | Line-delimited text (`.cef.log`), syslog RFC 5424 forwarder compatible |

## Header structure

Each line begins with the standard CEF header:
```text
CEF:0|Deedglass|Deedglass|<version>|<device_event_class_id>|<name>|<severity>|[extensions]
```

| Field | Source from record | Description |
|---|---|---|
| `device_event_class_id` | computed | `act_committed`, `act_blocked`, `act_failed`, `witness_mismatch`, `admin_event`, `act_amended` |
| `name` | computed | Human-readable title (e.g. `Action Executed and Proven`, `Witness Mismatch Detected`) |
| `severity` | computed integer | 1 (routine read), 3 (routine write), 4–5 (policy block or error), 8 (witness mismatch) |

## Extensions mapping

| CEF Extension | Field | Value / Source |
|---|---|---|
| `externalId` | Deed ID | `b.DeedID` |
| `rt` | Recorded Time | Epoch milliseconds from `issued_at` |
| `act` | Action Tool | `action.tool` (e.g. `fhir.create`, `postgres.execute`) |
| `suser` | Subject User / Agent | `agent.id` (or admin actor ID) |
| `duser` | Destination / Clinician User | `on_behalf_of` (or agent display name) |
| `cs1` / `cs1Label` | Policy Rule | Rule ID / `RuleID` |
| `cs2` / `cs2Label` | Policy Decision | `decision` (`allow`, `deny`, `require_approval`) / `PolicyDecision` |
| `cs3` / `cs3Label` | Witness Level | `L1`, `L2`, `NONE` / `WitnessLevel` |
| `cs4` / `cs4Label` | Witness Verdict | `MATCH`, `MISMATCH`, `UNVERIFIABLE` / `WitnessVerdict` |
| `cs5` / `cs5Label` | Action ID | `action.id` / `ActionID` |
| `cs6` / `cs6Label` | Deed ID | `deed_id` / `DeedID` |
| `cn1` / `cn1Label` | Latency | Execution latency in milliseconds / `LatencyMs` |
| `cn2` / `cn2Label` | Sequence Number | Ledger monotonic sequence `seq` / `SequenceNumber` |
| `outcome` | Execution Outcome | `success` or `failure` |
| `msg` | Summary | Machine summary string |
