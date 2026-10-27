//go:build attack

package attack

import (
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/deedglass/deedglass/attack/harness"
)

// People, sessions and the live stream: S34's group P (P02–P08, P10). P01 is S22's L01 and P09 is
// S23's U01. Each case reaches for a decision or a view it shouldn't have.

func init() {
	register(
		harness.Case{ID: "P02", Run: p02},
		harness.Case{ID: "P03", Run: p03},
		harness.Case{ID: "P04", Run: p04},
		harness.Case{ID: "P05", Run: p05, Cap: 60 * time.Second},
		harness.Case{ID: "P06", Run: p06},
		harness.Case{ID: "P07", Run: p07},
		harness.Case{ID: "P08", Run: p08},
		harness.Case{ID: "P10", Run: p10, Cap: 90 * time.Second},
	)
}

var pForbidden = []string{"orders", "other", "redirector", "secret-echo", "mcp"}

// P02: approval X's emailed link carrying approval Y's context digest acts only on X, which
// refuses the stale view; a link voided when its approval was cancelled works for nothing.
func p02(h *harness.H) harness.Observed {
	obs := harness.Observed{Evidence: map[string]any{}, Forbidden: pForbidden}
	_, ana, ben := approvers(h)
	since := h.Env.LogOffset()
	actX, aprX := submit(h, "approval-bot", "P02 X")
	tokX := linkFor(h, since, ana)
	actY, aprY := submit(h, "approval-bot", "P02 Y")
	dgY := harness.Str(approval(h, aprY), "context_digest")
	cross := link(h, "decide", map[string]any{"token": tokX, "vote": "approve", "context_digest": dgY})
	obs.Evidence["x_link_y_digest"] = refused(cross)
	diffX, evX := stillPending(h, actX, aprX, 0)
	diffY, evY := stillPending(h, actY, aprY, 0)
	obs.Evidence["x"], obs.Evidence["y"] = evX, evY
	// The voided link: Z is cancelled, which voids every link to it.
	since = h.Env.LogOffset()
	actZ, aprZ := submit(h, "approval-bot", "P02 Z")
	tokZ := linkFor(h, since, ben)
	dgZ := harness.Str(approval(h, aprZ), "context_digest")
	cancel(h, actZ)
	voided := link(h, "decide", map[string]any{"token": tokZ, "vote": "approve", "context_digest": dgZ})
	obs.Evidence["voided_link"] = refused(voided)
	obs.Evidence["z_status"] = harness.Str(approval(h, aprZ), "status")
	cancel(h, actX)
	cancel(h, actY)
	obs.ActionIDs = append(obs.ActionIDs, actX, actY, actZ)
	if is(cross, 409, "context_changed") && is(voided, 401, "approval_link_invalid") && diffX == "" && diffY == "" &&
		harness.Str(approval(h, aprZ), "status") == "cancelled" {
		obs.Outcome = "REJECTED"
		obs.Detail = "REJECTED (context_changed for X's link carrying Y's digest; approval_link_invalid for the voided link); X stays pending and Y gets no vote"
		obs.Line = "context_changed · approval_link_invalid · 0 votes"
	} else {
		obs.Outcome = "REJECTED?"
		obs.Detail = fmt.Sprintf("cross %d %s, voided %d %s, X %q, Y %q", cross.Status, cross.ErrorCode(), voided.Status, voided.ErrorCode(), diffX, diffY)
	}
	return obs
}

// ---- Slack (P03, P04) ----

const (
	slackTeam   = "T0ATTACK1"
	slackApp    = "A0ATTACK1"
	slackSecret = "a77ac4f0e3b21d9c8b6a5f4e3d2c1b0a" // a throwaway signing secret for this run's fake Slack
)

var pSlack struct {
	once sync.Once
	fake *harness.FakeSlack
	err  string
}

