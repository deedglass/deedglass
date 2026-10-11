//go:build outboundmail

package outbound

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/deedglass/deedglass/internal/connector"
)

// Facts is request.mail (policy.MailFacts has the same JSON): what the connector itself counts in
// the message, so the policy never relies on the sender's own counts.
type Facts struct {
	From             string `json:"from"`
	FromDomain       string `json:"from_domain"`
	ToDomain         string `json:"to_domain"`
	Words            int64  `json:"words"`
	Links            int64  `json:"links"`
	BareDomains      int64  `json:"bare_domains"`
	Questions        int64  `json:"questions"`
	HasHTML          bool   `json:"has_html"`
	Step             int64  `json:"step"`
	Threaded         bool   `json:"threaded"`
	UnsubscribeHTTPS bool   `json:"unsubscribe_https"`
	Enriched         bool   `json:"enriched"`
}

// Enrich implements connector.Enricher (pure: no call). A send that can't be read gives no facts
// (enriched false), which the policy treats as unknown.
func (c *Connector) Enrich(_ context.Context, inst *connector.Instance, t connector.ToolSpec, raw json.RawMessage) (map[string]any, error) {
	if t.Name != ToolSend {
		return nil, nil
	}
	st, err := c.parse(inst)
	if err != nil {
		return nil, nil
	}
	return map[string]any{"mail": FactsOf(st.cfg, raw)}, nil
}

// FactsOf computes request.mail for a mail.send's arguments under cfg.
func FactsOf(cfg Config, raw json.RawMessage) Facts {
	var a SendArgs
	if json.Unmarshal(raw, &a) != nil {
		return Facts{}
	}
	f := Facts{From: cfg.Mailbox, Step: int64(a.Step), Threaded: a.Thread != nil, Enriched: true,
		UnsubscribeHTTPS: strings.HasPrefix(cfg.UnsubscribeBase, "https://") && a.Unsubscribe.Token != ""}
	_, f.FromDomain, _ = strings.Cut(cfg.Mailbox, "@")
	if _, d, ok := strings.Cut(strings.ToLower(strings.TrimSpace(a.To)), "@"); ok {
		f.ToDomain = d
	}
	body := a.BodyText
	f.Words = int64(CountWords(body, cfg.FromName))
	f.Links = int64(CountLinks(body))
	f.BareDomains = int64(CountBareDomains(body))
	f.Questions = int64(strings.Count(body, "?"))
	f.HasHTML = strings.Contains(strings.ToLower(body), "<html")
	return f
}
