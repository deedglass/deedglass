// Package securitypackapi is the security review pack's HTTP routes (S51 §3.4): the free readiness
// view (GET /v1/security_pack/readiness) and the Pro pack build (POST /v1/security_pack), which
// answers with the folder as a tar stream. Agent keys get 403 on both: an agent never describes
// its own controls to a reviewer.
package securitypackapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/deedglass/deedglass/internal/deed"
	"github.com/deedglass/deedglass/internal/httpx"
	"github.com/deedglass/deedglass/internal/ids"
	"github.com/deedglass/deedglass/internal/ledger"
	"github.com/deedglass/deedglass/internal/securitypack"
	"github.com/deedglass/deedglass/internal/securitypack/pgsource"
)

// MsgAgentKey is insufficient_scope's message for an agent key on these routes.
const MsgAgentKey = "Agents can't read or build the security review pack; a person or an org admin key does. Use the Console or an org admin key."

// MsgProRequired is pro_required's message on POST /v1/security_pack.
const MsgProRequired = "The reviewer-ready document, sealed with your records, is part of Pro. The readiness list stays free: GET /v1/security_pack/readiness or deedglass security-pack readiness."

// Period limits, in days (S51 §3.2: --period 7d..365d).
const (
	DefaultPeriodDays = 90
	MinPeriodDays     = 7
	MaxPeriodDays     = 365
)

// Timeouts: readiness stays inside its budget; a pack exports and seals the whole period.
const (
	ReadinessChainBudget = 250 * time.Millisecond
	BuildTimeout         = 10 * time.Minute
)

// API is the routes' dependencies. Builder returns the Pro builder, or nil when serve's licence
// check didn't grant security_pack (cmd/deedglass/licence.go); then the build answers pro_required.
type API struct {
	Source  *pgsource.Source
	Ledger  *ledger.Ledger
	Builder func() securitypack.Builder
	Egress  *securitypack.Egress
	Build   securitypack.BuildFacts
	Now     func() time.Time
	TempDir string
	Log     *slog.Logger
}

// Routes are the two routes. With a nil API (route-table tests) they answer service_unavailable.
func Routes(a *API) []httpx.Route {
	admin := []string{httpx.ScopeAdmin}
	return []httpx.Route{
		{Method: "GET", Pattern: "/v1/security_pack/readiness", OperationID: "getSecurityPackReadiness", Auth: httpx.SessionOrKey,
			MinRole: httpx.RoleViewer, Purpose: "reading the security review readiness", Scopes: admin, AgentKeyMessage: MsgAgentKey,
			Timeout: 30 * time.Second, Handler: a.readiness},
		{Method: "POST", Pattern: "/v1/security_pack", OperationID: "createSecurityPack", Auth: httpx.SessionOrKey,
			MinRole: httpx.RoleAdmin, Purpose: "building the security review pack", Scopes: admin, AgentKeyMessage: MsgAgentKey,
			Idempotency: httpx.IdemNever, MaxBody: 1 << 10, Timeout: BuildTimeout, Handler: a.build},
	}
}

func (a *API) now() time.Time {
	if a.Now != nil {
		return a.Now().UTC()
	}
	return time.Now().UTC()
}

// principal is the caller: a person or an org admin key, never an agent key.
func (a *API) principal(r *http.Request) (httpx.Principal, deed.Ref, error) {
	if a == nil || a.Source == nil || a.Ledger == nil {
		return httpx.Principal{}, deed.Ref{}, httpx.E(httpx.CodeServiceUnavailable)
	}
	pr, ok := httpx.PrincipalFrom(r.Context())
	if !ok || pr.OrgID.IsZero() {
		return pr, deed.Ref{}, httpx.E(httpx.CodeAuthenticationRequired)
	}
	switch {
	case pr.Kind == httpx.KindUser && !pr.UserID.IsZero():
		return pr, deed.Ref{Type: "user", ID: pr.UserID.String()}, nil
	case pr.Kind == httpx.KindAdminKey && !pr.KeyID.IsZero():
		return pr, deed.Ref{Type: "api_key", ID: pr.KeyID.String()}, nil
	}
	e := httpx.ErrInsufficientScope(httpx.ScopeAdmin)
	e.Message = MsgAgentKey
	return pr, deed.Ref{}, e
}

func periodErr() error {
	return httpx.ErrValidation("period_days", "period_days is a whole number of days from 7 to 365, like 90.")
}

func (a *API) collect(ctx context.Context, org ids.OrgID, days int, chainFrom time.Time, budget time.Duration) (*securitypack.Facts, error) {
	now := a.now()
	period := securitypack.Period{From: now.Add(-time.Duration(days) * 24 * time.Hour), To: now}
	if chainFrom.IsZero() {
		chainFrom = period.From
	}
	return a.Source.Collect(ctx, pgsource.Input{Org: org, Period: period, Now: now, Build: a.Build, Egress: a.Egress,
		ChainFrom: chainFrom, ChainBudget: budget})
}

