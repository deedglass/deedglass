// Package executor is the prevention half of V0 (spec §2.1, §2.3, §4.4): an
// HTTP service (POST /v1/intents with the exact intent bytes and signature,
// base64url) that strict-parses the intent, requires canonical bytes, finds
// the kid in its trusted registry, verifies Ed25519, checks aud, the time
// window and the active bundle, then opens a READ COMMITTED transaction as
// txv_executor and calls txv.execute_transfer_v1 exactly once. It holds no
// signing key. SQLSTATE 23505 maps to REPLAY and TX0xx codes to typed errors;
// a retry always uses a new transaction, because a same-transaction retry
// trips the entry guard (TX001). If the executor is compromised, the verifier
// still detects the result.
package executor