// slackSetup gives Acme a Slack connector whose Web API is the harness's fake, with approvals in
// Slack on. The Slack user U0ANA is ana and U0BEN is ben.
func slackSetup(h *harness.H) *harness.FakeSlack {
	_, ana, ben := approvers(h)
	pSlack.once.Do(func() {
		pSlack.fake = harness.NewFakeSlack(map[string]string{"U0ANA": ana.Email, "U0BEN": ben.Email})
		cfg := map[string]any{"team_id": slackTeam, "app_id": slackApp, "channels": []string{"*"},
			"approvals": map[string]any{"enabled": true, "channel": "#deedglass-approvals"}, "api_base_url": pSlack.fake.URL + "/api"}
		r := h.Env.AddConnector(h.Acme, "slack", "slack", cfg, "fields",
			map[string]string{"bot_token": "xoxb-0000000000-0000000000-attackattackattack", "signing_secret": slackSecret})
		if r.Status != 201 {
			pSlack.err = fmt.Sprintf("create the Slack connector: HTTP %d %s", r.Status, r.Body)
		}
	})
	if pSlack.err != "" {
		panic(pSlack.err)
	}
	return pSlack.fake
}

// slackPost sends a signed (or forged) interaction to the public route.
func slackPost(h *harness.H, body []byte, ts int64, sig string) harness.Response {
	return h.Acme.Admin.WithKey("").Do("POST", "/v1/slack/interact", body, "Content-Type", "application/x-www-form-urlencoded",
		"X-Slack-Request-Timestamp", fmt.Sprint(ts), "X-Slack-Signature", sig)
}

func approvedVotes(h *harness.H, apr string) (string, int) {
	ap := approval(h, apr)
	return harness.Str(ap, "status"), countersigns(ap)
}

// P03: a forged Slack click (bad signature), a valid one replayed after 6 minutes (stale), and a
// valid one sent twice. Only the fresh, correctly signed request decides, once.
func p03(h *harness.H) harness.Observed {
	obs := harness.Observed{Evidence: map[string]any{}, Forbidden: pForbidden}
	fake := slackSetup(h)
	act, apr := submit(h, "approval-bot", "P03")
	obs.ActionIDs = append(obs.ActionIDs, act)
	dg := harness.Str(approval(h, apr), "context_digest")
	body := harness.SlackForm(harness.SlackClick(slackTeam, slackApp, "U0ANA", "deedglass_approve", apr+"|"+dg, fake.URL+"/respond"))
	now := time.Now().Unix()
	bad := slackPost(h, body, now, harness.SlackSign("0000000000000000000000000000000f", now, body))
	staleTS := now - 6*60
	stale := slackPost(h, body, staleTS, harness.SlackSign(slackSecret, staleTS, body))
	st0, n0 := approvedVotes(h, apr)
	good := slackPost(h, body, now, harness.SlackSign(slackSecret, now, body))
	deadline := time.Now().Add(5 * time.Second)
	st1, n1 := approvedVotes(h, apr)
	for st1 == "pending" && time.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)
		st1, n1 = approvedVotes(h, apr)
	}
	replay := slackPost(h, body, now, harness.SlackSign(slackSecret, now, body))
	time.Sleep(500 * time.Millisecond)
	st2, n2 := approvedVotes(h, apr)
	decided := h.Env.CPQuery(`SELECT count(*)::text FROM deedglass.admin_events WHERE action = 'approval.decided' AND target_id = $1`, apr)
	a := h.WaitSealed(h.Acme, act)
	obs.Evidence["bad"], obs.Evidence["stale"], obs.Evidence["good"], obs.Evidence["replay"] = bad.Status, stale.Status, good.Status, replay.Status
	obs.Evidence["before"] = map[string]any{"status": st0, "votes": n0}
	obs.Evidence["after"] = map[string]any{"status": st1, "votes": n1}
	obs.Evidence["after_replay"] = map[string]any{"status": st2, "votes": n2, "approval.decided": decided, "executions": executions(h, act)}
	bareBad := bad.Status == 401 && len(strings.TrimSpace(string(bad.Body))) == 0
	bareStale := stale.Status == 401 && len(strings.TrimSpace(string(stale.Body))) == 0
	if bareBad && bareStale && st0 == "pending" && n0 == 0 && good.Status == 200 && st1 == "approved" && n1 == 1 &&
		replay.Status == 200 && st2 == "approved" && n2 == 1 && len(decided) == 1 && decided[0][0] == "1" &&
		executions(h, act) == 1 && harness.Str(a, "state") == "sealed" {
		obs.Outcome = "REJECTED"
		obs.Detail = "REJECTED (a bare 401 for the bad and the stale signature; the replay answers 200 and changes nothing); the approval is decided once"
		obs.Line = "401 · stale 401 · replay no-op · decided once"
	} else {
		obs.Outcome = "REJECTED?"
		obs.Detail = fmt.Sprintf("bad %d (%q), stale %d, good %d → %s/%d, replay %d → %s/%d, decided %v", bad.Status, bad.Body, stale.Status, good.Status,
			st1, n1, replay.Status, st2, n2, decided)
	}
	return obs
}

