//go:build integration

package main

//deedglass:allow-sql-build table names are Medplum's own ("<Type>", "<Type>_History") for the configured resource types, quoted with pgx.Identifier

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/deedglass/deedglass/internal/connector/egress"
	"github.com/deedglass/deedglass/internal/corpus/fhirbench"
)

// medplum drives a throwaway Medplum server as its super admin: it logs in, keeps one client
// application for seeds and the other writer, makes a fresh client application (with its own
// AccessPolicy when the scenario asks) for each run's agent, and reads what was stored from
// Medplum's own tables ("<Type>_History").
type medplum struct {
	base, version   string
	adminDSN        string
	witnessDSN      string
	hc              *http.Client
	admin, project  string
	otherTok        string
	db              *pgx.Conn
	types           []string
	projectSnapshot map[string]any
}

func newMedplum(ctx context.Context, base, email, password, adminDSN, witnessDSN, version string, types []string) (*medplum, error) {
	m := &medplum{base: strings.TrimRight(base, "/"), version: version, adminDSN: adminDSN, witnessDSN: witnessDSN,
		hc: egress.FirstParty(egress.FirstPartyOptions{Timeout: 60 * time.Second}), types: types}
	var login struct {
		Code string `json:"code"`
	}
	if err := m.json(ctx, "POST", "/auth/login", "", map[string]any{"email": email, "password": password, "scope": "openid",
		"codeChallenge": "fhir-bench", "codeChallengeMethod": "plain"}, &login); err != nil {
		return nil, fmt.Errorf("medplum login: %w", err)
	}
	var tok struct {
		AccessToken string `json:"access_token"`
		Project     struct {
			Reference string `json:"reference"`
		} `json:"project"`
	}
	if err := m.form(ctx, url.Values{"grant_type": {"authorization_code"}, "code": {login.Code}, "code_verifier": {"fhir-bench"}}, &tok); err != nil {
		return nil, fmt.Errorf("medplum token: %w", err)
	}
	m.admin, m.project = tok.AccessToken, strings.TrimPrefix(tok.Project.Reference, "Project/")
	var err error
	if _, m.otherTok, err = m.client(ctx, "fhir-bench-other-writer", ""); err != nil {
		return nil, err
	}
	if m.db, err = pgx.Connect(ctx, adminDSN); err != nil {
		return nil, err
	}
	if err := m.json(ctx, "GET", "/fhir/R4/Project/"+m.project, m.admin, nil, &m.projectSnapshot); err != nil {
		return nil, err
	}
	return m, nil
}

func (m *medplum) Name() string    { return "medplum" }
func (m *medplum) Version() string { return m.version }

func (m *medplum) close() {
	if m.db != nil {
		_ = m.db.Close(context.Background())
	}
}

// client creates a ClientApplication (with an AccessPolicy when policy isn't "") and returns its
// id and a client-credentials access token.
func (m *medplum) client(ctx context.Context, name, policy string) (id, token string, err error) {
	body := map[string]any{"name": name}
	if policy != "" {
		body["accessPolicy"] = map[string]any{"reference": "AccessPolicy/" + policy}
	}
	var c struct {
		ID     string `json:"id"`
		Secret string `json:"secret"`
	}
	if err := m.json(ctx, "POST", "/admin/projects/"+m.project+"/client", m.admin, body, &c); err != nil {
		return "", "", fmt.Errorf("medplum client: %w", err)
	}
	var t struct {
		AccessToken string `json:"access_token"`
	}
	if err := m.form(ctx, url.Values{"grant_type": {"client_credentials"}, "client_id": {c.ID}, "client_secret": {c.Secret}}, &t); err != nil {
		return "", "", fmt.Errorf("medplum client token: %w", err)
	}
	return c.ID, t.AccessToken, nil
}

// setProject puts Project.features and Project.checkReferencesOnWrite to the given values on top
// of the project as it was at the start.
func (m *medplum) setProject(ctx context.Context, features []string, checkRefs bool) error {
	p := map[string]any{}
	for k, v := range m.projectSnapshot {
		if k != "meta" {
			p[k] = v
		}
	}
	if len(features) > 0 {
		p["features"] = features
	} else {
		delete(p, "features")
	}
	if checkRefs {
		p["checkReferencesOnWrite"] = true
	} else {
		delete(p, "checkReferencesOnWrite")
	}
	if err := m.json(ctx, "PUT", "/fhir/R4/Project/"+m.project, m.admin, p, nil); err != nil {
		return fmt.Errorf("medplum project settings: %w", err)
	}
	return nil
}

