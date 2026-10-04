package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/deedglass/deedglass/internal/connector"
	"github.com/deedglass/deedglass/internal/connector/egress"
	"github.com/deedglass/deedglass/internal/connector/saas"
	"github.com/deedglass/deedglass/internal/vault"
)

// MaxProbedRepos is how many configured repositories the test reads.
const MaxProbedRepos = 10

const destinationDoc = "https://docs.deedglass.com/errors/destination_blocked"

// Test implements connector.Connector: config · credentials · destination · auth · token · repos ·
// rate (S29 §3.2, §5). It changes nothing on GitHub (an App's installation token is minted, as any
// call would).
func (c *Connector) Test(ctx context.Context, inst *connector.Instance) (connector.TestReport, error) {
	start := time.Now()
	rep := connector.TestReport{}
	add := func(ch connector.Check) { rep.Checks = append(rep.Checks, ch) }
	skip := func(id, why string) {
		add(connector.Check{ID: id, Title: id, Status: connector.CheckSkip, Detail: why})
	}
	defer func() { rep.Duration = time.Since(start) }()
	ctx = vault.WithRedactor(ctx, vault.NewRedactor())

	st, err := c.state(inst)
	if err != nil {
		add(connector.Check{ID: "config", Title: "config", Status: connector.CheckFail, Detail: "the stored config can't be read",
			Fix: "Update the connector with a config that passes deedglass connector create's checks."})
		for _, id := range []string{"credentials", "destination", "auth", "token", "repos", "rate"} {
			skip(id, "the config can't be read")
		}
		rep.Settle()
		return rep, nil
	}
	cfgDetail := fmt.Sprintf("valid · %d %s · protected: %s", len(st.cfg.Repos), plural(len(st.cfg.Repos), "repo", "repos"), strings.Join(st.cfg.ProtectedBranches, ", "))
	if st.cfg.Auth == "app" {
		cfgDetail += " · GitHub App"
	}
	add(connector.Check{ID: "config", Title: "config", Status: connector.CheckPass, Detail: cfgDetail})

	// credentials
	credOK := false
	sec, err := inst.Secret(ctx)
	switch ce := connector.AsError(err); {
	case err == nil:
		kind, _, cerr := checkCredential(st.cfg, inst.Name, sec)
		hint := ""
		if cerr == nil && kind != tokenApp {
			hint = vault.Hint(vault.KindBearer, map[string][]byte{"token": []byte(sec.Field("token"))})
		}
		sec.Destroy()
		if cerr != nil {
			add(connector.Check{ID: "credentials", Title: "credentials", Status: connector.CheckFail, Detail: "set · can't be used", Fix: cerr.AdminDetail})
			break
		}
		d := "set · " + kind
		if hint != "" {
			d += " · " + hint
		}
		credOK = true
		add(connector.Check{ID: "credentials", Title: "credentials", Status: connector.CheckPass, Detail: d})
	case ce != nil && ce.Kind == connector.KindCredentialsMissing:
		fix := fmt.Sprintf("deedglass connector credentials set %s --from-env %s", inst.Name, connector.EnvVarFor(inst.Name))
		if st.cfg.Auth == "app" {
			fix = fmt.Sprintf("deedglass connector credentials set %s --field private_key=file:./app.pem", inst.Name)
		}
		add(connector.Check{ID: "credentials", Title: "credentials", Status: connector.CheckFail, Detail: "not set", Fix: fix})
	default:
		detail := "can't be opened"
		if ce != nil && ce.AdminDetail != "" {
			detail = ce.AdminDetail
		}
		add(connector.Check{ID: "credentials", Title: "credentials", Status: connector.CheckFail, Detail: detail,
			DocURL: "https://docs.deedglass.com/errors/vault_unavailable"})
	}

	// destination
	destOK := c.destination(ctx, inst, st, add)

	if !credOK || !destOK {
		why := "no usable credentials"
		if !destOK {
			why = "the destination isn't reachable"
		}
		for _, id := range []string{"auth", "token", "repos", "rate"} {
			skip(id, why)
		}
		rep.Settle()
		return rep, nil
	}

	// auth
	s, err := c.open(ctx, inst, st)
	if err != nil {
		add(connector.Check{ID: "auth", Title: "auth", Status: connector.CheckFail, Detail: checkText(err), Fix: authFix(inst, st)})
		for _, id := range []string{"token", "repos", "rate"} {
			skip(id, "auth failed")
		}
		rep.Settle()
		return rep, nil
	}
	defer s.close()
	login, err := s.fetchLogin(ctx)
	if err == nil && st.cfg.Auth == "app" {
		err = s.installation(ctx)
	}
	if err != nil {
		add(connector.Check{ID: "auth", Title: "auth", Status: connector.CheckFail, Detail: checkText(err), Fix: authFix(inst, st)})
		for _, id := range []string{"token", "repos", "rate"} {
			skip(id, "auth failed")
		}
		rep.Settle()
		return rep, nil
	}
	authDetail := "signed in as " + login
	if st.cfg.Auth == "app" {
		authDetail += fmt.Sprintf(" · installation %d · %d %s", st.cfg.App.InstallationID, s.installed, plural(s.installed, "repo", "repos"))
	}
	add(connector.Check{ID: "auth", Title: "auth", Status: connector.CheckPass, Detail: authDetail})

	add(c.tokenCheck(s))
	add(c.reposCheck(ctx, s))
	add(c.rateCheck(ctx, s))
	rep.Settle()
	return rep, nil
}

