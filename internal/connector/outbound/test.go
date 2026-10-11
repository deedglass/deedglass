//go:build outboundmail

package outbound

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/deedglass/deedglass/internal/connector"
)

// TestCheckIDs are the checks of Test, in order (S40 §3.3).
var TestCheckIDs = []string{"config", "credentials", "destination", "auth", "profile", "scopes", "from_guard", "org_gate"}

// Test implements connector.Connector. It sends nothing and changes nothing in the mailbox.
func (c *Connector) Test(ctx context.Context, inst *connector.Instance) (connector.TestReport, error) {
	start := time.Now()
	rep := connector.TestReport{}
	add := func(ch connector.Check) { rep.Checks = append(rep.Checks, ch) }
	defer func() { rep.Duration = time.Since(start) }()
	orgGate := connector.Check{ID: "org_gate", Title: "org gate", Status: connector.CheckPass,
		Detail: "this org is listed in connectors.outbound_mail.orgs (the connector resolved)"}
	finish := func(from int, why string) (connector.TestReport, error) {
		for _, id := range TestCheckIDs[from : len(TestCheckIDs)-1] {
			add(connector.Check{ID: id, Title: strings.ReplaceAll(id, "_", " "), Status: connector.CheckSkip, Detail: why})
		}
		add(orgGate)
		rep.Settle()
		return rep, nil
	}
	st, err := c.parse(inst)
	if err != nil {
		add(connector.Check{ID: "config", Title: "config", Status: connector.CheckFail, Detail: "the stored config can't be read"})
		return finish(1, "the config can't be read")
	}
	add(connector.Check{ID: "config", Title: "config", Status: connector.CheckPass,
		Detail: fmt.Sprintf("valid · mailbox %s · window required %t", st.cfg.Mailbox, st.cfg.RequireWindow)})
	authorize := fmt.Sprintf("deedglass connector outbound authorize %s --client-file ./client_secret.json", inst.Name)
	s, err := c.open(ctx, inst)
	if err != nil {
		add(connector.Check{ID: "credentials", Title: "credentials", Status: connector.CheckFail, Detail: "no usable credentials", Fix: "Run " + authorize + " yourself."})
		return finish(2, "no usable credentials")
	}
	defer s.close()
	add(connector.Check{ID: "credentials", Title: "credentials", Status: connector.CheckPass, Detail: "client_id, client_secret and refresh_token stored"})
	add(connector.Check{ID: "destination", Title: "destination", Status: connector.CheckPass, Detail: c.apiBase})
	var p struct {
		EmailAddress string `json:"emailAddress"`
	}
	_, err = s.call(ctx, http.MethodGet, nil, nil, &p, "profile")
	if err != nil {
		add(connector.Check{ID: "auth", Title: "auth", Status: connector.CheckFail, Detail: "Google refused the sign-in or the profile read", Fix: "Run " + authorize + " again."})
		return finish(4, "auth failed")
	}
	add(connector.Check{ID: "auth", Title: "auth", Status: connector.CheckPass, Detail: "an access token was minted"})
	email := strings.ToLower(p.EmailAddress)
	if email != st.cfg.Mailbox {
		add(connector.Check{ID: "profile", Title: "profile", Status: connector.CheckFail,
			Detail: fmt.Sprintf("the token belongs to %s, not the mailbox %s", email, st.cfg.Mailbox), Fix: "Authorize as " + st.cfg.Mailbox + "."})
	} else {
		add(connector.Check{ID: "profile", Title: "profile", Status: connector.CheckPass, Detail: email})
	}
	sc := connector.Check{ID: "scopes", Title: "scopes", Status: connector.CheckPass, Detail: "gmail.send gmail.readonly"}
	for _, x := range st.cfg.Scopes {
		if x != ScopeSend && x != ScopeReadonly && x != "gmail.send" && x != "gmail.readonly" {
			sc.Status, sc.Detail, sc.Fix = connector.CheckWarn, "the grant is wider than gmail.send gmail.readonly ("+x+")",
				"Authorize again; Deedglass asks for exactly gmail.send and gmail.readonly."
			break
		}
	}
	if len(st.cfg.Scopes) > 0 && !slices.Contains(st.cfg.Scopes, ScopeSend) && !slices.Contains(st.cfg.Scopes, "gmail.send") {
		sc.Status, sc.Detail, sc.Fix = connector.CheckFail, "the grant has no gmail.send", "Authorize again."
	}
	add(sc)
	fg := connector.Check{ID: "from_guard", Title: "from guard", Status: connector.CheckPass,
		Detail: "From is always " + st.cfg.Mailbox + "; refused domains: " + strings.Join(st.cfg.denied(), ", ")}
	if DeniedDomain(st.cfg.Mailbox, st.cfg.denied()) {
		fg.Status, fg.Detail = connector.CheckFail, MsgFromDenied
	}
	add(fg)
	add(orgGate)
	rep.Settle()
	return rep, nil
}
