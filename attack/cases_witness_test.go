//go:build attack

package attack

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/deedglass/deedglass/attack/harness"
)

// Witness L2 cases (S21): H08–H11 (declared by S18, fixtures in attack/fixtures/sql/witness_h08_h11.sql)
// and W01–W13 (S21 §3.11, ADR-046). Every case runs on its own clone of the target, points the
// decoder at it (H.L2Ready: a new slot in the clone), and acts as witness-bot, whose rule asks for
// verify: L2. The outcome is the verdict the Witness sealed, read from the API with its detail; the
// detail head is built from what was observed, so a different number is a different detail.

const (
	shipOne      = "UPDATE public.orders SET status = 'shipped' WHERE id = 1"
	shipFortyTwo = "UPDATE public.orders SET status = 'shipped' WHERE id = 42"
)

func witnessBot(h *harness.H) *harness.Agent { return h.Acme.Agents["witness-bot"] }

// l2Act sends one postgres.execute as witness-bot and returns the action id and its sealed witness.
func l2Act(h *harness.H, sql string) (string, map[string]any) {
	_, id := h.Act(witnessBot(h), "postgres.execute", map[string]any{"sql": sql})
	if id == "" {
		panic("no action was recorded for " + sql)
	}
	return id, h.WitnessOf(h.Acme, id)
}

// mustExec runs fixture SQL on the clone as the superuser.
func mustExec(h *harness.H, sql string, args ...any) {
	if err := h.Target.Exec(sql, args...); err != nil {
		panic(fmt.Sprintf("fixture: %v\n%s", err, sql))
	}
}

func loadFixture(h *harness.H, name string) {
	b, err := os.ReadFile(filepath.Join("fixtures", "sql", name))
	if err != nil {
		panic(err)
	}
	mustExec(h, string(b))
}

func codesAt(w map[string]any, level string) []string { return harness.ReasonCodes(w, level) }

func witnessEvidence(id string, w map[string]any) map[string]any {
	return map[string]any{"action": id, "level": harness.Str(w, "level"), "verdict": harness.Str(w, "verdict"),
		"summary": harness.Str(w, "summary"), "L1": codesAt(w, "L1"), "L2": codesAt(w, "L2")}
}

// heroTrigger is S21's legacy trigger (examples/demo/sql/10_trigger_on.sql), on the attack target:
// when one order ships, it ships the next 300 orders that aren't shipped yet.
const heroTrigger = `CREATE SCHEMA IF NOT EXISTS acme_legacy;
CREATE FUNCTION acme_legacy.ship_open_batch() RETURNS trigger
  LANGUAGE plpgsql SET search_path = pg_catalog, pg_temp AS $fn$
BEGIN
  IF pg_catalog.pg_trigger_depth() = 1 THEN
    UPDATE public.orders o SET status = 'shipped'
     WHERE o.id IN (SELECT i.id FROM public.orders i WHERE i.id <> NEW.id AND i.status <> 'shipped' ORDER BY i.id LIMIT 300);
  END IF;
  RETURN NULL;
END $fn$;
REVOKE ALL ON FUNCTION acme_legacy.ship_open_batch() FROM PUBLIC;
CREATE TRIGGER orders_ship_open_batch AFTER UPDATE OF status ON public.orders
  FOR EACH ROW WHEN (NEW.status = 'shipped' AND OLD.status IS DISTINCT FROM 'shipped')
  EXECUTE FUNCTION acme_legacy.ship_open_batch();
INSERT INTO public.orders (id, customer_id, status, total_cents)
  SELECT n, 1 + (n % 20), 'paid', 1000 + n FROM pg_catalog.generate_series(51, 400) AS n;
UPDATE public.orders SET status = 'paid' WHERE id = 42;`

