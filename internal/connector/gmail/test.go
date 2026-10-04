package gmail

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/deedglass/deedglass/internal/connector"
	"github.com/deedglass/deedglass/internal/connector/saas"
)

// TestCheckIDs are the checks of Test, in order.
var TestCheckIDs = []string{"config", "credentials", "destination", "auth", "profile", "scopes", "send_guard"}

// MsgSendGuard is the send guard line: always a pass.
const MsgSendGuard = "Sending isn't possible through Deedglass. People send drafts themselves."

// TestingTokenLife is how long a refresh token lives while the Google app is in Testing.
const TestingTokenLife = 7 * 24 * time.Hour

// ReauthNote is the 7-day warning.
func ReauthNote(at time.Time) string {
	return "Google apps in Testing get 7-day refresh tokens. Authorize again before " + at.Add(TestingTokenLife).UTC().Format("Jan 2") + "."
}

// Test implements connector.Connector (S30 §3.2). It changes nothing in the mailbox.
func (c *Connector) Test(ctx context.Context, inst *connector.Instance) (connector.TestReport, error) {
	start := time.Now()
	rep := connector.TestReport{}
	add := func(ch connector.Check) { rep.Checks = append(rep.Checks, ch) }
	defer func() { rep.Duration = time.Since(start) }()
	guard := connector.Check{ID: "send_guard", Title: "send guard", Status: connector.CheckPass, Detail: MsgSendGuard}
	finish := func(from int, why string) (connector.TestReport, error) {
		for _, id := range TestCheckIDs[from : len(TestCheckIDs)-1] {
			add(connector.Check{ID: id, Title: id, Status: connector.CheckSkip, Detail: why})
		}
		add(guard)
		rep.Settle()
		return rep, nil
	}
	st, err := parse(inst)
	if err != nil {
		add(connector.Check{ID: "config", Title: "config", Status: connector.CheckFail, Detail: "the stored config can't be read"})
		return finish(1, "the config can't be read")
	}
	add(connector.Check{ID: "config", Title: "config", Status: connector.CheckPass,
		Detail: fmt.Sprintf("valid · %s · mailbox %s · at most %d recipients", st.cfg.Auth, st.cfg.Mailbox, st.cfg.MaxRecipients)})

	authorize := fmt.Sprintf("deedglass connector gmail authorize %s --client-file ./client_secret.json", inst.Name)
	sec, err := inst.Secret(ctx)
	if err != nil {
		add(connector.Check{ID: "credentials", Title: "credentials", Status: connector.CheckFail, Detail: "no credentials yet", Fix: "Run " + authorize + "."})
		return finish(2, "no usable credentials")
	}
	sec.Destroy()
	cred := connector.Check{ID: "credentials", Title: "credentials", Status: connector.CheckPass}
	switch {
	case st.cfg.Auth == "service_account":
		cred.Detail = "service account · acting as " + st.cfg.Mailbox
	case st.cfg.AuthorizedAt.IsZero():
		cred.Detail = "stored · not authorized through deedglass connector gmail authorize"
	default:
		cred.Detail = st.cfg.Email + " · authorized " + st.cfg.AuthorizedAt.UTC().Format("2006-01-02")
		if now().Sub(st.cfg.AuthorizedAt) > 6*24*time.Hour {
			cred.Status, cred.Fix = connector.CheckWarn, ReauthNote(st.cfg.AuthorizedAt)
			cred.Detail += " · " + ReauthNote(st.cfg.AuthorizedAt)
		}
	}
	add(cred)

	dc, ok := saas.DestinationCheck(ctx, inst, st.cfg.APIBaseURL)
	add(dc)
	if !ok {
		return finish(3, "the destination is refused")
	}
	s, err := open(ctx, inst)
	if err != nil {
		add(connector.Check{ID: "auth", Title: "auth", Status: connector.CheckFail, Detail: errText(err)})
		return finish(4, "the credential can't be used")
	}
	defer s.close()
	var p struct {
		EmailAddress string `json:"emailAddress"`
	}
	_, err = s.call(ctx, http.MethodGet, nil, nil, &p, "profile")
	if err != nil {
		ch := connector.Check{ID: "auth", Title: "auth", Status: connector.CheckFail, Detail: errText(err), Fix: "Run " + authorize + "."}
		add(ch)
		return finish(4, "Google didn't accept the credential")
	}
	add(connector.Check{ID: "auth", Title: "auth", Status: connector.CheckPass, Detail: "token refresh works"})
	want := st.cfg.Email
	if st.cfg.Auth == "service_account" {
		want = st.cfg.Mailbox
	}
	switch {
	case want == "":
		add(connector.Check{ID: "profile", Title: "profile", Status: connector.CheckPass, Detail: p.EmailAddress})
	case strings.EqualFold(want, p.EmailAddress):
		add(connector.Check{ID: "profile", Title: "profile", Status: connector.CheckPass, Detail: p.EmailAddress + " matches"})
	default:
		add(connector.Check{ID: "profile", Title: "profile", Status: connector.CheckFail, Detail: fmt.Sprintf("the mailbox is %s · the config says %s", p.EmailAddress, want),
			Fix: "Authorize again with the right Google account: " + authorize + "."})
	}
	sc := connector.Check{ID: "scopes", Title: "scopes", Status: connector.CheckPass, Detail: "gmail.compose"}
	if st.cfg.Auth == "oauth" && len(st.cfg.Scopes) > 0 && !hasScope(st.cfg.Scopes, ScopeCompose) {
		sc.Status, sc.Detail, sc.Fix = connector.CheckFail, "gmail.compose is missing", "Run "+authorize+"."
	} else if st.cfg.readonly() {
		sc.Detail = "gmail.compose, gmail.readonly · get_thread is on"
	} else {
		sc.Detail = "gmail.compose · get_thread is off (authorize with --readonly to read threads)"
	}
	add(sc)
	add(guard)
	rep.Settle()
	return rep, nil
}

func hasScope(xs []string, s string) bool {
	for _, x := range xs {
		if x == s || "https://www.googleapis.com/auth/"+x == s {
			return true
		}
	}
	return false
}

func errText(err error) string {
	if ce := connector.AsError(err); ce != nil {
		if ce.AdminDetail != "" {
			return ce.AdminDetail
		}
		return ce.AgentMessage
	}
	return "Google didn't answer"
}
