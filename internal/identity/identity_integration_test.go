//go:build integration

package identity_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/deedglass/deedglass/api"
	"github.com/deedglass/deedglass/internal/approve"
	"github.com/deedglass/deedglass/internal/httpx"
	"github.com/deedglass/deedglass/internal/identity"
	"github.com/deedglass/deedglass/internal/ids"
	"github.com/deedglass/deedglass/internal/ledger"
	"github.com/deedglass/deedglass/internal/mailer"
	"github.com/deedglass/deedglass/internal/store"
)

// The brief's demo, end to end: org create → the owner signs in through the link → invites a
// viewer → the viewer accepts and is refused when inviting → reusing the link is refused → the
// admin deeds are sealed and the chain is intact.
func TestDemoFlow(t *testing.T) {
	w := newWorld(t)
	c, priya := w.org(t, "Acme", "priya@acme.com")
	r := priya.do("GET", "/v1/me", "")
	var me identity.Me
	r.json(t, &me)
	if r.status != 200 || me.Object != "me" || me.Membership.Role != "owner" || me.Org.Name != "Acme" || me.User.Email != "priya@acme.com" ||
		len(me.Orgs) != 1 || me.CSRFToken == nil || *me.CSRFToken != priya.csrf {
		t.Fatalf("me: %d %s", r.status, r.body)
	}
	// PATCH /v1/me sets the name the invite email uses.
	if r := priya.do("PATCH", "/v1/me", `{"name":"Priya Raman"}`); r.status != 200 || !strings.Contains(string(r.body), `"name": "Priya Raman"`) {
		t.Fatalf("patch me: %d %s", r.status, r.body)
	}
	mem, sam := w.invite(t, priya, "sam@acme.com", "viewer")
	if mem.Status != "invited" || mem.Role != "viewer" || mem.Deed == nil || mem.InvitedBy == nil || *mem.InvitedBy != c.Owner.ID.String() {
		t.Fatalf("invite: %+v", mem)
	}
	msg, _, _ := w.mail.last(t, "sam@acme.com")
	if msg.Subject != "Priya Raman invited you to Acme on Deedglass" || msg.Kind != "invite" {
		t.Fatalf("invite email: %q %q", msg.Subject, msg.Kind)
	}
	r = sam.do("GET", "/v1/me", "")
	if !strings.Contains(string(r.body), `"role": "viewer"`) || !strings.Contains(string(r.body), `"status": "active"`) {
		t.Fatalf("sam me: %s", r.body)
	}
	r = sam.do("POST", "/v1/org/members", `{"email":"x@acme.com","role":"viewer"}`)
	if r.status != 403 || r.code() != "insufficient_role" ||
		r.message() != "Your role in Acme is Viewer, and inviting people needs Admin. Ask an owner to change your role." {
		t.Fatalf("viewer invite: %d %s", r.status, r.body)
	}
	// The members list, and a viewer can read it.
	r = sam.do("GET", "/v1/org/members", "")
	var list struct {
		Object string            `json:"object"`
		Data   []identity.Member `json:"data"`
	}
	r.json(t, &list)
	if r.status != 200 || len(list.Data) != 2 || list.Data[0].Role != "owner" || list.Data[1].User.Email != "sam@acme.com" {
		t.Fatalf("members: %s", r.body)
	}
	// Admin deeds: org.created, member.invited, member.joined; no email address in any deed.
	actions := adminActions(t, w, c.Org.ID)
	for _, want := range []string{"org.created", "login_link.issued", "member.invited", "member.joined"} {
		if actions[want] == 0 {
			t.Errorf("no %s deed: %v", want, actions)
		}
	}
	if actions["user.signed_in"] != 2 {
		t.Errorf("sign-ins recorded as admin events: %v", actions)
	}
	var deedsWithEmail int
	_ = w.e.DB.OrgReadTx(ctx, c.Org.ID, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT count(*) FROM deedglass.deeds WHERE convert_from(envelope, 'UTF8') LIKE '%@%'`).Scan(&deedsWithEmail)
	})
	if deedsWithEmail != 0 {
		t.Fatalf("%d deeds contain an @", deedsWithEmail)
	}
	chk, err := w.led.CheckChain(ctx, c.Org.ID, ledger.Range{}, ledger.CheckOptions{})
	if err != nil || chk.Status != "intact" {
		t.Fatalf("chain: %v %+v", err, chk)
	}
	// The Ledger routes take the session too.
	if r := priya.do("GET", "/v1/deeds?kind=admin", ""); r.status != 200 || !strings.Contains(string(r.body), "member.joined") {
		t.Fatalf("deeds via session: %d %s", r.status, r.body)
	}
}

func adminActions(t testing.TB, w *world, org ids.OrgID) map[string]int {
	t.Helper()
	out := map[string]int{}
	err := w.e.DB.OrgReadTx(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT action, (deed_id IS NOT NULL) FROM deedglass.admin_events`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var a string
			var sealed bool
			if err := rows.Scan(&a, &sealed); err != nil {
				return err
			}
			if strings.HasPrefix(a, "user.") == sealed {
				t.Errorf("%s: sealed=%v (sign-in events are unsealed; changes are sealed)", a, sealed)
			}
			out[a]++
		}
		return rows.Err()
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// POST /v1/auth/login through the worker: the email carries a link and a code; a newer email voids
// the older link; an unknown address gets no email; the code signs in from another browser.
func TestLoginByEmailAndCode(t *testing.T) {
	w := newWorld(t)
	w.org(t, "Acme", "priya@acme.com")
	anon := w.browser(t)
	if r := anon.do("POST", "/v1/auth/login", `{"email":"nobody@example.com"}`); r.status != 202 || w.mail.count("nobody@example.com") != 0 {
		t.Fatalf("unknown: %d, %d emails", r.status, w.mail.count("nobody@example.com"))
	}
	if r := anon.do("POST", "/v1/auth/login", `{"email":"Priya@Acme.com"}`, "User-Agent",
		"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/141.0 Safari/537.36"); r.status != 202 {
		t.Fatalf("login: %d %s", r.status, r.body)
	}
	msg, first, _ := w.mail.last(t, "priya@acme.com")
	if msg.Subject != "Sign in to Deedglass" || !strings.Contains(msg.Text, "Chrome on macOS") || msg.IdempotencyKey == "" {
		t.Fatalf("email: %q\n%s", msg.Subject, msg.Text)
	}
	anon.do("POST", "/v1/auth/login", `{"email":"priya@acme.com"}`)
	_, second, code := w.mail.last(t, "priya@acme.com")
	if first == second || len(code) != 6 {
		t.Fatalf("tokens %s %s code %q", first, second, code)
	}
	b := w.browser(t)
	if r := b.do("POST", "/v1/auth/preview", `{"token":"`+first+`"}`); r.status != 400 || r.code() != "login_link_invalid" {
		t.Fatalf("an older link after a newer one: %d %s", r.status, r.body)
	}
	// The code, typed with a space, from a second browser.
	other := w.browser(t)
	r := other.do("POST", "/v1/auth/callback", fmt.Sprintf(`{"email":"priya@acme.com","code":"%s %s"}`, code[:3], code[3:]))
	if r.status != 200 || !strings.Contains(string(r.body), `"role": "owner"`) {
		t.Fatalf("code: %d %s", r.status, r.body)
	}
	// The link of the same email is now used too: one email, one sign-in.
	if r := b.do("POST", "/v1/auth/callback", `{"token":"`+second+`"}`); r.status != 400 || r.code() != "login_link_invalid" {
		t.Fatalf("link after its code: %d %s", r.status, r.body)
	}
	// Five tokens per user per hour, counted in the database: the 6th request sends nothing.
	before := w.mail.count("priya@acme.com")
	for i := 0; i < 4; i++ {
		w.browser(t).do("POST", "/v1/auth/login", `{"email":"priya@acme.com"}`, "X-Forwarded-For", fmt.Sprintf("10.0.0.%d", i))
	}
	// (the IP limiter isn't the cap here: these come from 127.0.0.1 with no trusted proxy, and the
	// per-email limiter allows 5 per hour; count what the DB cap let through)
	if got := w.mail.count("priya@acme.com") - before; got > 3 {
		t.Fatalf("%d more emails; the per-user cap is %d tokens an hour", got, identity.TokensPerUser)
	}
}

// Codes: five wrong tries kill the token, so the right code then fails too; a code works only with
// its own address.
func TestCodeAttempts(t *testing.T) {
	w := newWorld(t)
	_, priya := w.org(t, "Acme", "priya@acme.com")
	_, sam := w.invite(t, priya, "sam@acme.com", "admin")
	_ = sam
	anon := w.browser(t)
	anon.do("POST", "/v1/auth/login", `{"email":"priya@acme.com"}`)
	_, _, code := w.mail.last(t, "priya@acme.com")
	anon.do("POST", "/v1/auth/login", `{"email":"sam@acme.com"}`)
	_, _, samCode := w.mail.last(t, "sam@acme.com")
	// A's code with B's address.
	if r := anon.do("POST", "/v1/auth/callback", `{"email":"sam@acme.com","code":"`+code+`"}`); r.status != 400 || r.code() != "login_code_invalid" {
		if code != samCode {
			t.Fatalf("A's code for B: %d %s", r.status, r.body)
		}
	}
	wrong := "000000"
	if code == wrong {
		wrong = "111111"
	}
	for i := 0; i < identity.MaxCodeAttempts; i++ {
		if r := anon.do("POST", "/v1/auth/callback", `{"email":"priya@acme.com","code":"`+wrong+`"}`); r.status != 400 || r.code() != "login_code_invalid" {
			t.Fatalf("wrong %d: %d %s", i, r.status, r.body)
		}
	}
	r := anon.do("POST", "/v1/auth/callback", `{"email":"priya@acme.com","code":"`+code+`"}`)
	if r.status != 400 || r.code() != "login_code_invalid" ||
		r.message() != "That code doesn't match, or it has expired. Check the latest email, or get a new code." {
		t.Fatalf("right code after 5 wrong: %d %s", r.status, r.body)
	}
	// Unknown address: the same answer.
	if r := anon.do("POST", "/v1/auth/callback", `{"email":"nobody@acme.com","code":"123456"}`); r.code() != "login_code_invalid" {
		t.Fatalf("unknown: %s", r.body)
	}
}

// A token is consumed once: two callbacks racing, 50 times, give exactly one session each time.
// An expired link is refused; GET never consumes anything.
func TestSingleUse(t *testing.T) {
	w := newWorld(t)
	c, _ := w.org(t, "Acme", "priya@acme.com")
	for run := 0; run < 50; run++ {
		l, err := w.svc.IssueLink(ctx, "priya@acme.com", c.Org.ID, false)
		if err != nil {
			t.Fatal(err)
		}
		tok := tokenOf(l.Link)
		// A scanner fetching the page first.
		if r := w.browser(t).do("GET", "/auth/confirm?t="+tok, ""); r.status != 200 {
			t.Fatal(r.status)
		}
		var wg sync.WaitGroup
		codes := make([]int, 2)
		for i := range codes {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				codes[i] = w.browser(t).do("POST", "/v1/auth/callback", `{"token":"`+tok+`"}`,
					"X-Forwarded-For", fmt.Sprintf("10.9.%d.%d", run, i)).status
			}(i)
		}
		wg.Wait()
		if !(codes[0] == 200 && codes[1] == 400) && !(codes[0] == 400 && codes[1] == 200) {
			t.Fatalf("run %d: %v", run, codes)
		}
	}
	l, _ := w.svc.IssueLink(ctx, "priya@acme.com", c.Org.ID, false)
	w.clock.Add(identity.LoginTTL + time.Second)
	if r := w.browser(t).do("POST", "/v1/auth/callback", `{"token":"`+tokenOf(l.Link)+`"}`); r.status != 400 || r.code() != "login_link_invalid" {
		t.Fatalf("expired: %d %s", r.status, r.body)
	}
	if r := w.browser(t).do("POST", "/v1/auth/preview", `{"token":"`+tokenOf(l.Link)+`"}`); r.code() != "login_link_invalid" {
		t.Fatalf("expired preview: %s", r.body)
	}
}