// numberEvidence reads row_count_exceeded's observed and expected counts and unexpected_key's
// outside count from a witness detail.
func numberEvidence(w map[string]any) (observed, expected, outside int64) {
	d, _ := w["detail"].(map[string]any)
	rs, _ := d["reasons"].([]any)
	for _, x := range rs {
		r, _ := x.(map[string]any)
		ev, _ := r["evidence"].(map[string]any)
		switch harness.Str(r, "code") {
		case "row_count_exceeded":
			observed, expected = num(ev["observed"]), num(ev["expected"])
		case "unexpected_key":
			outside = num(ev["outside"])
		}
	}
	return
}

func num(v any) int64 {
	switch x := v.(type) {
	case float64:
		return int64(x)
	case int64:
		return x
	}
	return -1
}

// verdictOf is the outcome word for a witness: the verdict, or ERROR when there is none.
func verdictOf(w map[string]any) string {
	if v := harness.Str(w, "verdict"); v != "" {
		return v
	}
	return "ERROR"
}

func init() {
	register(
		// H08: the trigger fans out to 300 order_events rows.
		harness.Case{ID: "H08", Run: func(h *harness.H) harness.Observed {
			loadFixture(h, "witness_h08_h11.sql")
			h.L2Ready()
			id, w := l2Act(h, shipOne)
			obs := harness.Observed{ActionIDs: []string{id}, Evidence: witnessEvidence(id, w), Outcome: verdictOf(w)}
			if obs.Outcome == "MISMATCH" && harness.Str(w, "level") == "L2" && slices.Contains(codesAt(w, "L2"), "unexpected_table") {
				obs.Detail, obs.Line = "MISMATCH (L2: rows outside the expected footprint)", "unexpected_table · public.order_events"
			} else {
				obs.Detail = fmt.Sprintf("%s (%s: %v)", obs.Outcome, harness.Str(w, "level"), codesAt(w, "L2"))
			}
			return obs
		}},
		// H09: a BEFORE trigger sets internal_note on the row the statement updates.
		harness.Case{ID: "H09", Run: func(h *harness.H) harness.Observed {
			loadFixture(h, "witness_h08_h11.sql")
			h.L2Ready()
			id, w := l2Act(h, "UPDATE public.orders SET total_cents = total_cents + 1 WHERE id = 2")
			obs := harness.Observed{ActionIDs: []string{id}, Evidence: witnessEvidence(id, w), Outcome: verdictOf(w)}
			if obs.Outcome == "MISMATCH" && harness.Str(w, "level") == "L2" && slices.Contains(codesAt(w, "L2"), "unexpected_column") &&
				strings.Contains(fmt.Sprint(w["reasons"]), "internal_note") {
				obs.Detail, obs.Line = "MISMATCH (L2: column outside the SET list)", "unexpected_column · internal_note"
			} else {
				obs.Detail = fmt.Sprintf("%s (%s: %v)", obs.Outcome, harness.Str(w, "level"), codesAt(w, "L2"))
			}
			return obs
		}},
		// H10: a second session forges the marker for a real action, with no ledger row.
		harness.Case{ID: "H10", Run: func(h *harness.H) harness.Observed {
			loadFixture(h, "witness_h08_h11.sql")
			h.L2Ready()
			id, w := l2Act(h, "UPDATE public.customers SET note = 'h10' WHERE id = 4")
			wait, stop := h.Env.ListenAlerts("forged_marker")
			defer stop()
			mustExec(h, `SELECT pg_catalog.pg_logical_emit_message(true, 'deedglass', $1::text)`, id)
			alerted := wait(10 * time.Second)
			after := h.WitnessOf(h.Acme, id)
			obs := harness.Observed{ActionIDs: []string{id}, Evidence: witnessEvidence(id, after)}
			obs.Evidence["alert"] = alerted
			same := harness.Str(after, "verdict") == harness.Str(w, "verdict") && harness.Str(after, "level") == harness.Str(w, "level")
			switch {
			case alerted && same:
				obs.Outcome, obs.Detail, obs.Line = "DETECTED", "DETECTED (forged_marker alert; no action's verdict changes)", "forged_marker alert"
			default:
				obs.Outcome = "NOT DETECTED"
				obs.Detail = fmt.Sprintf("alert %v, verdict %s → %s", alerted, harness.Str(w, "verdict"), harness.Str(after, "verdict"))
			}
			return obs
		}},
		// H11: customers back on the default replica identity.
		harness.Case{ID: "H11", Run: func(h *harness.H) harness.Observed {
			loadFixture(h, "witness_h08_h11.sql")
			mustExec(h, `ALTER TABLE public.customers REPLICA IDENTITY DEFAULT`)
			h.L2Ready()
			id, w := l2Act(h, "UPDATE public.customers SET note = 'x' WHERE id = 3")
			obs := harness.Observed{ActionIDs: []string{id}, Evidence: witnessEvidence(id, w), Outcome: verdictOf(w)}
			if obs.Outcome == "UNVERIFIABLE" && slices.Contains(codesAt(w, "L2"), "old_image_missing") {
				obs.Detail, obs.Line = "UNVERIFIABLE (replica identity gap; never MISMATCH)", "old_image_missing"
			} else {
				obs.Detail = fmt.Sprintf("%s (%s: %v)", obs.Outcome, harness.Str(w, "level"), codesAt(w, "L2"))
			}
			return obs
		}},

		// W01: the hero.
		harness.Case{ID: "W01", Run: func(h *harness.H) harness.Observed {
			mustExec(h, heroTrigger)
			h.L2Ready()
			id, w := l2Act(h, shipFortyTwo)
			obs := harness.Observed{ActionIDs: []string{id}, Evidence: witnessEvidence(id, w), Outcome: verdictOf(w)}
			o, e, out := numberEvidence(w)
			obs.Evidence["observed"], obs.Evidence["expected"], obs.Evidence["outside"] = o, e, out
			if obs.Outcome != "MISMATCH" || harness.Str(w, "level") != "L2" ||
				harness.Str(w, "summary") != "Postgres changed 301 rows in public.orders. The agent reported 1." {
				obs.Detail = fmt.Sprintf("%s (%s: %s)", obs.Outcome, harness.Str(w, "level"), harness.Str(w, "summary"))
				return obs
			}
			obs.Detail = fmt.Sprintf("MISMATCH (L2: Postgres changed %d rows in public.orders, the agent reported %d; %d rows outside id = 42)", o, e, out)
			obs.Line = fmt.Sprintf("%d rows vs %d · %d outside id = 42", o, e, out)
			return obs
		}},
		// W02: a BEFORE trigger also zeroes total_cents.
		harness.Case{ID: "W02", Run: func(h *harness.H) harness.Observed {
			mustExec(h, `CREATE FUNCTION public.zero_total() RETURNS trigger LANGUAGE plpgsql SET search_path = pg_catalog, pg_temp AS $fn$
BEGIN NEW.total_cents := 0; RETURN NEW; END $fn$;
CREATE TRIGGER orders_zero_total BEFORE UPDATE OF status ON public.orders FOR EACH ROW EXECUTE FUNCTION public.zero_total();`)
			h.L2Ready()
			id, w := l2Act(h, shipOne)
			return columnCase(id, w, "unexpected_column", "MISMATCH (L2: a column outside the SET list changed)", "total_cents")
		}},
		// W03: a BEFORE trigger rewrites the new status.
		harness.Case{ID: "W03", Run: func(h *harness.H) harness.Observed {
			mustExec(h, `CREATE FUNCTION public.cancel_instead() RETURNS trigger LANGUAGE plpgsql SET search_path = pg_catalog, pg_temp AS $fn$
BEGIN NEW.status := 'cancelled'; RETURN NEW; END $fn$;
CREATE TRIGGER orders_cancel_instead BEFORE UPDATE OF status ON public.orders FOR EACH ROW EXECUTE FUNCTION public.cancel_instead();`)
			h.L2Ready()
			id, w := l2Act(h, shipOne)
			obs := harness.Observed{ActionIDs: []string{id}, Evidence: witnessEvidence(id, w), Outcome: verdictOf(w)}
			if obs.Outcome == "MISMATCH" && harness.Str(w, "level") == "L2" && slices.Contains(codesAt(w, "L1"), "value_mismatch") &&
				slices.Contains(codesAt(w, "L2"), "value_mismatch") {
				obs.Detail = "MISMATCH at L1 and at L2 (value_mismatch: the committed value isn't the SET literal); the claim is L2"
				obs.Line = `value_mismatch at L1 and L2 · "cancelled", not "shipped"`
			} else {
				obs.Detail = fmt.Sprintf("%s (%s: L1 %v, L2 %v)", obs.Outcome, harness.Str(w, "level"), codesAt(w, "L1"), codesAt(w, "L2"))
			}
			return obs
		}},
		// W04: an AFTER trigger deletes another order.
		harness.Case{ID: "W04", Run: func(h *harness.H) harness.Observed {
			mustExec(h, `CREATE FUNCTION public.drop_other() RETURNS trigger LANGUAGE plpgsql SET search_path = pg_catalog, pg_temp AS $fn$
BEGIN
  IF pg_catalog.pg_trigger_depth() = 1 THEN DELETE FROM public.orders WHERE id = 50; END IF;
  RETURN NULL;
END $fn$;
CREATE TRIGGER orders_drop_other AFTER UPDATE OF status ON public.orders FOR EACH ROW EXECUTE FUNCTION public.drop_other();`)
			h.L2Ready()
			id, w := l2Act(h, shipOne)
			return columnCase(id, w, "unexpected_operation", "MISMATCH (L2: a DELETE where the statement only updates rows)", "deleted 1 row")
		}},
		// W05: another session forges a marker naming a live action; the action's own frame decides.
		harness.Case{ID: "W05", Run: func(h *harness.H) harness.Observed {
			h.L2Ready()
			wait, stop := h.Env.ListenAlerts("forged_marker")
			defer stop()
			// The action is sent without waiting, and the forged marker is committed while it waits.
			r := witnessBot(h).API.Do("POST", "/v1/actions", map[string]any{"tool": "postgres.execute", "args": map[string]any{"sql": shipOne},
				"mode": "gateway", "wait": 0})
			id := harness.Str(r.Map(), "id")
			mustExec(h, `SELECT pg_catalog.pg_logical_emit_message(true, 'deedglass', $1::text)`, id)
			alerted := wait(10 * time.Second)
			w := h.WitnessOf(h.Acme, id)
			obs := harness.Observed{ActionIDs: []string{id}, Evidence: witnessEvidence(id, w)}
			obs.Evidence["alert"] = alerted
			switch {
			case alerted && harness.Str(w, "level") == "L2" && harness.Str(w, "verdict") == "VERIFIED":
				obs.Outcome = "DETECTED"
				obs.Detail = "DETECTED (forged_marker frame and a decoder.alert); the live action is L2 · VERIFIED from its own frame"
				obs.Line = "forged_marker alert · the action L2 · VERIFIED"
			default:
				obs.Outcome = "NOT DETECTED"
				obs.Detail = fmt.Sprintf("alert %v; the action %s · %s", alerted, harness.Str(w, "level"), harness.Str(w, "verdict"))
			}
			return obs
		}},
		// W06: a superuser writes a second ledger row and marker for a sealed action (the first row is
		// deleted in its own transaction: the ledger's primary key allows one row per action).
		harness.Case{ID: "W06", Run: func(h *harness.H) harness.Observed {
			h.L2Ready()
			id, first := l2Act(h, shipOne)
			mustExec(h, fmt.Sprintf(`BEGIN;
CREATE TEMP TABLE w06_row AS SELECT * FROM deedglass.ledger WHERE action_id = %[1]s;
DELETE FROM deedglass.ledger WHERE action_id = %[1]s;
COMMIT;
BEGIN;
INSERT INTO deedglass.ledger (action_id, org_id, agent_id, intent_hash, sql_digest, xid8, created_at)
  SELECT action_id, org_id, agent_id, intent_hash, sql_digest, pg_catalog.pg_current_xact_id(), pg_catalog.now() FROM w06_row;
SELECT pg_catalog.pg_logical_emit_message(true, 'deedglass', %[1]s);
COMMIT;`, quoteID(id)))
			hist, w := waitHistory(h, id, 2, 20*time.Second)
			obs := harness.Observed{ActionIDs: []string{id}, Evidence: witnessEvidence(id, w), Outcome: verdictOf(w)}
			obs.Evidence["first"], obs.Evidence["history"] = harness.Str(first, "verdict"), hist
			if obs.Outcome == "MISMATCH" && slices.Contains(codesAt(w, "L2"), "multiple_transactions") && len(hist) == 2 &&
				harness.Str(hist[1], "kind") == "amendment" && harness.Str(hist[0], "verdict") == "VERIFIED" {
				obs.Detail = "MISMATCH (L2: multiple_transactions), as an amendment deed when the action was already sealed"
				obs.Line = "multiple_transactions · amendment deed"
			} else {
				obs.Detail = fmt.Sprintf("%s (%s: %v; %d history entries)", obs.Outcome, harness.Str(w, "level"), codesAt(w, "L2"), len(hist))
			}
			return obs
		}},
		// W07: a clean update of a table without full old rows, verify: L2.
		harness.Case{ID: "W07", Run: func(h *harness.H) harness.Observed {
			mustExec(h, `ALTER TABLE public.orders REPLICA IDENTITY DEFAULT`)
			h.L2Ready()
			id, w := l2Act(h, shipOne)
			return gapCase(id, w, "old_image_missing", "UNVERIFIABLE (L2 gap old_image_missing); never MISMATCH")
		}},
		// W08: the hero's trigger on a table without full old rows.
		harness.Case{ID: "W08", Run: func(h *harness.H) harness.Observed {
			mustExec(h, heroTrigger)
			mustExec(h, `ALTER TABLE public.orders REPLICA IDENTITY DEFAULT`)
			h.L2Ready()
			id, w := l2Act(h, shipFortyTwo)
			obs := harness.Observed{ActionIDs: []string{id}, Evidence: witnessEvidence(id, w), Outcome: verdictOf(w)}
			l2 := codesAt(w, "L2")
			if obs.Outcome == "MISMATCH" && harness.Str(w, "level") == "L2" && slices.Contains(l2, "row_count_exceeded") && slices.Contains(l2, "old_image_missing") {
				obs.Detail, obs.Line = "MISMATCH (L2: row_count_exceeded; the violation beats the old_image_missing gap)", "row_count_exceeded beats old_image_missing"
			} else {
				obs.Detail = fmt.Sprintf("%s (%s: %v)", obs.Outcome, harness.Str(w, "level"), l2)
			}
			return obs
		}},
		// W09: UPDATE … FROM another table.
		harness.Case{ID: "W09", Run: func(h *harness.H) harness.Observed {
			h.L2Ready()
			id, w := l2Act(h, "UPDATE public.orders o SET status = 'shipped' FROM public.customers c WHERE c.id = o.customer_id AND o.id = 1")
			return gapCase(id, w, "footprint_unknown", "UNVERIFIABLE (L2 gap footprint_unknown; the claim follows the required level); never MISMATCH")
		}},
		// W10: the decoder is held back past l2_timeout, then catches up: an amendment deed.
		harness.Case{ID: "W10", Run: func(h *harness.H) harness.Observed {
			h.L2Ready()
			holdDecoder(h)
			defer releaseDecoder(h)
			id, sealed := l2Act(h, shipOne)
			releaseDecoder(h)
			h.Target.Repoint()
			hist, w := waitHistory(h, id, 2, 20*time.Second)
			obs := harness.Observed{ActionIDs: []string{id}, Evidence: witnessEvidence(id, w), Outcome: verdictOf(w)}
			obs.Evidence["sealed"], obs.Evidence["history"] = harness.Str(sealed, "verdict")+" "+fmt.Sprint(codesAt(sealed, "L2")), hist
			amends := ""
			if len(hist) == 2 {
				amends = harness.Str(h.Deed(h.Acme, harness.Str(hist[1], "deed")), "amends")
			}
			if len(hist) == 2 && harness.Str(hist[0], "kind") == "action" && harness.Str(hist[0], "verdict") == "UNVERIFIABLE" &&
				harness.Str(hist[0], "reason") == "l2_timeout" && harness.Str(hist[1], "kind") == "amendment" &&
				harness.Str(hist[1], "level") == "L2" && harness.Str(hist[1], "verdict") == "VERIFIED" && amends == harness.Str(hist[0], "deed") {
				obs.Detail = "Action deed UNVERIFIABLE (l2_timeout); then an amendment deed L2 · VERIFIED that amends it; the chain verifies"
				obs.Line = "l2_timeout, then an amendment deed L2 · VERIFIED"
			} else {
				obs.Detail = fmt.Sprintf("history %v; amends %q", hist, amends)
			}
			return obs
		}},
		// W11: the slot is dropped while the action waits for L2.
		harness.Case{ID: "W11", Run: func(h *harness.H) harness.Observed {
			h.L2Ready()
			holdDecoder(h)
			defer releaseDecoder(h)
			r := witnessBot(h).API.Do("POST", "/v1/actions", map[string]any{"tool": "postgres.execute", "args": map[string]any{"sql": shipOne},
				"mode": "gateway", "wait": 0})
			id := harness.Str(r.Map(), "id")
			dropped := false
			for i := 0; i < 30 && !dropped; i++ {
				dropped = h.Target.Exec(`SELECT pg_catalog.pg_drop_replication_slot(slot_name) FROM pg_catalog.pg_replication_slots
				    WHERE database = pg_catalog.current_database() AND slot_name LIKE 'deedglass\_%' AND NOT active`) == nil && !slotLeft(h)
				if !dropped {
					time.Sleep(100 * time.Millisecond)
				}
			}
			releaseDecoder(h)
			h.Target.Repoint()
			w := h.WitnessOf(h.Acme, id)
			obs := gapCase(id, w, "slot_dropped", "UNVERIFIABLE (L2 gap slot_dropped); never MISMATCH")
			obs.Evidence["dropped"] = dropped
			return obs
		}},
		// W12: deedglass_pub dropped and re-created around an action (PG 18 decodes the marker as a
		// publication gap; 16 and 17 halt the decoder instead, which is decoder_halted).
		harness.Case{ID: "W12", Run: func(h *harness.H) harness.Observed {
			h.L2Ready()
			var def string
			c := h.Target.Conn()
			_ = c.QueryRow(t0(), `SELECT 'CREATE PUBLICATION deedglass_pub FOR TABLE ' || pg_catalog.string_agg(pg_catalog.quote_ident(schemaname) || '.' || pg_catalog.quote_ident(tablename), ', ')
			    FROM pg_catalog.pg_publication_tables WHERE pubname = 'deedglass_pub'`).Scan(&def)
			c.Close(t0())
			mustExec(h, `DROP PUBLICATION deedglass_pub`)
			id, w := l2Act(h, shipOne)
			mustExec(h, def)
			return gapCase(id, w, "publication_gap", "UNVERIFIABLE (L2 gap publication_gap); never MISMATCH")
		}},
		// W13: the ledger row's sql_digest isn't the authorized statement's (a simulated compromised
		// execution path: a trigger on deedglass.ledger rewrites it).
		harness.Case{ID: "W13", Run: func(h *harness.H) harness.Observed {
			mustExec(h, `CREATE FUNCTION public.swap_digest() RETURNS trigger LANGUAGE plpgsql SET search_path = pg_catalog, pg_temp AS $fn$
BEGIN NEW.sql_digest := 'sha256:' || pg_catalog.repeat('0', 64); RETURN NEW; END $fn$;
CREATE TRIGGER ledger_swap_digest BEFORE INSERT ON deedglass.ledger FOR EACH ROW EXECUTE FUNCTION public.swap_digest();`)
			h.L2Ready()
			id, w := l2Act(h, shipOne)
			obs := harness.Observed{ActionIDs: []string{id}, Evidence: witnessEvidence(id, w), Outcome: verdictOf(w)}
			msg := harness.Str(w, "summary")
			if obs.Outcome == "MISMATCH" && slices.Contains(codesAt(w, "L2"), "ledger_mismatch") && strings.Contains(msg, "(sql_digest)") &&
				!strings.Contains(fmt.Sprint(w["detail"]), "sha256:0000") {
				obs.Detail, obs.Line = "MISMATCH (L2: ledger_mismatch naming sql_digest, never the values)", "ledger_mismatch · sql_digest"
			} else {
				obs.Detail = fmt.Sprintf("%s (%s: %v; %s)", obs.Outcome, harness.Str(w, "level"), codesAt(w, "L2"), msg)
			}
			return obs
		}},
	)
}