func (a *API) readiness(w http.ResponseWriter, r *http.Request) error {
	pr, _, err := a.principal(r)
	if err != nil {
		return err
	}
	days := DefaultPeriodDays
	if v := r.URL.Query().Get("period_days"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < MinPeriodDays || n > MaxPeriodDays {
			return periodErr()
		}
		days = n
	}
	f, err := a.collect(r.Context(), pr.OrgID, days, a.now().Add(-24*time.Hour), ReadinessChainBudget)
	if err != nil {
		return err
	}
	p, err := securitypack.Build(r.Context(), f, securitypack.Options{})
	if err != nil {
		return err
	}
	rd, err := securitypack.ReadinessOf(p)
	if err != nil {
		return err
	}
	httpx.Annotate(r.Context(), pr.OrgID.String(), "", "")
	return httpx.WriteJSON(w, http.StatusOK, rd)
}

// buildBody is POST /v1/security_pack.
type buildBody struct {
	PeriodDays *int  `json:"period_days"`
	Aliases    *bool `json:"aliases"`
	Records    *bool `json:"records"`
}

// tarWriter sets the download headers before the first byte, so an error before the stream
// starts is still a JSON error.
type tarWriter struct {
	w       http.ResponseWriter
	started bool
}

func (t *tarWriter) Write(p []byte) (int, error) {
	if !t.started {
		h := t.w.Header()
		h.Set("Content-Type", "application/x-tar")
		h.Set("Content-Disposition", `attachment; filename="security-pack.tar"`)
		h.Set("Cache-Control", "no-store")
		h.Set("X-Content-Type-Options", "nosniff")
		t.w.WriteHeader(http.StatusOK)
		t.started = true
	}
	return t.w.Write(p)
}

func (a *API) build(w http.ResponseWriter, r *http.Request) error {
	pr, actor, err := a.principal(r)
	if err != nil {
		return err
	}
	var b buildBody
	if err := httpx.DecodeJSONOptional(r, &b); err != nil {
		return err
	}
	days := DefaultPeriodDays
	if b.PeriodDays != nil {
		if *b.PeriodDays < MinPeriodDays || *b.PeriodDays > MaxPeriodDays {
			return periodErr()
		}
		days = *b.PeriodDays
	}
	var builder securitypack.Builder
	if a.Builder != nil {
		builder = a.Builder()
	}
	if builder == nil {
		return httpx.E(httpx.CodeProRequired, httpx.Msg(MsgProRequired))
	}
	ctx := r.Context()
	f, err := a.collect(ctx, pr.OrgID, days, time.Time{}, 0)
	if err != nil {
		return err
	}
	org := pr.OrgID
	req := securitypack.BuildRequest{Facts: f, Aliases: b.Aliases != nil && *b.Aliases, Records: b.Records == nil || *b.Records,
		TempDir: a.TempDir,
		Export: func(ctx context.Context, from, to time.Time, w io.Writer) (int64, error) {
			m, err := a.Ledger.Export(ctx, org, ledger.Range{IssuedGTE: &from, IssuedLT: &to}, w)
			if errors.Is(err, ledger.ErrEmptyRange) {
				return 0, securitypack.ErrNoRecords
			}
			if err != nil {
				return 0, err
			}
			return m.Range.Count, nil
		},
		Seal: func(ctx context.Context, d securitypack.FileData) (json.RawMessage, error) {
			env, err := a.Ledger.SealFile(ctx, org, actor, ledger.FileSealedData{Bytes: d.Bytes, Label: d.Label, MediaType: d.MediaType, SHA256: d.SHA256})
			if err != nil {
				return nil, err
			}
			return env.MarshalCanonical()
		},
		Keys: func(ctx context.Context) (json.RawMessage, error) {
			ks, err := a.Ledger.SealKeySet(ctx)
			if err != nil {
				return nil, err
			}
			return json.Marshal(ks)
		},
	}
	httpx.Annotate(ctx, org.String(), "", "")
	tw := &tarWriter{w: w}
	res, err := builder.Build(ctx, req, tw)
	if err != nil {
		if tw.started {
			// The stream already began: the CLI sees a short tar and its verify fails, which is the
			// honest outcome. Log why.
			a.log().ErrorContext(ctx, "security pack: stream broke", "org_id", org.String(), "request_id", httpx.RequestID(ctx), "error", err)
			return nil
		}
		return err
	}
	a.log().InfoContext(ctx, "security pack built", "org_id", org.String(), "request_id", httpx.RequestID(ctx), "principal", pr.Ref(),
		"answers", res.Answers, "sealed", res.Sealed, "records", res.Records, "aliases", res.Aliases)
	return nil
}

func (a *API) log() *slog.Logger {
	if a.Log != nil {
		return a.Log
	}
	return slog.Default()
}