// P04: the person the action runs for clicks Approve in Slack.
func p04(h *harness.H) harness.Observed {
	obs := harness.Observed{Evidence: map[string]any{}, Forbidden: pForbidden}
	fake := slackSetup(h)
	_, ana, _ := approvers(h)
	since := h.Env.LogOffset()
	act, apr := submit(h, "approval-bot", "P04", "Deedglass-On-Behalf-Of", ana.UserID)
	obs.ActionIDs = append(obs.ActionIDs, act)
	dg := harness.Str(approval(h, apr), "context_digest")
	body := harness.SlackForm(harness.SlackClick(slackTeam, slackApp, "U0ANA", "deedglass_approve", apr+"|"+dg, fake.URL+"/respond"))
	now := time.Now().Unix()
	r := slackPost(h, body, now, harness.SlackSign(slackSecret, now, body))
	// The self rule's refusal is answered in Slack, to the person, as an ephemeral reply.
	refusedLogged := false
	deadline := time.Now().Add(5 * time.Second)
	for !refusedLogged && time.Now().Before(deadline) {
		refusedLogged = strings.Contains(fake.Said(), "You asked for this action, so someone else approves it.")
		if !refusedLogged {
			time.Sleep(100 * time.Millisecond)
		}
	}
	_ = since
	diff, ev := stillPending(h, act, apr, 0)
	obs.Evidence["click"], obs.Evidence["after"], obs.Evidence["refusal_logged"] = r.Status, ev, refusedLogged
	cancel(h, act)
	if r.Status == 200 && refusedLogged && diff == "" {
		obs.Outcome, obs.Detail = "REJECTED", "REJECTED (self_approval; the approval stays pending and nothing runs)"
		obs.Line = "self_approval · in Slack · 0 votes"
	} else {
		obs.Outcome = "REJECTED?"
		obs.Detail = fmt.Sprintf("click %d, refusal logged %v, after %q", r.Status, refusedLogged, diff)
	}
	return obs
}

// P05: the same person approves a four-eyes approval twice at once, ten times. The second vote is
// always already_countersigned, and 1 of 2 remains.
func p05(h *harness.H) harness.Observed {
	obs := harness.Observed{Evidence: map[string]any{}, Forbidden: pForbidden}
	_, ana, _ := approvers(h)
	ok := 0
	var rounds []any
	for i := range 10 {
		act, apr := submit(h, "foureyes-bot", fmt.Sprintf("P05 %d", i))
		obs.ActionIDs = append(obs.ActionIDs, act)
		dg := harness.Str(approval(h, apr), "context_digest")
		var wg sync.WaitGroup
		res := make([]harness.Response, 2)
		for k := range 2 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				res[k] = ana.Session.Do("POST", "/v1/approvals/"+apr+"/approve", map[string]any{"context_digest": dg})
			}()
		}
		wg.Wait()
		diff, ev := stillPending(h, act, apr, 1)
		one200 := (res[0].Status == 200) != (res[1].Status == 200)
		dup := is(res[0], 409, "already_countersigned") || is(res[1], 409, "already_countersigned")
		if one200 && dup && diff == "" {
			ok++
		}
		rounds = append(rounds, map[string]any{"statuses": []int{res[0].Status, res[1].Status}, "codes": []string{res[0].ErrorCode(), res[1].ErrorCode()}, "after": ev})
		cancel(h, act)
	}
	obs.Evidence["rounds"], obs.Evidence["clean_rounds"] = rounds, ok
	if ok == 10 {
		obs.Outcome = "REJECTED"
		obs.Detail = "REJECTED (already_countersigned for the second vote every time); 1 of 2 remains and nothing runs"
		obs.Line = "already_countersigned · 10 of 10 rounds · 1 of 2"
	} else {
		obs.Outcome, obs.Detail = "REJECTED?", fmt.Sprintf("%d of 10 rounds clean", ok)
	}
	return obs
}

