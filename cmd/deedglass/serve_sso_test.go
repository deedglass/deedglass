package main

import (
	"flag"
	"testing"

	"github.com/deedglass/deedglass/internal/console"
)

func TestServeSSOFlags(t *testing.T) {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	serveFlags(fs)

	args := []string{
		"--sso-provider", "entra_id",
		"--sso-issuer", "https://login.microsoftonline.com/tenant-123/v2.0",
		"--sso-client-id", "client-xyz",
		"--sso-client-secret", "secret-xyz",
		"--sso-redirect-url", "https://hospital.local/app/sso/callback",
		"--sso-display-name", "Manipal SSO",
		"--sso-mock",
	}

	if err := fs.Parse(args); err != nil {
		t.Fatalf("failed to parse serve sso flags: %v", err)
	}

	if serveFlagValues.ssoProvider != "entra_id" {
		t.Errorf("expected sso-provider entra_id, got %q", serveFlagValues.ssoProvider)
	}
	if serveFlagValues.ssoIssuer != "https://login.microsoftonline.com/tenant-123/v2.0" {
		t.Errorf("expected sso-issuer, got %q", serveFlagValues.ssoIssuer)
	}
	if serveFlagValues.ssoClientID != "client-xyz" {
		t.Errorf("expected sso-client-id client-xyz, got %q", serveFlagValues.ssoClientID)
	}
	if serveFlagValues.ssoClientSecret != "secret-xyz" {
		t.Errorf("expected sso-client-secret, got %q", serveFlagValues.ssoClientSecret)
	}
	if serveFlagValues.ssoRedirectURL != "https://hospital.local/app/sso/callback" {
		t.Errorf("expected sso-redirect-url, got %q", serveFlagValues.ssoRedirectURL)
	}
	if serveFlagValues.ssoDisplayName != "Manipal SSO" {
		t.Errorf("expected sso-display-name Manipal SSO, got %q", serveFlagValues.ssoDisplayName)
	}
	if !serveFlagValues.ssoMock {
		t.Errorf("expected sso-mock to be true")
	}
}

func TestServeSSOFromEnvSynthesis(t *testing.T) {
	t.Setenv("DEEDGLASS_OIDC_ENABLED", "true")
	t.Setenv("DEEDGLASS_OIDC_PROVIDER", "okta")
	t.Setenv("DEEDGLASS_OIDC_DISPLAY_NAME", "Okta Hospital SSO")
	t.Setenv("DEEDGLASS_OIDC_ISSUER", "https://hospital.okta.com")
	t.Setenv("DEEDGLASS_OIDC_CLIENT_ID", "okta-client-123")
	t.Setenv("DEEDGLASS_OIDC_CLIENT_SECRET", "okta-secret-456")
	t.Setenv("DEEDGLASS_OIDC_REDIRECT_URI", "https://deedglass.hospital.org/app/sso/callback")
	t.Setenv("DEEDGLASS_OIDC_MOCK", "1")

	opts, err := console.LoadSSOConfigFromEnv(nil)
	if err != nil {
		t.Fatalf("LoadSSOConfigFromEnv failed: %v", err)
	}
	if opts == nil {
		t.Fatal("expected non-nil SSOOptions")
	}
	if !opts.MockIdPEnabled {
		t.Error("expected MockIdPEnabled=true")
	}
	if len(opts.Providers) != 1 {
		t.Fatalf("expected 1 provider, got %d", len(opts.Providers))
	}
	p := opts.Providers[0]
	if p.Type != console.ProviderOkta {
		t.Errorf("expected provider okta, got %v", p.Type)
	}
	if p.DisplayName != "Okta Hospital SSO" {
		t.Errorf("expected display name Okta Hospital SSO, got %q", p.DisplayName)
	}
	if p.ClientID != "okta-client-123" {
		t.Errorf("expected client id okta-client-123, got %q", p.ClientID)
	}

	ssoSvc, err := console.NewSSOService(*opts)
	if err != nil {
		t.Fatalf("NewSSOService failed: %v", err)
	}
	if ssoSvc == nil {
		t.Fatal("expected non-nil SSOService")
	}
}
