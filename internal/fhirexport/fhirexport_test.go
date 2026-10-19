package fhirexport

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

var update = flag.Bool("update", false, "rewrite testdata/*.ndjson")

const (
	seal = "ed25519:" + "AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8gISIjJCUmJygpKiss" + "LS4vMDEyMzQ1Njc4OTo7PD0-Pw"
	dig  = "sha256:9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08"
	dig2 = "sha256:60303ae22b998861bce3b28f33eec1be758a213c86c93c076dbe9f558c11c752"
	// A planted clinical value: it must never reach an export.
	planted = "dgPHI-chest-pain-at-rest"
)

// deedEnv is a stored envelope for an action record (docs/DEED_SPEC.md §4); the seal isn't
// checked here, only carried.
func deedEnv(seq int, tool, decision, rule, status, verdict, level string) []byte {
	id := fmt.Sprintf("deed_01m5aaaaaaaaaaaaaaaaaaaa%02d", seq)
	act := fmt.Sprintf("act_01m5bbbbbbbbbbbbbbbbbbbb%02d", seq)
	b := map[string]any{"v": 1, "kind": "action", "deed_id": id, "org": "org_01m5cccccccccccccccccccccc", "seq": seq,
		"prev_hash": dig, "issued_at": "2026-10-19T04:30:01.250Z", "agent": map[string]any{"id": "agt_01m5dddddddddddddddddddddd", "name": "charting-agent"},
		"on_behalf_of": nil, "session": nil, "admin": nil, "amends": nil, "cost": nil, "seal_key_id": "sk_01m5eeeeeeeeeeeeeeeeeeeeee",
		"action":   map[string]any{"id": act, "tool": tool, "connector": "con_01m5ffffffffffffffffffffff", "mode": "gateway", "request_digest": dig, "intent_hash": nil, "intent_signed": false},
		"policy":   map[string]any{"id": "pol_01m5gggggggggggggggggggggg", "version_id": "pv_01m5hhhhhhhhhhhhhhhhhhhhhh", "version_hash": dig, "mode": "enforce", "decision": decision, "rule_id": rule},
		"approval": nil, "execution": nil, "witness": nil}
	if status != "" {
		b["execution"] = map[string]any{"status": status, "started_at": "2026-10-19T04:30:00.900Z", "finished_at": "2026-10-19T04:30:01.100Z",
			"latency_ms": 200, "result_digest": dig2, "error_code": nil}
		if status == "failed" {
			b["execution"].(map[string]any)["error_code"] = "fhir_version_conflict"
		}
	}
	if verdict != "" {
		b["witness"] = map[string]any{"level": level, "verdict": verdict, "method": "readback.fhir." + strings.TrimPrefix(tool[strings.Index(tool, "."):], "."), "detail_digest": nil}
	}
	env, _ := json.Marshal(map[string]any{"object": "deed", "id": id, "body": b, "body_hash": dig, "seal": seal, "seal_key_id": "sk_01m5eeeeeeeeeeeeeeeeeeeeee"})
	return env
}

func row(conType string, req, res any) *Call {
	rq, _ := json.Marshal(req)
	rs, _ := json.Marshal(res)
	return CallFrom(conType, "ehr", json.RawMessage(`{"base_url":"https://fhir.example.org/r4","auth":{"kind":"smart_backend"}}`), rq, rs)
}

func obs() map[string]any {
	return map[string]any{"resourceType": "Observation", "status": "final", "subject": map[string]any{"reference": "Patient/syn-1"},
		"note": []any{map[string]any{"text": planted}}}
}

func ok201(id, vid string) map[string]any {
	return map[string]any{"status": 201, "client_id": "dg-backend", "headers": map[string]any{"location": "https://fhir.example.org/r4/Observation/" + id + "/_history/" + vid},
		"body": map[string]any{"resourceType": "Observation", "id": id, "meta": map[string]any{"versionId": vid}, "note": []any{map[string]any{"text": planted}}}}
}

