package identity

import (
	"context"
	"encoding/json/jsontext"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/deedglass/deedglass/internal/httpx"
)

func TestParseRetention(t *testing.T) {
	for raw, want := range map[string]int{"1": 1, "90": 90, "3650": 3650, " 30 ": 30} {
		d, err := parseRetention(jsontext.Value(raw))
		if err != nil || d == nil || *d != want {
			t.Errorf("%q: %v %v", raw, d, err)
		}
	}
	if d, err := parseRetention(jsontext.Value("null")); err != nil || d != nil {
		t.Errorf("null: %v %v", d, err)
	}
	for _, raw := range []string{"0", "-1", "3651", "1.5", "30.0", "1e2", `"30"`, "true", "{}", "[]", "99999999999"} {
		_, err := parseRetention(jsontext.Value(raw))
		var e *httpx.Error
		if !errors.As(err, &e) || e.Code != httpx.CodeValidationFailed || e.Param != "retention_days" ||
			e.Message != "`retention_days` must be a whole number of days from 1 to 3650, or null for the default of 90." {
			t.Errorf("%q: %v", raw, err)
		}
	}
}

func TestApprovalSettingsDefaults(t *testing.T) {
	cases := map[string]ApprovalSettings{
		`{}`:                 {"decide", "summary", "deny"},
		`{"approvals":null}`: {"decide", "summary", "deny"},
		`{"approvals":{"email_links":"review","email_detail":"full","self_countersign":"allow"}}`: {"review", "full", "allow"},
		`{"approvals":{"email_links":"other","self_countersign":"yes"}}`:                          {"decide", "summary", "deny"},
		`not json`: {"decide", "summary", "deny"},
	}
	for raw, want := range cases {
		if got := approvalSettings([]byte(raw)); got != want {
			t.Errorf("%s: %+v, want %+v", raw, got, want)
		}
	}
}

// decodePatch decodes body the way the PATCH /v1/org handler does.
func decodePatch(t *testing.T, body string) (OrgPatch, error) {
	t.Helper()
	var in OrgPatch
	r := httptest.NewRequest(http.MethodPatch, "/v1/org", strings.NewReader(body))
	err := httpx.DecodeJSON(r, &in)
	return in, err
}

// UpdateOrg refuses before it reaches the database: owner-only fields for an admin, then each
// field's validation. (The integration suite covers the writes.)
func TestUpdateOrgRefusals(t *testing.T) {
	s := &Service{}
	owner := &Principal{Kind: KindUser, Role: RoleOwner, OrgName: "Acme"}
	admin := &Principal{Kind: KindUser, Role: RoleAdmin, OrgName: "Acme"}
	cases := []struct {
		who                        *Principal
		body, code, param, message string
	}{
		{admin, `{"retention_days":30}`, "insufficient_role", "",
			"Your role in Acme is Admin, and changing payload retention needs Owner. Ask an owner to change your role."},
		{admin, `{"retention_days":null}`, "insufficient_role", "",
			"Your role in Acme is Admin, and changing payload retention needs Owner. Ask an owner to change your role."},
		{admin, `{"name":"Acme","settings":{"approvals":{"email_links":"review"}}}`, "insufficient_role", "",
			"Your role in Acme is Admin, and changing approval settings needs Owner. Ask an owner to change your role."},
		{owner, `{"retention_days":0}`, "validation_failed", "retention_days",
			"`retention_days` must be a whole number of days from 1 to 3650, or null for the default of 90."},
		{owner, `{"retention_days":"30"}`, "validation_failed", "retention_days",
			"`retention_days` must be a whole number of days from 1 to 3650, or null for the default of 90."},
		{owner, `{"settings":{"approvals":{"email_links":"maybe"}}}`, "validation_failed", "settings.approvals.email_links",
			"`settings.approvals.email_links` must be decide or review."},
		{owner, `{"settings":{"approvals":{"email_detail":"all"}}}`, "validation_failed", "settings.approvals.email_detail",
			"`settings.approvals.email_detail` must be summary or full."},
		{owner, `{"settings":{"approvals":{"self_countersign":""}}}`, "validation_failed", "settings.approvals.self_countersign",
			"`settings.approvals.self_countersign` must be deny or allow."},
		{owner, `{}`, "validation_failed", "", "Send at least one of name, timezone, retention_days or settings.approvals."},
		{owner, `{"settings":{}}`, "validation_failed", "", "Send at least one of name, timezone, retention_days or settings.approvals."},
		{owner, `{"settings":{"approvals":{}}}`, "validation_failed", "", "Send at least one of name, timezone, retention_days or settings.approvals."},
	}
	for _, c := range cases {
		in, err := decodePatch(t, c.body)
		if err != nil {
			t.Fatalf("%s: decode: %v", c.body, err)
		}
		_, _, err = s.UpdateOrg(context.Background(), c.who, in)
		var e *httpx.Error
		if !errors.As(err, &e) || string(e.Code) != c.code || e.Param != c.param || e.Message != c.message {
			t.Errorf("%s as %s: %#v", c.body, c.who.Role.Title(), err)
		}
	}
}

// Unknown keys under settings are refused by the strict decoder, with the path as the param.
func TestOrgPatchUnknownKeys(t *testing.T) {
	for body, param := range map[string]string{
		`{"settings":{"approvals":{"foo":"x"}}}`: "settings.approvals.foo",
		`{"settings":{"webhooks":{}}}`:           "settings.webhooks",
		`{"retention":30}`:                       "retention",
	} {
		_, err := decodePatch(t, body)
		var e *httpx.Error
		if !errors.As(err, &e) || e.Code != httpx.CodeValidationFailed || e.Param != param {
			t.Errorf("%s: %#v", body, err)
		}
	}
	in, err := decodePatch(t, `{"retention_days":null}`)
	if err != nil || in.RetentionDays == nil || string(in.RetentionDays) != "null" {
		t.Fatalf("null retention: %q %v", in.RetentionDays, err)
	}
	if in, err := decodePatch(t, `{"name":"x"}`); err != nil || in.RetentionDays != nil {
		t.Fatalf("absent retention: %q %v", in.RetentionDays, err)
	}
}
