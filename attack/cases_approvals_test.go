//go:build attack

package attack

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"time"

	"github.com/deedglass/deedglass/attack/harness"
	"github.com/deedglass/deedglass/pkg/deedglass/webhook"
)

// Approvals, links and webhooks: L01–L08 (S22 §6). A person's countersign is the control here, so
// every attack tries to get one without the right person: a replayed link, a second vote from the
// same person, the person the action runs for, an agent key, a late vote, a vote on a stale view.
// Then a webhook pointed at cloud metadata, and a forged webhook signature.
//
// The agents approval-bot, foureyes-bot and expiry-bot meet the attack-baseline rules
// approval-one-admin, approval-four-eyes and approval-short-expiry. Acme gets two admins besides
// its owner, signed in through the dev link flow; their approval links come from the dev mail log.

var lPeople struct {
	once            sync.Once
	owner, ana, ben *harness.Member
}

// approvers adds Acme's two admins (once per run) and the L agents.
func approvers(h *harness.H) (owner, ana, ben *harness.Member) {
	lPeople.once.Do(func() {
		lPeople.owner = h.Env.Owner(h.Acme)
		lPeople.ana = h.Env.AddMember(h.Acme, "ana@acme.example.test", "admin")
		lPeople.ben = h.Env.AddMember(h.Acme, "ben@acme.example.test", "admin")
	})
	if lPeople.ana == nil {
		panic("the L approvers couldn't be set up")
	}
	for _, n := range []string{"approval-bot", "foureyes-bot", "expiry-bot"} {
		if h.Acme.Agents[n] == nil {
			h.Env.AddAgent(h.Acme, n, nil)
		}
	}
	return lPeople.owner, lPeople.ana, lPeople.ben
}

var lForbidden = []string{"orders", "other", "redirector", "secret-echo", "mcp"}

// submit sends fake.echo as agent and returns the paused action and its approval.
func submit(h *harness.H, agent, msg string, hdr ...string) (act, apr string) {
	a := h.Acme.Agents[agent]
	r := a.API.Do("POST", "/v1/actions", map[string]any{"tool": "fake.echo", "args": echo(msg), "mode": "gateway", "wait": 0}, hdr...)
	m := r.Map()
	act, apr = harness.Str(m, "id"), harness.Str(m, "approval", "id")
	if apr == "" {
		apr = harness.Str(m, "next_action", "approval")
	}
	if act == "" || apr == "" || harness.Str(m, "status") != "pending_approval" {
		panic(fmt.Sprintf("%s's call didn't pause for approval: HTTP %d %s", agent, r.Status, r.Body))
	}
	return act, apr
}

// approval reads the approval as the org admin key (the full object, with context_digest).
func approval(h *harness.H, apr string) map[string]any {
	return h.Acme.Admin.Do("GET", "/v1/approvals/"+apr, nil).Map()
}

func countersigns(ap map[string]any) int {
	cs, _ := harness.Get(ap, "countersigned").([]any)
	return len(cs)
}

// executions counts the action's moves into executing: 0 means it never ran, 1 that it ran once.
func executions(h *harness.H, act string) int {
	r := h.Env.CPQuery(`SELECT count(*)::text FROM deedglass.action_events WHERE action_id = $1 AND to_state = 'executing'`, act)
	n := 0
	if len(r) == 1 {
		_, _ = fmt.Sscan(r[0][0], &n)
	}
	return n
}

