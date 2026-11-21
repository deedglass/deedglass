package console

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/deedglass/deedglass/internal/connector/egress"
	"github.com/deedglass/deedglass/internal/httpx"
)

// ProviderType identifies the identity provider category.
type ProviderType string

const (
	ProviderEntraID ProviderType = "entra_id" // Microsoft Entra ID (Azure AD)
	ProviderOkta    ProviderType = "okta"     // Okta Workforce Identity
	ProviderGoogle  ProviderType = "google"   // Google Workspace
	ProviderGeneric ProviderType = "generic"  // Generic OpenID Connect
)

// DutyProfile represents the clinical duty rota and ward assignment for a clinician.
// It supports non-SaMD operational governance such as emergency break-glass authorization (CH-07).
type DutyProfile struct {
	Ward            string `json:"ward,omitempty"`              // e.g. "Emergency Department", "ICU", "Ward 4B"
	RoleTitle       string `json:"role_title,omitempty"`        // e.g. "Emergency Senior Consultant", "Triage Nurse"
	CanBreakGlass   bool   `json:"can_break_glass,omitempty"`   // Eligible for emergency break-glass elevation
	RequireFourEyes bool   `json:"require_four_eyes,omitempty"` // Requires dual-clinician sign-off
}

// RoleMappingConfig maps identity provider groups or claims to Deedglass roles and duty profiles.
type RoleMappingConfig struct {
	GroupsClaim  string                 `json:"groups_claim,omitempty"`  // Claim holding groups (defaults to "groups")
	OwnerGroups  []string               `json:"owner_groups,omitempty"`  // Groups mapped to httpx.RoleOwner
	AdminGroups  []string               `json:"admin_groups,omitempty"`  // Groups mapped to httpx.RoleAdmin
	ViewerGroups []string               `json:"viewer_groups,omitempty"` // Groups mapped to httpx.RoleViewer
	DefaultRole  httpx.Role             `json:"default_role,omitempty"`  // Fallback role if no group matches (default RoleViewer)
	FailClosed   bool                   `json:"fail_closed,omitempty"`   // Refuse sign-in if no group matches
	DutyRota     map[string]DutyProfile `json:"duty_rota,omitempty"`     // IdP Group name -> DutyProfile mapping
}

// ProviderConfig specifies the settings for an enterprise hospital OIDC identity provider.
type ProviderConfig struct {
	ID           string            `json:"id"`                      // Unique identifier (e.g. "manipal_entra", "cleveland_okta")
	Type         ProviderType      `json:"type"`                    // entra_id, okta, google, or generic
	DisplayName  string            `json:"display_name"`            // User-visible label (e.g. "Manipal Hospital SSO")
	Issuer       string            `json:"issuer"`                  // OIDC issuer URL (e.g. https://login.microsoftonline.com/{tenant}/v2.0)
	ClientID     string            `json:"client_id"`               // OAuth2 Client ID
	ClientSecret string            `json:"client_secret,omitempty"` // OAuth2 Client Secret (optional if using PKCE public client)
	RedirectURI  string            `json:"redirect_uri"`            // Callback URL (e.g. https://deedglass.hospital.local/app/sso/callback)
	AuthURL      string            `json:"auth_url,omitempty"`      // Explicit authorization endpoint (discovered if empty)
	TokenURL     string            `json:"token_url,omitempty"`     // Explicit token endpoint (discovered if empty)
	JWKSURL      string            `json:"jwks_url,omitempty"`      // Explicit JWKS endpoint (discovered if empty)
	Scopes       []string          `json:"scopes,omitempty"`        // Scopes requested (defaults to ["openid", "profile", "email"])
	RoleMapping  RoleMappingConfig `json:"role_mapping,omitempty"`  // Group-to-role and clinical duty mapping
}

// ClinicianIdentity is the authenticated identity of a hospital staff member.
type ClinicianIdentity struct {
	ProviderID        string      `json:"provider_id"`         // Provider identifier
	Subject           string      `json:"subject"`             // OIDC unique subject identifier ("sub")
	Email             string      `json:"email"`               // Clinician work email address
	Name              string      `json:"name,omitempty"`      // Clinician full name
	Groups            []string    `json:"groups,omitempty"`    // Raw group memberships from IdP
	AssignedRole      httpx.Role  `json:"assigned_role"`       // Resulting Deedglass role (owner, admin, viewer)
	Duty              DutyProfile `json:"duty"`                // Clinical duty rota and break-glass capabilities
	SafeSubjectDigest string      `json:"safe_subject_digest"` // Salted zero-PHI cryptographic digest for non-SaMD audit ledgers
	IssuedAt          time.Time   `json:"issued_at"`           // Authentication timestamp
}

// SessionCreator handles session establishment (e.g. writing Deedglass session cookies).
type SessionCreator interface {
	CreateSessionForIdentity(ctx context.Context, w http.ResponseWriter, r *http.Request, id ClinicianIdentity) error
}

// SessionCreatorFunc allows using a function as a SessionCreator.
type SessionCreatorFunc func(ctx context.Context, w http.ResponseWriter, r *http.Request, id ClinicianIdentity) error

func (f SessionCreatorFunc) CreateSessionForIdentity(ctx context.Context, w http.ResponseWriter, r *http.Request, id ClinicianIdentity) error {
	return f(ctx, w, r, id)
}

// SSOOptions configures the Hospital OIDC SSO Service.
type SSOOptions struct {
	Providers      []ProviderConfig // Configured hospital identity providers
	StateSecret    []byte           // Secret for HMAC-signing state tokens (generated if empty)
	DigestSalt     []byte           // Salt for zero-PHI safe subject digest calculation
	SessionCreator SessionCreator   // Optional session hook; if nil, a signed cookie is used
	HTTPClient     *http.Client     // HTTP client for OIDC token exchange and JWKS fetch (defaults to http.DefaultClient)
	Logger         *slog.Logger     // Structured logger
	Now            func() time.Time // Clock for testing
	MockIdPEnabled bool             // Allows simulated IdP flows for offline testing and air-gapped demo
}