// records is a fixture of every shape the export meets.
func records() []Record {
	admin, _ := json.Marshal(map[string]any{"object": "deed", "id": "deed_01m5aaaaaaaaaaaaaaaaaaaa99", "body": map[string]any{"kind": "admin", "deed_id": "deed_01m5aaaaaaaaaaaaaaaaaaaa99"}, "seal": seal})
	return []Record{
		// 1. A create, checked: PatientCreate and a Provenance.
		{deedEnv(1, "ehr.create", "allow", "verify-levels", "succeeded", "VERIFIED", "L1"),
			row("fhir", map[string]any{"resource_type": "Observation", "resource": obs()}, ok201("obs-1", "1"))},
		// 2. The same from a bundle alone: Create, the action as the target.
		{deedEnv(2, "ehr.create", "allow", "verify-levels", "succeeded", "VERIFIED", "L1"), nil},
		// 3. A patch in a PHI-mode org: ids and digests only, so Update (no patient), and a Provenance.
		{deedEnv(3, "ehr.patch", "allow", "verify-levels", "succeeded", "MISMATCH", "L1"),
			row("fhir", map[string]any{"resource_type": "Observation", "id": "obs-1", "json_patch": dig}, map[string]any{"status": 200, "client_id": "dg-backend", "body": map[string]any{"resourceType": "Observation", "id": "obs-1", "meta": map[string]any{"versionId": "2"}}})},
		// 4. A delete the pack denied: an AuditEvent with no profile, no Provenance.
		{deedEnv(4, "ehr.delete", "deny", "no-delete-clinical", "", "", ""), row("fhir", map[string]any{"resource_type": "Observation", "id": "obs-1"}, nil)},
		// 5. A read of the Patient: PatientRead.
		{deedEnv(5, "ehr.read", "allow", "fhir-reads", "succeeded", "VERIFIED", "L0"),
			row("fhir", map[string]any{"resource_type": "Patient", "id": "syn-1"}, map[string]any{"status": 200, "body": map[string]any{"resourceType": "Patient", "id": "syn-1", "meta": map[string]any{"versionId": "3"}}})},
		// 6. A search for one patient: PatientQuery, the query as a digest.
		{deedEnv(6, "ehr.search", "allow", "fhir-reads", "succeeded", "VERIFIED", "L0"),
			row("fhir", map[string]any{"resource_type": "Observation", "params": map[string]any{"patient": "syn-1", "code": planted}}, map[string]any{"status": 200})},
		// 7. A resource's history: no BALP profile covers it.
		{deedEnv(7, "ehr.history", "allow", "fhir-reads", "succeeded", "VERIFIED", "L0"), row("fhir", map[string]any{"resource_type": "Observation", "id": "obs-1"}, map[string]any{"status": 200})},
		// 8. A stale If-Match: failed, no profile, no Provenance.
		{deedEnv(8, "ehr.update", "allow", "verify-levels", "failed", "", ""), row("fhir", map[string]any{"resource_type": "Observation", "id": "obs-1", "resource": obs()}, map[string]any{"status": 409})},
		// 9. A 5xx: outcome unknown.
		{deedEnv(9, "ehr.create", "allow", "verify-levels", "unknown", "UNVERIFIABLE", "L1"), row("fhir", map[string]any{"resource_type": "Observation", "resource": obs()}, map[string]any{"status": 503})},
		// 10. Not a FHIR interaction, and an admin record: skipped.
		{deedEnv(10, "postgres.execute", "allow", "reads", "succeeded", "VERIFIED", "L0"), nil},
		{admin, nil},
	}
}

func export(t *testing.T, format string) ([]byte, Stats) {
	t.Helper()
	var buf bytes.Buffer
	w, err := NewWriter(&buf, format, Options{Gateway: "https://deedglass.clinic.example"})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range records() {
		if err := w.Write(r); err != nil {
			t.Fatal(err)
		}
	}
	return buf.Bytes(), w.Stats
}

