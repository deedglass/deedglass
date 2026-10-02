// Package decoder reads Postgres logical replication (pgoutput v1) for the Witness (S20): one
// runner per witnessed connector peeks its slot, checks every message's layout (SafeParse),
// assembles whole transactions into frames with exact before and after images, classifies each
// frame against S17's ledger row and marker (ADR-003), persists frames and the checkpoint in one
// transaction, and only then advances the slot (ADR-002, ADR-045). docs/DECODER.md is the guide.
//
// Invariants:
//   - The slot advances only after the evidence is committed in Deedglass's database
//     (decoded_txs, decoder_slots): delivery is at least once, storage exactly once.
//   - Every message goes through SafeParse (full layout check, then pglogrepl.Parse in recover); a
//     failure halts the runner before anything from that peek is persisted.
//   - Decode sessions pin TimeZone, DateStyle, IntervalStyle, bytea_output and
//     extra_float_digits in the startup packet and assert them before every peek.
//   - Frames are classified by frame position and each message's transactional flag, never by the
//     peek xid or peek LSN order; the marker LSN is never compared with the commit LSN.
//   - A frame binds only with exactly one ledger insert and one marker naming the same action, of
//     the connector's org, with low32(xid8) = BEGIN.xid. The decoder never decides a verdict.
//   - Lag above DEEDGLASS_SLOT_LAG_ALERT_MB (or a quarter of max_slot_wal_keep_size) degrades the
//     runner (decoder.degraded); security signals publish decoder.alert.
//
// API: Supervisor (Run, Sync, Drop, Retire, Nudge, Health), Runner (Nudge, ReconcileNow, View),
// Hooks (OnBound, OnMissing, SlotLost) for S21, BuildView for GET /v1/connectors/{id}/decoder.
package decoder