// SSOService manages hospital OIDC SSO authentication, token validation, and clinical role mapping.
type SSOService struct {
	opts        SSOOptions
	providers   map[string]ProviderConfig
	stateSecret []byte
	digestSalt  []byte
	jwksCache   sync.Map // providerID -> *cachedJWKS
	log         *slog.Logger
	now         func() time.Time
	httpClient  *http.Client
}

type cachedJWKS struct {
	set       *JWKSet
	expiresAt time.Time
}

// NewSSOService creates a new Hospital OIDC SSO Service.
func NewSSOService(opts SSOOptions) (*SSOService, error) {
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	httpClient := opts.HTTPClient
	if httpClient == nil {
		httpClient = egress.FirstParty(egress.FirstPartyOptions{Timeout: 10 * time.Second, Proxy: true})
	}
	stateSecret := opts.StateSecret
	if len(stateSecret) == 0 {
		stateSecret = make([]byte, 32)
		if _, err := io.ReadFull(rand.Reader, stateSecret); err != nil {
			return nil, fmt.Errorf("sso: failed to generate state secret: %w", err)
		}
	}
	digestSalt := opts.DigestSalt
	if len(digestSalt) == 0 {
		digestSalt = []byte("deedglass_zero_phi_salt")
	}

	pMap := make(map[string]ProviderConfig, len(opts.Providers))
	for _, p := range opts.Providers {
		if p.ID == "" {
			return nil, errors.New("sso: provider id must not be empty")
		}
		if p.ClientID == "" {
			return nil, fmt.Errorf("sso: provider %q client_id must not be empty", p.ID)
		}
		p = normalizeProviderConfig(p)
		pMap[p.ID] = p
	}

	return &SSOService{
		opts:        opts,
		providers:   pMap,
		stateSecret: stateSecret,
		digestSalt:  digestSalt,
		log:         opts.Logger,
		now:         now,
		httpClient:  httpClient,
	}, nil
}

// normalizeProviderConfig applies sensible OIDC defaults based on provider type.
func normalizeProviderConfig(p ProviderConfig) ProviderConfig {
	if len(p.Scopes) == 0 {
		p.Scopes = []string{"openid", "profile", "email"}
	}
	if p.RoleMapping.GroupsClaim == "" {
		p.RoleMapping.GroupsClaim = "groups"
	}
	if p.RoleMapping.DefaultRole == "" {
		p.RoleMapping.DefaultRole = httpx.RoleViewer
	}

	switch p.Type {
	case ProviderEntraID:
		if p.DisplayName == "" {
			p.DisplayName = "Microsoft Entra ID"
		}
	case ProviderOkta:
		if p.DisplayName == "" {
			p.DisplayName = "Okta Workforce"
		}
	case ProviderGoogle:
		if p.DisplayName == "" {
			p.DisplayName = "Google Workspace"
		}
		if p.Issuer == "" {
			p.Issuer = "https://accounts.google.com"
		}
		if p.AuthURL == "" {
			p.AuthURL = "https://accounts.google.com/o/oauth2/v2/auth"
		}
		if p.TokenURL == "" {
			p.TokenURL = "https://oauth2.googleapis.com/token"
		}
		if p.JWKSURL == "" {
			p.JWKSURL = "https://www.googleapis.com/oauth2/v3/certs"
		}
	case ProviderGeneric:
		if p.DisplayName == "" {
			p.DisplayName = "Hospital SSO"
		}
	}
	return p
}

// NewEntraIDConfig constructs a ProviderConfig preset for Microsoft Entra ID.
func NewEntraIDConfig(id, tenantID, clientID, clientSecret, redirectURI string, mapping RoleMappingConfig) ProviderConfig {
	tenantID = strings.TrimSpace(tenantID)
	issuer := fmt.Sprintf("https://login.microsoftonline.com/%s/v2.0", tenantID)
	authURL := fmt.Sprintf("https://login.microsoftonline.com/%s/oauth2/v2.0/authorize", tenantID)
	tokenURL := fmt.Sprintf("https://login.microsoftonline.com/%s/oauth2/v2.0/token", tenantID)
	jwksURL := fmt.Sprintf("https://login.microsoftonline.com/%s/discovery/v2.0/keys", tenantID)

	if mapping.GroupsClaim == "" {
		mapping.GroupsClaim = "groups" // In Entra ID, group object IDs appear under "groups" or directory roles under "roles"
	}

	return ProviderConfig{
		ID:           id,
		Type:         ProviderEntraID,
		DisplayName:  "Microsoft Entra ID",
		Issuer:       issuer,
		ClientID:     clientID,
		ClientSecret: clientSecret,
		RedirectURI:  redirectURI,
		AuthURL:      authURL,
		TokenURL:     tokenURL,
		JWKSURL:      jwksURL,
		Scopes:       []string{"openid", "profile", "email"},
		RoleMapping:  mapping,
	}
}

// NewOktaConfig constructs a ProviderConfig preset for Okta.
func NewOktaConfig(id, domain, clientID, clientSecret, redirectURI string, mapping RoleMappingConfig) ProviderConfig {
	domain = strings.TrimPrefix(strings.TrimSuffix(domain, "/"), "https://")
	issuer := fmt.Sprintf("https://%s", domain)
	authURL := fmt.Sprintf("https://%s/oauth2/v1/authorize", domain)
	tokenURL := fmt.Sprintf("https://%s/oauth2/v1/token", domain)
	jwksURL := fmt.Sprintf("https://%s/oauth2/v1/keys", domain)

	if mapping.GroupsClaim == "" {
		mapping.GroupsClaim = "groups"
	}

	return ProviderConfig{
		ID:           id,
		Type:         ProviderOkta,
		DisplayName:  "Okta",
		Issuer:       issuer,
		ClientID:     clientID,
		ClientSecret: clientSecret,
		RedirectURI:  redirectURI,
		AuthURL:      authURL,
		TokenURL:     tokenURL,
		JWKSURL:      jwksURL,
		Scopes:       []string{"openid", "profile", "email", "groups"},
		RoleMapping:  mapping,
	}
}

