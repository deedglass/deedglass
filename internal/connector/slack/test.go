package slack

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/deedglass/deedglass/internal/connector"
)

// Scopes are the bot scopes of the app manifest (S29 §3.6).
var Scopes = []string{"chat:write", "channels:read", "groups:read", "channels:history", "groups:history",
	"reactions:read", "reactions:write", "users:read", "users:read.email"}

// Test implements connector.Connector: auth, app, channels, approvals and scopes (S29 §5). It
// changes nothing in Slack.
func (c *Connector) Test(ctx context.Context, inst *connector.Instance) (connector.TestReport, error) {
	start := time.Now()
	rep := connector.TestReport{}
	add := func(ch connector.Check) { rep.Checks = append(rep.Checks, ch) }
	skipRest := func(why string, from int) {
		ids := []string{"auth", "app", "channels", "approvals", "scopes"}
		for _, id := range ids[from:] {
			add(connector.Check{ID: id, Title: id, Status: connector.CheckSkip, Detail: why})
		}
	}
	defer func() { rep.Duration = time.Since(start) }()

	s, err := open(ctx, inst)
	if err != nil {
		add(credentialCheck(inst, err))
		skipRest("no usable credentials", 1)
		rep.Settle()
		return rep, nil
	}
	defer s.close()
	cfg := s.st.cfg

	// auth
	me, err := s.whoami(ctx, true)
	switch {
	case err != nil:
		add(failCheck("auth", "auth.test", err, inst))
		skipRest("Slack didn't accept the bot token", 1)
		rep.Settle()
		return rep, nil
	case me.TeamID != cfg.TeamID:
		add(connector.Check{ID: "auth", Title: "auth", Status: connector.CheckFail,
			Detail: fmt.Sprintf("workspace %s (%s) · the config says %s", me.Team, me.TeamID, cfg.TeamID),
			Fix:    fmt.Sprintf("Set team_id to %s in the connector config, or store the bot token of workspace %s.", me.TeamID, cfg.TeamID)})
		skipRest("the token belongs to another workspace", 1)
		rep.Settle()
		return rep, nil
	}
	bot := "@" + me.UserName
	add(connector.Check{ID: "auth", Title: "auth", Status: connector.CheckPass, Detail: fmt.Sprintf("workspace %s (%s) · bot %s", me.Team, me.TeamID, bot)})

	add(s.appCheck(ctx, me))
	add(s.channelsCheck(ctx, bot))
	add(s.approvalsCheck(ctx, bot))
	add(scopesCheck(me))
	rep.Settle()
	return rep, nil
}

func credentialCheck(inst *connector.Instance, err error) connector.Check {
	ce := connector.AsError(err)
	ch := connector.Check{ID: "auth", Title: "auth", Status: connector.CheckFail}
	switch {
	case ce != nil && ce.Kind == connector.KindCredentialsMissing:
		ch.Detail, ch.Fix = "no credentials yet", "Store them: "+setCredentialsFix(inst.Name)+"."
	case ce != nil && ce.Kind == connector.KindConfig && ce.AdminDetail != "":
		d := ce.AdminDetail
		if i := strings.Index(d, " Store it again"); i > 0 {
			d = d[:i]
		}
		ch.Detail, ch.Fix = d, "Store the Bot User OAuth Token (xoxb-): "+setCredentialsFix(inst.Name)+"."
	case ce != nil && ce.AdminDetail != "":
		ch.Detail, ch.DocURL = ce.AdminDetail, "https://docs.deedglass.com/errors/vault_unavailable"
	default:
		ch.Detail = "the credentials can't be opened"
	}
	return ch
}

