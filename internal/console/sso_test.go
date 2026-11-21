package console

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/deedglass/deedglass/internal/httpx"
)

func TestPKCE(t *testing.T) {
	pkce, err := GeneratePKCE()
	if err != nil {
		t.Fatalf("GeneratePKCE: %v", err)
	}
	if len(pkce.Verifier) < 43 {
		t.Fatalf("verifier too short: %d", len(pkce.Verifier))
	}
	if pkce.Method != "S256" {
		t.Fatalf("expected S256 method, got %s", pkce.Method)
	}
	if !VerifyPKCE(pkce.Verifier, pkce.Challenge) {
		t.Fatal("VerifyPKCE failed on valid verifier and challenge")
	}
	if VerifyPKCE(pkce.Verifier+"x", pkce.Challenge) {
		t.Fatal("VerifyPKCE should fail on corrupted verifier")
	}
}

func TestStateSecurity(t *testing.T) {
	now := time.Date(2026, 11, 13, 10, 0, 0, 0, time.UTC)
	sso, err := NewSSOService(SSOOptions{
		StateSecret: []byte("test-32-byte-secret-key-01234567"),
		Now:         func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("NewSSOService: %v", err)
	}

	payload := StatePayload{
		ProviderID: "manipal_entra",
		Nonce:      "test-nonce-12345",
		Verifier:   "test-verifier-abcde",
		Next:       "/app/actions",
		ExpiresAt:  now.Add(10 * time.Minute).Unix(),
	}

	token, err := sso.SignState(payload)
	if err != nil {
		t.Fatalf("SignState: %v", err)
	}

	got, err := sso.VerifyState(token)
	if err != nil {
		t.Fatalf("VerifyState: %v", err)
	}
	if got.ProviderID != payload.ProviderID || got.Nonce != payload.Nonce || got.Next != payload.Next {
		t.Fatalf("state mismatch: want %+v, got %+v", payload, got)
	}

	// Tampered signature
	parts := strings.Split(token, ".")
	tamperedSig := parts[0] + ".corrupted_signature"
	if _, err := sso.VerifyState(tamperedSig); err == nil {
		t.Fatal("VerifyState should fail on tampered signature")
	}

	// Tampered payload
	tamperedPayload := "eyJhbGciOiJub25lIn0." + parts[1]
	if _, err := sso.VerifyState(tamperedPayload); err == nil {
		t.Fatal("VerifyState should fail on tampered payload")
	}

	// Expired state
	expiredPayload := StatePayload{
		ProviderID: "manipal_entra",
		ExpiresAt:  now.Add(-1 * time.Second).Unix(),
	}
	expiredToken, _ := sso.SignState(expiredPayload)
	if _, err := sso.VerifyState(expiredToken); err == nil {
		t.Fatal("VerifyState should fail on expired state")
	}
}

func TestSafeDigest(t *testing.T) {
	sso, err := NewSSOService(SSOOptions{
		DigestSalt: []byte("salt-12345"),
	})
	if err != nil {
		t.Fatal(err)
	}

	d1 := sso.SafeDigest("https://login.microsoftonline.com/tenant/v2.0", "user-uuid-001")
	d2 := sso.SafeDigest("https://login.microsoftonline.com/tenant/v2.0", "user-uuid-001")
	d3 := sso.SafeDigest("https://login.microsoftonline.com/tenant/v2.0", "user-uuid-002")

	if d1 != d2 {
		t.Fatalf("SafeDigest must be deterministic: %q != %q", d1, d2)
	}
	if d1 == d3 {
		t.Fatalf("SafeDigest must differentiate subjects: %q == %q", d1, d3)
	}
	if len(d1) != 64 {
		t.Fatalf("expected 64-char hex sha256 digest, got %d", len(d1))
	}
}

func TestRoleResolutionAndDutyRota(t *testing.T) {
	mapping := RoleMappingConfig{
		OwnerGroups:  []string{"Hospital-CIO", "Deedglass-Admins"},
		AdminGroups:  []string{"Emergency-HOD", "Chief-Medical-Officer"},
		ViewerGroups: []string{"Emergency-Physicians", "Ward-Nurses"},
		DefaultRole:  httpx.RoleViewer,
		FailClosed:   true,
		DutyRota: map[string]DutyProfile{
			"Emergency-Physicians": {
				Ward:            "Emergency Medicine",
				RoleTitle:       "Emergency Attending Physician",
				CanBreakGlass:   true,
				RequireFourEyes: false,
			},
			"Ward-Nurses": {
				Ward:            "General Ward",
				RoleTitle:       "Staff Nurse",
				CanBreakGlass:   false,
				RequireFourEyes: true,
			},
		},
	}

	// 1. Owner priority
	role, _, err := ResolveRole(mapping, []string{"Hospital-CIO", "Emergency-Physicians"})
	if err != nil || role != httpx.RoleOwner {
		t.Fatalf("expected RoleOwner, got %s (err: %v)", role, err)
	}

	// 2. Admin priority
	role, _, err = ResolveRole(mapping, []string{"Emergency-HOD", "Ward-Nurses"})
	if err != nil || role != httpx.RoleAdmin {
		t.Fatalf("expected RoleAdmin, got %s (err: %v)", role, err)
	}

	// 3. Viewer with Duty Rota and Break-Glass
	role, duty, err := ResolveRole(mapping, []string{"Emergency-Physicians"})
	if err != nil || role != httpx.RoleViewer {
		t.Fatalf("expected RoleViewer, got %s (err: %v)", role, err)
	}
	if duty.Ward != "Emergency Medicine" || !duty.CanBreakGlass {
		t.Fatalf("expected Emergency Medicine duty with break-glass, got %+v", duty)
	}

	// 4. Fail-closed on unrecognized group
	_, _, err = ResolveRole(mapping, []string{"Unrelated-Vendor-Group"})
	if err == nil {
		t.Fatal("expected fail-closed error for unauthorized group")
	}

	// 5. Non-fail-closed uses DefaultRole
	openMapping := mapping
	openMapping.FailClosed = false
	openMapping.DefaultRole = httpx.RoleViewer
	role, _, err = ResolveRole(openMapping, []string{"Unrelated-Vendor-Group"})
	if err != nil || role != httpx.RoleViewer {
		t.Fatalf("expected RoleViewer fallback, got %s (err: %v)", role, err)
	}
}

func TestExtractGroups(t *testing.T) {
	// From "groups" array
	c1 := map[string]any{"groups": []any{"GroupA", "GroupB"}}
	g1 := ExtractGroups(c1, "groups")
	if len(g1) != 2 || g1[0] != "GroupA" || g1[1] != "GroupB" {
		t.Fatalf("unexpected extracted groups: %v", g1)
	}

	// Fallback to "roles"
	c2 := map[string]any{"roles": []string{"RoleX", "RoleY"}}
	g2 := ExtractGroups(c2, "groups")
	if len(g2) != 2 || g2[0] != "RoleX" || g2[1] != "RoleY" {
		t.Fatalf("unexpected fallback roles: %v", g2)
	}

	// Fallback to "wids" (Entra ID Directory Role Template IDs)
	c3 := map[string]any{"wids": "wid-1 wid-2"}
	g3 := ExtractGroups(c3, "groups")
	if len(g3) != 2 || g3[0] != "wid-1" || g3[1] != "wid-2" {
		t.Fatalf("unexpected space-separated wids: %v", g3)
	}
}

// Helper to generate a self-signed RSA JWT and matching JWKS for testing
func makeTestRSAJWT(t *testing.T, key *rsa.PrivateKey, kid, iss, aud, sub, email, nonce string, exp time.Time, extraClaims map[string]any) (string, *JWKSet) {
	t.Helper()

	headerJSON := fmt.Sprintf(`{"alg":"RS256","typ":"JWT","kid":%q}`, kid)
	encHeader := base64.RawURLEncoding.EncodeToString([]byte(headerJSON))

	claimsMap := map[string]any{
		"iss":   iss,
		"sub":   sub,
		"aud":   aud,
		"exp":   exp.Unix(),
		"iat":   exp.Add(-1 * time.Hour).Unix(),
		"email": email,
		"nonce": nonce,
	}
	for k, v := range extraClaims {
		claimsMap[k] = v
	}
	claimsBytes, err := json.Marshal(claimsMap)
	if err != nil {
		t.Fatalf("marshal claims: %v", err)
	}
	encPayload := base64.RawURLEncoding.EncodeToString(claimsBytes)

	h := sha256.Sum256([]byte(encHeader + "." + encPayload))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, h[:])
	if err != nil {
		t.Fatalf("rsa sign: %v", err)
	}
	encSig := base64.RawURLEncoding.EncodeToString(sig)
	rawToken := encHeader + "." + encPayload + "." + encSig

	// Build JWKS
	nStr := base64.RawURLEncoding.EncodeToString(key.N.Bytes())
	eBytes := big.NewInt(int64(key.E)).Bytes()
	eStr := base64.RawURLEncoding.EncodeToString(eBytes)

	jwks := &JWKSet{
		Keys: []JWK{
			{
				Kty: "RSA",
				Kid: kid,
				Alg: "RS256",
				Use: "sig",
				N:   nStr,
				E:   eStr,
			},
		},
	}

	return rawToken, jwks
}