// NewGoogleConfig constructs a ProviderConfig preset for Google Workspace.
func NewGoogleConfig(id, clientID, clientSecret, redirectURI string, mapping RoleMappingConfig) ProviderConfig {
	return ProviderConfig{
		ID:           id,
		Type:         ProviderGoogle,
		DisplayName:  "Google Workspace",
		Issuer:       "https://accounts.google.com",
		ClientID:     clientID,
		ClientSecret: clientSecret,
		RedirectURI:  redirectURI,
		AuthURL:      "https://accounts.google.com/o/oauth2/v2/auth",
		TokenURL:     "https://oauth2.googleapis.com/token",
		JWKSURL:      "https://www.googleapis.com/oauth2/v3/certs",
		Scopes:       []string{"openid", "profile", "email"},
		RoleMapping:  mapping,
	}
}

// PKCE holds the cryptographic verifier and challenge for RFC 7636.
type PKCE struct {
	Verifier  string `json:"verifier"`
	Challenge string `json:"challenge"`
	Method    string `json:"method"`
}

// GeneratePKCE creates a new RFC 7636 PKCE verifier and S256 challenge.
func GeneratePKCE() (PKCE, error) {
	raw := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, raw); err != nil {
		return PKCE{}, fmt.Errorf("sso: pkce rand read: %w", err)
	}
	verifier := base64.RawURLEncoding.EncodeToString(raw)
	h := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(h[:])
	return PKCE{
		Verifier:  verifier,
		Challenge: challenge,
		Method:    "S256",
	}, nil
}

// VerifyPKCE verifies that the verifier corresponds to the challenge under S256.
func VerifyPKCE(verifier, challenge string) bool {
	h := sha256.Sum256([]byte(verifier))
	expected := base64.RawURLEncoding.EncodeToString(h[:])
	return subtle.ConstantTimeCompare([]byte(expected), []byte(challenge)) == 1
}

// StatePayload is stored in the HMAC-signed state token.
type StatePayload struct {
	ProviderID string `json:"provider_id"`
	Nonce      string `json:"nonce"`
	Verifier   string `json:"verifier"`
	Next       string `json:"next"`
	ExpiresAt  int64  `json:"expires_at"` // Unix timestamp in seconds
}

// SignState creates an HMAC-SHA256 signed state token string.
func (s *SSOService) SignState(payload StatePayload) (string, error) {
	data, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	encData := base64.RawURLEncoding.EncodeToString(data)
	mac := hmac.New(sha256.New, s.stateSecret)
	mac.Write([]byte(encData))
	sig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return encData + "." + sig, nil
}

// VerifyState parses and validates an HMAC-SHA256 signed state token string.
func (s *SSOService) VerifyState(token string) (*StatePayload, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 2 {
		return nil, errors.New("sso: invalid state format")
	}
	encData, sig := parts[0], parts[1]

	mac := hmac.New(sha256.New, s.stateSecret)
	mac.Write([]byte(encData))
	expectedSig := mac.Sum(nil)
	actualSig, err := base64.RawURLEncoding.DecodeString(sig)
	if err != nil || subtle.ConstantTimeCompare(expectedSig, actualSig) != 1 {
		return nil, errors.New("sso: state signature verification failed")
	}

	data, err := base64.RawURLEncoding.DecodeString(encData)
	if err != nil {
		return nil, errors.New("sso: corrupted state payload")
	}
	var payload StatePayload
	if err := json.Unmarshal(data, &payload); err != nil {
		return nil, errors.New("sso: invalid state payload json")
	}

	nowSec := s.now().Unix()
	if payload.ExpiresAt < nowSec {
		return nil, errors.New("sso: state token expired")
	}
	return &payload, nil
}

// SafeDigest computes a salted SHA-256 digest of a clinician identifier (Zero-PHI non-SaMD assurance).
func (s *SSOService) SafeDigest(issuer, subject string) string {
	h := sha256.New()
	h.Write(s.digestSalt)
	h.Write([]byte(":"))
	h.Write([]byte(issuer))
	h.Write([]byte(":"))
	h.Write([]byte(subject))
	return hex.EncodeToString(h.Sum(nil))
}

// ResolveRole maps clinician group memberships to a Deedglass role and clinical duty profile.
func ResolveRole(mapping RoleMappingConfig, groups []string) (httpx.Role, DutyProfile, error) {
	groupSet := make(map[string]struct{}, len(groups))
	for _, g := range groups {
		groupSet[strings.TrimSpace(g)] = struct{}{}
	}

	// Priority 1: Owner
	for _, og := range mapping.OwnerGroups {
		if _, ok := groupSet[strings.TrimSpace(og)]; ok {
			duty := resolveDuty(mapping.DutyRota, groupSet)
			return httpx.RoleOwner, duty, nil
		}
	}

	// Priority 2: Admin
	for _, ag := range mapping.AdminGroups {
		if _, ok := groupSet[strings.TrimSpace(ag)]; ok {
			duty := resolveDuty(mapping.DutyRota, groupSet)
			return httpx.RoleAdmin, duty, nil
		}
	}

	// Priority 3: Viewer
	for _, vg := range mapping.ViewerGroups {
		if _, ok := groupSet[strings.TrimSpace(vg)]; ok {
			duty := resolveDuty(mapping.DutyRota, groupSet)
			return httpx.RoleViewer, duty, nil
		}
	}

	// No explicit group matched. Check FailClosed
	if mapping.FailClosed {
		return "", DutyProfile{}, errors.New("sso: clinician does not belong to any authorized hospital groups")
	}

	defRole := mapping.DefaultRole
	if defRole == "" {
		defRole = httpx.RoleViewer
	}
	duty := resolveDuty(mapping.DutyRota, groupSet)
	return defRole, duty, nil
}

