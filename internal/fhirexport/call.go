package fhirexport

import (
	"encoding/json"
	"regexp"
	"strings"
)

var (
	reType = regexp.MustCompile(`^[A-Z][A-Za-z]{1,63}$`)
	reID   = regexp.MustCompile(`^[A-Za-z0-9\-.]{1,64}$`)
)

// CallFrom reads a FHIR connector's action row: the connector's type, name and config, and the
// action's stored request (the tool arguments) and result (the tool output). It returns nil for
// any connector type but fhir. Only ids pass: a value that isn't a FHIR type or id is left out, so
// a digest that PHI mode stored in place of a value never appears as one.
func CallFrom(connectorType, connectorName string, config, request, result json.RawMessage) *Call {
	if connectorType != "fhir" {
		return nil
	}
	c := &Call{Connector: connectorName}
	var cfg struct {
		BaseURL string `json:"base_url"`
	}
	_ = json.Unmarshal(config, &cfg)
	c.Server = cfg.BaseURL
	var req struct {
		ResourceType string         `json:"resource_type"`
		ID           string         `json:"id"`
		Resource     map[string]any `json:"resource"`
		Params       map[string]any `json:"params"`
	}
	_ = json.Unmarshal(request, &req)
	if reType.MatchString(req.ResourceType) {
		c.ResourceType = req.ResourceType
	}
	if reID.MatchString(req.ID) {
		c.ID = req.ID
	}
	var res struct {
		Status   int               `json:"status"`
		ClientID string            `json:"client_id"`
		Headers  map[string]string `json:"headers"`
		Body     json.RawMessage   `json:"body"`
	}
	_ = json.Unmarshal(result, &res)
	c.ClientID = res.ClientID
	if res.Status >= 200 && res.Status < 300 {
		var body struct {
			ResourceType string `json:"resourceType"`
			ID           string `json:"id"`
			Meta         struct {
				VersionID string `json:"versionId"`
			} `json:"meta"`
		}
		_ = json.Unmarshal(res.Body, &body)
		if body.ResourceType == c.ResourceType && reID.MatchString(body.ID) {
			c.ID = body.ID
		}
		if reID.MatchString(body.Meta.VersionID) {
			c.VersionID = body.Meta.VersionID
		}
		// Location: <base>/<Type>/<id>/_history/<vid>, as the read-back reads it.
		if loc := res.Headers["location"]; c.VersionID == "" && loc != "" {
			if i := strings.Index(loc, "/_history/"); i > 0 {
				if v := strings.Trim(loc[i+len("/_history/"):], "/"); reID.MatchString(v) {
					c.VersionID = v
				}
			}
		}
	}
	c.Patient = patientOf(c, req.Resource, req.Params)
	return c
}

// patientOf is the patient a call names in the clear: the Patient itself, the resource's subject
// or patient reference, or a search's patient or subject parameter. In PHI mode the resource and
// the search parameters are digests, so nothing is found.
func patientOf(c *Call, res, params map[string]any) string {
	if c.ResourceType == "Patient" && c.ID != "" {
		return "Patient/" + c.ID
	}
	for _, k := range []string{"subject", "patient"} {
		if m, ok := res[k].(map[string]any); ok {
			if ref, _ := m["reference"].(string); strings.HasPrefix(ref, "Patient/") && reID.MatchString(strings.TrimPrefix(ref, "Patient/")) {
				return ref
			}
		}
		if v, ok := params[k].(string); ok {
			id := strings.TrimPrefix(v, "Patient/")
			if reID.MatchString(id) {
				return "Patient/" + id
			}
		}
	}
	return ""
}