func t0() context.Context { return context.Background() }

// quoteID quotes an action id for SQL after checking its shape (it only ever comes from the API).
func quoteID(id string) string {
	if !reActID.MatchString(id) {
		panic("not an action id: " + id)
	}
	return "'" + id + "'"
}

var reActID = regexp.MustCompile(`^act_[0-7][0-9a-hjkmnp-tv-z]{25}$`)

// waitHistory waits until an action's witness.history has n entries and returns them with the
// witness.
func waitHistory(h *harness.H, id string, n int, d time.Duration) ([]map[string]any, map[string]any) {
	deadline := time.Now().Add(d)
	for {
		w := h.WitnessOf(h.Acme, id)
		var hist []map[string]any
		xs, _ := w["history"].([]any)
		for _, x := range xs {
			if m, ok := x.(map[string]any); ok {
				hist = append(hist, m)
			}
		}
		if len(hist) >= n || time.Now().After(deadline) {
			return hist, w
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// holdDecoder keeps the decoder from reading: its role can't log in, and its sessions end.
func holdDecoder(h *harness.H) {
	if err := h.Env.AdminExec(`ALTER ROLE deedglass_witness NOLOGIN`); err != nil {
		panic(err)
	}
	_ = h.Env.AdminExec(`SELECT pg_catalog.pg_terminate_backend(pid) FROM pg_catalog.pg_stat_activity WHERE usename = 'deedglass_witness'`)
}

func releaseDecoder(h *harness.H) { _ = h.Env.AdminExec(`ALTER ROLE deedglass_witness LOGIN`) }

func slotLeft(h *harness.H) bool {
	c := h.Target.Conn()
	defer c.Close(t0())
	var n int
	_ = c.QueryRow(t0(), `SELECT count(*) FROM pg_catalog.pg_replication_slots WHERE database = pg_catalog.current_database()
	    AND slot_name LIKE 'deedglass\_%'`).Scan(&n)
	return n > 0
}

// gapCase is an UNVERIFIABLE case whose L2 reasons must hold the gap code.
func gapCase(id string, w map[string]any, code, detail string) harness.Observed {
	obs := harness.Observed{ActionIDs: []string{id}, Evidence: witnessEvidence(id, w), Outcome: verdictOf(w)}
	if obs.Outcome == "UNVERIFIABLE" && slices.Contains(codesAt(w, "L2"), code) {
		obs.Detail, obs.Line = detail, code
		return obs
	}
	obs.Detail = fmt.Sprintf("%s (%s: %v)", obs.Outcome, harness.Str(w, "level"), codesAt(w, "L2"))
	return obs
}

// columnCase is a MISMATCH case whose L2 reasons must hold code (with what in its message).
func columnCase(id string, w map[string]any, code, detail, what string) harness.Observed {
	obs := harness.Observed{ActionIDs: []string{id}, Evidence: witnessEvidence(id, w), Outcome: verdictOf(w)}
	if obs.Outcome == "MISMATCH" && harness.Str(w, "level") == "L2" && slices.Contains(codesAt(w, "L2"), code) &&
		strings.Contains(fmt.Sprint(w["reasons"]), what) {
		obs.Detail, obs.Line = detail, code+" · "+what
		return obs
	}
	obs.Detail = fmt.Sprintf("%s (%s: %v)", obs.Outcome, harness.Str(w, "level"), codesAt(w, "L2"))
	return obs
}