func resolveDuty(dutyRota map[string]DutyProfile, groupSet map[string]struct{}) DutyProfile {
	if len(dutyRota) == 0 {
		return DutyProfile{}
	}
	// Sorted iteration for deterministic evaluation
	keys := make([]string, 0, len(dutyRota))
	for k := range dutyRota {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	for _, k := range keys {
		if _, ok := groupSet[strings.TrimSpace(k)]; ok {
			return dutyRota[k]
		}
	}
	return DutyProfile{}
}

// BuildAuthURL constructs the complete IdP authorization URL with PKCE and HMAC state.
func (s *SSOService) BuildAuthURL(providerID, next string) (string, error) {
	p, ok := s.providers[providerID]
	if !ok {
		return "", fmt.Errorf("sso: unknown provider %q", providerID)
	}
	if p.AuthURL == "" {
		return "", fmt.Errorf("sso: provider %q has no auth_url configured", providerID)
	}

	pkce, err := GeneratePKCE()
	if err != nil {
		return "", err
	}

	nonceBytes := make([]byte, 16)
	if _, err := io.ReadFull(rand.Reader, nonceBytes); err != nil {
		return "", err
	}
	nonce := hex.EncodeToString(nonceBytes)

	safeNext := SafeNext(next)
	if safeNext == "" {
		safeNext = "/app"
	}

	statePayload := StatePayload{
		ProviderID: providerID,
		Nonce:      nonce,
		Verifier:   pkce.Verifier,
		Next:       safeNext,
		ExpiresAt:  s.now().Add(10 * time.Minute).Unix(),
	}

	signedState, err := s.SignState(statePayload)
	if err != nil {
		return "", fmt.Errorf("sso: sign state: %w", err)
	}

	v := url.Values{}
	v.Set("client_id", p.ClientID)
	v.Set("response_type", "code")
	v.Set("redirect_uri", p.RedirectURI)
	v.Set("scope", strings.Join(p.Scopes, " "))
	v.Set("state", signedState)
	v.Set("nonce", nonce)
	v.Set("code_challenge", pkce.Challenge)
	v.Set("code_challenge_method", pkce.Method)

	u, err := url.Parse(p.AuthURL)
	if err != nil {
		return "", fmt.Errorf("sso: invalid auth_url: %w", err)
	}
	q := u.Query()
	for k, vals := range v {
		for _, val := range vals {
			q.Add(k, val)
		}
	}
	u.RawQuery = q.Encode()
	return u.String(), nil
}

// JWKSet holds parsed JSON Web Keys for cryptographic signature verification.
type JWKSet struct {
	Keys []JWK `json:"keys"`
}

// JWK is a single JSON Web Key.
type JWK struct {
	Kty string `json:"kty"`
	Kid string `json:"kid"`
	Use string `json:"use"`
	Alg string `json:"alg"`
	N   string `json:"n,omitempty"` // RSA modulus (base64url)
	E   string `json:"e,omitempty"` // RSA exponent (base64url)
	Crv string `json:"crv,omitempty"`
	X   string `json:"x,omitempty"` // EC X coordinate (base64url)
	Y   string `json:"y,omitempty"` // EC Y coordinate (base64url)
}

// ParseJWKSet parses a JWKS JSON document.
func ParseJWKSet(data []byte) (*JWKSet, error) {
	var set JWKSet
	if err := json.Unmarshal(data, &set); err != nil {
		return nil, fmt.Errorf("sso: parse jwks: %w", err)
	}
	return &set, nil
}

// IDTokenClaims holds verified claims from an OpenID Connect ID Token.
type IDTokenClaims struct {
	Issuer            string         `json:"iss"`
	Subject           string         `json:"sub"`
	Audience          any            `json:"aud"` // string or []string
	ExpiresAt         int64          `json:"exp"`
	NotBefore         int64          `json:"nbf,omitempty"`
	IssuedAt          int64          `json:"iat,omitempty"`
	Nonce             string         `json:"nonce,omitempty"`
	Email             string         `json:"email,omitempty"`
	EmailVerified     bool           `json:"email_verified,omitempty"`
	Name              string         `json:"name,omitempty"`
	PreferredUsername string         `json:"preferred_username,omitempty"`
	RawClaims         map[string]any `json:"-"`
}

// VerifyIDToken cryptographically verifies an ID Token JWS against a JWK Set.
func VerifyIDToken(rawToken string, jwks *JWKSet, expectedIssuer, expectedClientID, expectedNonce string, now time.Time) (*IDTokenClaims, error) {
	parts := strings.Split(rawToken, ".")
	if len(parts) != 3 {
		return nil, errors.New("sso: id_token must have 3 segments")
	}
	headerBytes, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return nil, errors.New("sso: invalid id_token header encoding")
	}
	payloadBytes, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, errors.New("sso: invalid id_token payload encoding")
	}
	sigBytes, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return nil, errors.New("sso: invalid id_token signature encoding")
	}

	var header struct {
		Alg string `json:"alg"`
		Kid string `json:"kid"`
	}
	if err := json.Unmarshal(headerBytes, &header); err != nil {
		return nil, errors.New("sso: invalid id_token header json")
	}

	signedContent := []byte(parts[0] + "." + parts[1])

	// Find matching key in JWKS
	var matchingKey *JWK
	for i := range jwks.Keys {
		k := &jwks.Keys[i]
		if header.Kid == "" || k.Kid == "" || k.Kid == header.Kid {
			matchingKey = k
			break
		}
	}
	if matchingKey == nil {
		return nil, fmt.Errorf("sso: no matching key found in jwks for kid %q", header.Kid)
	}

	// Verify cryptographic signature
	switch header.Alg {
	case "RS256":
		if matchingKey.Kty != "RSA" || matchingKey.N == "" || matchingKey.E == "" {
			return nil, errors.New("sso: jwk key is not valid RSA")
		}
		pubKey, err := parseRSAPublicKey(matchingKey.N, matchingKey.E)
		if err != nil {
			return nil, fmt.Errorf("sso: parse rsa public key: %w", err)
		}
		h := sha256.Sum256(signedContent)
		if err := rsa.VerifyPKCS1v15(pubKey, crypto.SHA256, h[:], sigBytes); err != nil {
			return nil, errors.New("sso: RSA signature verification failed")
		}
	case "ES256":
		if matchingKey.Kty != "EC" || matchingKey.X == "" || matchingKey.Y == "" {
			return nil, errors.New("sso: jwk key is not valid EC")
		}
		pubKey, err := parseECPublicKey(matchingKey.X, matchingKey.Y)
		if err != nil {
			return nil, fmt.Errorf("sso: parse ec public key: %w", err)
		}
		h := sha256.Sum256(signedContent)
		if !verifyECDSASignature(pubKey, h[:], sigBytes) {
			return nil, errors.New("sso: ECDSA signature verification failed")
		}
	default:
		return nil, fmt.Errorf("sso: unsupported token algorithm %q", header.Alg)
	}

	// Parse and validate claims
	var claims IDTokenClaims
	if err := json.Unmarshal(payloadBytes, &claims); err != nil {
		return nil, errors.New("sso: invalid id_token payload json")
	}
	var rawClaims map[string]any
	if err := json.Unmarshal(payloadBytes, &rawClaims); err == nil {
		claims.RawClaims = rawClaims
	}

	// Validate Issuer
	if expectedIssuer != "" && claims.Issuer != expectedIssuer {
		return nil, fmt.Errorf("sso: issuer mismatch (expected %q, got %q)", expectedIssuer, claims.Issuer)
	}

	// Validate Audience
	if expectedClientID != "" && !audienceMatches(claims.Audience, expectedClientID) {
		return nil, fmt.Errorf("sso: audience mismatch for client_id %q", expectedClientID)
	}

	// Validate Expiry (60-second clock skew tolerance)
	nowUnix := now.Unix()
	if claims.ExpiresAt < nowUnix-60 {
		return nil, errors.New("sso: token has expired")
	}

	// Validate Not Before (if set)
	if claims.NotBefore > 0 && claims.NotBefore > nowUnix+60 {
		return nil, errors.New("sso: token used before valid time (nbf)")
	}

	// Validate Nonce (if expected)
	if expectedNonce != "" && claims.Nonce != expectedNonce {
		return nil, errors.New("sso: nonce mismatch")
	}

	return &claims, nil
}

