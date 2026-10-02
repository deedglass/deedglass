//go:build attack

package harness

import (
	"encoding/json"
	"fmt"
	"net/http/cookiejar"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// People for S22's approval cases (group L): approvals need a person, so the cases add members to
// an org and sign them in through the dev link flow, and read the approval links the dev mail log
// prints on the server's stderr. Nothing here sends real email: serverEnv sets no mail provider.

// Member is a person in an org with a signed-in session.
type Member struct {
	Email   string
	UserID  string
	Role    string
	Session *Client
}

// signIn redeems a sign-in or invite link's token (POST /v1/auth/callback) and returns the
// person's session (cookies and the CSRF header) and the /v1/me answer.
func (e *Env) signIn(link string) (*Client, map[string]any) {
	m := reToken.FindStringSubmatch(link)
	if m == nil {
		panic("no token in the sign-in link")
	}
	jar, _ := cookiejar.New(nil)
	sc := newClient(e.Server, "", e.Bodies).WithSession(jar, nil)
	r := ok(sc.Do("POST", "/v1/auth/callback", map[string]string{"token": m[1]}), "sign in", 200)
	base, _ := url.Parse(e.Server)
	csrf := func() string {
		for _, c := range jar.Cookies(base) {
			if strings.Contains(c.Name, "csrf") {
				return c.Value
			}
		}
		return ""
	}
	return sc.WithSession(jar, csrf), r.Map()
}

// Owner is the org's owner as a Member (the session createOrg signed in).
func (e *Env) Owner(o *Org) *Member {
	if o.Session == nil {
		panic(o.Slug + " has no owner session")
	}
	me := ok(o.Session.Do("GET", "/v1/me", nil), "GET /v1/me", 200).Map()
	return &Member{Email: Str(me, "user", "email"), UserID: Str(me, "user", "id"), Role: Str(me, "membership", "role"), Session: o.Session}
}

// AddMember invites email to o with role (through the owner's session), then signs them in with a
// dev link from `deedglass org login-link`, which accepts the invite.
func (e *Env) AddMember(o *Org, email, role string) *Member {
	if o.Session == nil {
		panic(o.Slug + " has no owner session")
	}
	ok(o.Session.Do("POST", "/v1/org/members", map[string]string{"email": email, "role": role}), "invite "+email, 200, 201)
	out := e.CLI(nil, "org", "login-link", "--email", email, "--org", o.Slug, "--json")
	var l struct {
		Link string `json:"link"`
	}
	must(json.Unmarshal(out, &l))
	s, me := e.signIn(l.Link)
	m := &Member{Email: email, UserID: Str(me, "user", "id"), Role: Str(me, "membership", "role"), Session: s}
	if m.UserID == "" {
		panic(fmt.Sprintf("signing in %s returned no user id", email))
	}
	return m
}

// reApprovalLink is an approval link in the dev mail log: the token rides in the fragment.
var reApprovalLink = regexp.MustCompile(`http://127\.0\.0\.1:\d+/approve#t=([A-Za-z0-9_-]{43})`)

// ApprovalLinkFor waits up to d for an approval email to `to` written to the server's log after
// byte offset since, and returns its link's token ("" if none came).
func (e *Env) ApprovalLinkFor(since int, to string, d time.Duration) string {
	deadline := time.Now().Add(d)
	for {
		logs := e.Logs.Bytes()
		if since < len(logs) {
			for _, block := range strings.Split(string(logs[since:]), "──── deedglass mail · ")[1:] {
				if !strings.Contains(block, "\nTo: "+to+"\n") {
					continue
				}
				if m := reApprovalLink.FindStringSubmatch(block); m != nil {
					return m[1]
				}
			}
		}
		if time.Now().After(deadline) {
			return ""
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// LogOffset is the server log's length now (pass it to ApprovalLinkFor).
func (e *Env) LogOffset() int { return len(e.Logs.Bytes()) }
