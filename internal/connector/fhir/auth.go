package fhir

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/deedglass/deedglass/internal/connector"
	"github.com/deedglass/deedglass/internal/connector/saas"
	"github.com/deedglass/deedglass/internal/vault"
)

// SMART Backend Services (ADR-105 §5) as generic OAuth 2.0 client credentials with a signed JWT
// client assertion (RFC 6749 §4.4, RFC 7523, private_key_jwt). SMART discovery is a thin layer on
// top. The key is opened from the vault for each mint and never leaves this process; tokens live
// in memory for at most min(300 s, expires_in) and there is never a refresh token.

const assertionType = "urn:ietf:params:oauth:client-assertion-type:jwt-bearer"

func msDur(ms int) time.Duration { return time.Duration(ms) * time.Millisecond }

func credError(msg string) *connector.Error {
	return &connector.Error{Kind: connector.KindConfig, Code: "connector_misconfigured", AgentMessage: "This connector's credentials can't be used. An admin can check Console › Connectors.", AdminDetail: msg}
}

// signer is a parsed private JWK.
type signer struct {
	alg, kid string
	rsa      *rsa.PrivateKey
	ec       *ecdsa.PrivateKey
}

type jwk struct {
	Kty, Crv, Alg, Kid, Use string
	N, E, D, P, Q, X, Y     string
	Keys                    []jwk `json:"keys"`
}

func b64(s string) ([]byte, error) {
	return base64.RawURLEncoding.DecodeString(strings.TrimRight(s, "="))
}

func bigOf(s string) (*big.Int, error) {
	b, err := b64(s)
	if err != nil || len(b) == 0 {
		return nil, errors.New("fhir: a JWK number isn't base64url")
	}
	return new(big.Int).SetBytes(b), nil
}

// parseJWK reads a private JWK (RS384 or ES384), or a JWK Set of one or two keys whose first
// private key signs (the second is the one being rotated out). Errors never quote the key.
func parseJWK(raw string) (*signer, error) {
	var k jwk
	if err := json.Unmarshal([]byte(raw), &k); err != nil {
		return nil, errors.New("private_jwk isn't JSON")
	}
	if k.Keys != nil {
		if len(k.Keys) == 0 || len(k.Keys) > 2 {
			return nil, errors.New("a private_jwk key set must hold one or two keys")
		}
		k = k.Keys[0]
	}
	if k.D == "" {
		return nil, errors.New("private_jwk has no private part (d)")
	}
	s := &signer{alg: k.Alg, kid: k.Kid}
	switch k.Kty {
	case "RSA":
		if s.alg == "" {
			s.alg = "RS384"
		}
		var nums [5]*big.Int
		for i, v := range []string{k.N, k.E, k.D, k.P, k.Q} {
			n, err := bigOf(v)
			if err != nil {
				return nil, errors.New("an RSA private_jwk needs n, e, d, p and q")
			}
			nums[i] = n
		}
		if !nums[1].IsInt64() || nums[1].Int64() > 1<<31 {
			return nil, errors.New("the RSA exponent is out of range")
		}
		key := &rsa.PrivateKey{PublicKey: rsa.PublicKey{N: nums[0], E: int(nums[1].Int64())}, D: nums[2], Primes: []*big.Int{nums[3], nums[4]}}
		if err := key.Validate(); err != nil || key.N.BitLen() < saas.MinRSABits {
			return nil, errors.New("the RSA private_jwk isn't a valid key of at least 2048 bits")
		}
		key.Precompute()
		s.rsa = key
	case "EC":
		if s.alg == "" {
			s.alg = "ES384"
		}
		d, err := b64(k.D)
		if k.Crv != "P-384" || err != nil {
			return nil, errors.New("an EC private_jwk must be on P-384")
		}
		key, err := ecdsa.ParseRawPrivateKey(elliptic.P384(), d)
		if err != nil {
			return nil, errors.New("the EC private_jwk isn't a valid P-384 key")
		}
		x, ex := b64(k.X)
		y, ey := b64(k.Y)
		pub, _ := key.PublicKey.Bytes()
		if ex != nil || ey != nil || len(pub) != 97 || string(pub[1:49]) != string(x) || string(pub[49:]) != string(y) {
			return nil, errors.New("the EC private_jwk's x and y don't match d")
		}
		s.ec = key
	default:
		return nil, errors.New("private_jwk must be an RSA or EC key")
	}
	if s.alg != "RS384" && s.alg != "ES384" || (s.alg == "RS384") != (s.rsa != nil) {
		return nil, errors.New("private_jwk's alg must be RS384 (RSA) or ES384 (EC P-384)")
	}
	return s, nil
}

