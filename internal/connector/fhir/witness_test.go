package fhir

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/deedglass/deedglass/internal/connector"
	"github.com/deedglass/deedglass/internal/connector/fhir/pgmap"
)

const testWitnessDSN = "postgres://witness:throwaway@127.0.0.1:5432/hapi"

func witnessed(t *testing.T, server string) (*Connector, *connector.Instance) {
	t.Helper()
	return setup(t, "http://127.0.0.1:9/fhir", map[string]any{"witness": map[string]any{"enabled": true, "server": server}},
		&opener{kind: "fields", fields: map[string]string{"witness_dsn": testWitnessDSN}})
}

func findingCodes(j connector.L2Judgement) []string {
	var out []string
	for _, f := range j.Findings {
		out = append(out, f.Code)
	}
	return out
}

// stored is a resource as HAPI keeps its text: no id, no meta.
func stored(v int, status string) map[string]any {
	m := obs(v)
	m["status"] = status
	return m
}

func frame(id, class, action string, at time.Time, vs ...pgmap.SynthVersion) connector.L2Frame {
	tables, changes := pgmap.SynthHAPI(leafKey(testWitnessDSN), vs...)
	return connector.L2Frame{ID: id, Class: class, ActionID: action, CommitTime: at, CommitLSN: "0/1", SystemID: "7", Timeline: 1, Tables: tables, Changes: changes}
}

// TestL2Judge is ADR-108 §6 on HAPI-shaped frames: what the reported version in the bound transaction
// says against what the agent sent, the transaction's other versions, and the notes about others.
func TestL2Judge(t *testing.T) {
	c, inst := witnessed(t, "hapi")
	me := testAct.String()
	upd := callOf(t, c, inst, "update", map[string]any{"resource_type": "Observation", "id": "1004", "resource": func() map[string]any {
		m := obs(73)
		m["id"] = "1004"
		return m
	}()})
	res := connector.Result{Evidence: map[string]string{connector.EvidenceVersionKey: "Observation/1004/2", connector.EvidenceBindingToken: token(testAct)}}
	at := testAct.Time().Add(2 * time.Second)
	honest := pgmap.SynthVersion{Type: "Observation", ID: "1004", Version: 2, ResID: 1004, Resource: obs(73), RequestID: token(testAct)}
	judge := func(fs ...connector.L2Frame) connector.L2Judgement {
		t.Helper()
		j, err := c.L2Judge(ctx(), inst, upd, res, fs)
		if err != nil {
			t.Fatal(err)
		}
		return j
	}

	j := judge(frame("dtx_1", "bound", me, at, honest))
	if !slices.Equal(findingCodes(j), []string{"version_matched"}) || j.Method != "l2.fhir.hapi.8" || j.Primary != "dtx_1" {
		t.Fatalf("honest: %+v", j)
	}

	// The server stored another status, and a generated narrative of its own (allowed).
	lied := honest
	lied.Resource = stored(73, "preliminary")
	lied.Resource["text"] = map[string]any{"status": "generated", "div": "<div>Heart rate</div>"}
	j = judge(frame("dtx_1", "bound", me, at, lied))
	if !slices.Equal(findingCodes(j), []string{"version_field_changed"}) || j.Findings[0].Vars["path"] != "status" || len(j.Findings[0].Diff) != 1 {
		t.Fatalf("changed: %+v", j)
	}
	if b, _ := json.Marshal(j); strings.Contains(string(b), "preliminary") {
		t.Fatalf("the judgement shows a stored value: %s", b)
	}

	// I07: one write, two versions (a placeholder reference target).
	ghost := pgmap.SynthVersion{Type: "Patient", ID: "ghost-3", Version: 1, ResID: 2001, Resource: map[string]any{"resourceType": "Patient"}, RequestID: token(testAct)}
	j = judge(frame("dtx_1", "bound", me, at, honest, ghost))
	if !slices.Equal(findingCodes(j), []string{"fhir_extra_versions", "version_matched"}) || j.Findings[0].Vars["resources"] != "Patient/ghost-3 version 1" || j.Findings[0].Vars["n"] != 2 {
		t.Fatalf("extra versions: %+v", j)
	}

	// I08: the text is in a large object the change log doesn't carry.
	lo := honest
	lo.Encoding = "JSONC"
	j = judge(frame("dtx_1", "bound", me, at, lo))
	if !slices.Equal(findingCodes(j), []string{"large_object_not_replicated"}) {
		t.Fatalf("large object: %+v", j)
	}

	// I05: another writer's version of the same resource while the call was open is a note; one
	// after the action's commit, or of another resource, isn't this action's business.
	other := pgmap.SynthVersion{Type: "Observation", ID: "1004", Version: 1, ResID: 1004, Resource: stored(70, "final")}
	elsewhere := pgmap.SynthVersion{Type: "Observation", ID: "9", Version: 4, ResID: 9, Resource: stored(70, "final")}
	j = judge(frame("dtx_1", "bound", me, at, honest), frame("dtx_0", "unattributed", "", at.Add(-time.Second), other),
		frame("dtx_9", "unattributed", "", at.Add(-time.Second), elsewhere), frame("dtx_2", "unattributed", "", at.Add(time.Second), other))
	if !slices.Equal(findingCodes(j), []string{"version_matched", "out_of_band"}) || j.Findings[1].Vars["version"] != "1" {
		t.Fatalf("out of band: %+v", j)
	}

	// I06: a forged token in another transaction is a note, never the action's effect.
	forged := pgmap.SynthVersion{Type: "Observation", ID: "1005", Version: 1, ResID: 1005, Resource: stored(99, "final"), RequestID: token(testAct)}
	j = judge(frame("dtx_1", "bound", me, at, honest), frame("dtx_3", "request_id_reused", me, at, forged))
	if !slices.Equal(findingCodes(j), []string{"version_matched", "request_id_reused"}) || j.Findings[1].Vars["resource"] != "Observation/1005" {
		t.Fatalf("reused: %+v", j)
	}
	// Nothing bound yet: nothing to say (the wait goes on), even with a forged frame.
	if j = judge(frame("dtx_3", "request_id_reused", me, at, forged)); len(j.Findings) != 0 {
		t.Fatalf("unbound: %+v", j)
	}
	// B2: the reported version carries another request's id.
	if j = judge(frame("dtx_4", "version_tag_mismatch", me, at, honest)); !slices.Equal(findingCodes(j), []string{"version_tag_mismatch"}) {
		t.Fatalf("tag mismatch: %+v", j)
	}
	// Two bound transactions for one action.
	if j = judge(frame("dtx_1", "bound", me, at, honest), frame("dtx_5", "bound", me, at, honest)); !slices.Contains(findingCodes(j), "multiple_transactions") {
		t.Fatalf("two bound: %+v", j)
	}
	// A frame bound to another action isn't this one's.
	if j = judge(frame("dtx_6", "bound", "act_01m58nq2jthtfkkv8qdejqts5c", at, honest)); len(j.Findings) != 0 {
		t.Fatalf("another action's frame: %+v", j)
	}
}

