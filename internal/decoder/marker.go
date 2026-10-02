package decoder

import (
	"crypto/sha256"
	"encoding/hex"
	"unicode/utf8"

	"github.com/deedglass/deedglass/internal/ids"
)

// MarkerPrefix is the logical message prefix S17's begin_action emits (contract §4).
const MarkerPrefix = "deedglass"

// ParseMarker returns the action id a transactional deedglass marker names. S17's content is
// exactly the action id (pg_logical_emit_message(true, 'deedglass', 'act_…')); anything else is
// not a valid marker (it still counts as a marker for classification, so a frame with one is never
// bound).
func ParseMarker(content []byte) (string, bool) {
	if len(content) != len("act_")+26 {
		return "", false
	}
	id, err := ids.Parse[ids.ActionKind](string(content))
	if err != nil {
		return "", false
	}
	return id.String(), true
}

// markerEvidence is how a marker's content is kept in a frame: the action id when it is one, else
// the sha256 of the bytes (a forged marker's content is the forger's, and never stored as is).
func markerEvidence(content []byte) string {
	if id, ok := ParseMarker(content); ok {
		return id
	}
	sum := sha256.Sum256(content)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// printable reports whether s is short, valid UTF-8 without control characters (safe to keep in
// a flag such as origin:<name>).
func printable(s string, max int) bool {
	if len(s) > max || !utf8.ValidString(s) {
		return false
	}
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}