// Helper to generate a self-signed ECDSA JWT and matching JWKS for testing
func makeTestECJWT(t *testing.T, key *ecdsa.PrivateKey, kid, iss, aud, sub, nonce string, exp time.Time) (string, *JWKSet) {
	t.Helper()

	headerJSON := fmt.Sprintf(`{"alg":"ES256","typ":"JWT","kid":%q}`, kid)
	encHeader := base64.RawURLEncoding.EncodeToString([]byte(headerJSON))

	claimsMap := map[string]any{
		"iss":   iss,
		"sub":   sub,
		"aud":   aud,
		"exp":   exp.Unix(),
		"nonce": nonce,
	}
	claimsBytes, _ := json.Marshal(claimsMap)
	encPayload := base64.RawURLEncoding.EncodeToString(claimsBytes)

	h := sha256.Sum256([]byte(encHeader + "." + encPayload))
	sig, err := ecdsa.SignASN1(rand.Reader, key, h[:])
	if err != nil {
		t.Fatalf("ecdsa sign: %v", err)
	}
	encSig := base64.RawURLEncoding.EncodeToString(sig)
	rawToken := encHeader + "." + encPayload + "." + encSig

	// Build JWKS
	xStr := base64.RawURLEncoding.EncodeToString(key.X.Bytes())
	yStr := base64.RawURLEncoding.EncodeToString(key.Y.Bytes())

	jwks := &JWKSet{
		Keys: []JWK{
			{
				Kty: "EC",
				Kid: kid,
				Alg: "ES256",
				Use: "sig",
				Crv: "P-256",
				X:   xStr,
				Y:   yStr,
			},
		},
	}

	return rawToken, jwks
}