// P06: a viewer reaches for an approval, a policy activation, Stop all and a new key.
func p06(h *harness.H) harness.Observed {
	obs := harness.Observed{Evidence: map[string]any{}, Forbidden: pForbidden}
	approvers(h)
	vera := h.Env.AddMember(h.Acme, "vera@acme.example.test", "viewer")
	a := h.Acme.Agents["attacker"]
	keysBefore := len(listData(h.Acme.Admin.Do("GET", "/v1/agents/"+a.ID+"/keys", nil)))
	activeBefore := string(h.Acme.Admin.Do("GET", "/v1/policies/active", nil).Body)
	act, apr := submit(h, "approval-bot", "P06")
	obs.ActionIDs = append(obs.ActionIDs, act)
	dg := harness.Str(approval(h, apr), "context_digest")
	approve := vera.Session.Do("POST", "/v1/approvals/"+apr+"/approve", map[string]any{"context_digest": dg})
	activate := vera.Session.Do("POST", "/v1/policies/"+h.Acme.Policy+"/versions/1/activate", map[string]any{"mode": "enforce", "reason": "A viewer tries."})
	stop := vera.Session.Do("POST", "/v1/org/stop-all", map[string]any{"reason": "A viewer tries."})
	key := vera.Session.Do("POST", "/v1/agents/"+a.ID+"/keys", map[string]any{"name": "viewer-key", "mode": "test"})
	diff, ev := stillPending(h, act, apr, 0)
	org := h.Acme.Admin.Do("GET", "/v1/org", nil).Map()
	stopped := harness.Get(org, "stop", "stopped") == true
	keysAfter := len(listData(h.Acme.Admin.Do("GET", "/v1/agents/"+a.ID+"/keys", nil)))
	activeAfter := string(h.Acme.Admin.Do("GET", "/v1/policies/active", nil).Body)
	obs.Evidence["approve"], obs.Evidence["activate"], obs.Evidence["stop_all"], obs.Evidence["create_key"] = refused(approve), refused(activate), refused(stop), refused(key)
	obs.Evidence["after"] = map[string]any{"approval": ev, "org_stop": harness.Get(org, "stop"), "keys": []int{keysBefore, keysAfter}, "policy_unchanged": activeBefore == activeAfter}
	cancel(h, act)
	if is(approve, 403, "approver_not_eligible") && is(activate, 403, "insufficient_role") && is(stop, 403, "insufficient_role") &&
		is(key, 403, "insufficient_role") && diff == "" && !stopped && keysBefore == keysAfter && activeBefore == activeAfter {
		obs.Outcome = "REJECTED"
		obs.Detail = "REJECTED (approver_not_eligible for approve; insufficient_role for the other three); nothing changes"
		obs.Line = "approver_not_eligible · insufficient_role ×3"
	} else {
		obs.Outcome = "REJECTED?"
		obs.Detail = fmt.Sprintf("approve %d %s, activate %d %s, stop %d %s, key %d %s, pending %q, stopped %v, keys %d→%d", approve.Status, approve.ErrorCode(),
			activate.Status, activate.ErrorCode(), stop.Status, stop.ErrorCode(), key.Status, key.ErrorCode(), diff, stopped, keysBefore, keysAfter)
	}
	return obs
}

func listData(r harness.Response) []any {
	d, _ := r.Map()["data"].([]any)
	return d
}

