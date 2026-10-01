package rest

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/deedglass/deedglass/internal/connector"
	"github.com/deedglass/deedglass/internal/connector/egress"
	"github.com/deedglass/deedglass/internal/vault"
)

const destinationDoc = "https://docs.deedglass.com/errors/destination_blocked"

// Test implements connector.Connector: config, credentials, destination, tls and auth (S16 §3.5).
// It answers "will this work?" without changing anything: the auth check is the config's test
// request, which must be a GET or a HEAD.
func (c *Connector) Test(ctx context.Context, inst *connector.Instance) (connector.TestReport, error) {
	start := time.Now()
	rep := connector.TestReport{}
	add := func(ch connector.Check) { rep.Checks = append(rep.Checks, ch) }
	defer func() { rep.Duration = time.Since(start) }()

	st, err := parse(inst)
	if err != nil {
		add(connector.Check{ID: "config", Title: "config", Status: connector.CheckFail, Detail: "the stored config can't be read",
			Fix: "Update the connector with a config that passes deedglass connector create's checks."})
		rep.Settle()
		return rep, nil
	}
	add(connector.Check{ID: "config", Title: "config", Status: connector.CheckPass, Detail: fmt.Sprintf("valid · %d %s", len(st.specs), plural(len(st.specs), "tool", "tools"))})

	// credentials
	var sec *vault.Secret
	switch {
	case st.cfg.Auth.Kind == "none":
		add(connector.Check{ID: "credentials", Title: "credentials", Status: connector.CheckSkip, Detail: "auth: none"})
	default:
		s, err := inst.Secret(ctx)
		ce := connector.AsError(err)
		switch {
		case err == nil:
			sec = s
			defer sec.Destroy()
			kind := s.Kind()
			if kind != st.cfg.Auth.Kind {
				add(connector.Check{ID: "credentials", Title: "credentials", Status: connector.CheckFail,
					Detail: fmt.Sprintf("set · %s, but auth.kind is %s", kind, st.cfg.Auth.Kind),
					Fix:    fmt.Sprintf("Store a %s credential: deedglass connector credentials set %s --kind %s --from-env %s", st.cfg.Auth.Kind, inst.Name, st.cfg.Auth.Kind, connector.EnvVarFor(inst.Name))})
				sec = nil
			} else {
				d := "set · " + kind
				if h := vault.Hint(kind, secretFields(s)); h != "" {
					d += " · " + h
				}
				add(connector.Check{ID: "credentials", Title: "credentials", Status: connector.CheckPass, Detail: d})
			}
		case ce != nil && ce.Kind == connector.KindCredentialsMissing:
			add(connector.Check{ID: "credentials", Title: "credentials", Status: connector.CheckFail, Detail: "not set",
				Fix: fmt.Sprintf("deedglass connector credentials set %s --from-env %s", inst.Name, connector.EnvVarFor(inst.Name))})
		default:
			detail := "can't be opened"
			if ce != nil && ce.AdminDetail != "" {
				detail = ce.AdminDetail
			}
			add(connector.Check{ID: "credentials", Title: "credentials", Status: connector.CheckFail, Detail: detail,
				DocURL: "https://docs.deedglass.com/errors/vault_unavailable"})
		}
	}

	// destination
	pol := inst.Env.Egress
	if pol == nil {
		pol = &egress.Policy{Env: egress.EnvProd}
	}
	chk, err := pol.CheckURL(ctx, st.cfg.BaseURL)
	var re *egress.ResolveError
	destOK := false
	switch be, blocked := egress.IsBlocked(err); {
	case blocked:
		detail := be.Host
		if be.Addr != "" && be.Addr != be.Host {
			detail += " → " + be.Addr
		}
		if be.Class != "" {
			detail += " · " + be.Class.Label()
		}
		add(connector.Check{ID: "destination", Title: "destination", Status: connector.CheckFail, Detail: detail, Fix: be.Reason, DocURL: destinationDoc})
	case errors.As(err, &re):
		add(connector.Check{ID: "destination", Title: "destination", Status: connector.CheckFail, Detail: re.Error()})
	case err != nil:
		add(connector.Check{ID: "destination", Title: "destination", Status: connector.CheckFail, Detail: err.Error()})
	default:
		destOK = true
		addrs := make([]string, 0, len(chk.Addrs))
		for _, a := range chk.Addrs {
			addrs = append(addrs, a.Addr().String())
		}
		d := chk.Host
		if len(addrs) > 0 && addrs[0] != chk.Host {
			d += " → " + strings.Join(addrs[:min(2, len(addrs))], ", ")
		}
		add(connector.Check{ID: "destination", Title: "destination", Status: connector.CheckPass, Detail: d + " · " + chk.Class.Label()})
	}

	// tls
	switch {
	case !destOK:
		add(connector.Check{ID: "tls", Title: "tls", Status: connector.CheckSkip, Detail: "the destination isn't reachable"})
	case chk.URL.Scheme != "https":
		add(connector.Check{ID: "tls", Title: "tls", Status: connector.CheckSkip, Detail: "plain http (dev only; prod needs https)"})
	default:
		v, err := tlsVersion(ctx, pol, chk)
		if err != nil {
			add(connector.Check{ID: "tls", Title: "tls", Status: connector.CheckFail, Detail: lastPart(err), Fix: "Check the host name and its TLS setup."})
			destOK = false
		} else {
			add(connector.Check{ID: "tls", Title: "tls", Status: connector.CheckPass, Detail: v})
		}
	}

	// auth
	switch {
	case st.cfg.Test == nil:
		add(connector.Check{ID: "auth", Title: "auth", Status: connector.CheckSkip, Detail: "no test request in the config",
			Fix: "Add test: {method: GET, path: /v1/health} to check the credential."})
	case !destOK:
		add(connector.Check{ID: "auth", Title: "auth", Status: connector.CheckSkip, Detail: "the destination isn't reachable"})
	case st.cfg.Auth.Kind != "none" && sec == nil:
		add(connector.Check{ID: "auth", Title: "auth", Status: connector.CheckSkip, Detail: "no usable credentials"})
	default:
		add(st.authCheck(ctx, inst, sec))
	}
	rep.Settle()
	return rep, nil
}

