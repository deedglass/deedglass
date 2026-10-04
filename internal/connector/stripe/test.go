package stripe

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/deedglass/deedglass/internal/connector"
	"github.com/deedglass/deedglass/internal/connector/saas"
)

// TestCheckIDs are the checks of Test, in order.
var TestCheckIDs = []string{"config", "credentials", "destination", "auth", "version", "permissions", "writes"}

// MsgWritesNote is the writes line: Test never creates anything.
const MsgWritesNote = "not probed · Deedglass never creates objects to test a connection"

// Test implements connector.Connector (S30 §3.1): config, credentials, destination, auth (a
// sandbox answer, livemode false; true trips the breaker), version, permissions (read probes) and
// writes (never probed). It changes nothing in Stripe.
func (c *Connector) Test(ctx context.Context, inst *connector.Instance) (connector.TestReport, error) {
	start := time.Now()
	rep := connector.TestReport{}
	add := func(ch connector.Check) { rep.Checks = append(rep.Checks, ch) }
	defer func() { rep.Duration = time.Since(start) }()
	finish := func(from int, why string) (connector.TestReport, error) {
		for _, id := range TestCheckIDs[from:] {
			if id == "writes" {
				add(connector.Check{ID: id, Title: id, Status: connector.CheckSkip, Detail: MsgWritesNote})
				continue
			}
			add(connector.Check{ID: id, Title: id, Status: connector.CheckSkip, Detail: why})
		}
		rep.Settle()
		return rep, nil
	}

	st, err := parse(inst)
	if err != nil {
		add(connector.Check{ID: "config", Title: "config", Status: connector.CheckFail, Detail: "the stored config can't be read", Fix: "Save the config again: deedglass connector create --config examples/connectors/stripe.yaml."})
		return finish(1, "the config can't be read")
	}
	curs := make([]string, 0, len(st.cfg.RefundLimits))
	for k := range st.cfg.RefundLimits {
		curs = append(curs, k)
	}
	sort.Strings(curs)
	add(connector.Check{ID: "config", Title: "config", Status: connector.CheckPass, Detail: "valid · refund limits " + strings.Join(curs, ", ")})

	sec, err := inst.Secret(ctx)
	if err != nil {
		ch := connector.Check{ID: "credentials", Title: "credentials", Status: connector.CheckFail, Detail: "no credentials yet",
			Fix: fmt.Sprintf("Store a restricted sandbox key: deedglass connector credentials set %s --from-env STRIPE_TEST_KEY. Create one at %s.", inst.Name, KeysURL)}
		if ce := connector.AsError(err); ce != nil && ce.Kind != connector.KindCredentialsMissing {
			ch.Detail = "the credentials can't be opened"
			ch.DocURL = "https://docs.deedglass.com/errors/vault_unavailable"
		}
		add(ch)
		return finish(2, "no usable credentials")
	}
	key := sec.Field(FieldToken)
	code, msg := CheckKey(key)
	hint := Hint(key)
	if code != "" {
		sec.Destroy()
		add(connector.Check{ID: "credentials", Title: "credentials", Status: connector.CheckFail, Detail: msg + " (" + Prefix(key) + ")",
			Fix: "Store a restricted key that starts with rk_test_: " + KeysURL})
		// open() trips the breaker for a stored live key; Test reports it the same way.
		if _, oerr := open(ctx, inst); oerr == nil {
			sec.Destroy()
		}
		return finish(2, "the key isn't a sandbox key")
	}
	isRestricted := restricted(key)
	sec.Destroy()
	if isRestricted {
		add(connector.Check{ID: "credentials", Title: "credentials", Status: connector.CheckPass, Detail: "restricted key · " + hint})
	} else {
		add(connector.Check{ID: "credentials", Title: "credentials", Status: connector.CheckWarn, Detail: "secret key · " + hint + " · " + MsgSecretKeyWarn,
			Fix: "Create a restricted key at " + KeysURL + ", then store it with deedglass connector credentials set " + inst.Name + " --from-env STRIPE_TEST_KEY."})
	}

	dc, ok := saas.DestinationCheck(ctx, inst, st.cfg.APIBaseURL)
	add(dc)
	if !ok {
		return finish(3, "the destination is refused")
	}

	s, err := open(ctx, inst)
	if err != nil {
		add(failCheck("auth", err, inst))
		return finish(4, "the key can't be used")
	}
	defer s.close()
	var bal struct {
		Livemode bool `json:"livemode"`
	}
	resp, err := s.get(ctx, saas.MustSegments("v1", "balance"), nil, &bal)
	if err != nil {
		add(failCheck("auth", err, inst))
		return finish(4, "Stripe didn't accept the key")
	}
	add(connector.Check{ID: "auth", Title: "auth", Status: connector.CheckPass, Detail: "sandbox · livemode false"})

	got := resp.Header.Get("Stripe-Version")
	switch {
	case got == st.cfg.APIVersion:
		add(connector.Check{ID: "version", Title: "version", Status: connector.CheckPass, Detail: got})
	case got == "":
		add(connector.Check{ID: "version", Title: "version", Status: connector.CheckWarn, Detail: "Stripe didn't name the version it answered with · pinned " + st.cfg.APIVersion})
	default:
		add(connector.Check{ID: "version", Title: "version", Status: connector.CheckFail, Detail: fmt.Sprintf("Stripe answered with %s · pinned %s", clipMsg(got), st.cfg.APIVersion),
			Fix: "Set api_version in the connector config to a version your Stripe account accepts."})
	}

	add(s.permissions(ctx))
	add(connector.Check{ID: "writes", Title: "writes", Status: connector.CheckSkip, Detail: MsgWritesNote})
	rep.Settle()
	return rep, nil
}