func (m *medplum) Prepare(ctx context.Context, s *fhirbench.Scenario) (fhirbench.Env, func(), error) {
	if err := m.setProject(ctx, s.Setup.ProjectFeatures, s.Setup.CheckReferencesOnWrite); err != nil {
		return fhirbench.Env{}, func() {}, err
	}
	restore := func() { _ = m.setProject(context.Background(), nil, false) }
	policy := ""
	if len(s.Setup.ReadonlyFields) > 0 {
		var res []any
		for _, t := range m.types {
			r := map[string]any{"resourceType": t}
			if f := s.Setup.ReadonlyFields[t]; len(f) > 0 {
				r["readonlyFields"] = f
			}
			res = append(res, r)
		}
		var ap struct {
			ID string `json:"id"`
		}
		if err := m.json(ctx, "POST", "/fhir/R4/AccessPolicy", m.admin, map[string]any{"resourceType": "AccessPolicy", "name": "fhir-bench " + s.ID, "resource": res}, &ap); err != nil {
			restore()
			return fhirbench.Env{}, func() {}, fmt.Errorf("medplum access policy: %w", err)
		}
		policy = ap.ID
	}
	id, tok, err := m.client(ctx, "fhir-bench "+s.ID, policy)
	if err != nil {
		restore()
		return fhirbench.Env{}, func() {}, err
	}
	// Medplum reads project settings through a short cache; give a change a moment to apply.
	time.Sleep(1500 * time.Millisecond)
	return fhirbench.Env{Server: "medplum", BaseURL: m.base + "/fhir/R4", Token: tok, ClientID: id, WitnessDSN: m.witnessDSN, AdminDSN: m.adminDSN}, restore, nil
}

func (m *medplum) Do(ctx context.Context, env fhirbench.Env, agent bool, r fhirbench.Request) (fhirbench.Exchange, error) {
	tok := m.otherTok
	if agent {
		tok = env.Token
	}
	return send(ctx, m.hc, env.BaseURL, tok, r)
}

// send is one FHIR REST call, as any client sends it.
func send(ctx context.Context, hc *http.Client, base, token string, r fhirbench.Request) (fhirbench.Exchange, error) {
	u := base
	if r.Path != "" {
		u += "/" + r.Path
	}
	var body io.Reader
	if len(r.Body) > 0 {
		body = bytes.NewReader(r.Body)
	}
	req, err := http.NewRequestWithContext(ctx, r.Method, u, body)
	if err != nil {
		return fhirbench.Exchange{}, err
	}
	req.Header.Set("Accept", "application/fhir+json")
	if len(r.Body) > 0 {
		ct := "application/fhir+json"
		if r.Method == "PATCH" {
			ct = "application/json-patch+json"
		}
		req.Header.Set("Content-Type", ct)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	for k, v := range r.Headers {
		req.Header.Set(k, v)
	}
	res, err := hc.Do(req)
	if err != nil {
		return fhirbench.Exchange{Error: err.Error()}, nil
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 4<<20))
	ex := fhirbench.Exchange{Status: res.StatusCode, Location: res.Header.Get("Location"), ETag: res.Header.Get("ETag")}
	if json.Valid(b) {
		ex.Body = scrubTracing(b)
	}
	return ex, nil
}

// scrubTracing drops Medplum's tracing extension (request and trace ids differ every run) from
// an answer, so recorded answers compare across runs.
func scrubTracing(b []byte) json.RawMessage {
	var m map[string]any
	if json.Unmarshal(b, &m) != nil {
		return b
	}
	if ex, ok := m["extension"].([]any); ok {
		var keep []any
		for _, x := range ex {
			if e, ok := x.(map[string]any); ok && e["url"] == "https://medplum.com/fhir/StructureDefinition/tracing" {
				continue
			}
			keep = append(keep, x)
		}
		if len(keep) == 0 {
			delete(m, "extension")
		} else {
			m["extension"] = keep
		}
	}
	out, err := json.Marshal(m)
	if err != nil {
		return b
	}
	return out
}

type mark map[string]bool

func (m *medplum) Mark(ctx context.Context) (any, error) {
	seen := mark{}
	for _, t := range m.types {
		rows, err := m.db.Query(ctx, fmt.Sprintf(`SELECT "versionId"::text FROM public.%s`, pgx.Identifier{t + "_History"}.Sanitize()))
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var v string
			if err := rows.Scan(&v); err != nil {
				rows.Close()
				return nil, err
			}
			seen[v] = true
		}
		rows.Close()
		if rows.Err() != nil {
			return nil, rows.Err()
		}
	}
	return seen, nil
}

func (m *medplum) Since(ctx context.Context, mk any) ([]fhirbench.Version, error) {
	seen := mk.(mark)
	type row struct {
		v    fhirbench.Version
		when time.Time
	}
	var all []row
	for _, t := range m.types {
		tbl := pgx.Identifier{t + "_History"}.Sanitize()
		rows, err := m.db.Query(ctx, fmt.Sprintf(`SELECT h."versionId"::text, h.id::text, h.content, h."lastUpdated",
		    EXISTS (SELECT 1 FROM public.%s e WHERE e.id = h.id AND e."lastUpdated" < h."lastUpdated")
		  FROM public.%s h ORDER BY h."lastUpdated"`, tbl, tbl))
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var r row
			var content string
			var earlier bool
			if err := rows.Scan(&r.v.VersionID, &r.v.ID, &content, &r.when, &earlier); err != nil {
				rows.Close()
				return nil, err
			}
			if seen[r.v.VersionID] {
				continue
			}
			r.v.Type, r.v.Content = t, json.RawMessage(content)
			switch {
			case deleted(r.v.Content):
				r.v.Op = "delete"
			case earlier:
				r.v.Op = "update"
			default:
				r.v.Op = "create"
			}
			all = append(all, r)
		}
		rows.Close()
		if rows.Err() != nil {
			return nil, rows.Err()
		}
	}
	for i := 1; i < len(all); i++ {
		for j := i; j > 0 && all[j].when.Before(all[j-1].when); j-- {
			all[j], all[j-1] = all[j-1], all[j]
		}
	}
	out := make([]fhirbench.Version, 0, len(all))
	for _, r := range all {
		out = append(out, r.v)
	}
	return out, nil
}

