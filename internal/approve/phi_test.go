package approve

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/deedglass/deedglass/internal/gate"
)

// HC-P09: a PHI-mode org's approval snapshot shows argument names and digests, never the values,
// and leaves the connector's sections out (they can quote the request).
func TestPHISnapshot(t *testing.T) {
	snap := Snapshot{Sections: map[string]json.RawMessage{"email_preview": json.RawMessage(`{"summary":"To Jane Q Synthetic"}`)}}
	in := gate.ApprovalInput{Args: json.RawMessage(`{"id":"1001","note":"Jane Q Synthetic, MRN-0042-SYN"}`)}
	phiSnapshot(&snap, in)
	b, _ := json.Marshal(snap)
	if strings.Contains(string(b), "Jane") || strings.Contains(string(b), "MRN-0042") {
		t.Fatalf("the snapshot keeps a planted value: %s", b)
	}
	var args map[string]string
	if err := json.Unmarshal(snap.Args, &args); err != nil || !strings.HasPrefix(args["id"], "sha256:") || !strings.HasPrefix(args["note"], "sha256:") {
		t.Fatalf("args %s (with no connector, nothing is an identifier)", snap.Args)
	}
	if snap.ArgsNote == nil || *snap.ArgsNote != PHINote || len(snap.Sections) != 0 {
		t.Fatalf("note %v sections %v", snap.ArgsNote, snap.Sections)
	}
	snap = Snapshot{}
	phiSnapshot(&snap, gate.ApprovalInput{})
	if string(snap.Args) != "null" {
		t.Fatalf("no args: %s", snap.Args)
	}
}