func parseRSAPublicKey(nStr, eStr string) (*rsa.PublicKey, error) {
	nBytes, err := base64.RawURLEncoding.DecodeString(nStr)
	if err != nil {
		return nil, err
	}
	eBytes, err := base64.RawURLEncoding.DecodeString(eStr)
	if err != nil {
		return nil, err
	}
	n := new(big.Int).SetBytes(nBytes)
	e := int(new(big.Int).SetBytes(eBytes).Int64())
	return &rsa.PublicKey{N: n, E: e}, nil
}

func parseECPublicKey(xStr, yStr string) (*ecdsa.PublicKey, error) {
	xBytes, err := base64.RawURLEncoding.DecodeString(xStr)
	if err != nil {
		return nil, err
	}
	yBytes, err := base64.RawURLEncoding.DecodeString(yStr)
	if err != nil {
		return nil, err
	}
	curve := elliptic.P256()
	return &ecdsa.PublicKey{
		Curve: curve,
		X:     new(big.Int).SetBytes(xBytes),
		Y:     new(big.Int).SetBytes(yBytes),
	}, nil
}

func verifyECDSASignature(pub *ecdsa.PublicKey, hash, sig []byte) bool {
	if len(sig) != 64 {
		// Attempt ASN.1 DER decode if not IEEE P1363 (r || s)
		return ecdsa.VerifyASN1(pub, hash, sig)
	}
	r := new(big.Int).SetBytes(sig[:32])
	s := new(big.Int).SetBytes(sig[32:])
	return ecdsa.Verify(pub, hash, r, s)
}

func audienceMatches(aud any, expected string) bool {
	switch v := aud.(type) {
	case string:
		return v == expected
	case []any:
		for _, item := range v {
			if s, ok := item.(string); ok && s == expected {
				return true
			}
		}
	case []string:
		for _, item := range v {
			if item == expected {
				return true
			}
		}
	}
	return false
}

// ExtractGroups retrieves group strings from token claims according to configuration.
func ExtractGroups(rawClaims map[string]any, claimName string) []string {
	if claimName == "" {
		claimName = "groups"
	}
	val, ok := rawClaims[claimName]
	if !ok {
		// Fallbacks: check "roles" or "wids"
		if val, ok = rawClaims["roles"]; !ok {
			val, ok = rawClaims["wids"]
		}
	}
	if !ok || val == nil {
		return nil
	}

	var res []string
	switch v := val.(type) {
	case []any:
		for _, item := range v {
			if s, ok := item.(string); ok && strings.TrimSpace(s) != "" {
				res = append(res, strings.TrimSpace(s))
			}
		}
	case []string:
		for _, s := range v {
			if strings.TrimSpace(s) != "" {
				res = append(res, strings.TrimSpace(s))
			}
		}
	case string:
		// Space or comma-delimited string
		for _, part := range strings.FieldsFunc(v, func(r rune) bool { return r == ' ' || r == ',' }) {
			if strings.TrimSpace(part) != "" {
				res = append(res, strings.TrimSpace(part))
			}
		}
	}
	return res
}

// ServeHTTP routes and handles SSO requests under the /app/sso/ namespace.
func (s *SSOService) ServeHTTP(w http.ResponseWriter, r *http.Request, action string) error {
	action = strings.TrimPrefix(action, "/")
	switch {
	case action == "providers" || action == "providers/":
		return s.handleProviders(w, r)
	case action == "login" || action == "login/":
		return s.handleLogin(w, r)
	case action == "callback" || action == "callback/":
		return s.handleCallback(w, r)
	case action == "simulate" || action == "simulate/":
		return s.handleSimulate(w, r)
	default:
		http.NotFound(w, r)
		return nil
	}
}

