package github

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/deedglass/deedglass/internal/connector"
	"github.com/deedglass/deedglass/internal/connector/saas"
)

// Facts is request.github (policy.GitHubFacts' JSON keys exactly).
type Facts struct {
	Repo             string `json:"repo"`
	Owner            string `json:"owner"`
	Name             string `json:"name"`
	DefaultBranch    string `json:"default_branch"`
	Branch           string `json:"branch"`
	Base             string `json:"base"`
	Head             string `json:"head"`
	Path             string `json:"path"`
	ProtectedBranch  bool   `json:"protected_branch"`
	IsDefaultBranch  bool   `json:"is_default_branch"`
	TouchesWorkflows bool   `json:"touches_workflows"`
	Enriched         bool   `json:"enriched"`
}

// Map is the facts as the plain map the Gate reads ({"github": {...}}).
func (f Facts) Map() map[string]any {
	return map[string]any{"github": map[string]any{
		"repo": f.Repo, "owner": f.Owner, "name": f.Name, "default_branch": f.DefaultBranch, "branch": f.Branch,
		"base": f.Base, "head": f.Head, "path": f.Path, "protected_branch": f.ProtectedBranch,
		"is_default_branch": f.IsDefaultBranch, "touches_workflows": f.TouchesWorkflows, "enriched": f.Enriched,
	}}
}

// touchesWorkflows reports whether p is under .github/workflows/ or .github/actions/.
func touchesWorkflows(p string) bool {
	l := strings.ToLower(p)
	return strings.HasPrefix(l, ".github/workflows/") || strings.HasPrefix(l, ".github/actions/")
}

// Enrich implements connector.Enricher: request.github for the decision. The default branch comes
// from GET /repos/{o}/{r}, cached per repo for 5 minutes; merge_pr also reads the pull request for
// its base and head. A failed lookup never blocks the decision: enriched is false, and a branch
// that couldn't be learned counts as protected. Arguments the connector would refuse give no facts.
func (c *Connector) Enrich(ctx context.Context, inst *connector.Instance, t connector.ToolSpec, raw json.RawMessage) (map[string]any, error) {
	st, err := c.state(inst)
	if err != nil {
		return nil, nil
	}
	spec, ok := specFor(t.Name)
	if !ok || connector.ValidateArgs(spec.InputSchema, raw) != nil {
		return nil, nil
	}
	a, ce := decodeArgs(st, inst.Name, spec.Name, raw)
	if ce != nil {
		return nil, nil
	}
	f := Facts{Repo: a.Repo, Owner: a.owner, Name: a.name, Path: a.Path, TouchesWorkflows: a.Path != "" && touchesWorkflows(a.Path), Enriched: true}
	switch spec.Name {
	case "create_branch", "delete_branch", "create_or_update_file":
		f.Branch = a.Branch
	case "create_pr", "list_prs":
		f.Base, f.Head = a.Base, a.Head
	}

	db, ok := st.cachedDefault(a.Repo, c.now())
	var s *session
	if !ok || spec.Name == "merge_pr" {
		s, err = c.open(ctx, inst, st)
		if err != nil {
			s = nil
		}
		defer s.close()
	}
	if !ok {
		if s == nil {
			f.Enriched = false
		} else if b, err := s.defaultBranch(ctx, a); err == nil {
			db = b
		} else {
			f.Enriched = false
		}
	}
	if spec.Name == "merge_pr" {
		if s == nil {
			f.Enriched = false
		} else if p, err := s.pull(ctx, a); err == nil {
			f.Base, f.Head = p.Base.Ref, p.Head.Ref
		} else {
			f.Enriched = false
		}
	}
	f.DefaultBranch = db

	// The branch a write lands on: a commit with no branch writes to the default branch.
	target := f.Branch
	switch spec.Name {
	case "create_or_update_file":
		if target == "" {
			target = db
			f.Branch = db
		}
	case "create_pr", "merge_pr":
		target = f.Base
	}
	switch {
	case target != "":
		f.ProtectedBranch = protected(st.cfg.ProtectedBranches, target)
		f.IsDefaultBranch = db != "" && target == db
	case !f.Enriched && (spec.Name == "create_or_update_file" || spec.Name == "merge_pr"):
		f.ProtectedBranch = true // the branch written is unknown: treat it as protected
	}
	return f.Map(), nil
}

// pull reads one pull request (merge_pr's enrichment).
func (s *session) pull(ctx context.Context, a args) (ghPR, error) {
	var p ghPR
	path, err := repoPath(a, "pulls", num(a.Number))
	if err != nil {
		return p, badPath(err)
	}
	resp, err := s.do(ctx, saas.Request{Method: http.MethodGet, Path: path, Idempotent: true})
	if err != nil {
		return p, err
	}
	return p, decode(resp, &p)
}
