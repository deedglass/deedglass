package config

import (
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"
)

// The connectors section (S40, file-only): connectors.outbound_mail.orgs and .api_base. The
// outbound-mail connector exists only in a binary built with -tags outboundmail (make ops-build);
// these settings name the orgs that may use it. A plain build refuses to start with them set.

// DefaultOutboundMailAPIBase is Gmail's API base, the only value prod accepts.
const DefaultOutboundMailAPIBase = "https://gmail.googleapis.com"

// ConnectorSettings are the connectors.* settings.
type ConnectorSettings struct {
	OutboundMail OutboundMailSettings
}

// OutboundMailSettings are connectors.outbound_mail.*.
type OutboundMailSettings struct {
	Orgs    []string // org ids; empty means no org may use outbound_mail
	APIBase string   // DefaultOutboundMailAPIBase unless set (dev may point at a fake)
	Set     bool     // the section appears in deedglass.yaml
}

var reOrgID = regexp.MustCompile(`^org_[0-7][0-9a-hjkmnp-tv-z]{25}$`)

// readConnectors reads the connectors section.
func (l *loader) readConnectors(file string, n *yaml.Node) {
	at := func(x *yaml.Node) string { return fmt.Sprintf("%s:%d", file, x.Line) }
	if n.Kind == yaml.ScalarNode && n.Tag == "!!null" {
		return
	}
	if n.Kind != yaml.MappingNode {
		l.problem(at(n), `"connectors" must be a mapping of settings.`, "See docs/CONFIG.md for the connectors section.")
		return
	}
	cs := &ConnectorSettings{}
	for i := 0; i+1 < len(n.Content); i += 2 {
		k, v := n.Content[i], n.Content[i+1]
		if k.Value != "outbound_mail" {
			l.problem(at(k), fmt.Sprintf("%q isn't a Deedglass setting.", "connectors."+k.Value), "The connectors section takes outbound_mail.")
			continue
		}
		if v.Kind != yaml.MappingNode {
			l.problem(at(v), `"connectors.outbound_mail" must be a mapping of settings.`, "Write orgs and api_base under it.")
			continue
		}
		om := OutboundMailSettings{Set: true, APIBase: DefaultOutboundMailAPIBase}
		for j := 0; j+1 < len(v.Content); j += 2 {
			kk, vv := v.Content[j], v.Content[j+1]
			switch kk.Value {
			case "orgs":
				for _, s := range l.stringList(file, "connectors.outbound_mail.orgs", vv) {
					if !reOrgID.MatchString(s) {
						l.problem(at(vv), fmt.Sprintf("connectors.outbound_mail.orgs: %q isn't an org id.", s),
							"List org ids like org_01…, never a slug or *. deedglass org show prints the id.")
						continue
					}
					if !slices.Contains(om.Orgs, s) {
						om.Orgs = append(om.Orgs, s)
					}
				}
			case "api_base":
				if vv.Kind != yaml.ScalarNode {
					l.problem(at(vv), `"connectors.outbound_mail.api_base" must be a single value.`, "Write it on one line.")
					continue
				}
				om.APIBase = strings.TrimSuffix(vv.Value, "/")
			default:
				l.problem(at(kk), fmt.Sprintf("%q isn't a Deedglass setting.", "connectors.outbound_mail."+kk.Value),
					"connectors.outbound_mail takes orgs and api_base.")
			}
		}
		cs.OutboundMail = om
	}
	l.connectors = cs
}

// checkConnectors applies the env rules: prod accepts only Gmail's API base.
func (l *loader) checkConnectors(c *Config) {
	if l.connectors == nil {
		return
	}
	c.Connectors = *l.connectors
	om := c.Connectors.OutboundMail
	if !om.Set {
		return
	}
	u, err := url.Parse(om.APIBase)
	switch {
	case err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/"):
		l.problem("connectors.outbound_mail.api_base", fmt.Sprintf("%q isn't a plain URL.", om.APIBase), "Leave it out to use "+DefaultOutboundMailAPIBase+".")
	case l.env == Prod && om.APIBase != DefaultOutboundMailAPIBase:
		l.problem("connectors.outbound_mail.api_base", "must be "+DefaultOutboundMailAPIBase+" in prod.", "Leave it out, or run a fake mailbox in dev only.")
	case u.Scheme != "https" && u.Scheme != "http":
		l.problem("connectors.outbound_mail.api_base", "must be an http or https URL.", "Leave it out to use "+DefaultOutboundMailAPIBase+".")
	}
}