// Invites: a preview names the inviter; re-inviting voids the old link; an invite link can't be used
// once the invite is gone; inviting an active member is 409.
func TestInvites(t *testing.T) {
	w := newWorld(t)
	_, priya := w.org(t, "Acme", "priya@acme.com")
	priya.do("PATCH", "/v1/me", `{"name":"Priya Raman"}`)
	r := priya.do("POST", "/v1/org/members", `{"email":"Sam@Acme.com","role":"viewer"}`)
	if r.status != 201 {
		t.Fatalf("%d %s", r.status, r.body)
	}
	var m identity.Member
	r.json(t, &m)
	_, old, code := w.mail.last(t, "sam@acme.com")
	if code != "" {
		t.Fatal("an invite carries no code")
	}
	b := w.browser(t)
	r = b.do("POST", "/v1/auth/preview", `{"token":"`+old+`"}`)
	var pv identity.Preview
	r.json(t, &pv)
	if pv.Purpose != "invite" || pv.Email != "sam@acme.com" || pv.Role != "viewer" || pv.InvitedBy == nil || pv.InvitedBy.Name != "Priya Raman" {
		t.Fatalf("preview: %s", r.body)
	}
	// Re-invite: 200, the same membership, a fresh link; the old one is void.
	r = priya.do("POST", "/v1/org/members", `{"email":"sam@acme.com","role":"admin"}`)
	var again identity.Member
	r.json(t, &again)
	if r.status != 200 || again.ID != m.ID || again.Role != "admin" {
		t.Fatalf("re-invite: %d %s", r.status, r.body)
	}
	_, fresh, _ := w.mail.last(t, "sam@acme.com")
	if r := b.do("POST", "/v1/auth/callback", `{"token":"`+old+`"}`); r.code() != "login_link_invalid" {
		t.Fatalf("old invite link: %s", r.body)
	}
	// Removing the pending invitee kills the fresh link too.
	if r := priya.do("DELETE", "/v1/org/members/"+m.ID, ""); r.status != 200 {
		t.Fatalf("remove invitee: %d %s", r.status, r.body)
	}
	if r := b.do("POST", "/v1/auth/callback", `{"token":"`+fresh+`"}`); r.code() != "login_link_invalid" {
		t.Fatalf("invite after removal: %s", r.body)
	}
	// Invite again (removed → invited), accept, then inviting an active member is 409.
	_, sam := w.invite(t, priya, "sam@acme.com", "admin")
	if r := priya.do("POST", "/v1/org/members", `{"email":"sam@acme.com","role":"viewer"}`); r.status != 409 || r.code() != "conflict" ||
		r.message() != "sam@acme.com is already a member of Acme." {
		t.Fatalf("active member: %d %s", r.status, r.body)
	}
	// An admin can't invite an owner, or remove one.
	if r := sam.do("POST", "/v1/org/members", `{"email":"o@acme.com","role":"owner"}`); r.status != 403 ||
		r.message() != "Your role in Acme is Admin, and inviting an owner needs Owner. Ask an owner to change your role." {
		t.Fatalf("admin invites owner: %d %s", r.status, r.body)
	}
	var owner identity.Member
	priya.do("GET", "/v1/org/members?status=active&limit=1", "").json(t, &struct {
		Data []identity.Member `json:"data"`
	}{})
	_ = owner
	r = sam.do("GET", "/v1/org/members?status=active", "")
	var list struct {
		Data []identity.Member `json:"data"`
	}
	r.json(t, &list)
	if r := sam.do("DELETE", "/v1/org/members/"+list.Data[0].ID, ""); r.status != 403 || r.code() != "insufficient_role" {
		t.Fatalf("admin removes owner: %d %s", r.status, r.body)
	}
	// Validation.
	for body, param := range map[string]string{`{"email":"x","role":"viewer"}`: "email", `{"email":"x@a.co","role":"god"}`: "role"} {
		if r := priya.do("POST", "/v1/org/members", body); r.status != 400 || !strings.Contains(string(r.body), `"param": "`+param+`"`) {
			t.Errorf("%s: %d %s", body, r.status, r.body)
		}
	}
	if r := priya.do("GET", "/v1/org/members?sort=x", ""); r.status != 400 {
		t.Errorf("unknown filter: %d", r.status)
	}
}