// stillPending reports what differs from "the approval is pending with n countersigns and the
// action has not run", or "".
func stillPending(h *harness.H, act, apr string, n int) (string, map[string]any) {
	ap := approval(h, apr)
	a := h.Acme.Admin.Do("GET", "/v1/actions/"+act, nil).Map() // no ?wait: a paused action doesn't move
	ev := map[string]any{"approval_status": harness.Str(ap, "status"), "countersigned": countersigns(ap), "required": harness.Get(ap, "required"),
		"action_status": harness.Str(a, "status"), "executions": executions(h, act)}
	switch {
	case harness.Str(ap, "status") != "pending":
		return "approval " + harness.Str(ap, "status"), ev
	case countersigns(ap) != n:
		return fmt.Sprintf("%d countersigns, not %d", countersigns(ap), n), ev
	case harness.Str(a, "status") != "pending_approval":
		return "action " + harness.Str(a, "status"), ev
	case executions(h, act) != 0:
		return "the action ran", ev
	}
	return "", ev
}

// cancel ends a paused action (admin key), so it is sealed when the case ends.
func cancel(h *harness.H, act string) {
	if r := h.Acme.Admin.Do("POST", "/v1/actions/"+act+"/cancel", nil); r.Status != 200 {
		h.Problem("cancel %s: HTTP %d %s", act, r.Status, r.Body)
	}
	h.WaitSealed(h.Acme, act)
}

// link posts to a public approval-link route (no key, no session: the token is the credential).
func link(h *harness.H, route string, body map[string]any) harness.Response {
	return h.Acme.Admin.WithKey("").Do("POST", "/v1/approval-links/"+route, body)
}

func linkFor(h *harness.H, since int, m *harness.Member) string {
	tok := h.Env.ApprovalLinkFor(since, m.Email, 10*time.Second)
	if tok == "" {
		panic("no approval email to " + m.Email + " in the dev mail log")
	}
	return tok
}

func refused(r harness.Response) map[string]any {
	return map[string]any{"status": r.Status, "error.code": r.ErrorCode()}
}

func is(r harness.Response, status int, code string) bool {
	return r.Status == status && r.ErrorCode() == code
}

func init() {
	register(
		harness.Case{ID: "L01", Run: l01},
		harness.Case{ID: "L02", Run: l02},
		harness.Case{ID: "L03", Run: l03},
		harness.Case{ID: "L04", Run: l04},
		// A real expiry: the shortest expires_in a rule accepts is 1m, and the case waits it out.
		harness.Case{ID: "L05", Run: l05, Cap: 100 * time.Second},
		harness.Case{ID: "L06", Run: l06},
		harness.Case{ID: "L07", Run: l07},
		harness.Case{ID: "L08", Run: l08},
	)
}

// L01: the emailed link decides once (approved, the action runs once); every reuse is 401
// approval_link_invalid, and so is another approver's link, voided by the final decision.
func l01(h *harness.H) harness.Observed {
	obs := harness.Observed{Evidence: map[string]any{}, Forbidden: lForbidden}
	_, ana, ben := approvers(h)
	off := h.Env.LogOffset()
	act, apr := submit(h, "approval-bot", "L01")
	obs.ActionIDs = append(obs.ActionIDs, act)
	tok, other := linkFor(h, off, ana), linkFor(h, off, ben)
	pv := link(h, "preview", map[string]any{"token": tok})
	dg := harness.Str(pv.Map(), "approval", "context_digest")
	d := link(h, "decide", map[string]any{"token": tok, "vote": "approve", "context_digest": dg})
	a := h.WaitSealed(h.Acme, act)
	obs.Evidence["first"] = map[string]any{"preview": pv.Status, "decide": d.Status, "approval_status": harness.Str(d.Map(), "status"),
		"action_status": harness.Str(d.Map(), "action_status"), "execution": harness.Str(a, "execution", "status")}
	reuses := []harness.Response{
		link(h, "decide", map[string]any{"token": tok, "vote": "approve", "context_digest": dg}),
		link(h, "decide", map[string]any{"token": tok, "vote": "approve", "context_digest": dg}),
		link(h, "decide", map[string]any{"token": tok, "vote": "deny", "reason": "Replayed.", "context_digest": dg}),
		link(h, "preview", map[string]any{"token": tok}),
		link(h, "decide", map[string]any{"token": other, "vote": "approve", "context_digest": dg}),
	}
	invalid := 0
	var rs []any
	for _, r := range reuses {
		rs = append(rs, refused(r))
		if is(r, 401, "approval_link_invalid") {
			invalid++
		}
	}
	time.Sleep(300 * time.Millisecond)
	runs := executions(h, act)
	obs.Evidence["reuses"], obs.Evidence["executions"] = rs, runs
	obs.Evidence["approval"] = harness.Str(approval(h, apr), "status")
	if harness.Str(pv.Map(), "approval", "id") != apr {
		h.Problem("the link previewed %s, not %s", harness.Str(pv.Map(), "approval", "id"), apr)
	}
	if d.Status == 200 && harness.Str(d.Map(), "status") == "approved" && harness.Str(a, "execution", "status") == "succeeded" &&
		runs == 1 && invalid == len(reuses) {
		obs.Outcome, obs.Detail = "REJECTED", "REJECTED (approval_link_invalid on every reuse; the action ran once)"
		obs.Line = fmt.Sprintf("approval_link_invalid · %d of %d reuses · ran once", invalid, len(reuses))
	} else {
		obs.Outcome = "REJECTED?"
		obs.Detail = fmt.Sprintf("decide HTTP %d %s, execution %s, ran %d times, %d of %d reuses refused", d.Status,
			harness.Str(d.Map(), "status"), harness.Str(a, "execution", "status"), runs, invalid, len(reuses))
	}
	return obs
}