// TestL2JudgeCreateMetaPatchDelete: a create's server-assigned id doesn't count; the tags and
// profiles the agent sent are compared with the rebuilt meta; a patch is checked on its paths; a
// delete must be a delete version.
func TestL2JudgeCreateMetaPatchDelete(t *testing.T) {
	c, inst := witnessed(t, "hapi")
	me := testAct.String()
	at := testAct.Time().Add(time.Second)
	sent := obs(72)
	sent["meta"] = map[string]any{"tag": []any{map[string]any{"system": "urn:t", "code": "y"}}, "profile": []any{"http://example.org/p"}}
	create := callOf(t, c, inst, "create", map[string]any{"resource_type": "Observation", "resource": sent})
	res := connector.Result{Evidence: map[string]string{connector.EvidenceVersionKey: "Observation/1003/1"}}
	v := pgmap.SynthVersion{Type: "Observation", ID: "1003", Version: 1, ResID: 1003, Resource: obs(72),
		Tags: [][3]string{{"0", "urn:t", "y"}, {"1", "", "http://example.org/p"}}}
	j, err := c.L2Judge(ctx(), inst, create, res, []connector.L2Frame{frame("dtx_1", "bound", me, at, v)})
	if err != nil || !slices.Equal(findingCodes(j), []string{"version_matched"}) {
		t.Fatalf("create with meta: %+v %v", j, err)
	}
	// A tag whose definition is in an earlier transaction: meta isn't compared at L2.
	tables, changes := pgmap.SynthHAPI(leafKey(testWitnessDSN), v)
	var cs []map[string]any
	_ = json.Unmarshal(changes, &cs)
	var kept []map[string]any
	for _, c := range cs {
		if c["table"] != "public.hfj_tag_def" {
			kept = append(kept, c)
		}
	}
	changes, _ = json.Marshal(kept)
	unresolved := connector.L2Frame{ID: "dtx_1", Class: "bound", ActionID: me, CommitTime: at, Tables: tables, Changes: changes}
	if j, _ = c.L2Judge(ctx(), inst, create, res, []connector.L2Frame{unresolved}); !slices.Equal(findingCodes(j), []string{"version_matched"}) {
		t.Fatalf("unresolved tags: %+v", j)
	}
	v.Tags = [][3]string{{"0", "urn:t", "z"}, {"1", "", "http://example.org/p"}}
	j, _ = c.L2Judge(ctx(), inst, create, res, []connector.L2Frame{frame("dtx_1", "bound", me, at, v)})
	if !slices.Equal(findingCodes(j), []string{"version_field_changed"}) || j.Findings[0].Vars["path"] != "meta.tag" {
		t.Fatalf("another tag: %+v", j)
	}

	patch := callOf(t, c, inst, "patch", map[string]any{"resource_type": "Observation", "id": "1003",
		"json_patch": []any{map[string]any{"op": "replace", "path": "/status", "value": "amended"}, map[string]any{"op": "remove", "path": "/valueQuantity"}}})
	res = connector.Result{Evidence: map[string]string{connector.EvidenceVersionKey: "Observation/1003/2"}}
	after := stored(0, "amended")
	delete(after, "valueQuantity")
	pv := pgmap.SynthVersion{Type: "Observation", ID: "1003", Version: 2, ResID: 1003, Resource: after}
	if j, _ = c.L2Judge(ctx(), inst, patch, res, []connector.L2Frame{frame("dtx_2", "bound", me, at, pv)}); !slices.Equal(findingCodes(j), []string{"version_matched"}) {
		t.Fatalf("patch: %+v", j)
	}
	pv.Resource = stored(72, "amended")
	if j, _ = c.L2Judge(ctx(), inst, patch, res, []connector.L2Frame{frame("dtx_2", "bound", me, at, pv)}); !slices.Equal(findingCodes(j), []string{"version_field_changed"}) || j.Findings[0].Vars["path"] != "valueQuantity" {
		t.Fatalf("patch not applied: %+v", j)
	}

	del := callOf(t, c, inst, "delete", map[string]any{"resource_type": "Observation", "id": "1003"})
	res = connector.Result{Evidence: map[string]string{connector.EvidenceVersionKey: "Observation/1003/3"}}
	dv := pgmap.SynthVersion{Type: "Observation", ID: "1003", Version: 3, ResID: 1003, Encoding: "DEL"}
	if j, _ = c.L2Judge(ctx(), inst, del, res, []connector.L2Frame{frame("dtx_3", "bound", me, at, dv)}); !slices.Equal(findingCodes(j), []string{"version_matched"}) {
		t.Fatalf("delete: %+v", j)
	}
}

