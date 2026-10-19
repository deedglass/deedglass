//go:build attack

package attack

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/deedglass/deedglass/attack/harness"
	"github.com/deedglass/deedglass/internal/connector/fhir/fhirtest"
	"github.com/deedglass/deedglass/internal/packs"
)

// FHIR servers: healthcare phase 1's group I (HC-P02, ADR-105). Acme gets a fhir connector on an
// in-process FHIR R4 server (internal/connector/fhir/fhirtest, which answers the way HAPI FHIR JPA
// v8.12 did in HC-P05's spike) and the agent clinic-bot, whose calls the attack-baseline rule
// fhir-clinic allows when they build a request (request.fhir.enriched), checked at L1. The server
// is the attacker: it answers success in ways that don't mean what the agent says. I05 to I08 are
// Proven (L2) cases for Postgres-backed FHIR servers (HC-P05, cases_fhir_proven_test.go). I12 runs the
// healthcare-fhir pack (HC-P06) exactly as the binary embeds it.

func init() {
	register(
		harness.Case{ID: "I01", Run: i01},
		harness.Case{ID: "I02", Run: i02},
		harness.Case{ID: "I03", Run: i03},
		harness.Case{ID: "I04", Run: i04},
		harness.Case{ID: "I05", Run: i05, Cap: 60 * time.Second},
		harness.Case{ID: "I06", Run: i06, Cap: 60 * time.Second},
		harness.Case{ID: "I07", Run: i07, Cap: 60 * time.Second},
		harness.Case{ID: "I08", Run: i08, Cap: 60 * time.Second},
		harness.Case{ID: "I09", Run: i09},
		harness.Case{ID: "I10", Run: i10},
		harness.Case{ID: "I11", Run: i11, Cap: 60 * time.Second},
		harness.Case{ID: "I12", Run: i12},
	)
}

var (
	fhirOnce   sync.Once
	fhirSrv    *fhirtest.Server
	fhirSeq    int
	fhirServer = "urn:deedglass:attack:server" // the stand-in server's own meta.tag system
)

var iForbidden = []string{"orders", "other", "redirector", "secret-echo", "mcp"}

// fhirConfig is the connector both orgs use: two types, every interaction but delete.
func fhirConfig() map[string]any {
	return map[string]any{"base_url": fhirSrv.Base(), "auth": map[string]any{"kind": "none"},
		"allowed_resource_types": []string{"Observation", "Patient"},
		"allowed_interactions":   []string{"read", "vread", "search", "history", "create", "update", "patch"},
		"server_tag_systems":     []string{fhirServer}}
}

// fhirAgent sets up the stand-in server, Acme's fhir connector and clinic-bot once, resets the
// server's knobs, and returns the agent.
func fhirAgent(h *harness.H) *harness.Agent {
	fhirOnce.Do(func() {
		fhirSrv = fhirtest.New()
		if r := h.Env.AddConnector(h.Acme, "fhir", "fhir", fhirConfig(), "", nil); r.Status != 201 {
			panic(fmt.Sprintf("create the fhir connector: HTTP %d %s", r.Status, r.Body))
		}
		h.Env.AddAgent(h.Acme, "clinic-bot", nil)
	})
	fhirSrv.Mutate, fhirSrv.FailNext, fhirSrv.StoreRequestID = nil, 0, false
	return h.Acme.Agents["clinic-bot"]
}

// observation is a synthetic heart-rate Observation with an identifier unique to this run.
func observation(status string, v int) (map[string]any, string) {
	fhirSeq++
	ident := fmt.Sprintf("attack-%d-%d", time.Now().UnixNano(), fhirSeq)
	return map[string]any{"resourceType": "Observation", "status": status,
		"identifier":    []any{map[string]any{"system": "urn:deedglass:attack", "value": ident}},
		"code":          map[string]any{"coding": []any{map[string]any{"system": "http://loinc.org", "code": "8867-4"}}},
		"subject":       map[string]any{"reference": "Patient/attack-synthetic"},
		"valueQuantity": map[string]any{"value": v, "unit": "beats/minute"}}, ident
}

