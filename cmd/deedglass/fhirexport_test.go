package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The offline path: a .jsonl of records with no database, converted record by record.
func TestDeedsExportFHIROffline(t *testing.T) {
	dir := t.TempDir()
	line := func(seq int, tool, status string) string {
		b := map[string]any{"v": 1, "kind": "action", "deed_id": "deed_01m5aaaaaaaaaaaaaaaaaaaa0" + string(rune('0'+seq)), "org": "org_01m5cccccccccccccccccccccc",
			"seq": seq, "issued_at": "2026-10-19T04:30:01.250Z", "agent": map[string]any{"id": "agt_01m5dddddddddddddddddddddd", "name": "charting-agent"},
			"action":    map[string]any{"id": "act_01m5bbbbbbbbbbbbbbbbbbbb0" + string(rune('0'+seq)), "tool": tool, "connector": nil, "request_digest": "sha256:" + strings.Repeat("a", 64)},
			"policy":    map[string]any{"decision": "allow", "rule_id": "verify-levels", "mode": "enforce"},
			"execution": map[string]any{"status": status, "started_at": "2026-10-19T04:30:00.900Z", "finished_at": "2026-10-19T04:30:01.100Z"}}
		env, _ := json.Marshal(map[string]any{"object": "deed", "id": b["deed_id"], "body": b, "seal": "ed25519:" + strings.Repeat("A", 86)})
		return string(env)
	}
	src := filepath.Join(dir, "deeds.jsonl")
	_ = os.WriteFile(src, []byte(line(1, "ehr.create", "succeeded")+"\n"+line(2, "postgres.execute", "succeeded")+"\n"+line(3, "ehr.update", "failed")+"\n"), 0o600)
	out := filepath.Join(dir, "ae.ndjson")
	r := cli(t, nil, false, "deeds", "export", "--from", src, "--format", "fhir-auditevent", "--out", out)
	if r.code != 0 || !strings.Contains(r.stdout, "2 AuditEvents") || !strings.Contains(r.stdout, "3 records read") {
		t.Fatalf("%+v", r)
	}
	b, _ := os.ReadFile(out)
	if n := strings.Count(string(b), "\n"); n != 2 || !strings.Contains(string(b), "IHE.BasicAudit.Create") {
		t.Fatalf("%d lines:\n%s", n, b)
	}
	r = cli(t, nil, false, "deeds", "export", "--from", src, "--format", "fhir-provenance", "--out", "-")
	if r.code != 0 || strings.Count(r.stdout, "\n") != 1 || !strings.Contains(r.stdout, `"resourceType":"Provenance"`) {
		t.Fatalf("%+v", r)
	}
	if r = cli(t, nil, false, "deeds", "export", "--from", src, "--format", "jsonl"); r.code != 2 {
		t.Fatalf("--from with jsonl: %+v", r)
	}
}
