//go:build attack

package attack

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/deedglass/deedglass/attack/harness"
	"github.com/deedglass/deedglass/internal/connector/fhir/fhirtest"
)

// Proven (L2) for a Postgres-backed FHIR server (HC-P05, ADR-105 §7, ADR-108): I05 to I08. Acme gets
// a second fhir connector, fhirpg, on a stand-in server that also writes HAPI FHIR JPA 8.x's own
// tables in a database on the throwaway cluster (fhirtest.UsePostgres), with witness.enabled and
// witness.server hapi; the decoder reads that database's change log through a REPLICATION role.
// The agent proven-bot meets the attack-baseline rule fhir-proven (verify: L2). The server is the
// attacker, or lets another client be: it commits more than the call asked for, or stores the text
// where the change log can't see it.

var (
	provenOnce sync.Once
	provenSrv  *fhirtest.Server
	provenDB   = "attack_fhir"
)

// provenAgent sets up the stand-in, its database and publication, Acme's fhirpg connector and
// proven-bot once, waits for the decoder to run, resets the knobs, and returns the agent.
func provenAgent(h *harness.H) *harness.Agent {
	provenOnce.Do(func() {
		admin, witness := h.Env.FHIRDatabase(provenDB)
		provenSrv = fhirtest.New()
		if err := provenSrv.UsePostgres(context.Background(), admin); err != nil {
			panic(err)
		}
		h.Env.FHIRPublication(provenDB, fhirtest.HAPITables)
		cfg := map[string]any{"base_url": provenSrv.Base(), "auth": map[string]any{"kind": "none"},
			"allowed_resource_types": []string{"Observation", "Patient"},
			"allowed_interactions":   []string{"read", "vread", "search", "history", "create", "update", "patch"},
			"server_tag_systems":     []string{fhirServer}}
		cfg["witness"] = map[string]any{"enabled": true, "server": "hapi", "l2_timeout": "20s", "bind_grace": "3s"}
		if r := h.Env.AddConnector(h.Acme, "fhir", "fhirpg", cfg, "fields", map[string]string{"witness_dsn": witness}); r.Status != 201 {
			panic(fmt.Sprintf("create the fhirpg connector: HTTP %d %s", r.Status, r.Body))
		}
		h.Env.AddAgent(h.Acme, "proven-bot", nil)
		id := h.Acme.Conns["fhirpg"]
		deadline := time.Now().Add(30 * time.Second)
		for {
			v := h.Acme.Admin.Do("GET", "/v1/connectors/"+id+"/decoder", nil).Map()
			if harness.Str(v, "status") == "running" {
				break
			}
			if time.Now().After(deadline) {
				panic(fmt.Sprintf("the fhirpg decoder isn't running: %v", v))
			}
			time.Sleep(200 * time.Millisecond)
		}
	})
	provenSrv.Mutate, provenSrv.FailNext = nil, 0
	provenSrv.PlaceholderRefs, provenSrv.LegacyText, provenSrv.InterleaveNext, provenSrv.ForgeNext = false, false, false, false
	return h.Acme.Agents["proven-bot"]
}

// l2Of is an action's witness once sealed: verdict, level, method and the L2 reasons' codes.
func l2Of(h *harness.H, act string) (map[string]any, []string) {
	h.WaitSealedWithin(h.Acme, act, 25*time.Second)
	w := h.WitnessOf(h.Acme, act)
	return w, harness.ReasonCodes(w, "L2")
}

// frames lists fhirpg's stored frames (class, action, flags) newest last.
func frames(h *harness.H) [][]string {
	return h.Env.CPQuery(`SELECT class, COALESCE(action_id, ''), array_to_string(flags, ',') FROM deedglass.decoded_txs
	   WHERE connector_id = $1 ORDER BY commit_lsn`, h.Acme.Conns["fhirpg"])
}

