package config

import (
	"errors"
	"strings"
	"testing"
	"testing/fstest"
)

func TestConnectorsOutboundMail(t *testing.T) {
	c, err := loadYAML(t, "connectors:\n  outbound_mail:\n    orgs: [org_01k6c9kb2zcjsyn7b8g2k1vqvp, org_01k6c9kb2zcjsyn7b8g2k1vqvp]\n    api_base: http://127.0.0.1:18299/\n")
	if err != nil {
		t.Fatal(err)
	}
	om := c.Connectors.OutboundMail
	if !om.Set || len(om.Orgs) != 1 || om.APIBase != "http://127.0.0.1:18299" {
		t.Fatalf("%+v", om)
	}
	if c, err := loadYAML(t, ""); err != nil || c.Connectors.OutboundMail.Set {
		t.Fatalf("absent: %v %+v", err, c.Connectors)
	}
	if c, err := loadYAML(t, "connectors:\n  outbound_mail:\n    orgs: []\n"); err != nil || c.Connectors.OutboundMail.APIBase != DefaultOutboundMailAPIBase {
		t.Fatalf("default base: %v", err)
	}
	for yml, want := range map[string]string{
		"connectors:\n  outbound_mail:\n    orgs: [growth]\n":                 "isn't an org id",
		"connectors:\n  outbound_mail:\n    orgs: ['*']\n":                    "isn't an org id",
		"connectors:\n  outbound_mail:\n    from: x\n":                        "isn't a Deedglass setting",
		"connectors:\n  other: {}\n":                                          "takes outbound_mail",
		"connectors: 3\n":                                                     "must be a mapping",
		"connectors:\n  outbound_mail: 3\n":                                   "must be a mapping",
		"connectors:\n  outbound_mail:\n    api_base: ftp://x.test\n":         "http or https",
		"connectors:\n  outbound_mail:\n    api_base: https://x.test/p?q=1\n": "plain URL",
		"connectors:\n  outbound_mail:\n    api_base: [a]\n":                  "single value",
	} {
		_, err := loadYAML(t, yml)
		var ps *Problems
		if !errors.As(err, &ps) || !strings.Contains(ps.Error(), want) {
			t.Errorf("%q: want %q, got %v", yml, want, err)
		}
	}
	env := map[string]string{"DEEDGLASS_ENV": "prod", "DEEDGLASS_VAULT_KEY": key32, "DEEDGLASS_API_KEY_PEPPER": key48}
	_, _, err = Load(LoadOptions{Env: env, FS: fstest.MapFS{"deedglass.yaml": {Data: []byte("connectors:\n  outbound_mail:\n    api_base: http://127.0.0.1:18299\n")}}})
	var ps *Problems
	if !errors.As(err, &ps) || !strings.Contains(ps.Error(), "must be https://gmail.googleapis.com in prod") {
		t.Fatalf("prod api_base: %v", err)
	}
}