// sign returns a compact JWS (RFC 7515) over claims.
func (s *signer) sign(claims map[string]any) (string, error) {
	h := map[string]string{"alg": s.alg, "typ": "JWT"}
	if s.kid != "" {
		h["kid"] = s.kid
	}
	hb, _ := json.Marshal(h)
	cb, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	enc := base64.RawURLEncoding
	input := enc.EncodeToString(hb) + "." + enc.EncodeToString(cb)
	sum := sha512.Sum384([]byte(input))
	var sig []byte
	if s.rsa != nil {
		sig, err = rsa.SignPKCS1v15(rand.Reader, s.rsa, crypto.SHA384, sum[:])
	} else {
		var r, ss *big.Int
		if r, ss, err = ecdsa.Sign(rand.Reader, s.ec, sum[:]); err == nil {
			sig = make([]byte, 96)
			r.FillBytes(sig[:48])
			ss.FillBytes(sig[48:])
		}
	}
	if err != nil {
		return "", err
	}
	return input + "." + enc.EncodeToString(sig), nil
}

// fingerprint is a credential's version for cache keys: a short digest, never the value.
func fingerprint(sec *vault.Secret) string {
	h := sha256.New()
	for _, n := range sec.Names() {
		h.Write([]byte(n + "\x00" + sec.Field(n) + "\x00"))
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

func (c *Connector) cacheKey(inst *connector.Instance) string {
	return "fhir:" + inst.ID.String() + ":" + inst.ConfigHash
}

// forget drops the instance's cached token (a 401: the next call mints again).
func (c *Connector) forget(st *state) {
	if k, ok := st.tokenKey.Load().(string); ok {
		c.tokens.Forget(k)
	}
}

// auth returns how a request gets its credential. done destroys what was opened.
func (c *Connector) auth(ctx context.Context, inst *connector.Instance, st *state) (func(context.Context, *http.Request) error, func(), error) {
	nothing := func() {}
	if st.cfg.Auth.Kind == AuthNone {
		return nil, nothing, nil
	}
	sec, err := inst.Secret(ctx)
	if err != nil {
		return nil, nothing, err
	}
	if st.cfg.Auth.Kind == AuthBearer {
		if sec.Kind() != vault.KindBearer || sec.Field("token") == "" {
			sec.Destroy()
			return nil, nothing, credError("auth.kind is bearer: store a bearer credential with a token.")
		}
		return func(_ context.Context, r *http.Request) error {
			r.Header.Set("Authorization", "Bearer "+sec.Field("token"))
			return nil
		}, sec.Destroy, nil
	}
	defer sec.Destroy()
	tok, err := c.smartToken(ctx, inst, st, sec)
	if err != nil {
		return nil, nothing, err
	}
	vault.RedactorFrom(ctx).Merge(tok.Redactor())
	return func(_ context.Context, r *http.Request) error {
		v := tok.Field("token")
		if v == "" {
			return &connector.Error{Kind: connector.KindAuth, Code: connector.CodeUpstreamAuthFailed,
				AgentMessage: "The connector's access token expired before the call was sent. Nothing was changed; send the call again."}
		}
		r.Header.Set("Authorization", "Bearer "+v)
		return nil
	}, nothing, nil
}

// smartToken returns a cached access token, or mints one: the client assertion, then the token
// request to the token endpoint (auth.token_url, or the one SMART discovery names).
func (c *Connector) smartToken(ctx context.Context, inst *connector.Instance, st *state, sec *vault.Secret) (*vault.Secret, error) {
	if sec.Kind() != vault.KindFields || sec.Field("client_id") == "" || sec.Field("private_jwk") == "" {
		return nil, credError("auth.kind is smart_backend: store a fields credential with client_id and private_jwk.")
	}
	key := c.cacheKey(inst) + ":" + fingerprint(sec)
	st.tokenKey.Store(key)
	st.clientID.Store(sec.Field("client_id"))
	return c.tokens.Get(ctx, key, 30*time.Second, func(ctx context.Context) (*vault.Secret, time.Time, error) {
		k, err := parseJWK(sec.Field("private_jwk"))
		if err != nil {
			return nil, time.Time{}, credError(err.Error())
		}
		tokenURL, err := c.tokenURL(ctx, inst, st)
		if err != nil {
			return nil, time.Time{}, err
		}
		now := c.clock()
		id := sec.Field("client_id")
		jti := make([]byte, 16)
		_, _ = rand.Read(jti)
		assertion, err := k.sign(map[string]any{"iss": id, "sub": id, "aud": tokenURL, "exp": now.Add(MaxTokenLife).Unix(),
			"iat": now.Unix(), "jti": hex.EncodeToString(jti)})
		if err != nil {
			return nil, time.Time{}, credError("The client assertion can't be signed with the stored key.")
		}
		vault.RedactorFrom(ctx).Add(assertion)
		u, _ := url.Parse(tokenURL)
		_, wr := st.clients(inst)
		cl := saas.Client{Base: u, HTTP: wr, Headers: http.Header{"Accept": {"application/json"}}, MaxBody: 64 << 10}
		form := url.Values{"grant_type": {"client_credentials"}, "scope": {strings.Join(st.cfg.Auth.Scopes, " ")},
			"client_assertion_type": {assertionType}, "client_assertion": {assertion}}
		resp, err := cl.Do(ctx, saas.Request{Method: http.MethodPost, Form: form})
		if err != nil {
			if ce := connector.AsError(err); ce != nil {
				ce.AgentMessage = "The FHIR server's authorization server refused the connector's token request, so nothing was sent. An admin can run deedglass connector test."
				return nil, time.Time{}, ce
			}
			return nil, time.Time{}, err
		}
		var body struct {
			AccessToken string      `json:"access_token"`
			TokenType   string      `json:"token_type"`
			ExpiresIn   json.Number `json:"expires_in"`
		}
		if resp.JSON(&body) != nil || body.AccessToken == "" || !strings.EqualFold(body.TokenType, "bearer") { //deedglass:allow-compare token_type is the public word "bearer", not a secret
			return nil, time.Time{}, &connector.Error{Kind: connector.KindAuth, Code: connector.CodeUpstreamAuthFailed,
				AgentMessage: "The authorization server's answer had no bearer access token, so nothing was sent. An admin can run deedglass connector test."}
		}
		life := MaxTokenLife
		if n, err := body.ExpiresIn.Int64(); err == nil && n > 0 && time.Duration(n)*time.Second < life {
			life = time.Duration(n) * time.Second
		}
		tok := vault.NewSecretForTest(vault.KindBearer, map[string]string{"token": body.AccessToken})
		vault.RedactorFrom(ctx).Merge(tok.Redactor())
		return tok, now.Add(life), nil
	})
}

// tokenURL is auth.token_url, or token_endpoint from <base>/.well-known/smart-configuration,
// checked against the egress rules like base_url.
func (c *Connector) tokenURL(ctx context.Context, inst *connector.Instance, st *state) (string, error) {
	if st.cfg.Auth.TokenURL != "" {
		return st.cfg.Auth.TokenURL, nil
	}
	read, _ := st.clients(inst)
	cl := saas.Client{Base: st.base, HTTP: read, Headers: http.Header{"Accept": {"application/json"}}, MaxBody: 256 << 10}
	resp, err := cl.Do(ctx, saas.Request{Path: "/.well-known/smart-configuration", Idempotent: true})
	var conf struct {
		TokenEndpoint string `json:"token_endpoint"`
	}
	if err != nil || resp.JSON(&conf) != nil || conf.TokenEndpoint == "" {
		return "", &connector.Error{Kind: connector.KindConfig, Code: connector.CodeConnectorUnavailable,
			AgentMessage: "Deedglass couldn't find the FHIR server's token endpoint, so nothing was sent. An admin can set auth.token_url.", Err: err}
	}
	if pol := inst.Env.Egress; pol != nil {
		if _, err := pol.CheckURL(ctx, conf.TokenEndpoint); err != nil {
			return "", &connector.Error{Kind: connector.KindDestinationBlocked, Code: connector.CodeDestinationBlocked,
				AgentMessage: "The FHIR server's token endpoint is blocked by Deedglass's network rules, so nothing was sent.", Err: err}
		}
	}
	return conf.TokenEndpoint, nil
}
