package github

import (
	"context"
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

// githubMessage is GitHub's own "message" from an error body, clipped and scrubbed; "" when absent.
func githubMessage(body []byte) string {
	var m struct {
		Message string `json:"message"`
		Errors  []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if json.Unmarshal(body, &m) != nil {
		return ""
	}
	msg := strings.TrimSpace(m.Message)
	if len(m.Errors) > 0 && m.Errors[0].Message != "" {
		msg = strings.TrimSuffix(msg, ".") + ": " + strings.TrimSpace(m.Errors[0].Message)
	}
	msg = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, msg)
	return vault.Scrub(clip(msg, 200))
}

// rateWait reads GitHub's rate-limit answer: Retry-After, else x-ratelimit-remaining: 0 with
// x-ratelimit-reset (an epoch second), else a secondary limit (default 60 s). ok is false when the
// answer isn't a rate limit. The wait is capped at an hour.
func (c *Connector) rateWait(r *saas.Response) (time.Duration, bool) {
	if r.Status != http.StatusForbidden && r.Status != http.StatusTooManyRequests {
		return 0, false
	}
	if ra := strings.TrimSpace(r.Header.Get("Retry-After")); ra != "" {
		if n, err := strconv.Atoi(ra); err == nil && n >= 0 {
			return min(time.Duration(n)*time.Second, maxRateLimitWait), true
		}
	}
	if strings.TrimSpace(r.Header.Get("X-Ratelimit-Remaining")) == "0" {
		d := defaultRateWait
		if n, err := strconv.ParseInt(strings.TrimSpace(r.Header.Get("X-Ratelimit-Reset")), 10, 64); err == nil {
			d = time.Unix(n, 0).Sub(c.now())
			if d < time.Second {
				d = time.Second
			}
		}
		return min(d, maxRateLimitWait), true
	}
	if r.Status == http.StatusTooManyRequests || strings.Contains(strings.ToLower(string(r.Body)), "secondary rate limit") {
		return defaultRateWait, true
	}
	return 0, false
}

// classify maps GitHub's answers onto S16's taxonomy (S29 §3.2 "Error mapping"). The request's
// method and path are added by session.explain.
func (c *Connector) classify(r *saas.Response) *connector.Error {
	if r.Status < 400 {
		return nil
	}
	msg := githubMessage(r.Body)
	if d, ok := c.rateWait(r); ok {
		return &connector.Error{Kind: connector.KindRateLimited, Code: connector.CodeUpstreamRateLimited, Retryable: true, RetryAfter: d,
			HTTPStatus:   r.Status,
			AgentMessage: fmt.Sprintf("GitHub is limiting this connector's requests (HTTP %d). Wait %s, then send the call again.", r.Status, waitText(d)),
			AdminDetail:  fmt.Sprintf("GitHub rate limit: %s left, resets in %s.", nonEmpty(r.Header.Get("X-Ratelimit-Remaining"), "unknown"), waitText(d))}
	}
	e := &connector.Error{HTTPStatus: r.Status}
	switch r.Status {
	case http.StatusUnauthorized:
		e.Kind, e.Code = connector.KindAuth, connector.CodeUpstreamAuthFailed
		e.AgentMessage = "GitHub refused the connector's credential (HTTP 401). Nothing was changed. An admin can replace it in Console › Connectors."
	case http.StatusForbidden:
		e.Kind, e.Code = connector.KindAuth, connector.CodeUpstreamAuthFailed
		e.AgentMessage = "GitHub refused this request for the connector's credential (HTTP 403). Nothing was changed. An admin can check its permissions in Console › Connectors."
	case http.StatusNotFound:
		e.Kind, e.Code = connector.KindNotFound, connector.CodeUpstreamNotFound
		e.AgentMessage = "GitHub has nothing at that address, or this connector can't see it (HTTP 404). Check the arguments."
	case http.StatusMethodNotAllowed, http.StatusConflict, http.StatusUnprocessableEntity:
		e.Kind, e.Code = connector.KindInvalidRequest, connector.CodeUpstreamError
		e.AgentMessage = fmt.Sprintf("GitHub refused the request (HTTP %d). Nothing was changed. Check the arguments.", r.Status)
		if msg != "" {
			e.AgentMessage = fmt.Sprintf("GitHub refused the request (HTTP %d): %s. Nothing was changed.", r.Status, strings.TrimSuffix(msg, "."))
		}
	default:
		e = connector.FromStatus(r.Status, 0)
		e.AgentMessage = strings.Replace(e.AgentMessage, "The service", "GitHub", 1)
	}
	if msg != "" {
		e.AdminDetail = "GitHub said: " + msg
	}
	return e
}

// explain adds what the admin needs to an error: the request, the missing permission, the 404
// caveat. It never adds a credential.
func (s *session) explain(ce *connector.Error, r saas.Request, resp *saas.Response) {
	line := r.Method + " " + r.Path
	switch {
	case ce.Kind == connector.KindAuth && resp.Status == http.StatusForbidden:
		fix := "Update the fine-grained token, then run deedglass connector test " + s.inst.Name + "."
		if s.st.cfg.Auth == "app" {
			fix = "Grant the GitHub App that permission and accept it on the installation, then run deedglass connector test " + s.inst.Name + "."
		}
		if perms := permissionsText(resp.Header.Get("X-Accepted-Github-Permissions")); perms != "" {
			ce.AdminDetail = fmt.Sprintf("GitHub refused this token for %s. It needs %s. %s", line, perms, fix)
		} else {
			ce.AdminDetail = fmt.Sprintf("GitHub refused this token for %s (HTTP 403). %s %s", line, strings.TrimSpace(ce.AdminDetail), fix)
		}
	case ce.Kind == connector.KindAuth:
		ce.AdminDetail = fmt.Sprintf("GitHub answered 401 for %s: the credential is expired, revoked or wrong. Store a new one with deedglass connector credentials set %s.", line, s.inst.Name)
		if s.cacheKey != "" {
			s.c.tokens.Forget(s.cacheKey)
		}
	case ce.Kind == connector.KindNotFound:
		ce.AdminDetail = fmt.Sprintf("GitHub answered 404 for %s. GitHub answers 404 when a token can't see a private repository, so check the token's repository access too.", line)
	case ce.AdminDetail != "":
		ce.AdminDetail = line + ": " + ce.AdminDetail
	}
}

// do sends one request with the session's auth and explains a failure.
func (s *session) do(ctx context.Context, r saas.Request) (*saas.Response, error) {
	resp, err := s.cl.Do(ctx, r)
	if resp != nil {
		s.lastResp = resp
	}
	if ce := connector.AsError(err); ce != nil && resp != nil {
		s.explain(ce, r, resp)
	}
	return resp, err
}

// permissionsText renders x-accepted-github-permissions ("issues=write; pull_requests=write,
// contents=read") as "Issues: write or Pull requests: write and Contents: read".
func permissionsText(h string) string {
	h = strings.TrimSpace(h)
	if h == "" {
		return ""
	}
	var alts []string
	for _, alt := range strings.Split(h, ";") {
		var all []string
		for _, p := range strings.Split(alt, ",") {
			name, level, ok := strings.Cut(strings.TrimSpace(p), "=")
			if !ok || name == "" {
				continue
			}
			all = append(all, permissionName(name)+": "+strings.TrimSpace(level))
		}
		if len(all) > 0 {
			alts = append(alts, strings.Join(all, " and "))
		}
	}
	return clip(strings.Join(alts, " or "), 300)
}

func permissionName(n string) string {
	n = strings.ReplaceAll(strings.TrimSpace(n), "_", " ")
	if n == "" {
		return n
	}
	return strings.ToUpper(n[:1]) + n[1:]
}

// waitText is "3 s", "2 min", "1 h" (rounded up to the unit).
func waitText(d time.Duration) string {
	switch {
	case d < 2*time.Minute:
		return fmt.Sprintf("%d s", int((d+time.Second-1)/time.Second))
	case d < time.Hour:
		return fmt.Sprintf("%d min", int((d+time.Minute-1)/time.Minute))
	}
	return "1 h"
}

func nonEmpty(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

// meta is a result's Meta: the HTTP status and, when GitHub sent it, the rate-limit budget left.
func meta(resp *saas.Response) map[string]any {
	m := map[string]any{}
	if resp == nil {
		return m
	}
	m["http_status"] = resp.Status
	if n, err := strconv.Atoi(resp.Header.Get("X-Ratelimit-Remaining")); err == nil {
		m["ratelimit_remaining"] = n
	}
	return m
}