// P07: a cross-site POST to a Console mutation: the owner's cookie without the CSRF header, then
// with a CSRF token from another person's session.
func p07(h *harness.H) harness.Observed {
	obs := harness.Observed{Evidence: map[string]any{}, Forbidden: pForbidden}
	_, ana, _ := approvers(h)
	owner := h.Acme.Session
	before := len(listData(h.Acme.Admin.Do("GET", "/v1/agents?limit=100", nil)))
	cross := []string{"Origin", "https://evil.test", "Sec-Fetch-Site", "cross-site"}
	none := owner.Do("POST", "/v1/agents", map[string]any{"name": "csrf-none", "description": "Should never exist."}, append(cross, "Deedglass-CSRF", "")...)
	other := owner.Do("POST", "/v1/agents", map[string]any{"name": "csrf-other", "description": "Should never exist."}, append(cross, "Deedglass-CSRF", ana.Session.CSRF())...)
	stop := owner.Do("POST", "/v1/org/stop-all", map[string]any{"reason": "A cross-site form."}, append(cross, "Deedglass-CSRF", "")...)
	after := len(listData(h.Acme.Admin.Do("GET", "/v1/agents?limit=100", nil)))
	org := h.Acme.Admin.Do("GET", "/v1/org", nil).Map()
	obs.Evidence["no_header"], obs.Evidence["other_session_token"], obs.Evidence["stop_all_no_header"] = refused(none), refused(other), refused(stop)
	obs.Evidence["agents"] = []int{before, after}
	obs.Evidence["org_stop"] = harness.Get(org, "stop")
	if is(none, 403, "csrf_failed") && is(other, 403, "csrf_failed") && is(stop, 403, "csrf_failed") && before == after && ana.Session.CSRF() != "" {
		obs.Outcome, obs.Detail = "REJECTED", "REJECTED (csrf_failed); no state change"
		obs.Line = "csrf_failed · no header · another session's token"
	} else {
		obs.Outcome = "REJECTED?"
		obs.Detail = fmt.Sprintf("none %d %s, other %d %s, stop %d %s, agents %d→%d", none.Status, none.ErrorCode(), other.Status, other.ErrorCode(),
			stop.Status, stop.ErrorCode(), before, after)
	}
	return obs
}

// P08: Globex's owner opens the event stream with a Last-Event-ID copied from Acme's stream while
// Acme's agents act. Nothing of Acme's arrives.
func p08(h *harness.H) harness.Observed {
	obs := harness.Observed{Evidence: map[string]any{}, Forbidden: pForbidden}
	acmeAgent := h.Acme.Agents["attacker"]
	var acmeIDs []string
	var mu sync.Mutex
	act := func(n int) {
		for i := range n {
			_, id := h.Act(acmeAgent, "fake.echo", echo(fmt.Sprintf("P08 %d", i)))
			mu.Lock()
			acmeIDs = append(acmeIDs, id)
			mu.Unlock()
		}
	}
	// Acme's own stream, to learn a real event id of Acme's.
	done := make(chan harness.Response, 1)
	go func() { done <- h.Acme.Admin.Peek("/v1/events", 2*time.Second) }()
	time.Sleep(300 * time.Millisecond)
	act(3)
	acmeStream := <-done
	lastID := ""
	acmeEvents := 0
	for _, e := range harness.ParseSSE(acmeStream.Body) {
		if e.ID != "" && e.Event != "hello" {
			lastID = e.ID
			acmeEvents++
		}
	}
	// Globex, as a person and as an admin key, with that id.
	owner := h.Env.Owner(h.Globex).Session
	gs := *owner
	gs.Headers = map[string]string{"Last-Event-ID": lastID}
	gk := *h.Globex.Admin
	gk.Headers = map[string]string{"Last-Event-ID": lastID}
	results := make([]harness.Response, 2)
	var wg sync.WaitGroup
	for i, c := range []*harness.Client{&gs, &gk} {
		wg.Add(1)
		go func() { defer wg.Done(); results[i] = c.Peek("/v1/events", 3*time.Second) }()
	}
	time.Sleep(300 * time.Millisecond)
	act(3)
	wg.Wait()
	leaked := 0
	var seen []string
	for _, r := range results {
		body := string(r.Body)
		for _, id := range append(append([]string{}, acmeIDs...), h.Acme.ID) {
			if id != "" && strings.Contains(body, id) {
				leaked++
			}
		}
		for _, e := range harness.ParseSSE(r.Body) {
			seen = append(seen, e.Event)
		}
	}
	obs.Evidence["acme_events"], obs.Evidence["last_event_id"] = acmeEvents, lastID
	obs.Evidence["globex_statuses"] = []int{results[0].Status, results[1].Status}
	obs.Evidence["globex_events"], obs.Evidence["acme_ids_seen"] = seen, leaked
	obs.ActionIDs = append(obs.ActionIDs, acmeIDs...)
	if acmeEvents > 0 && lastID != "" && leaked == 0 && results[0].Status == 200 && results[1].Status == 200 {
		obs.Outcome, obs.Detail = "REJECTED", "REJECTED (no org A event delivered; 0 ids leaked)"
		obs.Line = "0 events from acme · person and admin key"
	} else {
		obs.Outcome = "REJECTED?"
		obs.Detail = fmt.Sprintf("acme events %d (last %q), globex statuses %d/%d, acme ids seen %d", acmeEvents, lastID, results[0].Status, results[1].Status, leaked)
	}
	return obs
}

