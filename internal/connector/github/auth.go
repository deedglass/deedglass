package github

import (
	"context"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/deedglass/deedglass/internal/connector"
	"github.com/deedglass/deedglass/internal/connector/saas"
	"github.com/deedglass/deedglass/internal/vault"
)

// Token kinds a check reports.
const (
	tokenFineGrained = "fine-grained token"
	tokenClassic     = "classic token"
	tokenApp         = "GitHub App key"
)

// credError is a credential Deedglass refuses to send: nothing reached GitHub.
func credError(detail string) *connector.Error {
	return &connector.Error{Kind: connector.KindConfig, Code: connector.CodeValidationFailed,
		AgentMessage: "This connector's GitHub credential can't be used, so nothing was sent. An admin can replace it in Console › Connectors.",
		AdminDetail:  detail}
}

// checkCredential checks that sec fits the config's auth and returns the token kind. A PAT must be
// a fine-grained token (github_pat_) or a classic one (ghp_, allowed; the test warns). OAuth, user
// to server, refresh and installation tokens (gho_, ghu_, ghr_, ghs_) are refused: they expire or
// belong to another flow. An App credential is an RSA private key of at least 2048 bits.
func checkCredential(cfg Config, name string, sec *vault.Secret) (string, *rsa.PrivateKey, *connector.Error) {
	switch cfg.Auth {
	case "app":
		if sec.Kind() != vault.KindFields || sec.Field("private_key") == "" {
			return "", nil, credError(fmt.Sprintf("auth: app needs the App's private key: deedglass connector credentials set %s --field private_key=file:./app.pem", name))
		}
		key, err := saas.ParseRSAPrivateKey([]byte(sec.Field("private_key")))
		if err != nil {
			return "", nil, credError("The App's private key can't be used: it must be the RSA PEM file GitHub generated (2048 bits or more). " + strings.TrimPrefix(err.Error(), "saas: ") + ".")
		}
		return tokenApp, key, nil
	default:
		tok := sec.Field("token")
		if sec.Kind() != vault.KindBearer || tok == "" {
			return "", nil, credError(fmt.Sprintf("auth: pat needs a bearer token: deedglass connector credentials set %s --kind bearer --from-env %s", name, connector.EnvVarFor(name)))
		}
		switch {
		case strings.HasPrefix(tok, "github_pat_"):
			return tokenFineGrained, nil, nil
		case strings.HasPrefix(tok, "ghp_"):
			return tokenClassic, nil, nil
		case strings.HasPrefix(tok, "ghs_"):
			return "", nil, credError("This is a GitHub App installation token (ghs_). It expires within an hour. Store a fine-grained token (github_pat_…), or set auth: app and store the App's private key.")
		case strings.HasPrefix(tok, "gho_"), strings.HasPrefix(tok, "ghu_"), strings.HasPrefix(tok, "ghr_"):
			return "", nil, credError("This is an OAuth or user-to-server GitHub token (" + tok[:4] + "). Store a fine-grained personal access token (github_pat_…) instead.")
		}
		return "", nil, credError("The credential isn't a GitHub personal access token. Store a fine-grained token, which starts with github_pat_.")
	}
}