// Signup closed refuses new invites and pending ones.
func TestSignupClosed(t *testing.T) {
	w := newWorld(t, func(o *identity.Options) { o.Signup = "closed" })
	_, priya := w.org(t, "Acme", "priya@acme.com")
	if r := priya.do("POST", "/v1/org/members", `{"email":"sam@acme.com","role":"viewer"}`); r.status != 409 ||
		!strings.Contains(r.message(), "DEEDGLASS_SIGNUP=closed") {
		t.Fatalf("%d %s", r.status, r.body)
	}
}

// CSRF: three layers. Every unsafe session request needs the header, equal to the cookie and bound
// to the session; cross-site requests are refused even with it; GETs need nothing.
func TestCSRF(t *testing.T) {
	w := newWorld(t)
	_, priya := w.org(t, "Acme", "priya@acme.com")
	_, sam := w.invite(t, priya, "sam@acme.com", "admin")
	body := `{"name":"Acme Inc"}`
	for name, hdr := range map[string][]string{
		"no header":         {identity.CSRFHeader, ""},
		"header ≠ cookie":   {identity.CSRFHeader, strings.Repeat("A", 43)},
		"another session's": {identity.CSRFHeader, sam.csrf},
		"malformed":         {identity.CSRFHeader, "x"},
		"cross-site":        {"Sec-Fetch-Site", "cross-site"},
		"evil origin":       {"Origin", "https://evil.example"},
	} {
		r := priya.do("PATCH", "/v1/org", body, hdr...)
		if r.status != 403 || r.code() != "csrf_failed" ||
			r.message() != "This request is missing its security token. Reload the page and try again." {
			t.Errorf("%s: %d %s", name, r.status, r.body)
		}
	}
	if r := priya.do("GET", "/v1/org", ""); r.status != 200 {
		t.Fatalf("GET without a token: %d", r.status)
	}
	r := priya.do("PATCH", "/v1/org", `{"name":"Acme Inc","timezone":"Asia/Kolkata"}`, "Sec-Fetch-Site", "same-origin")
	if r.status != 200 || !strings.Contains(string(r.body), `"timezone": "Asia/Kolkata"`) || !strings.Contains(string(r.body), `"deed": "deed_`) {
		t.Fatalf("same-origin PATCH: %d %s", r.status, r.body)
	}
	if r := priya.do("PATCH", "/v1/org", `{"timezone":"Mars/Base"}`); r.status != 400 {
		t.Fatalf("bad zone: %d", r.status)
	}
	// No CORS anywhere.
	if r := priya.do("GET", "/v1/me", "", "Origin", "https://evil.example"); r.header.Get("Access-Control-Allow-Origin") != "" {
		t.Fatal("CORS header sent")
	}
}