// L02: four-eyes (required 2). Ana countersigns with her link; her link again is 401
// approval_link_invalid and her session again is 409 already_countersigned. It stays 1 of 2.
func l02(h *harness.H) harness.Observed {
	obs := harness.Observed{Evidence: map[string]any{}, Forbidden: lForbidden}
	_, ana, _ := approvers(h)
	off := h.Env.LogOffset()
	act, apr := submit(h, "foureyes-bot", "L02")
	obs.ActionIDs = append(obs.ActionIDs, act)
	tok := linkFor(h, off, ana)
	dg := harness.Str(link(h, "preview", map[string]any{"token": tok}).Map(), "approval", "context_digest")
	first := link(h, "decide", map[string]any{"token": tok, "vote": "approve", "context_digest": dg})
	obs.Evidence["countersign"] = map[string]any{"status": first.Status, "approval_status": harness.Str(first.Map(), "status"),
		"action_status": harness.Str(first.Map(), "action_status")}
	viaLink := link(h, "decide", map[string]any{"token": tok, "vote": "approve", "context_digest": dg})
	viaSession := ana.Session.Do("POST", "/v1/approvals/"+apr+"/approve", map[string]any{"context_digest": dg})
	obs.Evidence["again_link"], obs.Evidence["again_session"] = refused(viaLink), refused(viaSession)
	time.Sleep(300 * time.Millisecond)
	diff, ev := stillPending(h, act, apr, 1)
	obs.Evidence["after"] = ev
	cancel(h, act)
	if first.Status == 200 && harness.Str(first.Map(), "status") == "pending" && is(viaLink, 401, "approval_link_invalid") &&
		is(viaSession, 409, "already_countersigned") && diff == "" {
		obs.Outcome = "REJECTED"
		obs.Detail = "REJECTED (approval_link_invalid or already_countersigned; the approval stays at 1 of 2 and nothing runs)"
		obs.Line = "approval_link_invalid · already_countersigned · 1 of 2 · nothing ran"
	} else {
		obs.Outcome = "REJECTED?"
		obs.Detail = fmt.Sprintf("first HTTP %d, link again %d %s, session again %d %s, after: %s", first.Status,
			viaLink.Status, viaLink.ErrorCode(), viaSession.Status, viaSession.ErrorCode(), diff)
	}
	return obs
}