// created runs an honest create and returns the action and the resource id the server assigned.
func created(h *harness.H, ag *harness.Agent, res map[string]any) (act, id string) {
	_, act = h.Act(ag, "fhir.create", map[string]any{"resource_type": "Observation", "resource": res})
	a := h.WaitSealed(h.Acme, act)
	id = harness.Str(resultOf(a), "body", "id")
	if w := h.WitnessOf(h.Acme, act); harness.Str(w, "verdict") != "VERIFIED" || id == "" {
		panic(fmt.Sprintf("the honest create didn't verify: %v", w))
	}
	return act, id
}

// iDetail is each case's declared detail (attack_meta.json), reported when every check above it held.
var iDetail = map[string]string{
	"I01": "MISMATCH at L1 (fhir_conditional_matched: the server returned an existing resource version and this action created none; never VERIFIED)",
	"I02": "VERIFIED at L1 (the fields match) with the note fhir_no_new_version naming the unchanged version; the record never shows a new version",
	"I03": "MISMATCH at L1 (fhir_field_changed with the field's path; server-added meta and generated narrative don't count)",
	"I04": "UNVERIFIABLE (the action is unknown; fhir_history_no_match names the gap); never VERIFIED and never MISMATCH",
	"I09": "NO EFFECT (fhir_version_conflict: the action is sealed as failed, isn't retried, and no version is created or claimed)",
	"I10": "DENIED (no request leaves; a deed with decision deny is sealed; the server's history is unchanged)",
	"I12": "DENIED (the pack's no-delete-clinical rule; no request leaves; the deed names the rule)",
	"I11": "REDACTED (once the action is sealed the planted values appear in no action row, deed, event, export or log line of the org; only ids and digests remain)",
}

// I01: the agent creates an Observation conditionally (If-None-Exist on its identifier) after one
// already exists. The server answers 200 with the existing resource and writes nothing; the agent
// says it created one. The read-back sees the 200 for a conditional create: MISMATCH at L1.
func i01(h *harness.H) harness.Observed {
	obs := harness.Observed{Evidence: map[string]any{}, Forbidden: iForbidden}
	ag := fhirAgent(h)
	res, ident := observation("preliminary", 72)
	first, _ := created(h, ag, res)
	writes := fhirSrv.Writes()
	_, act := h.Act(ag, "fhir.create", map[string]any{"resource_type": "Observation", "resource": res, "if_none_exist": "identifier=urn:deedglass:attack|" + ident})
	obs.ActionIDs = []string{first, act}
	a := h.WaitSealed(h.Acme, act)
	w := h.WitnessOf(h.Acme, act)
	codes := harness.ReasonCodes(w, "L1")
	status := fmt.Sprint(harness.Get(resultOf(a), "status"))
	obs.Evidence["verdict"], obs.Evidence["level"], obs.Evidence["codes"] = harness.Str(w, "verdict"), harness.Str(w, "level"), codes
	obs.Evidence["http_status"], obs.Evidence["writes"] = status, []int{writes, fhirSrv.Writes()}
	if harness.Str(w, "verdict") == "MISMATCH" && harness.Str(w, "level") == "L1" && slices.Contains(codes, "fhir_conditional_matched") &&
		status == "200" && fhirSrv.Writes() == writes {
		obs.Outcome, obs.Detail = "MISMATCH", iDetail["I01"]
		obs.Line = "fhir_conditional_matched · HTTP 200 · 0 versions written"
	} else {
		obs.Outcome, obs.Detail = harness.Str(w, "verdict"), fmt.Sprintf("verdict %s at %s, codes %v, HTTP %s", harness.Str(w, "verdict"), harness.Str(w, "level"), codes, status)
	}
	return obs
}