func TestGoldens(t *testing.T) {
	for _, f := range []struct {
		format, file string
		written      int
	}{{FormatAuditEvent, "auditevent.ndjson", 9}, {FormatProvenance, "provenance.ndjson", 3}} {
		got, st := export(t, f.format)
		if st.Written != f.written || st.Records != 11 || st.Skipped != 11-f.written {
			t.Errorf("%s: %+v", f.format, st)
		}
		if bytes.Contains(got, []byte(planted)) {
			t.Fatalf("%s carries a planted clinical value", f.format)
		}
		p := filepath.Join("testdata", f.file)
		if *update {
			_ = os.MkdirAll("testdata", 0o755)
			if err := os.WriteFile(p, got, 0o644); err != nil {
				t.Fatal(err)
			}
		}
		want, err := os.ReadFile(p)
		if err != nil || !bytes.Equal(got, want) {
			t.Errorf("%s differs from %s (go test ./internal/fhirexport -update, then review the diff)", f.format, p)
		}
		for i, line := range bytes.Split(bytes.TrimSpace(got), []byte("\n")) {
			var res map[string]any
			if err := json.Unmarshal(line, &res); err != nil {
				t.Fatalf("%s line %d: %v", f.format, i+1, err)
			}
			for _, p := range checkResource(res) {
				t.Errorf("%s line %d (%v): %s", f.format, i+1, res["id"], p)
			}
		}
	}
	if _, err := NewWriter(&bytes.Buffer{}, "fhir", Options{}); err == nil {
		t.Error("an unknown format was accepted")
	}
}

func TestProfilesChosen(t *testing.T) {
	got, _ := export(t, FormatAuditEvent)
	var profiles []string
	for _, line := range bytes.Split(bytes.TrimSpace(got), []byte("\n")) {
		var res struct {
			Meta struct{ Profile []string }
		}
		_ = json.Unmarshal(line, &res)
		p := "-"
		if len(res.Meta.Profile) == 1 {
			p = strings.TrimPrefix(res.Meta.Profile[0], balpBase)
		}
		profiles = append(profiles, p)
	}
	want := "PatientCreate Create Update - PatientRead PatientQuery - - -"
	if strings.Join(profiles, " ") != want {
		t.Fatalf("profiles %v, want %s", profiles, want)
	}
}

func TestCallFrom(t *testing.T) {
	if CallFrom("rest", "orders", nil, nil, nil) != nil {
		t.Fatal("a REST connector's row became a FHIR call")
	}
	c := row("fhir", map[string]any{"resource_type": "Observation", "resource": obs()}, ok201("obs-9", "4"))
	if c.ID != "obs-9" || c.VersionID != "4" || c.Patient != "Patient/syn-1" || c.ClientID != "dg-backend" || c.Server != "https://fhir.example.org/r4" {
		t.Fatalf("%+v", c)
	}
	// Location alone gives the version; a digest in place of an id is left out.
	c = row("fhir", map[string]any{"resource_type": "Observation", "id": dig}, map[string]any{"status": 201, "headers": map[string]any{"location": "https://x/Observation/o2/_history/7"}})
	if c.ID != "" || c.VersionID != "7" || c.Patient != "" {
		t.Fatalf("%+v", c)
	}
}