func waitFrames(h *harness.H, n int) [][]string {
	deadline := time.Now().Add(15 * time.Second)
	for {
		fs := frames(h)
		if len(fs) >= n || time.Now().After(deadline) {
			return fs
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// provenCreate runs an honest create on fhirpg and returns the action and the resource id; it must
// be VERIFIED at L2.
func provenCreate(h *harness.H, ag *harness.Agent, res map[string]any) (act, id string) {
	_, act = h.Act(ag, "fhirpg.create", map[string]any{"resource_type": "Observation", "resource": res})
	a := h.WaitSealedWithin(h.Acme, act, 25*time.Second)
	id = harness.Str(resultOf(a), "body", "id")
	if w, _ := l2Of(h, act); harness.Str(w, "verdict") != "VERIFIED" || harness.Str(w, "level") != "L2" || id == "" {
		panic(fmt.Sprintf("the honest create on fhirpg didn't verify at L2: %v", w))
	}
	return act, id
}

var provenDetail = map[string]string{
	"I05": "DETECTED (the other writer's version is reported as out_of_band and isn't bound to the action; the action's verdict comes from its own transaction only)",
	"I06": "DETECTED (the version is reported as request_id_reused and is never bound: binding needs the version the server reported to this action, so the forged version never makes the action VERIFIED)",
	"I07": "MISMATCH at L2 (fhir_extra_versions: the action's transaction committed 2 resource versions, the call reported 1; the extra resource is named)",
	"I08": "UNVERIFIABLE (L2 gap large_object_not_replicated); never MISMATCH and never VERIFIED",
}

// I05: while the agent's update is in flight, another writer commits its own version of the same
// resource. That transaction holds no version any action reported, so it is stored unattributed
// and flagged out_of_band; the agent's own transaction is bound and VERIFIED at L2, with the other
// version named in an out_of_band note.
func i05(h *harness.H) harness.Observed {
	obs := harness.Observed{Evidence: map[string]any{}, Forbidden: iForbidden}
	ag := provenAgent(h)
	res, _ := observation("preliminary", 81)
	first, id := provenCreate(h, ag, res)
	before := len(frames(h))
	provenSrv.InterleaveNext = true
	upd := map[string]any{}
	for k, v := range res {
		upd[k] = v
	}
	upd["status"] = "final"
	_, act := h.Act(ag, "fhirpg.update", map[string]any{"resource_type": "Observation", "id": id, "resource": upd})
	obs.ActionIDs = []string{first, act}
	w, codes := l2Of(h, act)
	fs := waitFrames(h, before+2)
	var oob, bound int
	for _, f := range fs[before:] {
		switch {
		case f[0] == "unattributed" && strings.Contains(f[2], "out_of_band") && f[1] == "":
			oob++
		case f[0] == "bound" && f[1] == act:
			bound++
		}
	}
	note := ""
	for _, r := range reasonsOf(w) {
		if harness.Str(r, "code") == "out_of_band" {
			note = harness.Str(r, "message")
		}
	}
	obs.Evidence["verdict"], obs.Evidence["level"], obs.Evidence["codes"] = harness.Str(w, "verdict"), harness.Str(w, "level"), codes
	obs.Evidence["frames"], obs.Evidence["note"], obs.Evidence["versions"] = fs[before:], note, provenSrv.Versions("Observation", id)
	if harness.Str(w, "verdict") == "VERIFIED" && harness.Str(w, "level") == "L2" && slices.Contains(codes, "out_of_band") &&
		strings.Contains(note, "Observation/"+id) && oob == 1 && bound == 1 && provenSrv.Versions("Observation", id) == 3 {
		obs.Outcome, obs.Detail = "DETECTED", provenDetail["I05"]
		obs.Line = "out_of_band · 1 unattributed transaction · the action's own VERIFIED at L2"
	} else {
		obs.Outcome, obs.Detail = "DETECTED?", fmt.Sprintf("verdict %s at %s, codes %v, frames %v", harness.Str(w, "verdict"), harness.Str(w, "level"), codes, fs[before:])
	}
	return obs
}

// I06: while the agent's create is in flight, another client creates a resource carrying the
// action's X-Request-ID token. HAPI stores it in request_id, but no action reported that version,
// so the transaction is request_id_reused (a decoder alert), never bound; the action is judged on its
// own transaction only.
func i06(h *harness.H) harness.Observed {
	obs := harness.Observed{Evidence: map[string]any{}, Forbidden: iForbidden}
	ag := provenAgent(h)
	wait, stop := h.Env.ListenAlerts("request_id_reused")
	defer stop()
	before := len(frames(h))
	provenSrv.ForgeNext = true
	res, _ := observation("final", 82)
	_, act := h.Act(ag, "fhirpg.create", map[string]any{"resource_type": "Observation", "resource": res})
	obs.ActionIDs = []string{act}
	w, codes := l2Of(h, act)
	fs := waitFrames(h, before+2)
	var reused, bound int
	for _, f := range fs[before:] {
		switch {
		case f[0] == "request_id_reused" && f[1] == act:
			reused++
		case f[0] == "bound" && f[1] == act:
			bound++
		}
	}
	alerted := wait(5 * time.Second)
	obs.Evidence["verdict"], obs.Evidence["level"], obs.Evidence["codes"], obs.Evidence["frames"], obs.Evidence["alert"] =
		harness.Str(w, "verdict"), harness.Str(w, "level"), codes, fs[before:], alerted
	if reused == 1 && bound == 1 && slices.Contains(codes, "request_id_reused") && slices.Contains(codes, "version_matched") &&
		harness.Str(w, "verdict") == "VERIFIED" && harness.Str(w, "level") == "L2" && alerted {
		obs.Outcome, obs.Detail = "DETECTED", provenDetail["I06"]
		obs.Line = "request_id_reused · decoder.alert · never bound; the action's own transaction judged"
	} else {
		obs.Outcome, obs.Detail = "DETECTED?", fmt.Sprintf("verdict %s at %s, codes %v, frames %v, alert %v", harness.Str(w, "verdict"), harness.Str(w, "level"), codes, fs[before:], alerted)
	}
	return obs
}

// I07: the agent creates one Observation whose subject names a Patient the server doesn't know;
// the server commits a placeholder Patient in the same transaction. The call reported one version,
// the transaction holds two: MISMATCH at L2 with fhir_extra_versions naming the Patient.
func i07(h *harness.H) harness.Observed {
	obs := harness.Observed{Evidence: map[string]any{}, Forbidden: iForbidden}
	ag := provenAgent(h)
	provenSrv.PlaceholderRefs = true
	ghost := fmt.Sprintf("ghost-%d", time.Now().UnixNano()%1_000_000_000)
	res, _ := observation("final", 83)
	res["subject"] = map[string]any{"reference": "Patient/" + ghost}
	_, act := h.Act(ag, "fhirpg.create", map[string]any{"resource_type": "Observation", "resource": res})
	obs.ActionIDs = []string{act}
	w, codes := l2Of(h, act)
	named := ""
	for _, r := range reasonsOf(w) {
		if harness.Str(r, "code") == "fhir_extra_versions" {
			named = harness.Str(r, "message")
		}
	}
	obs.Evidence["verdict"], obs.Evidence["level"], obs.Evidence["codes"], obs.Evidence["message"] = harness.Str(w, "verdict"), harness.Str(w, "level"), codes, named
	if harness.Str(w, "verdict") == "MISMATCH" && harness.Str(w, "level") == "L2" && slices.Contains(codes, "fhir_extra_versions") &&
		strings.Contains(named, "committed 2 resource versions") && strings.Contains(named, "Patient/"+ghost) && provenSrv.Versions("Patient", ghost) == 1 {
		obs.Outcome, obs.Detail = "MISMATCH", provenDetail["I07"]
		obs.Line = "fhir_extra_versions · Patient/" + ghost + " committed in the same transaction"
	} else {
		obs.Outcome, obs.Detail = harness.Str(w, "verdict"), fmt.Sprintf("verdict %s at %s, codes %v, message %q", harness.Str(w, "verdict"), harness.Str(w, "level"), codes, named)
	}
	return obs
}

// I08: the server keeps the version's text in a large object (JSONC, as HAPI did before 7.0).
// Large objects aren't in Postgres's change log, so the version is bound by its columns but its
// content can't be compared: UNVERIFIABLE with large_object_not_replicated, never MISMATCH and
// never VERIFIED, though the read-back (L1) matched.
func i08(h *harness.H) harness.Observed {
	obs := harness.Observed{Evidence: map[string]any{}, Forbidden: iForbidden}
	ag := provenAgent(h)
	provenSrv.LegacyText = true
	res, _ := observation("final", 84)
	_, act := h.Act(ag, "fhirpg.create", map[string]any{"resource_type": "Observation", "resource": res})
	obs.ActionIDs = []string{act}
	w, codes := l2Of(h, act)
	l1 := harness.ReasonCodes(w, "L1")
	obs.Evidence["verdict"], obs.Evidence["level"], obs.Evidence["l2"], obs.Evidence["l1"] = harness.Str(w, "verdict"), harness.Str(w, "level"), codes, l1
	if harness.Str(w, "verdict") == "UNVERIFIABLE" && slices.Contains(codes, "large_object_not_replicated") && slices.Contains(l1, "readback_matched") {
		obs.Outcome, obs.Detail = "UNVERIFIABLE", provenDetail["I08"]
		obs.Line = "large_object_not_replicated · L1 matched, L2 can't compare"
	} else {
		obs.Outcome, obs.Detail = harness.Str(w, "verdict"), fmt.Sprintf("verdict %s at %s, L2 codes %v, L1 codes %v", harness.Str(w, "verdict"), harness.Str(w, "level"), codes, l1)
	}
	return obs
}