func (c *Connector) destination(ctx context.Context, inst *connector.Instance, st *state, add func(connector.Check)) bool {
	pol := inst.Env.Egress
	if pol == nil {
		pol = &egress.Policy{Env: egress.EnvProd}
	}
	chk, err := pol.CheckURL(ctx, st.cfg.APIBaseURL)
	var re *egress.ResolveError
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
		return false
	case errors.As(err, &re):
		add(connector.Check{ID: "destination", Title: "destination", Status: connector.CheckFail, Detail: re.Error()})
		return false
	case err != nil:
		add(connector.Check{ID: "destination", Title: "destination", Status: connector.CheckFail, Detail: err.Error()})
		return false
	}
	d := chk.Host
	if len(chk.Addrs) > 0 && chk.Addrs[0].Addr().String() != chk.Host {
		d += " → " + chk.Addrs[0].Addr().String()
	}
	add(connector.Check{ID: "destination", Title: "destination", Status: connector.CheckPass, Detail: d + " · " + chk.Class.Label()})
	return true
}

func authFix(inst *connector.Instance, st *state) string {
	if st.cfg.Auth == "app" {
		return "Check the App's client_id and installation_id, and that the App is installed on the repositories, then store its private key again if needed."
	}
	return fmt.Sprintf("Create a fine-grained token for the repositories, then run deedglass connector credentials set %s --from-env %s.", inst.Name, connector.EnvVarFor(inst.Name))
}

// checkText is a failed check's detail: the admin detail when there is one.
func checkText(err error) string {
	if ce := connector.AsError(err); ce != nil {
		if ce.AdminDetail != "" {
			return ce.AdminDetail
		}
		return ce.AgentMessage
	}
	return "failed"
}

// installation lists the repositories the App's installation token reaches (the auth check).
func (s *session) installation(ctx context.Context) error {
	resp, err := s.do(ctx, saas.Request{Method: http.MethodGet, Path: saas.MustSegments("installation", "repositories"), Query: map[string][]string{"per_page": {"100"}}, Idempotent: true})
	if err != nil {
		return err
	}
	var r struct {
		TotalCount int `json:"total_count"`
	}
	if err := decode(resp, &r); err != nil {
		return err
	}
	s.installed = r.TotalCount
	return nil
}

// tokenCheck: a classic token warns (it reaches every repository the user can see); an expiry
// within 7 days warns; a past one fails.
func (c *Connector) tokenCheck(s *session) connector.Check {
	ch := connector.Check{ID: "token", Title: "token", Status: connector.CheckPass}
	now := c.now()
	if s.kind == tokenApp {
		ch.Detail = "installation token · downscoped to the configured repos"
		if !s.tokenExp.IsZero() {
			ch.Detail = fmt.Sprintf("installation token · renews before %s UTC · downscoped to the configured repos", s.tokenExp.UTC().Format("15:04"))
		}
		return ch
	}
	var parts []string
	if s.kind == tokenClassic {
		ch.Status = connector.CheckWarn
		parts = append(parts, "classic token: it reaches every repository this user can see")
		ch.Fix = "Create a fine-grained token limited to the configured repos (Contents, Issues and Pull requests: read and write), then store it."
	}
	var exp time.Time
	if s.userResp != nil {
		exp = parseExpiry(s.userResp.Header.Get("Github-Authentication-Token-Expiration"))
	}
	switch {
	case exp.IsZero():
		parts = append(parts, "no expiry date")
	case !exp.After(now):
		ch.Status = connector.CheckFail
		parts = append(parts, "expired "+exp.UTC().Format("2006-01-02"))
		ch.Fix = "Create a new token on GitHub, then store it with deedglass connector credentials set " + s.inst.Name + "."
	default:
		days := int(exp.Sub(now).Hours() / 24)
		parts = append(parts, fmt.Sprintf("expires %s · %d %s left", exp.UTC().Format("2006-01-02"), days, plural(days, "day", "days")))
		if exp.Sub(now) <= 7*24*time.Hour {
			if ch.Status == connector.CheckPass {
				ch.Status = connector.CheckWarn
			}
			if ch.Fix == "" {
				ch.Fix = "Create a new token on GitHub before it expires, then store it with deedglass connector credentials set " + s.inst.Name + "."
			}
		}
	}
	ch.Detail = strings.Join(parts, " · ")
	return ch
}