func TestVerifyIDTokenRS256(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}

	now := time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)
	kid := "key-2026-rsa"
	iss := "https://login.microsoftonline.com/tenant-123/v2.0"
	aud := "deedglass-hospital-client"
	sub := "dr-arjun-001"
	email := "arjun@manipal.edu"
	nonce := "random-nonce-abc"

	rawToken, jwks := makeTestRSAJWT(t, key, kid, iss, aud, sub, email, nonce, now.Add(1*time.Hour), map[string]any{
		"groups": []string{"Emergency-Clinicians"},
	})

	claims, err := VerifyIDToken(rawToken, jwks, iss, aud, nonce, now)
	if err != nil {
		t.Fatalf("VerifyIDToken failed: %v", err)
	}
	if claims.Subject != sub || claims.Email != email || claims.Issuer != iss {
		t.Fatalf("claims mismatch: %+v", claims)
	}

	// 1. Wrong issuer
	if _, err := VerifyIDToken(rawToken, jwks, "https://wrong-issuer.com", aud, nonce, now); err == nil {
		t.Fatal("expected failure on wrong issuer")
	}

	// 2. Wrong audience
	if _, err := VerifyIDToken(rawToken, jwks, iss, "wrong-client-id", nonce, now); err == nil {
		t.Fatal("expected failure on wrong client id")
	}

	// 3. Wrong nonce
	if _, err := VerifyIDToken(rawToken, jwks, iss, aud, "wrong-nonce", now); err == nil {
		t.Fatal("expected failure on wrong nonce")
	}

	// 4. Expired token
	past := now.Add(2 * time.Hour)
	if _, err := VerifyIDToken(rawToken, jwks, iss, aud, nonce, past); err == nil {
		t.Fatal("expected failure on expired token")
	}

	// 5. Corrupted signature
	parts := strings.Split(rawToken, ".")
	corrupted := parts[0] + "." + parts[1] + ".corrupted_sig"
	if _, err := VerifyIDToken(corrupted, jwks, iss, aud, nonce, now); err == nil {
		t.Fatal("expected failure on corrupted signature")
	}
}