// L03: the agent says it acts for Ana (her usr_ id, then her email in capitals). Ana's approve is
// 403 self_approval; nothing runs until Ben countersigns, and then it runs once.
func l03(h *harness.H) harness.Observed {
	obs := harness.Observed{Evidence: map[string]any{}, Forbidden: lForbidden}
	_, ana, ben := approvers(h)
	actID, aprID := submit(h, "approval-bot", "L03 by id", "Deedglass-On-Behalf-Of", ana.UserID)
	actEm, aprEm := submit(h, "approval-bot", "L03 by email", "Deedglass-On-Behalf-Of", strings.ToUpper(ana.Email))
	obs.ActionIDs = append(obs.ActionIDs, actID, actEm)
	dgID, dgEm := harness.Str(approval(h, aprID), "context_digest"), harness.Str(approval(h, aprEm), "context_digest")
	selfID := ana.Session.Do("POST", "/v1/approvals/"+aprID+"/approve", map[string]any{"context_digest": dgID})
	selfEm := ana.Session.Do("POST", "/v1/approvals/"+aprEm+"/approve", map[string]any{"context_digest": dgEm})
	obs.Evidence["self_by_id"], obs.Evidence["self_by_email"] = refused(selfID), refused(selfEm)
	diffID, evID := stillPending(h, actID, aprID, 0)
	diffEm, evEm := stillPending(h, actEm, aprEm, 0)
	obs.Evidence["after_self"] = map[string]any{"by_id": evID, "by_email": evEm}
	cancel(h, actEm)
	other := ben.Session.Do("POST", "/v1/approvals/"+aprID+"/approve", map[string]any{"context_digest": dgID})
	a := h.WaitSealed(h.Acme, actID)
	runs := executions(h, actID)
	obs.Evidence["other_approver"] = map[string]any{"status": other.Status, "approval_status": harness.Str(other.Map(), "status"),
		"execution": harness.Str(a, "execution", "status"), "executions": runs}
	if is(selfID, 403, "self_approval") && is(selfEm, 403, "self_approval") && diffID == "" && diffEm == "" && other.Status == 200 &&
		harness.Str(a, "execution", "status") == "succeeded" && runs == 1 {
		obs.Outcome, obs.Detail = "REJECTED", "REJECTED (self_approval; nothing runs until another approver countersigns)"
		obs.Line = "self_approval · by id and by email · ran once after another approver"
	} else {
		obs.Outcome = "REJECTED?"
		obs.Detail = fmt.Sprintf("self %d %s / %d %s, after: %q %q, other approver HTTP %d, execution %s, ran %d", selfID.Status,
			selfID.ErrorCode(), selfEm.Status, selfEm.ErrorCode(), diffID, diffEm, other.Status, harness.Str(a, "execution", "status"), runs)
	}
	return obs
}

// L04: the agent's own key approves (and denies) its paused action; so does the org admin key.
// Every one is 403 approval_needs_person.
func l04(h *harness.H) harness.Observed {
	obs := harness.Observed{Evidence: map[string]any{}, Forbidden: lForbidden}
	approvers(h)
	act, apr := submit(h, "approval-bot", "L04")
	obs.ActionIDs = append(obs.ActionIDs, act)
	dg := harness.Str(approval(h, apr), "context_digest")
	bot := h.Acme.Agents["approval-bot"].API
	tries := map[string]harness.Response{
		"agent_approve": bot.Do("POST", "/v1/approvals/"+apr+"/approve", map[string]any{"context_digest": dg}),
		"agent_deny":    bot.Do("POST", "/v1/approvals/"+apr+"/deny", map[string]any{"context_digest": dg, "reason": "Denied by the agent."}),
		"admin_key":     h.Acme.Admin.Do("POST", "/v1/approvals/"+apr+"/approve", map[string]any{"context_digest": dg}),
	}
	all := true
	for k, r := range tries {
		obs.Evidence[k] = refused(r)
		if !is(r, 403, "approval_needs_person") {
			all = false
		}
	}
	diff, ev := stillPending(h, act, apr, 0)
	obs.Evidence["after"] = ev
	cancel(h, act)
	if all && diff == "" {
		obs.Outcome, obs.Detail = "REJECTED", "REJECTED (approval_needs_person; the approval stays pending and nothing runs)"
		obs.Line = "approval_needs_person · agent key and admin key · still pending"
	} else {
		obs.Outcome, obs.Detail = "REJECTED?", fmt.Sprintf("refusals %v, after: %s", obs.Evidence, diff)
	}
	return obs
}

