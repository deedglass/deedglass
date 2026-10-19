package fhir

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/deedglass/deedglass/internal/connector"
	"github.com/deedglass/deedglass/internal/connector/egress"
	"github.com/deedglass/deedglass/internal/connector/saas"
)

// Test implements connector.Connector: config, credentials, destination, token (smart_backend)
// and metadata (the server's CapabilityStatement: FHIR 4.0.x and the allowed types). It changes
// nothing.
func (c *Connector) Test(ctx context.Context, inst *connector.Instance) (rep connector.TestReport, _ error) {
	start := time.Now()
	add := func(id string, s connector.CheckStatus, detail, fix string) {
		rep.Checks = append(rep.Checks, connector.Check{ID: id, Title: id, Status: s, Detail: detail, Fix: fix})
	}
	defer func() { rep.Duration = time.Since(start); rep.Settle() }()
	st, err := parse(inst)
	if err != nil {
		add("config", connector.CheckFail, "the stored config can't be read", "Update the connector with a config that passes deedglass connector create's checks.")
		return rep, nil
	}
	add("config", connector.CheckPass, fmt.Sprintf("valid · %d types · %s", len(st.cfg.AllowedResourceTypes), strings.Join(st.cfg.AllowedInteractions, ", ")), "")

	pol := inst.Env.Egress
	if pol == nil {
		pol = &egress.Policy{Env: egress.EnvProd}
	}
	chk, err := pol.CheckURL(ctx, st.cfg.BaseURL)
	if err != nil {
		fix := ""
		if be, ok := egress.IsBlocked(err); ok {
			fix = be.Reason
		}
		add("destination", connector.CheckFail, clip(err.Error(), 200), fix)
		return rep, nil
	}
	add("destination", connector.CheckPass, chk.Host+" · "+chk.Class.Label(), "")

	cl, done, err := c.client(ctx, inst, st, false)
	switch ce := connector.AsError(err); {
	case err == nil:
		defer done()
		detail := "not needed (auth: none)"
		if st.cfg.Auth.Kind != AuthNone {
			detail = "set · " + st.cfg.Auth.Kind
		}
		add("credentials", connector.CheckPass, detail, "")
		if st.cfg.Auth.Kind == AuthSMART {
			add("token", connector.CheckPass, "an access token for "+strings.Join(st.cfg.Auth.Scopes, " "), "")
		}
	case ce != nil && ce.Kind == connector.KindCredentialsMissing:
		add("credentials", connector.CheckFail, "not set", "deedglass connector credentials set "+inst.Name+" (see docs/connectors/fhir.md)")
		return rep, nil
	default:
		detail := clip(err.Error(), 200)
		if ce != nil && ce.AdminDetail != "" {
			detail = ce.AdminDetail
		} else if ce != nil {
			detail = ce.AgentMessage
		}
		id := "credentials"
		if st.cfg.Auth.Kind == AuthSMART && ce != nil && ce.Code != "connector_misconfigured" {
			id = "token"
		}
		add(id, connector.CheckFail, detail, "Check the client id, the key and the scopes registered on the authorization server.")
		return rep, nil
	}

	resp, err := cl.Do(ctx, saas.Request{Path: "/metadata", Idempotent: true})
	var cs struct {
		ResourceType string `json:"resourceType"`
		FHIRVersion  string `json:"fhirVersion"`
		Rest         []struct {
			Resource []struct {
				Type string `json:"type"`
			} `json:"resource"`
		} `json:"rest"`
	}
	if err != nil || resp.JSON(&cs) != nil || cs.ResourceType != "CapabilityStatement" {
		detail := "no CapabilityStatement at /metadata"
		if ce := connector.AsError(err); ce != nil {
			detail = ce.AgentMessage
		}
		add("metadata", connector.CheckFail, detail, "Check base_url: it is the FHIR base, the address /metadata hangs off.")
		return rep, nil
	}
	var types []string
	for _, r := range cs.Rest {
		for _, x := range r.Resource {
			types = append(types, x.Type)
		}
	}
	var missing []string
	for _, t := range st.cfg.AllowedResourceTypes {
		if !slices.Contains(types, t) {
			missing = append(missing, t)
		}
	}
	switch {
	case !strings.HasPrefix(cs.FHIRVersion, "4.0."):
		add("metadata", connector.CheckFail, "the server speaks FHIR "+clip(cs.FHIRVersion, 16)+", not R4 (4.0.x)", "Point base_url at the server's R4 endpoint.")
	case len(missing) > 0:
		add("metadata", connector.CheckWarn, "FHIR "+cs.FHIRVersion+" · the server doesn't list "+strings.Join(missing, ", "), "Remove the types the server doesn't serve from allowed_resource_types.")
	default:
		add("metadata", connector.CheckPass, "FHIR "+cs.FHIRVersion+" · every allowed type is served", "")
	}
	return rep, nil
}