// Sessions: idle and absolute expiry, logout, logout everywhere across orgs, removal, org switch.
func TestSessions(t *testing.T) {
	w := newWorld(t)
	c, priya := w.org(t, "Acme", "priya@acme.com")
	// Idle: 12 h without a request.
	w.clock.Add(identity.SessionIdle + time.Second)
	if r := priya.do("GET", "/v1/me", ""); r.status != 401 || r.code() != "session_expired" ||
		!strings.Contains(r.message(), "Your session ended after 12 hours without activity.") || r.header.Get("WWW-Authenticate") == "" {
		t.Fatalf("idle: %d %s", r.status, r.body)
	}
	// Absolute: kept alive every 11 h, it still ends after 7 days.
	l, _ := w.svc.IssueLink(ctx, "priya@acme.com", c.Org.ID, false)
	b := w.browser(t)
	b.signInWithToken(tokenOf(l.Link))
	for i := 0; i < 15; i++ {
		w.clock.Add(11 * time.Hour)
		if r := b.do("GET", "/v1/me", ""); r.status != 200 {
			t.Fatalf("hour %d: %d %s", (i+1)*11, r.status, r.body)
		}
	}
	w.clock.Add(4*time.Hour + time.Second) // 169 h + 1 s
	if r := b.do("GET", "/v1/me", ""); r.status != 401 || !strings.Contains(r.message(), "after 7 days") {
		t.Fatalf("absolute: %d %s", r.status, r.body)
	}

	// Two orgs for one person; logout everywhere ends both sessions.
	w2 := newWorld(t)
	c1, p1 := w2.org(t, "Acme", "priya@acme.com")
	c2, err := identity.CreateOrg(ctx, w2.e.DB, w2.led, "Beta", "", "priya@acme.com")
	if err != nil {
		t.Fatal(err)
	}
	l2, _ := w2.svc.IssueLink(ctx, "priya@acme.com", c2.Org.ID, false)
	p2 := w2.browser(t)
	me2 := p2.signInWithToken(tokenOf(l2.Link))
	if len(me2.Orgs) != 2 {
		t.Fatalf("orgs: %+v", me2.Orgs)
	}
	// Switch org: a new session in Acme, the old Beta one ends.
	r := p2.do("POST", "/v1/auth/switch-org", `{"org":"`+c1.Org.ID.String()+`"}`)
	var sw identity.Me
	r.json(t, &sw)
	if r.status != 200 || sw.Org.ID != c1.Org.ID.String() || sw.Session.ID == me2.Session.ID {
		t.Fatalf("switch: %d %s", r.status, r.body)
	}
	p2.csrf = *sw.CSRFToken
	if r := p2.do("POST", "/v1/auth/switch-org", `{"org":"org_01k6h7kp6msmzrhdzwtcr3jnkp"}`); r.status != 404 {
		t.Fatalf("switch to a stranger's org: %d %s", r.status, r.body)
	}
	// p3: a third session, in Beta.
	l3, _ := w2.svc.IssueLink(ctx, "priya@acme.com", c2.Org.ID, false)
	p3 := w2.browser(t)
	p3.signInWithToken(tokenOf(l3.Link))
	if r := p1.do("POST", "/v1/auth/logout", `{"everywhere":true}`); r.status != 204 {
		t.Fatalf("logout everywhere: %d %s", r.status, r.body)
	}
	for name, b := range map[string]*browser{"acme": p1, "switched": p2, "beta": p3} {
		if r := b.do("GET", "/v1/me", ""); r.status != 401 {
			t.Errorf("%s still signed in: %d", name, r.status)
		}
	}
	// Plain logout ends one session and clears the cookies.
	l4, _ := w2.svc.IssueLink(ctx, "priya@acme.com", c1.Org.ID, false)
	p4 := w2.browser(t)
	p4.signInWithToken(tokenOf(l4.Link))
	r = p4.do("POST", "/v1/auth/logout", "")
	if r.status != 204 || !strings.Contains(strings.Join(r.header.Values("Set-Cookie"), ";"), "Max-Age=0") {
		t.Fatalf("logout: %d %v", r.status, r.header)
	}
	// Removal ends the member's sessions at once.
	m, sam := w2.invite(t, p4relogin(t, w2, c1.Org.ID), "sam@acme.com", "viewer")
	owner := p4relogin(t, w2, c1.Org.ID)
	if r := owner.do("DELETE", "/v1/org/members/"+m.ID, ""); r.status != 200 || !strings.Contains(string(r.body), `"status": "removed"`) {
		t.Fatalf("remove: %d %s", r.status, r.body)
	}
	if r := sam.do("GET", "/v1/me", ""); r.status != 401 {
		t.Fatalf("removed member: %d %s", r.status, r.body)
	}
	actions := adminActions(t, w2, c1.Org.ID)
	if actions["member.removed"] != 1 || actions["user.signed_out"] < 2 {
		t.Fatalf("actions %v", actions)
	}
}

func p4relogin(t *testing.T, w *world, org ids.OrgID) *browser {
	l, err := w.svc.IssueLink(ctx, "priya@acme.com", org, false)
	if err != nil {
		t.Fatal(err)
	}
	b := w.browser(t)
	b.signInWithToken(tokenOf(l.Link))
	return b
}

// The last owner: demoting or removing the only owner is 409; two owners demoting each other at
// the same time, 100 times, always leave exactly one owner.
func TestLastOwner(t *testing.T) {
	w := newWorld(t)
	c, priya := w.org(t, "Acme", "priya@acme.com")
	r := priya.do("GET", "/v1/me", "")
	var me identity.Me
	r.json(t, &me)
	if r := priya.do("PATCH", "/v1/org/members/"+me.Membership.ID, `{"role":"admin"}`); r.status != 409 || r.code() != "last_owner" ||
		r.message() != "Acme needs at least one owner. Make someone else an owner first." {
		t.Fatalf("demote the only owner: %d %s", r.status, r.body)
	}
	if r := priya.do("DELETE", "/v1/org/members/"+me.Membership.ID, ""); r.status != 409 {
		t.Fatalf("remove the only owner: %d %s", r.status, r.body)
	}
	m2, olu := w.invite(t, priya, "olu@acme.com", "owner")
	pPriya := &identity.Principal{Kind: "user", OrgID: c.Org.ID, UserID: c.Owner.ID, Role: identity.RoleOwner, OrgName: "Acme"}
	var oluMe identity.Me
	olu.do("GET", "/v1/me", "").json(t, &oluMe)
	pOlu := &identity.Principal{Kind: "user", OrgID: c.Org.ID, UserID: ids.MustParse[ids.UserKind](oluMe.User.ID), Role: identity.RoleOwner, OrgName: "Acme"}
	reset := func() {
		admin := w.e.Admin(t)
		defer admin.Close(ctx)
		if _, err := admin.Exec(ctx, `UPDATE deedglass.memberships SET role = 'owner' WHERE org_id = $1 AND status = 'active'`, c.Org.ID.String()); err != nil {
			t.Fatal(err)
		}
	}
	for run := 0; run < 100; run++ {
		reset()
		var wg sync.WaitGroup
		errs := make([]error, 2)
		wg.Add(2)
		go func() { defer wg.Done(); _, errs[0] = w.svc.ChangeRole(ctx, pPriya, m2.ID, "viewer") }()
		go func() { defer wg.Done(); _, errs[1] = w.svc.ChangeRole(ctx, pOlu, me.Membership.ID, "viewer") }()
		wg.Wait()
		ok, last := 0, 0
		for _, err := range errs {
			switch {
			case err == nil:
				ok++
			case strings.Contains(err.Error(), "last_owner"):
				last++
			default:
				t.Fatalf("run %d: %v", run, err)
			}
		}
		var owners int
		_ = w.e.DB.OrgReadTx(ctx, c.Org.ID, func(ctx context.Context, tx pgx.Tx) error {
			return tx.QueryRow(ctx, `SELECT count(*) FROM deedglass.memberships WHERE role = 'owner' AND status = 'active'`).Scan(&owners)
		})
		if ok != 1 || last != 1 || owners != 1 {
			t.Fatalf("run %d: ok %d last_owner %d owners %d", run, ok, last, owners)
		}
	}
	reset()
	// An owner may step down while another owner remains.
	if _, err := w.svc.ChangeRole(ctx, pPriya, me.Membership.ID, "admin"); err != nil {
		t.Fatalf("self-demote with another owner: %v", err)
	}
	if _, err := w.svc.ChangeRole(ctx, pOlu, m2.ID, "admin"); err == nil || !strings.Contains(err.Error(), "last_owner") {
		t.Fatalf("the remaining owner can't step down: %v", err)
	}
	// Changing to the same role seals nothing.
	if m, err := w.svc.ChangeRole(ctx, pOlu, m2.ID, "owner"); err != nil || m.Deed != nil {
		t.Fatalf("no-op: %v %+v", err, m)
	}
	if _, err := w.svc.ChangeRole(ctx, pOlu, "mem_nope", "owner"); err == nil {
		t.Fatal("unknown member")
	}
}