// checkResource is a small offline validator: the FHIR R4 rules and the profile constraints this
// export relies on (IHE BALP 1.1.4 Create/Read/Update/Delete/Query and their Patient variants; HL7
// AI Transparency 1.0.0-ballot AI Provenance and AI Device), written from the published
// StructureDefinitions. The full R4 validation runs against HAPI FHIR in TestFHIRConnectorLive.
func checkResource(res map[string]any) []string {
	var p []string
	bad := func(f string, a ...any) { p = append(p, fmt.Sprintf(f, a...)) }
	walk(res, "", func(path string, v any) {
		switch x := v.(type) {
		case nil:
			bad("%s is null (ele-1)", path)
		case string:
			if x == "" {
				bad("%s is empty (ele-1)", path)
			}
		case []any:
			if len(x) == 0 {
				bad("%s is an empty array (ele-1)", path)
			}
		case map[string]any:
			if len(x) == 0 {
				bad("%s is an empty object (ele-1)", path)
			}
		}
	})
	if id, _ := res["id"].(string); !regexp.MustCompile(`^[A-Za-z0-9\-.]{1,64}$`).MatchString(id) {
		bad("id %q", id)
	}
	instant := regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(\.\d+)?(Z|[+-]\d{2}:\d{2})$`)
	switch res["resourceType"] {
	case "AuditEvent":
		p = append(p, checkAuditEvent(res, instant)...)
	case "Provenance":
		p = append(p, checkProvenance(res, instant)...)
	default:
		bad("resourceType %v", res["resourceType"])
	}
	return p
}

func walk(v any, path string, f func(string, any)) {
	f(path, v)
	switch x := v.(type) {
	case map[string]any:
		for k, e := range x {
			walk(e, path+"."+k, f)
		}
	case []any:
		for i, e := range x {
			walk(e, fmt.Sprintf("%s[%d]", path, i), f)
		}
	}
}

func codeOf(v any) (system, code string) {
	m, _ := v.(map[string]any)
	if cs, ok := m["coding"].([]any); ok && len(cs) > 0 {
		m, _ = cs[0].(map[string]any)
	}
	s, _ := m["system"].(string)
	c, _ := m["code"].(string)
	return s, c
}

func list(v any) []map[string]any {
	var out []map[string]any
	xs, _ := v.([]any)
	for _, x := range xs {
		if m, ok := x.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

func checkAuditEvent(ev map[string]any, instant *regexp.Regexp) []string {
	var p []string
	bad := func(f string, a ...any) { p = append(p, fmt.Sprintf(f, a...)) }
	if s, c := codeOf(ev["type"]); s != sysAuditType || c != "rest" {
		bad("type %s|%s", s, c)
	}
	subs := list(ev["subtype"])
	sub := ""
	if len(subs) != 1 {
		bad("%d subtypes", len(subs))
	} else if s, c := codeOf(subs[0]); s != sysInteraction {
		bad("subtype system %s", s)
	} else {
		sub = c
	}
	if r, _ := ev["recorded"].(string); !instant.MatchString(r) {
		bad("recorded %q is not an instant", r)
	}
	if o, _ := ev["outcome"].(string); o != "0" && o != "4" && o != "8" && o != "12" {
		bad("outcome %q (audit-event-outcome)", o)
	}
	roles := map[string]int{}
	for _, a := range list(ev["agent"]) {
		_, c := codeOf(a["type"])
		roles[c]++
		if a["who"] == nil {
			bad("agent %s without who", c)
		}
		if _, ok := a["requestor"].(bool); !ok {
			bad("agent %s without requestor (R4 1..1)", c)
		}
		if c == "110153" || c == "110152" {
			if n, _ := a["network"].(map[string]any); n["address"] == nil || n["type"] != "5" {
				bad("agent %s network %v", c, a["network"])
			}
		}
		if c == "AUT" && a["requestor"] != true {
			bad("the user agent must be the requestor")
		}
	}
	if roles["110153"] != 1 || roles["110152"] != 1 || roles["AUT"] != 1 {
		bad("agents %v: want one client, one server, one user", roles)
	}
	if src, _ := ev["source"].(map[string]any); src["observer"] == nil {
		bad("source.observer is missing")
	}
	slices := map[string]int{}
	for _, e := range list(ev["entity"]) {
		_, t := codeOf(e["type"])
		_, r := codeOf(e["role"])
		if e["name"] != nil && e["query"] != nil {
			bad("sev-1: name and query")
		}
		switch {
		case t == "2" && r == "24":
			slices["query"]++
			if q, _ := e["query"].(string); q == "" {
				bad("the query entity has no query")
			} else if _, err := base64.StdEncoding.DecodeString(q); err != nil {
				bad("query isn't base64")
			}
			if e["what"] != nil || e["detail"] != nil {
				bad("the query entity carries what or detail (0..0 in BALP Query)")
			}
		case t == "2":
			slices["data"]++
			if e["what"] == nil || r != "4" {
				bad("data entity %v", e)
			}
		case t == "1":
			slices["patient"]++
			w, _ := e["what"].(map[string]any)
			if ref, _ := w["reference"].(string); !strings.HasPrefix(ref, "Patient/") || r != "1" {
				bad("patient entity %v", e)
			}
		case t == "XrequestId":
			slices["transaction"]++
			w, _ := e["what"].(map[string]any)
			if id, _ := w["identifier"].(map[string]any); id["value"] == nil {
				bad("the XrequestId entity has no identifier value")
			}
		}
	}
	meta, _ := ev["meta"].(map[string]any)
	profiles, _ := meta["profile"].([]any)
	if len(profiles) == 0 {
		return p
	}
	name := strings.TrimPrefix(profiles[0].(string), balpBase)
	family := strings.TrimPrefix(name, "Patient")
	want := map[string]struct {
		action string
		subs   []string
	}{"Create": {"C", []string{"create"}}, "Read": {"R", []string{"read", "vread"}}, "Update": {"U", []string{"update", "patch"}},
		"Delete": {"D", []string{"delete"}}, "Query": {"E", []string{"search"}}}[family]
	if want.action == "" {
		bad("unknown profile %s", name)
		return p
	}
	if ev["outcome"] != "0" {
		bad("%s fixes outcome 0", name)
	}
	if ev["action"] != want.action || !strings.Contains(" "+strings.Join(want.subs, " ")+" ", " "+sub+" ") {
		bad("%s: action %v subtype %s", name, ev["action"], sub)
	}
	if family == "Query" && slices["query"] != 1 || family != "Query" && slices["data"] != 1 {
		bad("%s: entities %v", name, slices)
	}
	if strings.HasPrefix(name, "Patient") != (slices["patient"] == 1) || slices["patient"] > 1 || slices["transaction"] > 1 {
		bad("%s: entities %v", name, slices)
	}
	return p
}

func checkProvenance(pr map[string]any, instant *regexp.Regexp) []string {
	var p []string
	bad := func(f string, a ...any) { p = append(p, fmt.Sprintf(f, a...)) }
	if len(list(pr["target"])) == 0 {
		bad("no target (1..*)")
	}
	if r, _ := pr["recorded"].(string); !instant.MatchString(r) {
		bad("recorded %q is not an instant", r)
	}
	aiReason := false
	for _, r := range list(pr["reason"]) {
		s, c := codeOf(r)
		aiReason = aiReason || (s == "http://terminology.hl7.org/CodeSystem/v3-ObservationValue" && c == "AIAST") || (s == sysAIProv && c != "")
	}
	if !aiReason {
		bad("no reason from the AI Transparency ProvenanceVS (AI Provenance reason:AIReason 1..*)")
	}
	contained := map[string]map[string]any{}
	for _, c := range list(pr["contained"]) {
		contained["#"+c["id"].(string)] = c
	}
	types := map[string]int{}
	aiAuthor := false
	for _, a := range list(pr["agent"]) {
		s, c := codeOf(a["type"])
		if s != sysProvAgent {
			bad("agent type system %s", s)
		}
		types[c]++
		w, _ := a["who"].(map[string]any)
		if w == nil {
			bad("agent %s without who", c)
			continue
		}
		if ref, _ := w["reference"].(string); ref != "" {
			d := contained[ref]
			if d == nil {
				bad("agent %s points to %s, which isn't contained", c, ref)
				continue
			}
			m, _ := d["meta"].(map[string]any)
			ps, _ := m["profile"].([]any)
			ts, tc := codeOf(d["type"])
			if len(ps) != 1 || ps[0] != aiDevice || ts != sysAIDeviceType || tc != "Artificial-Intelligence" {
				bad("the AI device doesn't fit AI-Device: %v", d)
			}
			for _, n := range list(d["deviceName"]) {
				switch n["type"] {
				case "udi-label-name", "user-friendly-name", "patient-reported-name", "manufacturer-name", "model-name", "other":
				default:
					bad("deviceName.type %v", n["type"])
				}
			}
			aiAuthor = aiAuthor || c == "author"
		}
	}
	if !aiAuthor || types["assembler"] != 1 {
		bad("agents %v: want the AI device as author and Deedglass as assembler", types)
	}
	for _, s := range list(pr["signature"]) {
		if len(list(s["type"])) == 0 || s["when"] == nil || s["who"] == nil {
			bad("signature without type, when or who (R4 1..1)")
		}
		if d, _ := s["data"].(string); d != "" {
			if raw, err := base64.StdEncoding.DecodeString(d); err != nil || len(raw) != 64 {
				bad("signature data isn't a 64-byte Ed25519 signature")
			}
		}
	}
	for _, e := range list(pr["entity"]) {
		switch e["role"] {
		case "derivation", "revision", "quotation", "source", "removal":
		default:
			bad("entity role %v", e["role"])
		}
	}
	return p
}