// TestWitnessConfigAndTarget: witness.* validation, which writes are version-bound, and the
// evidence a write records.
func TestWitnessConfigAndTarget(t *testing.T) {
	c := New()
	for name, w := range map[string]map[string]any{
		"no server":         {"enabled": true},
		"unknown server":    {"enabled": true, "server": "aidbox"},
		"bad timeout":       {"enabled": true, "server": "hapi", "l2_timeout": "2h"},
		"author on hapi":    {"enabled": true, "server": "hapi", "author": "ClientApplication/x"},
		"bad author":        {"enabled": true, "server": "medplum", "author": "not a reference"},
		"settings when off": {"enabled": false, "server": "hapi"},
	} {
		raw, _ := json.Marshal(map[string]any{"base_url": "http://127.0.0.1:9/fhir", "auth": map[string]any{"kind": "none"},
			"allowed_resource_types": []string{"Observation"}, "witness": w})
		if _, errs := c.Validate(ctx(), devEnv(), raw); len(errs) == 0 {
			t.Errorf("%s: accepted", name)
		}
	}
	c, inst := witnessed(t, "medplum")
	for tool, want := range map[string]bool{"create": true, "update": true, "patch": true, "delete": false, "read": false} {
		args := map[string]any{"resource_type": "Observation", "id": "1", "resource": obs(1), "json_patch": []any{}}
		switch tool {
		case "create":
			delete(args, "id")
			delete(args, "json_patch")
		case "update":
			delete(args, "json_patch")
		case "patch":
			delete(args, "resource")
			args["json_patch"] = []any{map[string]any{"op": "replace", "path": "/status", "value": "final"}}
		default:
			delete(args, "resource")
			delete(args, "json_patch")
		}
		tg, err := c.L2Target(inst, callOf(t, c, inst, tool, args))
		if err != nil || (tg != nil) != want || (tg != nil && (!tg.Versions || tg.Method != "l2.fhir.medplum" || tg.Timeout != DefaultL2Timeout)) {
			t.Errorf("%s: %+v %v", tool, tg, err)
		}
	}
	r := &req{inter: "create", typ: "Observation"}
	ev := map[string]string{"fhir_status": "201", "fhir_id": "1003", "fhir_version": "1"}
	versionEvidence(ev, r, "", "tok")
	if ev[connector.EvidenceVersionKey] != "Observation/1003/1" || ev[connector.EvidenceBindingToken] != "tok" {
		t.Fatalf("create evidence: %v", ev)
	}
	ev = map[string]string{"fhir_status": "200", "fhir_id": "1003", "fhir_version": "1", "fhir_conditional": "true"}
	versionEvidence(ev, r, "", "tok")
	if _, ok := ev[connector.EvidenceVersionKey]; ok {
		t.Fatalf("a conditional create that matched names a version: %v", ev)
	}
	ev = map[string]string{"fhir_status": "200", "fhir_id": "1003", "fhir_version": "2"}
	versionEvidence(ev, &req{inter: "update", typ: "Observation"}, "2", "tok")
	if _, ok := ev[connector.EvidenceVersionKey]; ok {
		t.Fatalf("an update the server kept names a version: %v", ev)
	}
}