// ProviderInfo represents public metadata for configured identity providers.
type ProviderInfo struct {
	ID          string       `json:"id"`
	Type        ProviderType `json:"type"`
	DisplayName string       `json:"display_name"`
	LoginURL    string       `json:"login_url"`
}

func (s *SSOService) handleProviders(w http.ResponseWriter, r *http.Request) error {
	keys := make([]string, 0, len(s.providers))
	for k := range s.providers {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	list := make([]ProviderInfo, 0, len(keys))
	for _, k := range keys {
		p := s.providers[k]
		list = append(list, ProviderInfo{
			ID:          p.ID,
			Type:        p.Type,
			DisplayName: p.DisplayName,
			LoginURL:    "/app/sso/login?provider=" + url.QueryEscape(p.ID),
		})
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	return json.NewEncoder(w).Encode(map[string]any{
		"object":    "list",
		"providers": list,
	})
}

func (s *SSOService) handleLogin(w http.ResponseWriter, r *http.Request) error {
	pID := r.URL.Query().Get("provider")
	if pID == "" {
		// Pick first available provider if unspecified
		for k := range s.providers {
			pID = k
			break
		}
	}
	if pID == "" {
		http.Error(w, `{"error":"no_providers_configured","message":"No hospital SSO providers are configured"}`, http.StatusBadRequest)
		return nil
	}

	next := r.URL.Query().Get("next")
	authURL, err := s.BuildAuthURL(pID, next)
	if err != nil {
		s.log.Error("sso build auth url failed", "provider", pID, "err", err)
		http.Error(w, fmt.Sprintf(`{"error":"sso_init_failed","message":%q}`, err.Error()), http.StatusBadRequest)
		return nil
	}

	w.Header().Set("Location", authURL)
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusFound)
	return nil
}

func (s *SSOService) handleCallback(w http.ResponseWriter, r *http.Request) error {
	q := r.URL.Query()
	if errCode := q.Get("error"); errCode != "" {
		desc := q.Get("error_description")
		s.log.Warn("sso idp returned error", "error", errCode, "desc", desc)
		http.Error(w, fmt.Sprintf(`{"error":%q,"message":%q}`, errCode, desc), http.StatusUnauthorized)
		return nil
	}

	code := q.Get("code")
	state := q.Get("state")
	if code == "" || state == "" {
		http.Error(w, `{"error":"invalid_request","message":"Missing authorization code or state"}`, http.StatusBadRequest)
		return nil
	}

	statePayload, err := s.VerifyState(state)
	if err != nil {
		s.log.Warn("sso state verification failed", "err", err)
		http.Error(w, `{"error":"state_invalid","message":"The authentication state is invalid or expired. Please sign in again."}`, http.StatusBadRequest)
		return nil
	}

	p, ok := s.providers[statePayload.ProviderID]
	if !ok {
		http.Error(w, `{"error":"unknown_provider","message":"Unknown identity provider"}`, http.StatusBadRequest)
		return nil
	}

	// Token Exchange: code -> ID Token
	idTokenRaw, err := s.exchangeCode(r.Context(), p, code, statePayload.Verifier)
	if err != nil {
		s.log.Error("sso token exchange failed", "provider", p.ID, "err", err)
		http.Error(w, `{"error":"token_exchange_failed","message":"Failed to exchange authorization code with hospital IdP"}`, http.StatusBadGateway)
		return nil
	}

	// Fetch JWKS
	jwks, err := s.fetchJWKS(r.Context(), p)
	if err != nil {
		s.log.Error("sso fetch jwks failed", "provider", p.ID, "err", err)
		http.Error(w, `{"error":"jwks_fetch_failed","message":"Failed to retrieve hospital identity signing keys"}`, http.StatusBadGateway)
		return nil
	}

	// Verify ID Token
	claims, err := VerifyIDToken(idTokenRaw, jwks, p.Issuer, p.ClientID, statePayload.Nonce, s.now())
	if err != nil {
		s.log.Warn("sso id_token verification failed", "provider", p.ID, "err", err)
		http.Error(w, fmt.Sprintf(`{"error":"token_verification_failed","message":%q}`, err.Error()), http.StatusUnauthorized)
		return nil
	}

	// Extract groups and resolve role
	groups := ExtractGroups(claims.RawClaims, p.RoleMapping.GroupsClaim)
	role, duty, err := ResolveRole(p.RoleMapping, groups)
	if err != nil {
		s.log.Warn("sso role mapping refused access", "provider", p.ID, "subject", claims.Subject, "err", err)
		http.Error(w, `{"error":"access_denied","message":"Your hospital account is not assigned to an authorized Deedglass role."}`, http.StatusForbidden)
		return nil
	}

	email := claims.Email
	if email == "" {
		email = claims.PreferredUsername
	}
	name := claims.Name
	if name == "" {
		name = email
	}

	safeDigest := s.SafeDigest(p.Issuer, claims.Subject)

	identity := ClinicianIdentity{
		ProviderID:        p.ID,
		Subject:           claims.Subject,
		Email:             email,
		Name:              name,
		Groups:            groups,
		AssignedRole:      role,
		Duty:              duty,
		SafeSubjectDigest: safeDigest,
		IssuedAt:          s.now(),
	}

	s.log.Info("sso clinician signed in", "provider", p.ID, "safe_subject", safeDigest, "role", role, "ward", duty.Ward, "break_glass", duty.CanBreakGlass)

	// Create Session if hook configured
	if s.opts.SessionCreator != nil {
		if err := s.opts.SessionCreator.CreateSessionForIdentity(r.Context(), w, r, identity); err != nil {
			s.log.Error("sso session creation failed", "err", err)
			http.Error(w, `{"error":"session_creation_failed","message":"Failed to initialize local session"}`, http.StatusInternalServerError)
			return nil
		}
	} else {
		// Set lightweight signed SSO identity cookie
		setSSOCookie(w, identity, s.now().Add(12*time.Hour))
	}

	target := statePayload.Next
	if target == "" || SafeNext(target) == "" {
		target = "/app"
	}

	w.Header().Set("Location", target)
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusFound)
	return nil
}