// failCheck is a check whose Slack call failed.
func failCheck(id, method string, err error, inst *connector.Instance) connector.Check {
	ce := connector.AsError(err)
	ch := connector.Check{ID: id, Title: id, Status: connector.CheckFail, Detail: method + " failed"}
	if ce == nil {
		return ch
	}
	code := slackCode(err)
	switch {
	case ce.Kind == connector.KindAuth:
		ch.Detail = fmt.Sprintf("%s · Slack refused the bot token (%s)", method, code)
		ch.Fix = "Store a working bot token: " + setCredentialsFix(inst.Name) + "."
	case ce.Kind == connector.KindRateLimited:
		ch.Detail, ch.Fix = method+" · Slack is limiting requests", "Wait a minute, then run this test again."
	case code != "":
		ch.Detail = fmt.Sprintf("%s · Slack answered %s", method, code)
	default:
		ch.Detail = method + " · " + ce.AgentMessage
		ch.Fix = "Check the server's network access to Slack, then run this test again."
	}
	return ch
}

func (s *session) appCheck(ctx context.Context, me *selfInfo) connector.Check {
	want := s.st.cfg.AppID
	if me.BotID == "" {
		return connector.Check{ID: "app", Title: "app", Status: connector.CheckFail, Detail: "auth.test names no bot · the token isn't a bot token",
			Fix: "Store the Bot User OAuth Token (xoxb-): " + setCredentialsFix(s.inst.Name) + "."}
	}
	var b struct {
		Bot struct {
			AppID string `json:"app_id"`
		} `json:"bot"`
	}
	if _, err := s.get(ctx, "bots.info", url.Values{"bot": {me.BotID}}, true, &b); err != nil {
		return failCheck("app", "bots.info", err, s.inst)
	}
	if b.Bot.AppID != want {
		return connector.Check{ID: "app", Title: "app", Status: connector.CheckFail,
			Detail: fmt.Sprintf("the token belongs to app %s · the config says %s", b.Bot.AppID, want),
			Fix:    fmt.Sprintf("Set app_id to %s in the connector config, or store the bot token of app %s.", b.Bot.AppID, want)}
	}
	return connector.Check{ID: "app", Title: "app", Status: connector.CheckPass, Detail: want + " matches"}
}

// membership says how one configured channel resolves: its display name, whether it exists, and
// whether the bot is in it.
func (s *session) membership(ctx context.Context, ref string) (id, name string, found, member bool, err error) {
	name = ref
	cl, err := s.channels(ctx, false)
	if err != nil {
		return "", name, false, false, err
	}
	id, rerr := s.resolve(ctx, ref)
	if errors.Is(rerr, errChannelUnknown) {
		return "", name, false, false, nil
	}
	if rerr != nil {
		return "", name, false, false, rerr
	}
	if ch, ok := cl.byID[id]; ok {
		if reChannelID.MatchString(ref) && ch.Name != "" {
			name = "#" + ch.Name
		}
		return id, name, true, ch.IsMember, nil
	}
	ch, ierr := s.info(ctx, id)
	if ierr != nil {
		if ce := connector.AsError(ierr); ce != nil && ce.Kind == connector.KindNotFound {
			return id, name, false, false, nil
		}
		return id, name, false, false, ierr
	}
	if reChannelID.MatchString(ref) && ch.Name != "" {
		name = "#" + ch.Name
	}
	return id, name, true, ch.IsMember, nil
}

