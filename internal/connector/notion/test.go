package notion

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/deedglass/deedglass/internal/connector"
	"github.com/deedglass/deedglass/internal/connector/saas"
)

// TestCheckIDs are the checks of Test, in order.
var TestCheckIDs = []string{"config", "credentials", "destination", "auth", "parents", "writes"}

// MsgWritesNote is the writes line: Test never creates anything.
const MsgWritesNote = "not probed · Deedglass never creates pages to test a connection"

// Test implements connector.Connector: config, credentials, destination, auth (GET /v1/users/me),
// parents (each parent page readable and not in the trash) and writes (never probed). It changes
// nothing in Notion.
func (c *Connector) Test(ctx context.Context, inst *connector.Instance) (connector.TestReport, error) {
	start := time.Now()
	rep := connector.TestReport{}
	add := func(ch connector.Check) { rep.Checks = append(rep.Checks, ch) }
	defer func() { rep.Duration = time.Since(start) }()
	finish := func(from int, why string) (connector.TestReport, error) {
		for _, id := range TestCheckIDs[from:] {
			d := why
			if id == "writes" {
				d = MsgWritesNote
			}
			add(connector.Check{ID: id, Title: id, Status: connector.CheckSkip, Detail: d})
		}
		rep.Settle()
		return rep, nil
	}
	st, err := parse(inst)
	if err != nil {
		add(connector.Check{ID: "config", Title: "config", Status: connector.CheckFail, Detail: "the stored config can't be read",
			Fix: "Save the config again: deedglass connector create --config examples/connectors/notion.yaml."})
		return finish(1, "the config can't be read")
	}
	add(connector.Check{ID: "config", Title: "config", Status: connector.CheckPass,
		Detail: fmt.Sprintf("valid · %d parent pages · max_blocks %d", len(st.cfg.Parents), st.cfg.MaxBlocks)})

	sec, err := inst.Secret(ctx)
	if err != nil {
		ch := connector.Check{ID: "credentials", Title: "credentials", Status: connector.CheckFail, Detail: "no credentials yet",
			Fix: fmt.Sprintf("Store an internal integration token: deedglass connector credentials set %s --from-env NOTION_TOKEN. Create one at %s.", inst.Name, IntegrationsURL)}
		if ce := connector.AsError(err); ce != nil && ce.Kind != connector.KindCredentialsMissing {
			ch.Detail = "the credentials can't be opened"
			ch.DocURL = "https://docs.deedglass.com/errors/vault_unavailable"
		}
		add(ch)
		return finish(2, "no usable credentials")
	}
	tok := sec.Field(FieldToken)
	sec.Destroy()
	kind := "token"
	switch {
	case strings.HasPrefix(tok, "ntn_"):
		kind = "internal integration token · ntn_…"
	case strings.HasPrefix(tok, "secret_"):
		kind = "legacy integration token · secret_…"
	}
	add(connector.Check{ID: "credentials", Title: "credentials", Status: connector.CheckPass, Detail: kind})

	dc, ok := saas.DestinationCheck(ctx, inst, st.cfg.APIBaseURL)
	add(dc)
	if !ok {
		return finish(3, "the destination is refused")
	}
	s, err := c.open(ctx, inst)
	if err != nil {
		add(failCheck("auth", err, inst))
		return finish(4, "the token can't be used")
	}
	defer s.close()
	var me struct {
		Type string `json:"type"`
		Name string `json:"name"`
		Bot  *struct {
			WorkspaceName string `json:"workspace_name"`
		} `json:"bot"`
	}
	if _, err := s.get(ctx, saas.MustSegments("v1", "users", "me"), &me); err != nil {
		add(failCheck("auth", err, inst))
		return finish(4, "Notion didn't accept the token")
	}
	d := "bot"
	if me.Name != "" {
		d += " " + strconv.Quote(clip(me.Name, 60))
	}
	if me.Bot != nil && me.Bot.WorkspaceName != "" {
		d += " · workspace " + strconv.Quote(clip(me.Bot.WorkspaceName, 60))
	}
	add(connector.Check{ID: "auth", Title: "auth", Status: connector.CheckPass, Detail: d})
	add(s.parentsCheck(ctx, inst))
	add(connector.Check{ID: "writes", Title: "writes", Status: connector.CheckSkip, Detail: MsgWritesNote})
	rep.Settle()
	return rep, nil
}