func (s *SSOService) handleSimulate(w http.ResponseWriter, r *http.Request) error {
	if !s.opts.MockIdPEnabled {
		http.Error(w, `{"error":"forbidden","message":"Simulated SSO is only available in mock/test mode"}`, http.StatusForbidden)
		return nil
	}
	if r.Method != http.MethodPost {
		http.Error(w, `{"error":"method_not_allowed","message":"POST required"}`, http.StatusMethodNotAllowed)
		return nil
	}

	var req struct {
		ProviderID string   `json:"provider_id"`
		Subject    string   `json:"subject"`
		Email      string   `json:"email"`
		Name       string   `json:"name"`
		Groups     []string `json:"groups"`
		Next       string   `json:"next"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, fmt.Sprintf(`{"error":"bad_json","message":%q}`, err.Error()), http.StatusBadRequest)
		return nil
	}

	p, ok := s.providers[req.ProviderID]
	if !ok {
		// Use first provider if none passed
		for _, first := range s.providers {
			p = first
			ok = true
			break
		}
	}
	if !ok {
		http.Error(w, `{"error":"no_provider","message":"No providers configured"}`, http.StatusBadRequest)
		return nil
	}

	role, duty, err := ResolveRole(p.RoleMapping, req.Groups)
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"error":"access_denied","message":%q}`, err.Error()), http.StatusForbidden)
		return nil
	}

	safeDigest := s.SafeDigest(p.Issuer, req.Subject)
	identity := ClinicianIdentity{
		ProviderID:        p.ID,
		Subject:           req.Subject,
		Email:             req.Email,
		Name:              req.Name,
		Groups:            req.Groups,
		AssignedRole:      role,
		Duty:              duty,
		SafeSubjectDigest: safeDigest,
		IssuedAt:          s.now(),
	}

	if s.opts.SessionCreator != nil {
		if err := s.opts.SessionCreator.CreateSessionForIdentity(r.Context(), w, r, identity); err != nil {
			http.Error(w, fmt.Sprintf(`{"error":"session_err","message":%q}`, err.Error()), http.StatusInternalServerError)
			return nil
		}
	} else {
		setSSOCookie(w, identity, s.now().Add(12*time.Hour))
	}

	target := req.Next
	if target == "" || SafeNext(target) == "" {
		target = "/app"
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	return json.NewEncoder(w).Encode(map[string]any{
		"object":   "sso_simulation",
		"identity": identity,
		"redirect": target,
	})
}

func (s *SSOService) exchangeCode(ctx context.Context, p ProviderConfig, code, verifier string) (string, error) {
	if p.TokenURL == "" {
		return "", errors.New("token_url is not configured")
	}

	v := url.Values{}
	v.Set("grant_type", "authorization_code")
	v.Set("code", code)
	v.Set("redirect_uri", p.RedirectURI)
	v.Set("client_id", p.ClientID)
	if p.ClientSecret != "" {
		v.Set("client_secret", p.ClientSecret)
	}
	if verifier != "" {
		v.Set("code_verifier", verifier)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.TokenURL, strings.NewReader(v.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return "", fmt.Errorf("idp token endpoint returned status %d: %s", resp.StatusCode, string(body))
	}

	var tokenResp struct {
		IDToken     string `json:"id_token"`
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&tokenResp); err != nil {
		return "", fmt.Errorf("decode token response: %w", err)
	}
	if tokenResp.IDToken == "" {
		return "", errors.New("idp token response missing id_token")
	}
	return tokenResp.IDToken, nil
}

func (s *SSOService) fetchJWKS(ctx context.Context, p ProviderConfig) (*JWKSet, error) {
	if cached, ok := s.jwksCache.Load(p.ID); ok {
		c := cached.(*cachedJWKS)
		if s.now().Before(c.expiresAt) {
			return c.set, nil
		}
	}

	if p.JWKSURL == "" {
		return nil, errors.New("jwks_url is not configured")
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.JWKSURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("jwks endpoint returned status %d", resp.StatusCode)
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, 512*1024))
	if err != nil {
		return nil, err
	}

	set, err := ParseJWKSet(data)
	if err != nil {
		return nil, err
	}

	// Cache for 1 hour
	s.jwksCache.Store(p.ID, &cachedJWKS{
		set:       set,
		expiresAt: s.now().Add(1 * time.Hour),
	})
	return set, nil
}

// SetLocalJWKSet pre-populates or updates the in-memory JWKS cache (useful for tests and offline hospital environments).
func (s *SSOService) SetLocalJWKSet(providerID string, set *JWKSet, ttl time.Duration) {
	s.jwksCache.Store(providerID, &cachedJWKS{
		set:       set,
		expiresAt: s.now().Add(ttl),
	})
}

func setSSOCookie(w http.ResponseWriter, id ClinicianIdentity, expires time.Time) {
	val := fmt.Sprintf("%s|%s|%s|%s|%t", id.ProviderID, id.Subject, id.Email, string(id.AssignedRole), id.Duty.CanBreakGlass)
	http.SetCookie(w, &http.Cookie{
		Name:     "deedglass_sso_identity",
		Value:    base64.RawURLEncoding.EncodeToString([]byte(val)),
		Path:     "/",
		Expires:  expires,
		HttpOnly: true,
		Secure:   false, // Dev/local fallback; prod servers configure Secure
		SameSite: http.SameSiteLaxMode,
	})
}

// SSOConfigFile is the JSON representation for serializing/deserializing SSO configuration.
type SSOConfigFile struct {
	Providers      []ProviderConfig `json:"providers"`
	StateSecretHex string           `json:"state_secret_hex,omitempty"`
	StateSecret    string           `json:"state_secret,omitempty"`
	DigestSaltHex  string           `json:"digest_salt_hex,omitempty"`
	DigestSalt     string           `json:"digest_salt,omitempty"`
	MockIdPEnabled bool             `json:"mock_idp_enabled,omitempty"`
}