// L05: a real expiry. expiry-bot's rule gives the shortest expires_in a rule may (1m); the case
// waits past it, then Ana decides with her session (409 approval_expired) and with her link (401
// approval_link_invalid: the link expires with the approval). The action is sealed expired.
func l05(h *harness.H) harness.Observed {
	obs := harness.Observed{Evidence: map[string]any{}, Forbidden: lForbidden}
	_, ana, _ := approvers(h)
	off := h.Env.LogOffset()
	act, apr := submit(h, "expiry-bot", "L05")
	obs.ActionIDs = append(obs.ActionIDs, act)
	tok := linkFor(h, off, ana)
	ap := approval(h, apr)
	dg := harness.Str(ap, "context_digest")
	created, _ := time.Parse(time.RFC3339Nano, harness.Str(ap, "created_at"))
	exp, err := time.Parse(time.RFC3339Nano, harness.Str(ap, "expires_at"))
	if err != nil {
		panic("expires_at: " + err.Error())
	}
	obs.Evidence["expires_in_s"] = exp.Sub(created).Round(time.Second).Seconds()
	time.Sleep(time.Until(exp) + 1500*time.Millisecond)
	late := ana.Session.Do("POST", "/v1/approvals/"+apr+"/approve", map[string]any{"context_digest": dg})
	lateLink := link(h, "decide", map[string]any{"token": tok, "vote": "approve", "context_digest": dg})
	obs.Evidence["session"], obs.Evidence["link"] = refused(late), refused(lateLink)
	var a map[string]any
	for deadline := time.Now().Add(20 * time.Second); ; {
		a = h.Action(h.Acme, act)
		if harness.Str(a, "state") == "sealed" || time.Now().After(deadline) {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	runs := executions(h, act)
	obs.Evidence["action"] = map[string]any{"state": harness.Str(a, "state"), "status": harness.Str(a, "status"),
		"approval_status": harness.Str(approval(h, apr), "status"), "executions": runs}
	if is(late, 409, "approval_expired") && is(lateLink, 401, "approval_link_invalid") && harness.Str(a, "state") == "sealed" &&
		harness.Str(a, "status") == "expired" && runs == 0 && harness.Get(a, "execution", "started_at") == nil {
		obs.Outcome, obs.Detail = "REJECTED", "REJECTED (approval_expired; nothing runs)"
		obs.Line = fmt.Sprintf("approval_expired after a real %.0f s expiry · nothing ran", exp.Sub(created).Seconds())
	} else {
		obs.Outcome = "REJECTED?"
		obs.Detail = fmt.Sprintf("session %d %s, link %d %s, action %s %s, ran %d", late.Status, late.ErrorCode(), lateLink.Status,
			lateLink.ErrorCode(), harness.Str(a, "state"), harness.Str(a, "status"), runs)
	}
	return obs
}

// L06: a decision on a digest that isn't the approval's (what the person saw is out of date):
// 409 context_changed by session and by link; the link isn't burned and the approval stays pending.
func l06(h *harness.H) harness.Observed {
	obs := harness.Observed{Evidence: map[string]any{}, Forbidden: lForbidden}
	_, ana, _ := approvers(h)
	off := h.Env.LogOffset()
	act, apr := submit(h, "approval-bot", "L06")
	obs.ActionIDs = append(obs.ActionIDs, act)
	tok := linkFor(h, off, ana)
	stale := "sha256:" + strings.Repeat("0", 64)
	bySession := ana.Session.Do("POST", "/v1/approvals/"+apr+"/approve", map[string]any{"context_digest": stale})
	byLink := link(h, "decide", map[string]any{"token": tok, "vote": "approve", "context_digest": stale})
	still := link(h, "preview", map[string]any{"token": tok})
	obs.Evidence["session"], obs.Evidence["link"] = refused(bySession), refused(byLink)
	obs.Evidence["link_after"] = still.Status
	diff, ev := stillPending(h, act, apr, 0)
	obs.Evidence["after"] = ev
	cancel(h, act)
	if is(bySession, 409, "context_changed") && is(byLink, 409, "context_changed") && still.Status == 200 && diff == "" {
		obs.Outcome, obs.Detail = "REJECTED", "REJECTED (context_changed; the approval stays pending and nothing runs)"
		obs.Line = "context_changed · session and link · link not burned · still pending"
	} else {
		obs.Outcome = "REJECTED?"
		obs.Detail = fmt.Sprintf("session %d %s, link %d %s, link after %d, after: %s", bySession.Status, bySession.ErrorCode(),
			byLink.Status, byLink.ErrorCode(), still.Status, diff)
	}
	return obs
}

// L07: webhooks to cloud metadata, by address, by a name that resolves there (the harness's
// hosts file), and as an IPv4-mapped IPv6 address. Each is refused at create; no row, no delivery.
func l07(h *harness.H) harness.Observed {
	obs := harness.Observed{Evidence: map[string]any{}, Forbidden: lForbidden}
	approvers(h)
	count := func(q string) string {
		r := h.Env.CPQuery(q, h.Acme.ID)
		return r[0][0]
	}
	qHooks := `SELECT count(*)::text FROM deedglass.webhooks WHERE org_id = $1`
	qDeliveries := `SELECT count(*)::text FROM deedglass.webhook_deliveries WHERE org_id = $1`
	hooks0, del0 := count(qHooks), count(qDeliveries)
	urls := []string{"http://169.254.169.254/latest/meta-data/", "http://metadata.attack.test/latest/meta-data/",
		"http://[::ffff:169.254.169.254]/latest/meta-data/"}
	blocked := 0
	for i, u := range urls {
		r := h.Acme.Admin.Do("POST", "/v1/webhooks", map[string]any{"url": u, "events": []string{"approval.requested", "approval.decided"}})
		obs.Evidence[fmt.Sprintf("create_%d", i+1)] = map[string]any{"url": u, "status": r.Status, "error.code": r.ErrorCode(),
			"param": harness.Str(r.Map(), "error", "param")}
		if is(r, 400, "destination_blocked") {
			blocked++
		}
		if id := harness.Str(r.Map(), "id"); id != "" {
			h.Acme.Admin.Do("DELETE", "/v1/webhooks/"+id, nil)
		}
	}
	// An approval event after the attempts: there is no endpoint to deliver it to.
	act, _ := submit(h, "approval-bot", "L07")
	obs.ActionIDs = append(obs.ActionIDs, act)
	time.Sleep(500 * time.Millisecond)
	cancel(h, act)
	hooks1, del1 := count(qHooks), count(qDeliveries)
	obs.Evidence["webhooks"], obs.Evidence["deliveries"] = hooks0+" → "+hooks1, del0+" → "+del1
	if blocked == len(urls) && hooks0 == hooks1 && del0 == del1 {
		obs.Outcome, obs.Detail = "BLOCKED", "BLOCKED (destination_blocked at create; 0 deliveries)"
		obs.Line = fmt.Sprintf("destination_blocked · %d of %d URLs · 0 deliveries", blocked, len(urls))
	} else {
		obs.Outcome = "CREATED"
		obs.Detail = fmt.Sprintf("%d of %d refused, webhooks %s → %s, deliveries %s → %s", blocked, len(urls), hooks0, hooks1, del0, del1)
	}
	return obs
}

// L08: a real delivery (webhook.test to a loopback receiver) checked with pkg/deedglass/webhook:
// the genuine one verifies; a wrong secret, a changed body and a header signed with another
// secret are ErrSignature; the same delivery 301 s later or earlier is ErrTimestamp.
func l08(h *harness.H) harness.Observed {
	obs := harness.Observed{Evidence: map[string]any{}, Forbidden: lForbidden}
	type delivery struct {
		header string
		body   []byte
	}
	got := make(chan delivery, 8)
	recv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got <- delivery{r.Header.Get(webhook.Header), b}
	}))
	defer recv.Close()
	r := h.Acme.Admin.Do("POST", "/v1/webhooks", map[string]any{"url": recv.URL + "/hook", "events": []string{"approval.decided"}})
	if r.Status != 201 {
		panic(fmt.Sprintf("create the webhook: HTTP %d %s", r.Status, r.Body))
	}
	id, secret := harness.Str(r.Map(), "id"), harness.Str(r.Map(), "secret")
	defer h.Acme.Admin.Do("DELETE", "/v1/webhooks/"+id, nil)
	tr := h.Acme.Admin.Do("POST", "/v1/webhooks/"+id+"/test", nil)
	var d delivery
	select {
	case d = <-got:
	case <-time.After(10 * time.Second):
		panic(fmt.Sprintf("no delivery reached the receiver (test: HTTP %d %s)", tr.Status, tr.Body))
	}
	ts, _, err := webhook.ParseHeader(d.header)
	if err != nil {
		panic("the delivery's signature header: " + err.Error())
	}
	sent := time.Unix(ts, 0)
	changed := append([]byte(nil), d.body...)
	changed[len(changed)/2] ^= 0x01
	other := "whsec_" + strings.Repeat("A", 43)
	name := func(err error) string {
		switch {
		case err == nil:
			return "ok"
		case errors.Is(err, webhook.ErrSignature):
			return "ErrSignature"
		case errors.Is(err, webhook.ErrTimestamp):
			return "ErrTimestamp"
		}
		return err.Error()
	}
	checks := []struct {
		what, want string
		err        error
	}{
		{"genuine", "ok", webhook.VerifyAt(secret, d.header, d.body, 0, sent)},
		{"wrong secret", "ErrSignature", webhook.VerifyAt(other, d.header, d.body, 0, sent)},
		{"changed body", "ErrSignature", webhook.VerifyAt(secret, d.header, changed, 0, sent)},
		{"signed with another secret", "ErrSignature", webhook.VerifyAt(secret, webhook.Sign(other, sent, d.body), d.body, 0, sent)},
		{"301 s later", "ErrTimestamp", webhook.VerifyAt(secret, d.header, d.body, 0, sent.Add(301*time.Second))},
		{"301 s earlier", "ErrTimestamp", webhook.VerifyAt(secret, d.header, d.body, 0, sent.Add(-301*time.Second))},
		{"299 s later", "ok", webhook.VerifyAt(secret, d.header, d.body, 0, sent.Add(299*time.Second))},
		{"now", "ok", webhook.Verify(secret, d.header, d.body, 0)},
	}
	okAll := true
	for _, c := range checks {
		obs.Evidence[c.what] = name(c.err)
		if name(c.err) != c.want {
			okAll = false
		}
	}
	obs.Evidence["test"] = tr.Status
	if okAll {
		obs.Outcome, obs.Detail = "REJECTED", "REJECTED (ErrSignature for a wrong secret or changed body; ErrTimestamp outside 300 s)"
		obs.Line = "ErrSignature · ErrTimestamp · on a real delivery"
	} else {
		obs.Outcome, obs.Detail = "ACCEPTED", fmt.Sprintf("%v", obs.Evidence)
	}
	return obs
}