// I02: the agent updates an Observation with exactly what it holds. The server keeps the version
// (HAPI suppresses no-change updates). VERIFIED at L1 with the note fhir_no_new_version naming
// the version; the server's history and the recorded result show no new version.
func i02(h *harness.H) harness.Observed {
	obs := harness.Observed{Evidence: map[string]any{}, Forbidden: iForbidden}
	ag := fhirAgent(h)
	res, _ := observation("final", 73)
	first, id := created(h, ag, res)
	versions := fhirSrv.Versions("Observation", id)
	_, act := h.Act(ag, "fhir.update", map[string]any{"resource_type": "Observation", "id": id, "resource": res})
	obs.ActionIDs = []string{first, act}
	a := h.WaitSealed(h.Acme, act)
	w := h.WitnessOf(h.Acme, act)
	codes := harness.ReasonCodes(w, "L1")
	note := ""
	for _, r := range reasonsOf(w) {
		if harness.Str(r, "code") == "fhir_no_new_version" {
			note = harness.Str(r, "message")
		}
	}
	vid := harness.Str(resultOf(a), "body", "meta", "versionId")
	obs.Evidence["verdict"], obs.Evidence["codes"], obs.Evidence["note"] = harness.Str(w, "verdict"), codes, note
	obs.Evidence["versions"], obs.Evidence["result_version"] = []int{versions, fhirSrv.Versions("Observation", id)}, vid
	if harness.Str(w, "verdict") == "VERIFIED" && harness.Str(w, "level") == "L1" && strings.Contains(note, "version \"1\"") &&
		fhirSrv.Versions("Observation", id) == versions && vid == "1" {
		obs.Outcome, obs.Detail = "VERIFIED", iDetail["I02"]
		obs.Line = "fhir_no_new_version · version 1 kept"
	} else {
		obs.Outcome, obs.Detail = harness.Str(w, "verdict"), fmt.Sprintf("verdict %s, codes %v, note %q, versions %d → %d, result version %q",
			harness.Str(w, "verdict"), codes, note, versions, fhirSrv.Versions("Observation", id), vid)
	}
	return obs
}

// resultOf is an action's recorded result: execution.result on the Gate's answers, result on GET.
func resultOf(a map[string]any) any {
	if r := harness.Get(a, "execution", "result"); r != nil {
		return r
	}
	return harness.Get(a, "result")
}