func (st *state) authCheck(ctx context.Context, inst *connector.Instance, sec *vault.Secret) connector.Check {
	read, _ := st.clients(inst)
	full := *st.base
	full.Path = strings.TrimSuffix(st.base.Path, "/") + st.cfg.Test.Path
	b := &built{method: st.cfg.Test.Method, templ: st.cfg.Test.Path, path: st.cfg.Test.Path, host: st.base.Host, full: &full,
		query: map[string][]string{}, headers: http.Header{}}
	t0 := time.Now()
	res, _, err := st.attempt(vault.WithRedactor(ctx, vault.NewRedactor()), read, b, sec, connector.Call{}, 1)
	ms := time.Since(t0).Milliseconds()
	line := fmt.Sprintf("%s %s", b.method, b.templ)
	if err != nil {
		ce := connector.AsError(err)
		if ce != nil && ce.HTTPStatus != 0 {
			fix := ""
			if ce.Kind == connector.KindAuth {
				fix = fmt.Sprintf("Replace the credential: deedglass connector credentials set %s --from-env %s", inst.Name, connector.EnvVarFor(inst.Name))
			}
			return connector.Check{ID: "auth", Title: "auth", Status: connector.CheckFail, Detail: fmt.Sprintf("%s → %d · %s", line, ce.HTTPStatus, msText(ms)), Fix: fix}
		}
		detail := "failed"
		if ce != nil {
			detail = ce.AgentMessage
		}
		return connector.Check{ID: "auth", Title: "auth", Status: connector.CheckFail, Detail: line + " · " + detail}
	}
	status, _ := res.Meta["http_status"].(int)
	return connector.Check{ID: "auth", Title: "auth", Status: connector.CheckPass, Detail: fmt.Sprintf("%s → %d · %s", line, status, msText(ms))}
}

// tlsVersion opens a TLS connection through the egress dialer and reports its version.
func tlsVersion(ctx context.Context, pol *egress.Policy, chk *egress.Checked) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, egress.TLSHandshakeTimeout+egress.DialTimeout)
	defer cancel()
	conn, err := pol.DialContext(ctx, "tcp", net.JoinHostPort(chk.Host, strconv.Itoa(int(chk.Port))))
	if err != nil {
		return "", err
	}
	defer conn.Close()
	tc := tls.Client(conn, &tls.Config{MinVersion: tls.VersionTLS12, ServerName: chk.Host})
	if err := tc.HandshakeContext(ctx); err != nil {
		return "", err
	}
	switch tc.ConnectionState().Version {
	case tls.VersionTLS13:
		return "TLS 1.3", nil
	case tls.VersionTLS12:
		return "TLS 1.2", nil
	}
	return "TLS", nil
}

// msText is "41 ms", or "< 1 ms" for a call faster than a millisecond.
func msText(ms int64) string {
	if ms < 1 {
		return "< 1 ms"
	}
	return fmt.Sprintf("%d ms", ms)
}

func lastPart(err error) string {
	s := err.Error()
	if i := strings.LastIndex(s, ": "); i >= 0 {
		s = s[i+2:]
	}
	return s
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

func secretFields(s *vault.Secret) map[string][]byte {
	out := map[string][]byte{}
	for _, n := range s.Names() {
		out[n] = []byte(s.Field(n))
	}
	return out
}