func TestVerifyIDTokenES256(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	now := time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)
	kid := "key-2026-ec"
	iss := "https://accounts.google.com"
	aud := "deedglass-google-client"
	sub := "google-sub-789"
	nonce := "nonce-ec-xyz"

	rawToken, jwks := makeTestECJWT(t, key, kid, iss, aud, sub, nonce, now.Add(1*time.Hour))

	claims, err := VerifyIDToken(rawToken, jwks, iss, aud, nonce, now)
	if err != nil {
		t.Fatalf("VerifyIDToken ES256 failed: %v", err)
	}
	if claims.Subject != sub || claims.Issuer != iss {
		t.Fatalf("claims mismatch: %+v", claims)
	}
}

func TestSSOServiceHTTPFlow(t *testing.T) {
	now := time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)
	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}

	kid := "key-hospital-01"
	issuer := "https://login.microsoftonline.com/manipal-tenant/v2.0"
	clientID := "deedglass-ward-client"

	pConfig := NewEntraIDConfig(
		"manipal_entra",
		"manipal-tenant",
		clientID,
		"secret-123",
		"http://127.0.0.1:8080/app/sso/callback",
		RoleMappingConfig{
			OwnerGroups:  []string{"Hospital-CIO"},
			AdminGroups:  []string{"Emergency-HOD"},
			ViewerGroups: []string{"Emergency-Clinicians"},
			DefaultRole:  httpx.RoleViewer,
			FailClosed:   true,
			DutyRota: map[string]DutyProfile{
				"Emergency-Clinicians": {
					Ward:          "Emergency Department",
					RoleTitle:     "Emergency Senior Consultant",
					CanBreakGlass: true,
				},
			},
		},
	)

	// Mock IdP Server to serve token endpoint
	var mockIDToken string
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token": "mock-access-token",
				"id_token":     mockIDToken,
				"token_type":   "Bearer",
				"expires_in":   3600,
			})
			return
		}
		http.NotFound(w, r)
	}))
	defer mockServer.Close()

	pConfig.TokenURL = mockServer.URL + "/token"

	var sessionCreatedFor ClinicianIdentity
	sessionHook := SessionCreatorFunc(func(ctx context.Context, w http.ResponseWriter, r *http.Request, id ClinicianIdentity) error {
		sessionCreatedFor = id
		return nil
	})

	sso, err := NewSSOService(SSOOptions{
		Providers:      []ProviderConfig{pConfig},
		Now:            func() time.Time { return now },
		SessionCreator: sessionHook,
		MockIdPEnabled: true,
	})
	if err != nil {
		t.Fatalf("NewSSOService: %v", err)
	}

	// 1. GET /app/sso/providers
	wProv := httptest.NewRecorder()
	rProv := httptest.NewRequest(http.MethodGet, "/app/sso/providers", nil)
	if err := sso.ServeHTTP(wProv, rProv, "providers"); err != nil {
		t.Fatalf("handleProviders: %v", err)
	}
	if wProv.Code != http.StatusOK {
		t.Fatalf("providers status: %d", wProv.Code)
	}
	var provResp struct {
		Providers []ProviderInfo `json:"providers"`
	}
	if err := json.Unmarshal(wProv.Body.Bytes(), &provResp); err != nil {
		t.Fatalf("decode providers: %v", err)
	}
	if len(provResp.Providers) != 1 || provResp.Providers[0].ID != "manipal_entra" {
		t.Fatalf("unexpected providers response: %+v", provResp)
	}

	// 2. GET /app/sso/login?provider=manipal_entra&next=/app/actions
	wLogin := httptest.NewRecorder()
	rLogin := httptest.NewRequest(http.MethodGet, "/app/sso/login?provider=manipal_entra&next=/app/actions", nil)
	if err := sso.ServeHTTP(wLogin, rLogin, "login"); err != nil {
		t.Fatalf("handleLogin: %v", err)
	}
	if wLogin.Code != http.StatusFound {
		t.Fatalf("login redirect status: %d", wLogin.Code)
	}
	loc := wLogin.Header().Get("Location")
	if !strings.HasPrefix(loc, "https://login.microsoftonline.com/manipal-tenant/oauth2/v2.0/authorize?") {
		t.Fatalf("unexpected location header: %s", loc)
	}

	u, err := url.Parse(loc)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	codeChallenge := q.Get("code_challenge")
	signedState := q.Get("state")
	nonce := q.Get("nonce")
	if codeChallenge == "" || signedState == "" || nonce == "" {
		t.Fatalf("missing auth parameters in redirect: %+v", q)
	}

	// Verify state and extract verifier
	statePayload, err := sso.VerifyState(signedState)
	if err != nil {
		t.Fatalf("VerifyState: %v", err)
	}
	if !VerifyPKCE(statePayload.Verifier, codeChallenge) {
		t.Fatal("PKCE verifier does not match challenge")
	}

	// Prepare mock ID Token and pre-populate JWKS
	sub := "clinician-kmc-007"
	email := "dr.kmc@manipal.edu"
	mockIDToken, jwks := makeTestRSAJWT(t, rsaKey, kid, issuer, clientID, sub, email, nonce, now.Add(1*time.Hour), map[string]any{
		"groups": []string{"Emergency-Clinicians"},
	})
	sso.SetLocalJWKSet("manipal_entra", jwks, 1*time.Hour)

	// 3. GET /app/sso/callback?code=mock-code&state=...
	wCallback := httptest.NewRecorder()
	rCallback := httptest.NewRequest(http.MethodGet, "/app/sso/callback?code=mock-code&state="+url.QueryEscape(signedState), nil)
	if err := sso.ServeHTTP(wCallback, rCallback, "callback"); err != nil {
		t.Fatalf("handleCallback: %v", err)
	}
	if wCallback.Code != http.StatusFound {
		t.Fatalf("callback redirect status: %d (body: %s)", wCallback.Code, wCallback.Body.String())
	}
	if wCallback.Header().Get("Location") != "/app/actions" {
		t.Fatalf("expected redirect to /app/actions, got %s", wCallback.Header().Get("Location"))
	}

	// Check session hook was invoked with correct clinical identity
	if sessionCreatedFor.Subject != sub || sessionCreatedFor.Email != email {
		t.Fatalf("session identity mismatch: %+v", sessionCreatedFor)
	}
	if sessionCreatedFor.AssignedRole != httpx.RoleViewer || !sessionCreatedFor.Duty.CanBreakGlass {
		t.Fatalf("role / break-glass mismatch: %+v", sessionCreatedFor)
	}
	if sessionCreatedFor.SafeSubjectDigest == "" {
		t.Fatal("expected non-empty safe subject digest")
	}

	// 4. POST /app/sso/simulate
	simBody := `{"provider_id":"manipal_entra","subject":"sim-doc-99","email":"sim@manipal.edu","groups":["Emergency-Clinicians"],"next":"/app/approvals"}`
	wSim := httptest.NewRecorder()
	rSim := httptest.NewRequest(http.MethodPost, "/app/sso/simulate", strings.NewReader(simBody))
	if err := sso.ServeHTTP(wSim, rSim, "simulate"); err != nil {
		t.Fatalf("handleSimulate: %v", err)
	}
	if wSim.Code != http.StatusOK {
		t.Fatalf("simulate status: %d (body: %s)", wSim.Code, wSim.Body.String())
	}
	if !strings.Contains(wSim.Body.String(), `"sim-doc-99"`) {
		t.Fatalf("unexpected simulate response: %s", wSim.Body.String())
	}
}