// deleted: Medplum's history row for a delete holds only resourceType, id and meta, with
// meta.deleted set.
func deleted(c json.RawMessage) bool {
	var m map[string]any
	if json.Unmarshal(c, &m) != nil {
		return false
	}
	if meta, ok := m["meta"].(map[string]any); ok && meta["deleted"] == true {
		return true
	}
	for k := range m {
		if k != "resourceType" && k != "id" && k != "meta" {
			return false
		}
	}
	return true
}

func (m *medplum) known(typ string) bool {
	for _, t := range m.types {
		if t == typ {
			return true
		}
	}
	return false
}

func (m *medplum) Current(ctx context.Context, typ, id string) (string, error) {
	if !m.known(typ) {
		return "", nil
	}
	var v string
	err := m.db.QueryRow(ctx, fmt.Sprintf(`SELECT coalesce(content::json->'meta'->>'versionId', '') FROM public.%s WHERE id::text = $1`,
		pgx.Identifier{typ}.Sanitize()), id).Scan(&v)
	if err == pgx.ErrNoRows {
		return "", nil
	}
	return v, err
}

func (m *medplum) Lookup(ctx context.Context, vs []fhirbench.Version) (fhirbench.Lookups, error) {
	l := fhirbench.Lookups{Exists: map[string]bool{}, EarlierVersions: map[string]int{}}
	for _, v := range vs {
		if v.Op == "delete" {
			n := 0
			rows, err := m.db.Query(ctx, fmt.Sprintf(`SELECT content FROM public.%s WHERE id::text = $1 AND "versionId"::text <> $2`,
				pgx.Identifier{v.Type + "_History"}.Sanitize()), v.ID, v.VersionID)
			if err != nil {
				return l, err
			}
			for rows.Next() {
				var c string
				if err := rows.Scan(&c); err != nil {
					rows.Close()
					return l, err
				}
				if !deleted(json.RawMessage(c)) {
					n++
				}
			}
			rows.Close()
			l.EarlierVersions[v.Type+"/"+v.ID] = n
			continue
		}
		for _, refs := range fhirbench.References(v.Content) {
			for _, ref := range refs {
				if _, done := l.Exists[ref]; done {
					continue
				}
				typ, id, _ := strings.Cut(ref, "/")
				ok := false
				var reg *string
				if err := m.db.QueryRow(ctx, `SELECT pg_catalog.to_regclass($1)::text`, "public."+pgx.Identifier{typ}.Sanitize()).Scan(&reg); err != nil {
					return l, err
				}
				if reg != nil {
					if err := m.db.QueryRow(ctx, fmt.Sprintf(`SELECT EXISTS (SELECT 1 FROM public.%s WHERE id::text = $1 AND NOT deleted)`,
						pgx.Identifier{typ}.Sanitize()), id).Scan(&ok); err != nil {
						return l, err
					}
				}
				l.Exists[ref] = ok
			}
		}
	}
	return l, nil
}

// json sends a JSON request to Medplum's own API (not FHIR) or to FHIR as the admin.
func (m *medplum) json(ctx context.Context, method, path, token string, body, out any) error {
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, m.base+path, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		ct := "application/json"
		if strings.HasPrefix(path, "/fhir/") {
			ct = "application/fhir+json"
		}
		req.Header.Set("Content-Type", ct)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	res, err := m.hc.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 4<<20))
	if res.StatusCode >= 300 {
		if strings.HasPrefix(path, "/auth/") || strings.HasPrefix(path, "/oauth2/") {
			return fmt.Errorf("%s %s: HTTP %d", method, path, res.StatusCode)
		}
		return fmt.Errorf("%s %s: HTTP %d %.300s", method, path, res.StatusCode, b)
	}
	if out != nil {
		return json.Unmarshal(b, out)
	}
	return nil
}

func (m *medplum) form(ctx context.Context, v url.Values, out any) error {
	req, err := http.NewRequestWithContext(ctx, "POST", m.base+"/oauth2/token", strings.NewReader(v.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	res, err := m.hc.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return fmt.Errorf("token endpoint: HTTP %d", res.StatusCode)
	}
	return json.NewDecoder(res.Body).Decode(out)
}