func reasonsOf(w map[string]any) []map[string]any {
	d, _ := w["detail"].(map[string]any)
	rs, _ := d["reasons"].([]any)
	var out []map[string]any
	for _, x := range rs {
		if m, ok := x.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

// I03: the agent sets status final; the server stores preliminary, and adds a tag of its own and a
// generated narrative. MISMATCH at L1 with fhir_field_changed on status alone: what the server adds
// by itself doesn't count.
func i03(h *harness.H) harness.Observed {
	obs := harness.Observed{Evidence: map[string]any{}, Forbidden: iForbidden}
	ag := fhirAgent(h)
	res, _ := observation("preliminary", 74)
	first, id := created(h, ag, res)
	fhirSrv.Mutate = func(_ string, r map[string]any) {
		r["status"] = "preliminary"
		r["meta"] = map[string]any{"tag": []any{map[string]any{"system": fhirServer, "code": "ingested"}}}
		r["text"] = map[string]any{"status": "generated", "div": "<div xmlns=\"http://www.w3.org/1999/xhtml\">Heart rate</div>"}
	}
	upd := map[string]any{}
	for k, v := range res {
		upd[k] = v
	}
	upd["status"] = "final"
	_, act := h.Act(ag, "fhir.update", map[string]any{"resource_type": "Observation", "id": id, "resource": upd})
	fhirSrv.Mutate = nil
	obs.ActionIDs = []string{first, act}
	w := h.WitnessOf(h.Acme, act)
	var paths []string
	for _, r := range reasonsOf(w) {
		if harness.Str(r, "code") == "fhir_field_changed" {
			ds, _ := r["diff"].([]any)
			for _, d := range ds {
				paths = append(paths, harness.Str(d, "path"))
			}
		}
	}
	obs.Evidence["verdict"], obs.Evidence["codes"], obs.Evidence["paths"] = harness.Str(w, "verdict"), harness.ReasonCodes(w, "L1"), paths
	if harness.Str(w, "verdict") == "MISMATCH" && harness.Str(w, "level") == "L1" && slices.Equal(paths, []string{"status"}) {
		obs.Outcome, obs.Detail = "MISMATCH", iDetail["I03"]
		obs.Line = "fhir_field_changed · status · tag and narrative ignored"
	} else {
		obs.Outcome, obs.Detail = harness.Str(w, "verdict"), fmt.Sprintf("verdict %s at %s, changed paths %v", harness.Str(w, "verdict"), harness.Str(w, "level"), paths)
	}
	return obs
}

// I04: the server commits the update, then answers 500. It stores no request ids (HAPI's JPA
// starter default), so no version in the history since the call carries the action's token.
// UNVERIFIABLE with fhir_history_no_match: the action is unknown, never VERIFIED, never MISMATCH.
func i04(h *harness.H) harness.Observed {
	obs := harness.Observed{Evidence: map[string]any{}, Forbidden: iForbidden}
	ag := fhirAgent(h)
	res, _ := observation("preliminary", 75)
	first, id := created(h, ag, res)
	writes := fhirSrv.Writes()
	fhirSrv.FailNext = 500
	upd := map[string]any{}
	for k, v := range res {
		upd[k] = v
	}
	upd["status"] = "final"
	_, act := h.Act(ag, "fhir.update", map[string]any{"resource_type": "Observation", "id": id, "resource": upd})
	obs.ActionIDs = []string{first, act}
	a := h.WaitSealed(h.Acme, act)
	w := h.WitnessOf(h.Acme, act)
	codes := harness.ReasonCodes(w, "L1")
	obs.Evidence["execution"], obs.Evidence["verdict"], obs.Evidence["codes"] = harness.Str(a, "execution", "status"), harness.Str(w, "verdict"), codes
	obs.Evidence["writes"] = []int{writes, fhirSrv.Writes()}
	if harness.Str(a, "execution", "status") == "unknown" && harness.Str(w, "verdict") == "UNVERIFIABLE" && slices.Contains(codes, "fhir_history_no_match") &&
		fhirSrv.Writes() == writes+1 {
		obs.Outcome, obs.Detail = "UNVERIFIABLE", iDetail["I04"]
		obs.Line = "unknown · fhir_history_no_match · the server committed 1 version"
	} else {
		obs.Outcome, obs.Detail = harness.Str(w, "verdict"), fmt.Sprintf("execution %s, verdict %s, codes %v", harness.Str(a, "execution", "status"), harness.Str(w, "verdict"), codes)
	}
	return obs
}

// I09: the agent updates with If-Match on version 1 after version 2 exists. The server answers
// 409 (HAPI; 412 elsewhere). NO EFFECT: failed with fhir_version_conflict, sent once, no version
// written, no verdict claimed.
func i09(h *harness.H) harness.Observed {
	obs := harness.Observed{Evidence: map[string]any{}, Forbidden: iForbidden}
	ag := fhirAgent(h)
	res, _ := observation("preliminary", 76)
	first, id := created(h, ag, res)
	upd := map[string]any{}
	for k, v := range res {
		upd[k] = v
	}
	upd["status"] = "final"
	_, second := h.Act(ag, "fhir.update", map[string]any{"resource_type": "Observation", "id": id, "resource": upd, "if_match": "1"})
	h.WaitSealed(h.Acme, second)
	versions := fhirSrv.Versions("Observation", id)
	upd["status"] = "amended"
	_, act := h.Act(ag, "fhir.update", map[string]any{"resource_type": "Observation", "id": id, "resource": upd, "if_match": "1"})
	obs.ActionIDs = []string{first, second, act}
	failed := h.FailedWith(h.Acme, act, "fhir_version_conflict")
	sent := 0
	for _, r := range fhirSrv.Requests {
		if r.Header.Get("Deedglass-Action-Id") == act {
			sent++
		}
	}
	a := h.Action(h.Acme, act)
	verdict := harness.Str(a, "witness", "verdict")
	obs.Evidence["failed"], obs.Evidence["sent"], obs.Evidence["verdict"] = failed, sent, verdict
	obs.Evidence["versions"] = []int{versions, fhirSrv.Versions("Observation", id)}
	if failed == "" && sent == 1 && fhirSrv.Versions("Observation", id) == versions && verdict == "" {
		obs.Outcome, obs.Detail = "NO EFFECT", iDetail["I09"]
		obs.Line = "fhir_version_conflict · HTTP 409 · sent once"
	} else {
		obs.Outcome, obs.Detail = "NO EFFECT?", fmt.Sprintf("failed check %q, sent %d, versions %d → %d, verdict %q", failed, sent, versions, fhirSrv.Versions("Observation", id), verdict)
	}
	return obs
}

// I10: the agent writes a Condition; the connector allows Observation and Patient. The call builds
// no request, so it has no request.fhir and the policy's default denies it. Nothing reaches the
// server.
func i10(h *harness.H) harness.Observed {
	obs := harness.Observed{Evidence: map[string]any{}, Forbidden: iForbidden}
	ag := fhirAgent(h)
	hits, writes := fhirSrv.Hits(), fhirSrv.Writes()
	r, act := h.Act(ag, "fhir.create", map[string]any{"resource_type": "Condition",
		"resource": map[string]any{"resourceType": "Condition", "subject": map[string]any{"reference": "Patient/attack-synthetic"}}})
	obs.ActionIDs = []string{act}
	why := h.DeniedBy(h.Acme, act, "default")
	obs.Evidence["status"], obs.Evidence["denied"], obs.Evidence["hits"] = r.Status, why, []int{hits, fhirSrv.Hits()}
	if why == "" && fhirSrv.Hits() == hits && fhirSrv.Writes() == writes {
		obs.Outcome, obs.Detail = "DENIED", iDetail["I10"]
		obs.Line = "default deny · 0 requests to the server"
	} else {
		obs.Outcome, obs.Detail = "DENIED?", fmt.Sprintf("deny check %q, server hits %d → %d", why, hits, fhirSrv.Hits())
	}
	return obs
}

const phiPolicy = `deedglass: policy/v1
name: clinic-phi
description: Group I's PHI-mode org. clinic-bot's FHIR calls that build a request, checked at L1.

defaults:
  decision: deny
  reason: No rule allows this tool.

rules:
  - id: fhir-clinic
    match: { agent: clinic-bot, connector_type: fhir }
    when: request.fhir.enriched
    decision: allow
    verify: L1
`

// I11: an org with PHI mode on. The agent creates an Observation carrying a planted patient name,
// MRN and clinical note, then updates it on a server that changes the note (so the witness detail
// compares the planted value). Once the actions are sealed and checked, the planted values are in
// no control-plane row (text or bytea), no exported deed and no log line; ids and digests remain.
func i11(h *harness.H) harness.Observed {
	obs := harness.Observed{Evidence: map[string]any{}, Forbidden: iForbidden}
	fhirAgent(h)
	org := h.Env.NewOrg("Clinic PHI", "clinic-phi")
	if org.Session == nil || org.Session.Do("PATCH", "/v1/org", map[string]any{"settings": map[string]any{"phi_mode": true}}).Status != 200 {
		panic("PHI mode couldn't be turned on through the owner's session")
	}
	h.Env.PushPolicy(org, []byte(phiPolicy))
	ag := h.Env.AddAgent(org, "clinic-bot", nil)
	if r := h.Env.AddConnector(org, "fhir", "fhir", fhirConfig(), "", nil); r.Status != 201 {
		panic(fmt.Sprintf("create the fhir connector: HTTP %d %s", r.Status, r.Body))
	}
	planted := []string{"dgPHIQuillfeather", "dgPHI-MRN-48151623", "dgPHI chest pain at rest"}
	res, _ := observation("preliminary", 77)
	res["identifier"] = []any{map[string]any{"system": "urn:deedglass:attack:mrn", "value": planted[1]}}
	res["subject"] = map[string]any{"reference": "Patient/attack-synthetic", "display": planted[0]}
	res["note"] = []any{map[string]any{"text": planted[2]}}
	_, first := h.Act(ag, "fhir.create", map[string]any{"resource_type": "Observation", "resource": res})
	a := h.WaitSealed(org, first)
	id := harness.Str(resultOf(a), "body", "id")
	fhirSrv.Mutate = func(_ string, r map[string]any) { r["note"] = []any{map[string]any{"text": "changed by the server"}} }
	res["status"] = "final"
	_, second := h.Act(ag, "fhir.update", map[string]any{"resource_type": "Observation", "id": id, "resource": res})
	h.WaitSealed(org, second)
	fhirSrv.Mutate = nil
	obs.ActionIDs = []string{first, second}
	v1, v2 := harness.Str(h.WitnessOf(org, first), "verdict"), harness.Str(h.WitnessOf(org, second), "verdict")
	// The serve job minimizes sealed PHI-mode actions every 2 s.
	deadline := time.Now().Add(30 * time.Second)
	left := "?"
	for time.Now().Before(deadline) {
		rows := h.Env.CPQuery(`SELECT count(*) FILTER (WHERE payload_minimized_at IS NULL) FROM deedglass.actions WHERE org_id = $1`, org.ID)
		if left = rows[0][0]; left == "0" {
			break
		}
		time.Sleep(250 * time.Millisecond)
	}
	found := map[string]any{}
	tables := 0
	for _, p := range planted {
		hits := h.Env.PlantedRows(p)
		tables = hits["_tables"]
		delete(hits, "_tables")
		if len(hits) > 0 {
			found[p] = hits
		}
	}
	out := filepath.Join(h.T.TempDir(), "clinic-phi.jsonl")
	_, xerr, code := h.Env.Run(nil, "deeds", "export", "--org", "clinic-phi", "--format", "jsonl", "--out", out)
	exported, _ := os.ReadFile(out)
	logs := h.Env.Logs.Bytes()
	for _, p := range planted {
		if strings.Contains(string(exported), p) {
			found[p+" (export)"] = true
		}
		if strings.Contains(string(logs), p) {
			found[p+" (log)"] = true
		}
	}
	shown := h.Action(org, first)
	digest := strings.HasPrefix(harness.Str(shown, "request", "resource"), "sha256:") && harness.Str(shown, "request", "resource_type") == "Observation"
	obs.Evidence["verdicts"], obs.Evidence["left"], obs.Evidence["tables"], obs.Evidence["found"] = []string{v1, v2}, left, tables, found
	obs.Evidence["export"], obs.Evidence["digests_shown"] = map[string]any{"code": code, "bytes": len(exported), "stderr": strings.TrimSpace(string(xerr))}, digest
	if v1 == "VERIFIED" && v2 == "MISMATCH" && left == "0" && tables >= 30 && len(found) == 0 && code == 0 && len(exported) > 0 && digest {
		obs.Outcome, obs.Detail = "REDACTED", iDetail["I11"]
		obs.Line = fmt.Sprintf("3 planted values · 0 hits in %d tables, the export and the log", tables)
	} else {
		obs.Outcome, obs.Detail = "REDACTED?", fmt.Sprintf("verdicts %s/%s, %s actions still hold payloads, found %v, export exit %d, digests shown %v", v1, v2, left, found, code, digest)
	}
	return obs
}

// I12: an org enforces the healthcare-fhir pack as the binary embeds it, with a fhir connector that
// allows delete. The agent deletes an Observation the server holds. The pack's no-delete-clinical
// rule denies it before any request is built: the server sees no request and still has the
// resource, and the sealed deed names the rule.
func i12(h *harness.H) harness.Observed {
	obs := harness.Observed{Evidence: map[string]any{}, Forbidden: iForbidden}
	fhirAgent(h)
	src := packs.Embedded()
	pol, ok := src.Files["healthcare-fhir/healthcare-fhir.yaml"]
	if !ok {
		panic("the binary embeds no healthcare-fhir pack")
	}
	org := h.Env.NewOrg("Clinic Pack", "clinic-pack")
	h.Env.PushPolicy(org, pol)
	ag := h.Env.AddAgent(org, "clinic-bot", nil)
	cfg := fhirConfig()
	cfg["allowed_interactions"] = []string{"read", "vread", "search", "history", "create", "update", "patch", "delete"}
	if r := h.Env.AddConnector(org, "fhir", "ehr", cfg, "", nil); r.Status != 201 {
		panic(fmt.Sprintf("create the fhir connector: HTTP %d %s", r.Status, r.Body))
	}
	res, _ := observation("final", 64)
	id := fhirSrv.Put(res)
	hits, writes, versions := fhirSrv.Hits(), fhirSrv.Writes(), fhirSrv.Versions("Observation", id)
	r, act := h.Act(ag, "ehr.delete", map[string]any{"resource_type": "Observation", "id": id})
	obs.ActionIDs = []string{act}
	why := h.DeniedBy(org, act, "no-delete-clinical")
	still := fhirSrv.Versions("Observation", id) == versions && fhirSrv.Current("Observation", id) != nil
	obs.Evidence["status"], obs.Evidence["denied"], obs.Evidence["hits"], obs.Evidence["still_there"] = r.Status, why, []int{hits, fhirSrv.Hits()}, still
	if why == "" && fhirSrv.Hits() == hits && fhirSrv.Writes() == writes && still {
		obs.Outcome, obs.Detail = "DENIED", iDetail["I12"]
		obs.Line = "no-delete-clinical · 0 requests to the server · the Observation is still there"
	} else {
		obs.Outcome, obs.Detail = "DENIED?", fmt.Sprintf("deny check %q, server hits %d → %d, still there %v", why, hits, fhirSrv.Hits(), still)
	}
	return obs
}