// parseExpiry reads GitHub's "2026-11-09 00:00:00 UTC" (and a few close variants).
func parseExpiry(h string) time.Time {
	h = strings.TrimSpace(h)
	for _, layout := range []string{"2006-01-02 15:04:05 MST", "2006-01-02 15:04:05 -0700", time.RFC3339, "2006-01-02"} {
		if t, err := time.Parse(layout, h); err == nil {
			return t
		}
	}
	return time.Time{}
}

// reposCheck reads up to 10 configured repositories: each must be visible, and push allowed.
func (c *Connector) reposCheck(ctx context.Context, s *session) connector.Check {
	ch := connector.Check{ID: "repos", Title: "repos", Status: connector.CheckPass}
	var parts []string
	probed := 0
	for _, r := range s.st.cfg.Repos {
		if probed == MaxProbedRepos {
			parts = append(parts, fmt.Sprintf("%d more not probed", len(s.st.cfg.Repos)-probed))
			break
		}
		o, n, _ := strings.Cut(r, "/")
		if n == "*" {
			parts = append(parts, r+" · every repository of "+o+" (not probed)")
			continue
		}
		probed++
		p, err := saas.Segments("repos", o, n)
		if err != nil {
			continue
		}
		resp, err := s.do(ctx, saas.Request{Method: http.MethodGet, Path: p, Idempotent: true})
		if err != nil {
			ch.Status = connector.CheckFail
			if ce := connector.AsError(err); ce != nil && ce.Kind == connector.KindNotFound {
				parts = append(parts, r+" · not visible to this token")
				ch.Fix = "Give the token access to the repository (GitHub answers 404 when a token can't see a private repository), or remove it from repos."
			} else {
				parts = append(parts, r+" · "+checkText(err))
			}
			continue
		}
		var repo ghRepo
		if json.Unmarshal(resp.Body, &repo) == nil {
			s.st.cacheDefault(r, repo.DefaultBranch, c.now())
		}
		if repo.Permissions != nil && !repo.Permissions.Push {
			if ch.Status == connector.CheckPass {
				ch.Status = connector.CheckWarn
			}
			parts = append(parts, r+" · read-only for this token; write tools will fail")
			if ch.Fix == "" {
				ch.Fix = "Give the token Contents, Issues and Pull requests: read and write on the repository."
			}
			continue
		}
		parts = append(parts, r+" · push allowed")
	}
	ch.Detail = strings.Join(parts, " · ")
	return ch
}

// rateCheck reads GET /rate_limit (it doesn't count against the limit).
func (c *Connector) rateCheck(ctx context.Context, s *session) connector.Check {
	ch := connector.Check{ID: "rate", Title: "rate"}
	resp, err := s.do(ctx, saas.Request{Method: http.MethodGet, Path: saas.MustSegments("rate_limit"), Idempotent: true})
	if err != nil {
		ch.Status, ch.Detail = connector.CheckWarn, checkText(err)
		return ch
	}
	var r struct {
		Resources struct {
			Core struct {
				Limit     int   `json:"limit"`
				Remaining int   `json:"remaining"`
				Reset     int64 `json:"reset"`
			} `json:"core"`
		} `json:"resources"`
	}
	if err := json.Unmarshal(resp.Body, &r); err != nil {
		ch.Status, ch.Detail = connector.CheckWarn, "GitHub's rate limit answer can't be read"
		return ch
	}
	core := r.Resources.Core
	ch.Status = connector.CheckPass
	ch.Detail = fmt.Sprintf("%s of %s left · resets %s UTC", thousands(core.Remaining), thousands(core.Limit), time.Unix(core.Reset, 0).UTC().Format("15:04"))
	if core.Limit > 0 && core.Remaining*10 < core.Limit {
		ch.Status = connector.CheckWarn
		ch.Fix = "Fewer than 10% of this hour's requests are left. Calls may wait until the reset."
	}
	return ch
}

// thousands is 4987 → "4,987".
func thousands(n int) string {
	s := strconv.Itoa(n)
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	var b strings.Builder
	for i, r := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(r)
	}
	if neg {
		return "-" + b.String()
	}
	return b.String()
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