// fingerprint is the credential's version for cache keys: a short digest, never the value.
func fingerprint(sec *vault.Secret) string {
	h := sha256.New()
	h.Write([]byte(sec.Kind()))
	for _, n := range sec.Names() {
		h.Write([]byte{0})
		h.Write([]byte(n))
		h.Write([]byte{0})
		h.Write([]byte(sec.Field(n)))
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// session is one call's authenticated client. close destroys the credential.
type session struct {
	c         *Connector
	inst      *connector.Instance
	st        *state
	cl        saas.Client
	sec       *vault.Secret
	key       *rsa.PrivateKey
	kind      string
	fp        string
	cacheKey  string
	tokenExp  time.Time
	lastResp  *saas.Response // the last answer (rate-limit headers)
	userResp  *saas.Response // GET /user's answer (the token check)
	installed int            // App: repositories the installation token reaches (test)
}

func (s *session) close() {
	if s != nil && s.sec != nil {
		s.sec.Destroy()
	}
}

// open opens the credential and returns an authenticated session.
func (c *Connector) open(ctx context.Context, inst *connector.Instance, st *state) (*session, error) {
	sec, err := inst.Secret(ctx)
	if err != nil {
		return nil, err
	}
	kind, key, ce := checkCredential(st.cfg, inst.Name, sec)
	if ce != nil {
		sec.Destroy()
		return nil, ce
	}
	s := &session{c: c, inst: inst, st: st, cl: c.client(inst, st), sec: sec, key: key, kind: kind, fp: fingerprint(sec)}
	if st.cfg.Auth == "app" {
		s.cacheKey = "github:" + inst.ID.String() + ":" + s.fp
		tok, err := c.tokens.Get(ctx, s.cacheKey, tokenMargin, s.mint)
		if err != nil {
			sec.Destroy()
			return nil, err
		}
		vault.RedactorFrom(ctx).Merge(tok.Redactor())
		s.cl.Auth = func(_ context.Context, r *http.Request) error {
			v := tok.Field("token")
			if v == "" {
				return &connector.Error{Kind: connector.KindAuth, Code: connector.CodeUpstreamAuthFailed,
					AgentMessage: "The connector's GitHub App token expired before the call was sent. Nothing was changed; send the call again."}
			}
			r.Header.Set("Authorization", "Bearer "+v)
			return nil
		}
		return s, nil
	}
	tok := sec.Field("token")
	s.cl.Auth = func(_ context.Context, r *http.Request) error {
		r.Header.Set("Authorization", "Bearer "+tok)
		return nil
	}
	return s, nil
}

// appJWT is the App's signed assertion: {iat: now−60 s, exp: now+540 s, iss: client_id}. GitHub
// caps it at 10 minutes.
func appJWT(key *rsa.PrivateKey, clientID string, now time.Time) (string, error) {
	return saas.SignRS256(key, map[string]any{"iat": now.Add(-60 * time.Second).Unix(), "exp": now.Add(540 * time.Second).Unix(), "iss": clientID})
}

type downscopePermissions struct {
	Contents     string `json:"contents"`
	Issues       string `json:"issues"`
	PullRequests string `json:"pull_requests"`
	Metadata     string `json:"metadata"`
}

type downscope struct {
	Repositories []string             `json:"repositories,omitempty"`
	Permissions  downscopePermissions `json:"permissions"`
}

// downscopeBody is the installation token request: only the configured repositories (by name;
// omitted when an owner/* entry exists) and only the permissions the tools need.
func downscopeBody(cfg Config) downscope {
	d := downscope{Permissions: downscopePermissions{Contents: "write", Issues: "write", PullRequests: "write", Metadata: "read"}}
	rules := parseRules(cfg.Repos)
	if wildcard(rules) {
		return d
	}
	seen := map[string]bool{}
	for _, r := range cfg.Repos {
		_, n, _ := strings.Cut(r, "/")
		if !seen[strings.ToLower(n)] {
			seen[strings.ToLower(n)] = true
			d.Repositories = append(d.Repositories, n)
		}
	}
	return d
}

// jwtClient is the session's client authenticated as the App itself.
func (s *session) jwtClient(ctx context.Context) (saas.Client, error) {
	jwt, err := appJWT(s.key, s.st.cfg.App.ClientID, s.c.now())
	if err != nil {
		return saas.Client{}, credError("The App's JWT can't be signed with the stored key.")
	}
	vault.RedactorFrom(ctx).Add(jwt)
	cl := s.c.client(s.inst, s.st)
	cl.Auth = func(_ context.Context, r *http.Request) error {
		r.Header.Set("Authorization", "Bearer "+jwt)
		return nil
	}
	return cl, nil
}

// mint asks GitHub for an installation token downscoped to the configured repositories.
func (s *session) mint(ctx context.Context) (*vault.Secret, time.Time, error) {
	cl, err := s.jwtClient(ctx)
	if err != nil {
		return nil, time.Time{}, err
	}
	p, err := saas.Segments("app", "installations", strconv.FormatInt(s.st.cfg.App.InstallationID, 10), "access_tokens")
	if err != nil {
		return nil, time.Time{}, misconfigured(err)
	}
	r := saas.Request{Method: http.MethodPost, Path: p, JSON: downscopeBody(s.st.cfg)}
	resp, err := cl.Do(ctx, r)
	if err != nil {
		if ce := connector.AsError(err); ce != nil && resp != nil {
			s.explain(ce, r, resp)
		}
		return nil, time.Time{}, err
	}
	var body struct {
		Token        string    `json:"token"`
		ExpiresAt    time.Time `json:"expires_at"`
		Repositories []struct {
			FullName string `json:"full_name"`
		} `json:"repositories"`
	}
	if err := json.Unmarshal(resp.Body, &body); err != nil || body.Token == "" {
		return nil, time.Time{}, &connector.Error{Kind: connector.KindUpstream, Code: connector.CodeUpstreamError,
			AgentMessage: "GitHub answered the installation token request with something Deedglass can't read. Nothing else was sent."}
	}
	if body.ExpiresAt.IsZero() {
		body.ExpiresAt = s.c.now().Add(time.Hour)
	}
	tok := vault.NewSecretForTest(vault.KindBearer, map[string]string{"token": body.Token})
	vault.RedactorFrom(ctx).Merge(tok.Redactor())
	s.tokenExp = body.ExpiresAt
	return tok, body.ExpiresAt, nil
}

// login is the identity GitHub shows as the author of what this connector creates: the token's
// user (GET /user), or the App's bot (GET /app → "<slug>[bot]"). It is cached per credential.
func (s *session) login(ctx context.Context) (string, error) {
	s.st.mu.Lock()
	l, ok := s.st.logins[s.fp]
	s.st.mu.Unlock()
	if ok {
		return l, nil
	}
	return s.fetchLogin(ctx)
}

// fetchLogin asks GitHub who the credential is, and caches the answer.
func (s *session) fetchLogin(ctx context.Context) (string, error) {
	var l string
	var who struct {
		Login string `json:"login"`
		Slug  string `json:"slug"`
	}
	if s.st.cfg.Auth == "app" {
		cl, err := s.jwtClient(ctx)
		if err != nil {
			return "", err
		}
		r := saas.Request{Method: http.MethodGet, Path: saas.MustSegments("app"), Idempotent: true}
		resp, err := cl.Do(ctx, r)
		if err != nil {
			if ce := connector.AsError(err); ce != nil && resp != nil {
				s.explain(ce, r, resp)
			}
			return "", err
		}
		if json.Unmarshal(resp.Body, &who) != nil || who.Slug == "" {
			return "", unreadable()
		}
		l = who.Slug + "[bot]"
	} else {
		resp, err := s.do(ctx, saas.Request{Method: http.MethodGet, Path: saas.MustSegments("user"), Idempotent: true})
		if err != nil {
			return "", err
		}
		s.userResp = resp
		if json.Unmarshal(resp.Body, &who) != nil || who.Login == "" {
			return "", unreadable()
		}
		l = who.Login
	}
	s.st.mu.Lock()
	s.st.logins[s.fp] = l
	s.st.mu.Unlock()
	return l, nil
}

func unreadable() *connector.Error {
	return &connector.Error{Kind: connector.KindUpstream, Code: connector.CodeUpstreamError,
		AgentMessage: "GitHub answered with something Deedglass can't read."}
}
