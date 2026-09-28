// Package store is the verifier's local state in SQLite (modernc.org/sqlite,
// pure Go; spec §3.2, §8.3): the decoder checkpoint, the statement and
// evidence index keyed by (system_identifier, timeline_id, commit_lsn) so
// re-decoded transactions after a slot rewind are skipped, the global nonce
// memory used for REPLAY across timelines, the row-continuity map used by the
// CONTINUITY_BREAK check, and the heartbeat secret. Writes follow the
// crash-safe order: statement and evidence, then checkpoint, then slot advance.
package store