func (s *session) channelsCheck(ctx context.Context, bot string) connector.Check {
	cfg := s.st.cfg
	var refs []string
	star := false
	for _, r := range append(append([]string{}, cfg.Channels...), cfg.ReadChannels...) {
		if r == "*" {
			star = true
			continue
		}
		if !slices.Contains(refs, r) {
			refs = append(refs, r)
		}
	}
	ch := connector.Check{ID: "channels", Title: "channels", Status: connector.CheckPass}
	var parts []string
	if star {
		cl, err := s.channels(ctx, false)
		if err != nil {
			return failCheck("channels", "conversations.list", err, s.inst)
		}
		n := 0
		for _, c := range cl.byID {
			if c.IsMember {
				n++
			}
		}
		parts = append(parts, fmt.Sprintf("any channel the bot is in · %d now", n))
	}
	if len(refs) == 0 && !star {
		return connector.Check{ID: "channels", Title: "channels", Status: connector.CheckWarn, Detail: "no channels in the config · posts and reads are refused",
			Fix: `Add channels: ["#agent-updates"] to the connector config.`}
	}
	seen := map[string]bool{}
	for _, r := range refs {
		id, name, found, member, err := s.membership(ctx, r)
		if err != nil {
			return failCheck("channels", "conversations.list", err, s.inst)
		}
		if id != "" && seen[id] {
			continue
		}
		seen[id] = true
		switch {
		case !found:
			parts = append(parts, name+" · not found")
			ch.Status = connector.CheckFail
			if !strings.HasPrefix(ch.Fix, "Check") { // a missing channel's fix comes first
				ch.Fix = fmt.Sprintf("Check the name %s, or invite %s to it if it is private, then run this test again.", name, bot)
			}
		case !member:
			parts = append(parts, name+" · the bot isn't a member")
			if ch.Status == connector.CheckPass {
				ch.Status = connector.CheckWarn
			}
			if ch.Fix == "" {
				ch.Fix = fmt.Sprintf("Invite %s to %s, then run this test again.", bot, name)
			}
		default:
			parts = append(parts, name+" · member")
		}
	}
	ch.Detail = strings.Join(parts, ", ")
	return ch
}

func (s *session) approvalsCheck(ctx context.Context, bot string) connector.Check {
	a := s.st.cfg.Approvals
	if !a.Enabled {
		return connector.Check{ID: "approvals", Title: "approvals", Status: connector.CheckSkip, Detail: "approvals in Slack are off"}
	}
	if why := checkSigningSecret(s.sec.Field(FieldSigningSecret)); why != "" {
		return connector.Check{ID: "approvals", Title: "approvals", Status: connector.CheckFail, Detail: "signing secret · " + strings.TrimSuffix(why, "."),
			Fix: "Store it: " + setCredentialsFix(s.inst.Name) + "."}
	}
	_, name, found, member, err := s.membership(ctx, a.Channel)
	switch {
	case err != nil:
		return failCheck("approvals", "conversations.list", err, s.inst)
	case !found:
		return connector.Check{ID: "approvals", Title: "approvals", Status: connector.CheckFail, Detail: name + " · not found",
			Fix: fmt.Sprintf("Create %s, invite %s to it, then run this test again.", name, bot)}
	case !member:
		return connector.Check{ID: "approvals", Title: "approvals", Status: connector.CheckWarn, Detail: name + " · the bot isn't a member",
			Fix: fmt.Sprintf("Invite %s to %s, then run this test again.", bot, name)}
	}
	return connector.Check{ID: "approvals", Title: "approvals", Status: connector.CheckPass, Detail: name + " · member"}
}

func scopesCheck(me *selfInfo) connector.Check {
	if len(me.Scopes) == 0 {
		return connector.Check{ID: "scopes", Title: "scopes", Status: connector.CheckWarn, Detail: "Slack didn't report the token's scopes",
			Fix: "Check the app's OAuth & Permissions page against the manifest: deedglass slack manifest."}
	}
	var missing []string
	for _, sc := range Scopes {
		if !slices.Contains(me.Scopes, sc) {
			missing = append(missing, sc)
		}
	}
	got := len(Scopes) - len(missing)
	if len(missing) > 0 {
		return connector.Check{ID: "scopes", Title: "scopes", Status: connector.CheckFail,
			Detail: fmt.Sprintf("%d of %d granted · missing %s", got, len(Scopes), strings.Join(missing, ", ")),
			Fix:    "Add the missing scopes in the app's OAuth & Permissions page, reinstall the app, then run this test again."}
	}
	return connector.Check{ID: "scopes", Title: "scopes", Status: connector.CheckPass, Detail: fmt.Sprintf("%d of %d granted", got, len(Scopes))}
}