func TestConsoleSSOIntegration(t *testing.T) {
	pConfig := NewOktaConfig(
		"test_okta",
		"hospital.okta.com",
		"okta-client-id",
		"secret",
		"http://127.0.0.1:8080/app/sso/callback",
		RoleMappingConfig{
			DefaultRole: httpx.RoleViewer,
		},
	)
	sso, err := NewSSOService(SSOOptions{
		Providers: []ProviderConfig{pConfig},
	})
	if err != nil {
		t.Fatal(err)
	}

	c, err := New(Options{
		Sessions: fakeSessions{ok: false}, // signed out
		FS:       buildFS(),
		SSO:      sso,
	})
	if err != nil {
		t.Fatal(err)
	}

	if c.SSO() != sso {
		t.Fatal("c.SSO() must return configured SSO service")
	}

	srv := httpx.New(httpx.Options{})
	srv.Mount(c.Routes()...)

	// 1. /app/sso/providers should be accessible signed out
	rec := get(srv, "GET", "/app/sso/providers")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK for /app/sso/providers, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"test_okta"`) {
		t.Fatalf("expected test_okta in providers response: %s", rec.Body.String())
	}

	// 2. /app/sso/login redirects to IdP authorize URL
	recLogin := get(srv, "GET", "/app/sso/login?provider=test_okta")
	if recLogin.Code != http.StatusFound {
		t.Fatalf("expected 302 Found for /app/sso/login, got %d", recLogin.Code)
	}
	if !strings.Contains(recLogin.Header().Get("Location"), "https://hospital.okta.com/oauth2/v1/authorize") {
		t.Fatalf("unexpected login redirect location: %s", recLogin.Header().Get("Location"))
	}

	// 3. Normal /app screen still redirects to /login when signed out
	recActions := get(srv, "GET", "/app/actions")
	if recActions.Code != http.StatusFound || !strings.HasPrefix(recActions.Header().Get("Location"), "/login?next=") {
		t.Fatalf("expected redirect to /login for /app/actions, got %d (loc: %s)", recActions.Code, recActions.Header().Get("Location"))
	}
}

func TestLoadSSOConfig(t *testing.T) {
	// 1. JSON String parsing
	rawJSON := `{
		"mock_idp_enabled": true,
		"state_secret_hex": "0102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f20",
		"digest_salt_hex": "616263646566",
		"providers": [
			{
				"id": "entra_main",
				"type": "entra_id",
				"display_name": "Hospital Entra ID",
				"issuer": "https://login.microsoftonline.com/tenant-123/v2.0",
				"client_id": "client-456",
				"redirect_uri": "https://hospital.local/app/sso/callback"
			}
		]
	}`

	opts, err := LoadSSOConfig(rawJSON, nil)
	if err != nil {
		t.Fatalf("LoadSSOConfig failed on JSON: %v", err)
	}
	if !opts.MockIdPEnabled {
		t.Fatalf("expected MockIdPEnabled=true")
	}
	if len(opts.Providers) != 1 || opts.Providers[0].ID != "entra_main" {
		t.Fatalf("unexpected providers: %+v", opts.Providers)
	}
	if hex.EncodeToString(opts.StateSecret) != "0102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f20" {
		t.Fatalf("unexpected state secret: %x", opts.StateSecret)
	}
	if string(opts.DigestSalt) != "abcdef" {
		t.Fatalf("unexpected digest salt: %s", string(opts.DigestSalt))
	}

	// 2. File parsing with env overrides
	tmpFile := filepath.Join(t.TempDir(), "sso.json")
	if err := os.WriteFile(tmpFile, []byte(rawJSON), 0600); err != nil {
		t.Fatal(err)
	}

	t.Setenv("DEEDGLASS_SSO_STATE_SECRET", "env_override_secret")
	t.Setenv("DEEDGLASS_SSO_SUBJECT_SALT", "env_salt_override")

	optsFromFile, err := LoadSSOConfig(tmpFile, nil)
	if err != nil {
		t.Fatalf("LoadSSOConfig failed on file: %v", err)
	}
	if string(optsFromFile.StateSecret) != "env_override_secret" {
		t.Fatalf("expected env_override_secret, got %s", string(optsFromFile.StateSecret))
	}
	if string(optsFromFile.DigestSalt) != "env_salt_override" {
		t.Fatalf("expected env_salt_override, got %s", string(optsFromFile.DigestSalt))
	}

	// 3. Error cases
	if _, err := LoadSSOConfig("", nil); err == nil {
		t.Fatalf("expected error for empty config")
	}
	if _, err := LoadSSOConfig("/path/to/nonexistent/file.json", nil); err == nil {
		t.Fatalf("expected error for non-existent file")
	}
	if _, err := LoadSSOConfig("{invalid-json", nil); err == nil {
		t.Fatalf("expected error for malformed json")
	}
}