// LoadSSOConfig parses an SSO configuration from raw JSON or a filesystem path.
// It merges environment overrides:
// - DEEDGLASS_SSO_STATE_SECRET: overrides StateSecret
// - DEEDGLASS_SSO_SUBJECT_SALT: overrides DigestSalt
// - DEEDGLASS_SSO_MOCK_ENABLED: "1" or "true" enables mock IdP
func LoadSSOConfig(pathOrJSON string, log *slog.Logger) (*SSOOptions, error) {
	if strings.TrimSpace(pathOrJSON) == "" {
		return nil, errors.New("sso: configuration path or JSON is empty")
	}

	var raw []byte
	trimmed := strings.TrimSpace(pathOrJSON)
	if strings.HasPrefix(trimmed, "{") {
		raw = []byte(trimmed)
	} else {
		data, err := os.ReadFile(pathOrJSON)
		if err != nil {
			return nil, fmt.Errorf("sso: failed to read config file %q: %w", pathOrJSON, err)
		}
		raw = data
	}

	var parsed SSOConfigFile
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, fmt.Errorf("sso: invalid json configuration: %w", err)
	}

	opts := &SSOOptions{
		Providers:      parsed.Providers,
		MockIdPEnabled: parsed.MockIdPEnabled,
		Logger:         log,
	}

	// Secret resolution: environment variable takes precedence over file values
	if envSecret := os.Getenv("DEEDGLASS_SSO_STATE_SECRET"); envSecret != "" {
		opts.StateSecret = []byte(envSecret)
	} else if parsed.StateSecretHex != "" {
		b, err := hex.DecodeString(parsed.StateSecretHex)
		if err == nil && len(b) > 0 {
			opts.StateSecret = b
		}
	} else if parsed.StateSecret != "" {
		opts.StateSecret = []byte(parsed.StateSecret)
	}

	// Salt resolution: environment variable takes precedence over file values
	if envSalt := os.Getenv("DEEDGLASS_SSO_SUBJECT_SALT"); envSalt != "" {
		opts.DigestSalt = []byte(envSalt)
	} else if parsed.DigestSaltHex != "" {
		b, err := hex.DecodeString(parsed.DigestSaltHex)
		if err == nil && len(b) > 0 {
			opts.DigestSalt = b
		}
	} else if parsed.DigestSalt != "" {
		opts.DigestSalt = []byte(parsed.DigestSalt)
	}

	// Mock IdP override
	if envMock := os.Getenv("DEEDGLASS_SSO_MOCK_ENABLED"); envMock == "1" || strings.EqualFold(envMock, "true") {
		opts.MockIdPEnabled = true
	}

	// Client secret override
	if envSecret := os.Getenv("DEEDGLASS_SSO_CLIENT_SECRET"); envSecret != "" {
		for i := range opts.Providers {
			if opts.Providers[i].ClientSecret == "" {
				opts.Providers[i].ClientSecret = envSecret
			}
		}
	}

	return opts, nil
}

// LoadSSOConfigFromEnv synthesizes SSOOptions from DEEDGLASS_OIDC_* environment variables,
// used primarily in single-tenant hospital Kubernetes deployments via Helm.
func LoadSSOConfigFromEnv(log *slog.Logger) (*SSOOptions, error) {
	if !strings.EqualFold(os.Getenv("DEEDGLASS_OIDC_ENABLED"), "true") && os.Getenv("DEEDGLASS_OIDC_ENABLED") != "1" {
		return nil, nil
	}

	providerType := ProviderType(strings.ToLower(strings.TrimSpace(os.Getenv("DEEDGLASS_OIDC_PROVIDER"))))
	if providerType == "" {
		providerType = ProviderEntraID
	}

	displayName := os.Getenv("DEEDGLASS_OIDC_DISPLAY_NAME")
	if displayName == "" {
		displayName = "Hospital Clinical SSO"
	}

	issuer := os.Getenv("DEEDGLASS_OIDC_ISSUER")
	clientID := os.Getenv("DEEDGLASS_OIDC_CLIENT_ID")
	clientSecret := os.Getenv("DEEDGLASS_OIDC_CLIENT_SECRET")
	redirectURI := os.Getenv("DEEDGLASS_OIDC_REDIRECT_URI")
	mockEnabled := strings.EqualFold(os.Getenv("DEEDGLASS_OIDC_MOCK"), "true") || os.Getenv("DEEDGLASS_OIDC_MOCK") == "1"

	var roleMapping RoleMappingConfig
	if rawMapping := os.Getenv("DEEDGLASS_OIDC_ROLE_MAPPING"); rawMapping != "" {
		_ = json.Unmarshal([]byte(rawMapping), &roleMapping)
	} else {
		roleMapping = RoleMappingConfig{
			GroupsClaim: "groups",
			DefaultRole: httpx.RoleViewer,
			FailClosed:  true,
		}
	}

	provider := ProviderConfig{
		ID:           string(providerType),
		Type:         providerType,
		DisplayName:  displayName,
		Issuer:       issuer,
		ClientID:     clientID,
		ClientSecret: clientSecret,
		RedirectURI:  redirectURI,
		RoleMapping:  roleMapping,
	}

	opts := &SSOOptions{
		Providers:      []ProviderConfig{provider},
		MockIdPEnabled: mockEnabled,
		Logger:         log,
	}

	if envSecret := os.Getenv("DEEDGLASS_SSO_STATE_SECRET"); envSecret != "" {
		opts.StateSecret = []byte(envSecret)
	}
	if envSalt := os.Getenv("DEEDGLASS_SSO_SUBJECT_SALT"); envSalt != "" {
		opts.DigestSalt = []byte(envSalt)
	}

	return opts, nil
}