// Row-level security: one org's sessions see nothing of another's people.
func TestRLSIsolation(t *testing.T) {
	w := newWorld(t)
	a, _ := w.org(t, "Acme", "priya@acme.com")
	b, _ := w.org(t, "Beta", "bo@beta.com")
	err := w.e.DB.OrgReadTx(ctx, b.Org.ID, func(ctx context.Context, tx pgx.Tx) error {
		for table, q := range map[string]string{
			"users":        `SELECT count(*) FROM deedglass.users WHERE email = 'priya@acme.com'`,
			"memberships":  `SELECT count(*) FROM deedglass.memberships WHERE org_id = $1`,
			"sessions":     `SELECT count(*) FROM deedglass.sessions WHERE org_id = $1`,
			"login_tokens": `SELECT count(*) FROM deedglass.login_tokens WHERE org_id = $1`,
		} {
			var n int
			args := []any{a.Org.ID}
			if table == "users" {
				args = nil
			}
			if err := tx.QueryRow(ctx, q, args...).Scan(&n); err != nil {
				return err
			}
			if n != 0 {
				t.Errorf("org B sees %d %s rows of org A", n, table)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// The pre-auth functions work for the app role and fail for a role without EXECUTE.
func TestPreAuthGrants(t *testing.T) {
	w := newWorld(t)
	w.org(t, "Acme", "priya@acme.com")
	u, err := w.e.DB.PreAuth().UserByEmail(ctx, "priya@acme.com")
	if err != nil || len(u.Memberships) != 1 || u.Memberships[0].OrgName != "Acme" {
		t.Fatalf("%v %+v", err, u)
	}
	if _, err := w.e.DB.PreAuth().UserByEmail(ctx, "nobody@acme.com"); !errors.Is(err, store.ErrNotFound) {
		t.Fatal(err)
	}
	admin := w.e.Admin(t)
	defer admin.Close(ctx)
	if _, err := admin.Exec(ctx, `DROP ROLE IF EXISTS deedglass_it_noexec; CREATE ROLE deedglass_it_noexec NOLOGIN;
	    GRANT USAGE ON SCHEMA deedglass TO deedglass_it_noexec`); err != nil {
		t.Fatal(err)
	}
	defer admin.Exec(ctx, `REVOKE ALL ON SCHEMA deedglass FROM deedglass_it_noexec; DROP ROLE deedglass_it_noexec`)
	for _, fn := range []string{"preauth_session('\\x00'::bytea)", "preauth_login_token('\\x00'::bytea)", "preauth_user_by_email('a@b.co')",
		"preauth_code_token('usr_x')", "maint_prune_identity(1)"} {
		tx, err := admin.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = tx.Exec(ctx, `SET LOCAL ROLE deedglass_it_noexec`)
		_, err = tx.Exec(ctx, `SELECT * FROM deedglass.`+fn)
		_ = tx.Rollback(ctx)
		if store.SQLState(err) != store.CodeInsufficientPriv {
			t.Errorf("%s without EXECUTE: %v", fn, err)
		}
	}
	var definer, pinned bool
	if err := admin.QueryRow(ctx, `SELECT bool_and(prosecdef), bool_and(proconfig @> ARRAY['search_path=pg_catalog, pg_temp'])
	    FROM pg_proc WHERE pronamespace = 'deedglass'::regnamespace AND (proname LIKE 'preauth\_%' OR proname LIKE 'maint\_%')`).Scan(&definer, &pinned); err != nil || !definer || !pinned {
		t.Fatalf("SECURITY DEFINER %v, search_path pinned %v, %v", definer, pinned, err)
	}
}

// Prune: tokens 7 days after expiry and sessions 30 days after they end are deleted; newer rows stay.
func TestPruneIdentity(t *testing.T) {
	w := newWorld(t)
	c, _ := w.org(t, "Acme", "priya@acme.com")
	w.svc.IssueLink(ctx, "priya@acme.com", c.Org.ID, false)
	admin := w.e.Admin(t)
	defer admin.Close(ctx)
	if _, err := admin.Exec(ctx, `UPDATE deedglass.login_tokens SET expires_at = now() - interval '8 days'`); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, `UPDATE deedglass.sessions SET expires_at = now() - interval '31 days'`); err != nil {
		t.Fatal(err)
	}
	n, err := w.e.DB.PreAuth().PruneIdentity(ctx, 5000)
	if err != nil || n < 3 {
		t.Fatalf("pruned %d, %v", n, err)
	}
	var left int
	_ = admin.QueryRow(ctx, `SELECT (SELECT count(*) FROM deedglass.login_tokens) + (SELECT count(*) FROM deedglass.sessions)`).Scan(&left)
	if left != 0 {
		t.Fatalf("%d rows left", left)
	}
	if _, err := w.e.DB.PreAuth().PruneIdentity(ctx, 0); err == nil {
		t.Fatal("limit 0")
	}
}

// Nothing secret reaches the logs over a whole flow: no token, code, cookie, CSRF token or address.
func TestLogsCarryNoSecrets(t *testing.T) {
	w := newWorld(t)
	_, priya := w.org(t, "Acme", "priya@acme.com")
	w.invite(t, priya, "sam@acme.com", "viewer")
	anon := w.browser(t)
	anon.do("POST", "/v1/auth/login", `{"email":"priya@acme.com"}`)
	_, tok, code := w.mail.last(t, "priya@acme.com")
	other := w.browser(t)
	other.do("POST", "/v1/auth/callback", `{"email":"priya@acme.com","code":"`+code+`"}`)
	logs := w.logText()
	if len(logs) < 200 {
		t.Fatalf("too few logs to mean anything: %q", logs)
	}
	secrets := []string{tok, code, priya.csrf, "priya@acme.com", "sam@acme.com"}
	for _, c := range priya.c.Jar.Cookies(mustURL(w.ts.URL)) {
		secrets = append(secrets, c.Value)
	}
	for _, s := range secrets {
		if s != "" && strings.Contains(logs, s) {
			t.Errorf("the logs contain %q", s)
		}
	}
}

// Cookie attributes in prod form (__Host-, Secure, HttpOnly on the session, SameSite, Path=/, no
// Domain, Max-Age to absolute expiry).
func TestProdCookies(t *testing.T) {
	w := newWorld(t, func(o *identity.Options) { o.Prod = true; o.BaseURL = mustURL("https://app.deedglass.example") })
	c, err := identity.CreateOrg(ctx, w.e.DB, w.led, "Acme", "", "priya@acme.com")
	if err != nil {
		t.Fatal(err)
	}
	l, _ := w.svc.IssueLink(ctx, "priya@acme.com", c.Org.ID, false)
	r := w.browser(t).do("POST", "/v1/auth/callback", `{"token":"`+tokenOf(l.Link)+`"}`)
	if r.status != 200 {
		t.Fatalf("%d %s", r.status, r.body)
	}
	cookies := r.header.Values("Set-Cookie")
	if len(cookies) != 2 {
		t.Fatalf("%v", cookies)
	}
	max := fmt.Sprintf("Max-Age=%d", int(identity.SessionAbsolute.Seconds()))
	sess, csrf := cookies[0], cookies[1]
	for _, want := range []string{"__Host-deedglass_session=", "Path=/", max, "HttpOnly", "Secure", "SameSite=Lax"} {
		if !strings.Contains(sess, want) {
			t.Errorf("session cookie %q lacks %q", sess, want)
		}
	}
	for _, want := range []string{"__Host-deedglass_csrf=", "Path=/", max, "Secure", "SameSite=Strict"} {
		if !strings.Contains(csrf, want) {
			t.Errorf("csrf cookie %q lacks %q", csrf, want)
		}
	}
	if strings.Contains(csrf, "HttpOnly") || strings.Contains(sess+csrf, "Domain") {
		t.Errorf("csrf must be readable by the page and neither has a Domain: %q %q", sess, csrf)
	}
}

func mustURL(s string) *url.URL { u, _ := url.Parse(s); return u }

func pct(d []time.Duration, p int) time.Duration {
	s := slices.Clone(d)
	slices.Sort(s)
	return s[(len(s)-1)*p/100]
}

// nopQueue drops every job: the login handler's cost without the worker's.
type nopQueue struct{}

func (nopQueue) Submit(string, string, string, mailer.Job) bool { return true }

// Budgets (S07 §3.13), measured: session middleware, the login handler, an invite with its deed.
// It runs under make bench-identity (DEEDGLASS_IT_BENCH=1) only, like S06's TestLedgerBudget: on a
// shared, loaded machine p99s swing by 100×, which says nothing about the code.
func TestIdentityBudget(t *testing.T) {
	if os.Getenv("DEEDGLASS_IT_BENCH") != "1" {
		t.Skip("set DEEDGLASS_IT_BENCH=1 (make bench-identity)")
	}
	w := newWorld(t)
	c, priya := w.org(t, "Acme", "priya@acme.com")
	_ = c
	measure := func(name string, n int, f func(i int)) time.Duration {
		d := make([]time.Duration, n)
		for i := range d {
			s := time.Now()
			f(i)
			d[i] = time.Since(s)
		}
		p50, p99 := pct(d, 50), pct(d, 99)
		t.Logf("PERF {\"name\":%q,\"n\":%d,\"p50_us\":%.1f,\"p99_us\":%.1f}", name, n,
			float64(p50.Nanoseconds())/1000, float64(p99.Nanoseconds())/1000)
		return p99
	}
	// The session middleware alone: AuthenticateSession on a GET (no touch after the first).
	req, _ := http.NewRequest("GET", w.ts.URL+"/v1/me", nil)
	for _, ck := range priya.c.Jar.Cookies(mustURL(w.ts.URL)) {
		req.AddCookie(ck)
	}
	sess := measure("session middleware", 2000, func(int) {
		if _, err := w.svc.AuthenticateSession(ctx, req); err != nil {
			t.Fatal(err)
		}
	})
	quiet, err := identity.New(identity.Options{DB: w.e.DB, Ledger: w.led, Mail: nopQueue{}, Pepper: make([]byte, 32), BaseURL: mustURL(w.ts.URL)})
	if err != nil {
		t.Fatal(err)
	}
	login := measure("POST /v1/auth/login (handler, no worker)", 2000, func(i int) {
		if _, err := quiet.RequestLogin(ctx, fmt.Sprintf("u%d@bench.example", i), fmt.Sprintf("10.1.%d.%d", i/250, i%250), "bench"); err != nil {
			t.Fatal(err)
		}
	})
	pr := &identity.Principal{Kind: "user", OrgID: c.Org.ID, UserID: c.Owner.ID, Role: identity.RoleOwner, OrgName: "Acme"}
	inv := measure("invite with deed", 300, func(i int) {
		if _, _, err := w.svc.Invite(ctx, pr, identity.InviteInput{Email: fmt.Sprintf("b%d@acme.com", i), Role: "viewer"}); err != nil {
			t.Fatal(err)
		}
	})
	// S07 §3.13.
	if sess > 2*time.Millisecond || login > 3*time.Millisecond || inv > 30*time.Millisecond {
		t.Errorf("budget missed (load: check uptime): session %v (2 ms), login %v (3 ms), invite %v (30 ms)", sess, login, inv)
	}
}

// Operator paths: org create refusals, login links for active and invited members, and the prod
// org-create email sent inline.
func TestOperatorPaths(t *testing.T) {
	w := newWorld(t)
	c, priya := w.org(t, "Acme", "priya@acme.com")
	if _, err := identity.CreateOrg(ctx, w.e.DB, w.led, "Acme", "acme", "x@acme.com"); !errors.Is(err, identity.ErrSlugTaken) {
		t.Fatalf("slug taken: %v", err)
	}
	for name, args := range map[string][3]string{
		"name":  {"", "", "x@acme.com"},
		"email": {"Zeta", "", "x@acme"},
		"slug":  {"Zeta", "-zeta", "x@acme.com"},
	} {
		if _, err := identity.CreateOrg(ctx, w.e.DB, w.led, args[0], args[1], args[2]); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	// A second org for the same person reuses the user row.
	c2, err := identity.CreateOrg(ctx, w.e.DB, w.led, "Beta Labs", "", "Priya@Acme.com")
	if err != nil || c2.Owner.ID != c.Owner.ID || c2.Org.Slug != "beta-labs" {
		t.Fatalf("%v %+v", err, c2)
	}
	// A pending invitee gets an invite link from the CLI path.
	priya.do("POST", "/v1/org/members", `{"email":"sam@acme.com","role":"viewer"}`)
	l, err := w.svc.IssueLink(ctx, "sam@acme.com", ids.OrgID{}, true)
	if err != nil || l.Purpose != "invite" || l.Deed == "" || l.OrgID != c.Org.ID || l.ExpiresAt.Sub(w.clock.Now()) != identity.InviteTTL {
		t.Fatalf("invitee link: %v %+v", err, l)
	}
	sam := w.browser(t)
	if me := sam.signInWithToken(tokenOf(l.Link)); me.Membership.Role != "viewer" {
		t.Fatalf("%+v", me)
	}
	for name, tc := range map[string]struct {
		email string
		org   ids.OrgID
	}{"unknown": {"nobody@acme.com", ids.OrgID{}}, "not in that org": {"sam@acme.com", c2.Org.ID}, "bad address": {"nope", ids.OrgID{}}} {
		if _, err := w.svc.IssueLink(ctx, tc.email, tc.org, true); err == nil {
			t.Errorf("%s: issued", name)
		}
	}
	// EmailSignIn: the same email the worker sends, for one org.
	if err := w.svc.EmailSignIn(ctx, w.mail, "priya@acme.com", c2.Org.ID); err != nil {
		t.Fatal(err)
	}
	msg, tok, code := w.mail.last(t, "priya@acme.com")
	if !strings.Contains(msg.Text, "Beta Labs") || code == "" {
		t.Fatalf("org-create email:\n%s", msg.Text)
	}
	if me := w.browser(t).signInWithToken(tok); me.Org.ID != c2.Org.ID.String() || len(me.Orgs) != 2 {
		t.Fatalf("%+v", me)
	}
	// Leaving: a member removes themselves; their cookies are cleared.
	var sme identity.Me
	sam.do("GET", "/v1/me", "").json(t, &sme)
	adminB := w.browser(t)
	_ = adminB
	r := priya.do("PATCH", "/v1/org/members/"+sme.Membership.ID, `{"role":"admin"}`)
	if r.status != 200 || !strings.Contains(string(r.body), `"role": "admin"`) {
		t.Fatalf("promote: %d %s", r.status, r.body)
	}
	r = sam.do("DELETE", "/v1/org/members/"+sme.Membership.ID, "")
	if r.status != 200 || !strings.Contains(strings.Join(r.header.Values("Set-Cookie"), ";"), "Max-Age=0") {
		t.Fatalf("leave: %d %s", r.status, r.body)
	}
	if r := priya.do("DELETE", "/v1/org/members/"+sme.Membership.ID, ""); r.status != 404 {
		t.Fatalf("remove twice: %d", r.status)
	}
	if r := priya.do("PATCH", "/v1/org/members/"+sme.Membership.ID, `{"role":"viewer"}`); r.status != 409 {
		t.Fatalf("role of a removed member: %d %s", r.status, r.body)
	}
	if r := priya.do("PATCH", "/v1/org/members/mem_nope", `{"role":"viewer"}`); r.status != 404 {
		t.Fatalf("bad id: %d", r.status)
	}
	if r := priya.do("PATCH", "/v1/org/members/"+sme.Membership.ID, `{"role":"root"}`); r.status != 400 {
		t.Fatalf("bad role: %d", r.status)
	}
	if r := priya.do("DELETE", "/v1/org/members/mem_nope", ""); r.status != 404 {
		t.Fatalf("bad id: %d", r.status)
	}
}

// The members list pages with a cursor and filters by status; the org object and PATCH rules.
func TestMembersListAndOrg(t *testing.T) {
	w := newWorld(t)
	_, priya := w.org(t, "Acme", "priya@acme.com")
	for i := 0; i < 5; i++ {
		priya.do("POST", "/v1/org/members", fmt.Sprintf(`{"email":"p%d@acme.com","role":"viewer"}`, i))
	}
	seen := map[string]bool{}
	path := "/v1/org/members?limit=2"
	for pages := 0; pages < 5; pages++ {
		var page struct {
			Data       []identity.Member `json:"data"`
			HasMore    bool              `json:"has_more"`
			NextCursor *string           `json:"next_cursor"`
		}
		priya.do("GET", path, "").json(t, &page)
		for _, m := range page.Data {
			if seen[m.ID] {
				t.Fatalf("duplicate %s", m.ID)
			}
			seen[m.ID] = true
		}
		if !page.HasMore {
			break
		}
		path = "/v1/org/members?limit=2&cursor=" + *page.NextCursor
	}
	if len(seen) != 6 {
		t.Fatalf("paged %d members, want 6", len(seen))
	}
	var invited struct {
		Data []identity.Member `json:"data"`
	}
	priya.do("GET", "/v1/org/members?status=invited", "").json(t, &invited)
	if len(invited.Data) != 5 {
		t.Fatalf("invited: %d", len(invited.Data))
	}
	for _, q := range []string{"?status=gone", "?limit=0", "?cursor=zzz"} {
		if r := priya.do("GET", "/v1/org/members"+q, ""); r.status != 400 {
			t.Errorf("%s: %d", q, r.status)
		}
	}
	r := priya.do("GET", "/v1/org", "")
	var o identity.Org
	r.json(t, &o)
	if r.status != 200 || o.Object != "org" || o.Slug != "acme" || o.Timezone != "UTC" || o.Status != "active" {
		t.Fatalf("org: %s", r.body)
	}
	for body, want := range map[string]int{`{}`: 400, `{"name":""}`: 400, `{"name":"Acme"}`: 200, `{"name":"x","extra":1}`: 400} {
		if r := priya.do("PATCH", "/v1/org", body); r.status != want {
			t.Errorf("PATCH %s: %d %s", body, r.status, r.body)
		}
	}
	// Renaming to the same name seals nothing.
	r = priya.do("PATCH", "/v1/org", `{"name":"Acme"}`)
	if !strings.Contains(string(r.body), `"deed": null`) {
		t.Fatalf("no-op patch sealed: %s", r.body)
	}
	for body, want := range map[string]int{`{}`: 400, `{"name":"` + strings.Repeat("x", 81) + `"}`: 400, `{"name":""}`: 200} {
		if r := priya.do("PATCH", "/v1/me", body); r.status != want {
			t.Errorf("PATCH /v1/me %s: %d", body, r.status)
		}
	}
	if r := priya.do("POST", "/v1/auth/switch-org", `{"org":"nope"}`); r.status != 400 {
		t.Fatalf("switch to a bad id: %d", r.status)
	}
	var me identity.Me
	priya.do("GET", "/v1/me", "").json(t, &me)
	if r := priya.do("POST", "/v1/auth/switch-org", `{"org":"`+me.Org.ID+`"}`); r.status != 409 {
		t.Fatalf("switch to the current org: %d", r.status)
	}
}

// S26: PATCH /v1/org's owner-only fields. Retention and approval settings: an owner can, an admin
// and a viewer can't; validation; null resets retention; settings merge under approvals; one deed
// per change and none for a no-op; the approvals reader sees the new values.
func TestOrgRetentionAndSettings(t *testing.T) {
	w := newWorld(t)
	c, priya := w.org(t, "Acme", "priya@acme.com")
	_, sam := w.invite(t, priya, "sam@acme.com", "admin")
	_, viv := w.invite(t, priya, "viv@acme.com", "viewer")
	type orgView struct {
		identity.Org
		Deed *string `json:"deed"`
	}
	get := func() identity.Org {
		t.Helper()
		var o identity.Org
		r := priya.do("GET", "/v1/org", "")
		r.json(t, &o)
		for _, err := range httpx.CheckRequired(api.Spec, "Org", r.body) {
			t.Error(err)
		}
		return o
	}
	o := get()
	if o.RetentionDays == nil || *o.RetentionDays != ledger.DefaultRetentionDays || o.Settings == nil ||
		o.Settings.Approvals != (identity.ApprovalSettings{EmailLinks: "decide", EmailDetail: "summary", SelfCountersign: "deny"}) {
		t.Fatalf("defaults: %+v %+v", o.RetentionDays, o.Settings)
	}
	// /v1/me's org keeps its S07 shape.
	if r := priya.do("GET", "/v1/me", ""); strings.Contains(string(r.body), "retention_days") || strings.Contains(string(r.body), `"settings"`) {
		t.Fatalf("/v1/me grew: %s", r.body)
	}
	base := adminActions(t, w, c.Org.ID)["org.updated"]

	// Refusals: admin by the handler, viewer by the route.
	for _, b := range []string{`{"retention_days":30}`, `{"retention_days":null}`, `{"settings":{"approvals":{"email_links":"review"}}}`} {
		if r := sam.do("PATCH", "/v1/org", b); r.status != 403 || r.code() != "insufficient_role" || !strings.Contains(r.message(), "needs Owner") {
			t.Errorf("admin %s: %d %s", b, r.status, r.body)
		}
		if r := viv.do("PATCH", "/v1/org", b); r.status != 403 || r.code() != "insufficient_role" {
			t.Errorf("viewer %s: %d %s", b, r.status, r.body)
		}
	}
	if r := sam.do("PATCH", "/v1/org", `{"timezone":"Asia/Kolkata"}`); r.status != 200 {
		t.Fatalf("admin timezone: %d %s", r.status, r.body)
	}
	// Validation.
	for b, param := range map[string]string{
		`{"retention_days":0}`: "retention_days", `{"retention_days":3651}`: "retention_days", `{"retention_days":1.5}`: "retention_days",
		`{"retention_days":"30"}`: "retention_days", `{"settings":{"approvals":{"email_links":"x"}}}`: "settings.approvals.email_links",
		`{"settings":{"approvals":{"foo":"x"}}}`: "settings.approvals.foo", `{"settings":{"bar":1}}`: "settings.bar",
	} {
		r := priya.do("PATCH", "/v1/org", b)
		var e struct {
			Error struct{ Param string } `json:"error"`
		}
		r.json(t, &e)
		if r.status != 400 || r.code() != "validation_failed" || e.Error.Param != param {
			t.Errorf("%s: %d %s", b, r.status, r.body)
		}
	}
	if n := adminActions(t, w, c.Org.ID)["org.updated"]; n != base+1 {
		t.Fatalf("refusals sealed: %d deeds, want %d", n, base+1)
	}
	base++

	// Keys outside approvals survive the merge.
	if err := w.e.DB.OrgTx(ctx, c.Org.ID, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE deedglass.orgs SET settings = '{"other":{"k":1},"approvals":{"email_detail":"full"}}' WHERE id = $1`, c.Org.ID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	var v orgView
	r := priya.do("PATCH", "/v1/org", `{"retention_days":30,"settings":{"approvals":{"email_links":"review","self_countersign":"allow"}}}`)
	r.json(t, &v)
	if r.status != 200 || v.Deed == nil || *v.RetentionDays != 30 ||
		v.Settings.Approvals != (identity.ApprovalSettings{EmailLinks: "review", EmailDetail: "full", SelfCountersign: "allow"}) {
		t.Fatalf("owner patch: %d %s", r.status, r.body)
	}
	var raw string
	var data map[string]any
	if err := w.e.DB.OrgReadTx(ctx, c.Org.ID, func(ctx context.Context, tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `SELECT settings::text FROM deedglass.orgs WHERE id = $1`, c.Org.ID).Scan(&raw); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `SELECT data FROM deedglass.admin_events WHERE deed_id = $1`, *v.Deed).Scan(&data); err != nil {
			return err
		}
		s, err := approve.ReadSettings(ctx, tx, c.Org.ID)
		if err != nil {
			return err
		}
		if s != (approve.Settings{EmailLinks: "review", EmailDetail: "full", SelfCountersign: "allow"}) {
			t.Errorf("approvals reader: %+v", s)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if raw != `{"other": {"k": 1}, "approvals": {"email_links": "review", "email_detail": "full", "self_countersign": "allow"}}` {
		t.Fatalf("settings: %s", raw)
	}
	if fmt.Sprint(data["fields"]) != "[retention_days settings.approvals.email_links settings.approvals.self_countersign]" ||
		data["retention_days"] != float64(30) || data["timezone"] != "Asia/Kolkata" ||
		fmt.Sprint(data["approvals"]) != "map[email_detail:full email_links:review self_countersign:allow]" {
		t.Fatalf("deed data: %v", data)
	}
	if days, err := w.led.RetentionDays(ctx, c.Org.ID); err != nil || days != 30 {
		t.Fatalf("ledger retention: %d %v", days, err)
	}
	if n := adminActions(t, w, c.Org.ID)["org.updated"]; n != base+1 {
		t.Fatalf("deeds: %d, want %d", n, base+1)
	}
	// The same values again: no deed.
	r = priya.do("PATCH", "/v1/org", `{"retention_days":30,"settings":{"approvals":{"email_links":"review"}}}`)
	if r.status != 200 || !strings.Contains(string(r.body), `"deed": null`) {
		t.Fatalf("no-op: %d %s", r.status, r.body)
	}
	if n := adminActions(t, w, c.Org.ID)["org.updated"]; n != base+1 {
		t.Fatalf("no-op sealed: %d", n)
	}
	// null resets to the default, and that is a change.
	v = orgView{}
	r = priya.do("PATCH", "/v1/org", `{"retention_days":null}`)
	r.json(t, &v)
	if r.status != 200 || v.Deed == nil || *v.RetentionDays != ledger.DefaultRetentionDays {
		t.Fatalf("reset: %d %s", r.status, r.body)
	}
	var stored *int
	_ = w.e.DB.OrgReadTx(ctx, c.Org.ID, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT retention_days FROM deedglass.orgs WHERE id = $1`, c.Org.ID).Scan(&stored)
	})
	if stored != nil {
		t.Fatalf("reset stored %d, want NULL", *stored)
	}
	if n := adminActions(t, w, c.Org.ID)["org.updated"]; n != base+2 {
		t.Fatalf("reset deeds: %d", n)
	}
	// Resetting again changes nothing.
	if r := priya.do("PATCH", "/v1/org", `{"retention_days":null}`); !strings.Contains(string(r.body), `"deed": null`) {
		t.Fatalf("second reset: %s", r.body)
	}
	if o := get(); *o.RetentionDays != 90 || o.Settings.Approvals.EmailLinks != "review" || o.Stop == nil {
		t.Fatalf("after: %+v", o)
	}
}