// P10: Stop all in the middle of a burst of at least 200 calls, ten rounds. Every call that starts more than 2 s
// after the stop is refused with org_stopped, and none executes after that point.
func p10(h *harness.H) harness.Observed {
	obs := harness.Observed{Evidence: map[string]any{}, Forbidden: pForbidden}
	bot := h.Acme.Agents["burst2-bot"]
	if bot == nil {
		bot = h.Env.AddAgent(h.Acme, "burst2-bot", nil)
	}
	owner := h.Acme.Session
	type call struct {
		start  time.Time
		status int
		code   string
	}
	clean := 0
	var rounds []any
	for round := range 10 {
		calls := make([]call, 0, 200)
		var mu sync.Mutex
		var wg sync.WaitGroup
		t0 := time.Now()
		var stopAt time.Time
		// The burst keeps going until 3 s after the stop answered (the cut is stop + 2 s), so a
		// slow stop or a fast runner can't leave a round with no calls after the cut. At least 25
		// calls per worker (200 in all), at most 100 per worker.
		var stopNanos atomic.Int64
		for w := range 8 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for i := 0; i < 100; i++ {
					if sn := stopNanos.Load(); i >= 25 && sn != 0 && time.Now().After(time.Unix(0, sn).Add(3*time.Second)) {
						break
					}
					next := t0.Add(time.Duration(i*130+w*16) * time.Millisecond)
					time.Sleep(time.Until(next))
					s := time.Now()
					r := bot.API.Do("POST", "/v1/actions", map[string]any{"tool": "fake.echo", "args": echo(fmt.Sprintf("P10 %d %d %d", round, w, i)), "mode": "gateway", "wait": 0})
					mu.Lock()
					calls = append(calls, call{start: s, status: r.Status, code: r.ErrorCode()})
					mu.Unlock()
				}
			}()
		}
		time.Sleep(800 * time.Millisecond)
		st := owner.Do("POST", "/v1/org/stop-all", map[string]any{"reason": fmt.Sprintf("P10 round %d: stop mid-burst.", round)})
		stopAt = time.Now()
		stopNanos.Store(stopAt.UnixNano())
		wg.Wait()
		cut := stopAt.Add(2 * time.Second)
		late, lateOK := 0, 0
		for _, c := range calls {
			if c.start.After(cut) {
				late++
				if c.status == 403 && c.code == "org_stopped" {
					lateOK++
				}
			}
		}
		ran := h.Env.CPQuery(`SELECT count(*)::text FROM deedglass.action_events e JOIN deedglass.actions a ON a.org_id = e.org_id AND a.id = e.action_id
		    WHERE a.agent_id = $1 AND e.to_state = 'executing' AND e.at > $2`, bot.ID, cut)
		after := "?"
		if len(ran) == 1 {
			after = ran[0][0]
		}
		sa := owner.Do("POST", "/v1/org/start-all", map[string]any{"reason": fmt.Sprintf("P10 round %d: resume.", round)})
		// Wait until the org runs again (< 2 s), so the next round and the next case start clean.
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			if r := bot.API.Do("POST", "/v1/actions", map[string]any{"tool": "fake.echo", "args": echo("P10 resume check"), "mode": "gateway", "wait": 0}); r.Status < 300 {
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
		ok := st.Status == 200 && sa.Status == 200 && late > 0 && late == lateOK && after == "0"
		if ok {
			clean++
		}
		rounds = append(rounds, map[string]any{"stop": st.Status, "start": sa.Status, "calls": len(calls), "after_cut": late, "org_stopped": lateOK, "executed_after_cut": after})
	}
	obs.Evidence["rounds"], obs.Evidence["clean_rounds"] = rounds, clean
	if clean == 10 {
		obs.Outcome, obs.Detail = "REJECTED", "REJECTED (org_stopped) for every call that starts after stop + 2 s; none executes after"
		obs.Line = "org_stopped · 10 of 10 rounds · 0 executed after stop + 2 s"
	} else {
		obs.Outcome, obs.Detail = "REJECTED?", fmt.Sprintf("%d of 10 rounds clean: %v", clean, rounds)
	}
	return obs
}
