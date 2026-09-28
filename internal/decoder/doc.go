// Package decoder reads the evidence stream (spec §8, amended by plan §3.0
// #3, #5 and #9): slot management, the SQL peek-then-advance loop over
// pgoutput proto_version 1 with messages=true (every 250 ms, or at once after
// a full batch), parsing through a crash-safe safeParse wrapper around
// pglogrepl.Parse (any failure is MALFORMED_EVIDENCE and halts statement
// issuance), transaction assembly from BEGIN..COMMIT, and a relation cache.
// Every decode session pins TimeZone, DateStyle, IntervalStyle, bytea_output
// and extra_float_digits and asserts them on each poll. Transactions are
// classified by frame position and the transactional flag of each message,
// never by the peek xid or by assuming peek LSNs are monotonic; the
// checkpoint is max(COMMIT.end_lsn), and the slot is advanced only after
// statements and the checkpoint are persisted, so delivery is at-least-once
// and statements exactly-once.
package decoder
