package gate

import (
	"encoding/json"
	"regexp"
	"sort"
)

// The connector's execution evidence on the action (ADR-044, the DL-031 decision). A connector
// reports where its write landed (Postgres: xid8, marker_lsn, sql_digest and command_tag); the
// Gate keeps it on the action, written once with the execution, so the Witness can cross-check it
// against what the decoder read from the database's change stream (S21) and S23 can find the
// write again. It is for the Witness only: never sent to agents, never inside a deed, and never a
// value from the customer's rows. The decoded frame stays the system-of-record copy.

// Evidence limits: a few short tokens, at most 1 KiB as JSON (the column's CHECK).
const (
	maxEvidenceKeys  = 8
	maxEvidenceValue = 128
	maxEvidenceBytes = 900
)

var (
	reEvidenceKey   = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}$`)
	reEvidenceValue = regexp.MustCompile(`^[A-Za-z0-9:/._ -]{0,128}$`)
)

// EvidenceJSON is the stored form of a connector's evidence: only short, plain tokens under
// lowercase keys. Anything else is dropped rather than stored (it is never needed for a verdict:
// the decoded frame is the evidence). nil when nothing is left.
func EvidenceJSON(ev map[string]string) json.RawMessage {
	if len(ev) == 0 {
		return nil
	}
	keys := make([]string, 0, len(ev))
	for k, v := range ev {
		if reEvidenceKey.MatchString(k) && len(v) <= maxEvidenceValue && reEvidenceValue.MatchString(v) {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	if len(keys) > maxEvidenceKeys {
		keys = keys[:maxEvidenceKeys]
	}
	out := make(map[string]string, len(keys))
	for _, k := range keys {
		out[k] = ev[k]
	}
	for len(out) > 0 {
		b, err := json.Marshal(out)
		if err == nil && len(b) <= maxEvidenceBytes {
			return b
		}
		delete(out, keys[len(out)-1])
	}
	return nil
}