func (s *session) parentsCheck(ctx context.Context, inst *connector.Instance) connector.Check {
	var missing, trashed []string
	for _, id := range s.st.cfg.Parents {
		var p page
		_, err := s.get(ctx, saas.MustSegments("v1", "pages", id), &p)
		if err != nil {
			if ce := connector.AsError(err); ce != nil && ce.Kind == connector.KindNotFound {
				missing = append(missing, id)
				continue
			}
			return failCheck("parents", err, inst)
		}
		if p.trashed() {
			trashed = append(trashed, id)
		}
	}
	switch {
	case len(missing) > 0:
		return connector.Check{ID: "parents", Title: "parents", Status: connector.CheckFail,
			Detail: fmt.Sprintf("%d of %d not visible to the integration: %s", len(missing), len(s.st.cfg.Parents), strings.Join(missing, ", ")),
			Fix:    "Open each page in Notion › ••• › Connections, and add the integration."}
	case len(trashed) > 0:
		return connector.Check{ID: "parents", Title: "parents", Status: connector.CheckWarn,
			Detail: "in the trash: " + strings.Join(trashed, ", "), Fix: "Restore the page in Notion, or remove it from parents."}
	}
	return connector.Check{ID: "parents", Title: "parents", Status: connector.CheckPass, Detail: fmt.Sprintf("%d of %d readable", len(s.st.cfg.Parents), len(s.st.cfg.Parents))}
}

func failCheck(id string, err error, inst *connector.Instance) connector.Check {
	ch := connector.Check{ID: id, Title: id, Status: connector.CheckFail, Detail: "Notion didn't answer"}
	ce := connector.AsError(err)
	if ce == nil {
		return ch
	}
	switch {
	case ce.Kind == connector.KindAuth:
		ch.Detail = "Notion refused the token (" + strconv.Itoa(ce.HTTPStatus) + ")"
		ch.Fix = fmt.Sprintf("Store a working token: deedglass connector credentials set %s --from-env NOTION_TOKEN.", inst.Name)
	case ce.Kind == connector.KindRateLimited:
		ch.Detail, ch.Fix = "Notion is limiting requests", "Wait a minute, then run this test again."
	case ce.AdminDetail != "":
		ch.Detail = ce.AdminDetail
	default:
		ch.Detail = ce.AgentMessage
	}
	return ch
}

// Summarize implements connector.Summarizer: one sentence for approval cards.
func (c *Connector) Summarize(in connector.SummaryInput) connector.Summary {
	var a args
	if json.Unmarshal(in.Args, &a) != nil {
		return nil
	}
	agent := in.Agent
	if agent == "" {
		agent = "An agent"
	}
	strong := func(t string) connector.SummaryPart {
		return connector.SummaryPart{Text: t, Style: connector.SummaryStrong}
	}
	code := func(t string) connector.SummaryPart {
		return connector.SummaryPart{Text: t, Style: connector.SummaryCode}
	}
	plain := func(t string) connector.SummaryPart { return connector.SummaryPart{Text: t} }
	n := func(k int) string {
		if k == 1 {
			return "1 paragraph"
		}
		return strconv.Itoa(k) + " paragraphs"
	}
	switch in.Tool.Name {
	case ToolCreatePage:
		return connector.Summary{strong(agent), plain(" wants to create the Notion page "), strong(clip(a.Title, 120)), plain(" under "), code(a.ParentPageID), plain(" with " + n(len(a.Paragraphs)) + ".")}
	case ToolAppendBlock:
		return connector.Summary{strong(agent), plain(" wants to append " + n(len(a.Paragraphs)) + " to the Notion page "), code(a.PageID), plain(".")}
	}
	return nil
}