func (s *session) permissions(ctx context.Context) connector.Check {
	probes := []struct{ name, path string }{{"charges", "charges"}, {"customers", "customers"}, {"subscriptions", "subscriptions"}}
	var ok, denied []string
	var notes []string
	for _, p := range probes {
		_, err := s.get(ctx, saas.MustSegments("v1", p.path), url.Values{"limit": {"1"}}, nil)
		if err == nil {
			ok = append(ok, p.name)
			continue
		}
		ce := connector.AsError(err)
		if ce != nil && ce.HTTPStatus == http.StatusForbidden {
			denied = append(denied, p.name)
			if se := stripeErr(err); se != nil && se.Message != "" {
				notes = append(notes, clipMsg(se.Message))
			}
			continue
		}
		return failCheck("permissions", err, s.inst)
	}
	if len(denied) > 0 {
		d := strings.Join(denied, ", ") + " not readable"
		if len(notes) > 0 {
			d += " · Stripe says: " + notes[0]
		}
		return connector.Check{ID: "permissions", Title: "permissions", Status: connector.CheckWarn, Detail: d,
			Fix: "Give the restricted key Read on " + strings.Join(denied, ", ") + " at " + KeysURL + "."}
	}
	return connector.Check{ID: "permissions", Title: "permissions", Status: connector.CheckPass, Detail: strings.Join(ok, ", ") + " readable"}
}

func failCheck(id string, err error, inst *connector.Instance) connector.Check {
	ch := connector.Check{ID: id, Title: id, Status: connector.CheckFail, Detail: "Stripe didn't answer"}
	ce := connector.AsError(err)
	if ce == nil {
		return ch
	}
	switch {
	case ce.Code == connector.CodeStripeLivemodeDetected:
		ch.Detail = "livemode true · the breaker disabled the connector"
		ch.Fix = "Replace the key with a sandbox key, then enable the connector again."
	case ce.Kind == connector.KindAuth:
		ch.Detail = "Stripe refused the key (" + fmt.Sprint(ce.HTTPStatus) + ")"
		ch.Fix = fmt.Sprintf("Store a working sandbox key: deedglass connector credentials set %s --from-env STRIPE_TEST_KEY.", inst.Name)
	case ce.Kind == connector.KindRateLimited:
		ch.Detail, ch.Fix = "Stripe is limiting requests", "Wait a minute, then run this test again."
	case ce.AdminDetail != "":
		ch.Detail = ce.AdminDetail
	default:
		ch.Detail = ce.AgentMessage
	}
	return ch
}
