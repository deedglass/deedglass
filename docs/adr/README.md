# Architecture decision records

ADRs are written and approved in the private ops repository at `~/txv/ops/adr/` (numbered ADR-000 upward; plan §2.3, §3.10). This directory points there while the product repo is private.

| ADR | Topic | Source |
|---|---|---|
| ADR-001 | Language (Go) | spec 20_ §0 #1, §3.1 |
| ADR-002 | CDC mechanism (pgoutput, SQL peek then advance; safeParse; pinned session settings; frame-position classification) | spec 20_ §8; plan §3.0 #3, #5, #9 |
| ADR-003 | Marker and authenticity (marker last, same-transaction ledger row; marker_lsn is evidence only) | spec 20_ §7; plan §3.0 #1, #8 |
| ADR-004 | Signature location (outside PostgreSQL, twice) | spec 20_ §0 #4 |
| ADR-005 | Intent format (JCS, Ed25519, strict pre-parser before JCS) | spec 20_ §5; plan §3.0 #6 |
| ADR-006 | Operation Bundle authority | spec 20_ §6 |
| ADR-007 | Observation boundary | spec 20_ §9 |
| ADR-008 | Statement format (COSE_Sign1, alg -8) | spec 20_ §11; plan §3.0 #4 |
| ADR-009 | Payee allowlist (pending founder approval; adds attack case G01b) | plan §3.9 |
| ADR-010 | Demo MCP tool | plan §3.9 |
| ADR-011 | Site hero copy | plan §1.3, §3.9 |

Any deviation from the spec, any new dependency, and any change to an expected outcome in `attack/attack_meta.json` needs an ADR the founder approves.

TODO(G4): copy the accepted, publishable ADRs into this directory before the repo goes public.
