package github

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/deedglass/deedglass/internal/connector"
	"github.com/deedglass/deedglass/internal/connector/saas"
)

// args is every tool's arguments; each tool reads its own.
type args struct {
	Repo            string          `json:"repo"`
	Number          int64           `json:"number"`
	State           string          `json:"state"`
	Labels          json.RawMessage `json:"labels"`
	PerPage         int             `json:"per_page"`
	Page            int             `json:"page"`
	Path            string          `json:"path"`
	Ref             string          `json:"ref"`
	Base            string          `json:"base"`
	Head            string          `json:"head"`
	Title           string          `json:"title"`
	Body            string          `json:"body"`
	Draft           bool            `json:"draft"`
	Branch          string          `json:"branch"`
	From            string          `json:"from"`
	Message         string          `json:"message"`
	Content         string          `json:"content"`
	ExpectedSHA     string          `json:"expected_sha"`
	ExpectedHeadSHA string          `json:"expected_head_sha"`
	MergeMethod     string          `json:"merge_method"`
	CommitTitle     string          `json:"commit_title"`
	StateReason     string          `json:"state_reason"`

	owner, name string
	labels      []string
}

// Messages of the hard refusals (S29 §5).
const (
	msgDefaultBranch = "Deedglass never deletes a repository's default branch."
	msgMergeSHA      = "Send expected_head_sha so the merge can't include commits pushed after approval."
	maxLabels        = 10
)

func refuse(msg string) *connector.Error {
	return &connector.Error{Kind: connector.KindInvalidRequest, Code: connector.CodeValidationFailed, AgentMessage: msg}
}

// RepoRefusal is the message for a repo outside the connector's repos.
func RepoRefusal(repo, connectorName string) string {
	return fmt.Sprintf("%s isn't in this connector's repos. Add it in Console › Connectors › %s, or use an allowed repo.", clip(repo, 141), connectorName)
}

// decodeArgs reads a tool's arguments and applies every check that needs no request: the repo
// boundary, path and branch rules, merge_pr's expected_head_sha, the file size cap. A refusal is
// validation_failed and nothing is sent.
func decodeArgs(st *state, connName, tool string, raw json.RawMessage) (args, *connector.Error) {
	var a args
	if err := json.Unmarshal(raw, &a); err != nil {
		return a, connector.ErrValidation("args", "can't be read as the tool's arguments")
	}
	o, n, ok := strings.Cut(a.Repo, "/")
	if !reRepo.MatchString(a.Repo) || n == "." || n == ".." {
		return a, refuse("args.repo must be owner/name, like arjun/deedglass-sandbox.")
	}
	a.owner, a.name = o, n
	if !allowed(st.rules, a.Repo) || !ok {
		return a, refuse(RepoRefusal(a.Repo, connName))
	}
	branchArg := func(param, v string) *connector.Error {
		if v != "" && !validBranch(v) {
			return refuse(fmt.Sprintf("args.%s isn't a branch name Deedglass sends: use letters, digits, '.', '_', '-' and '/', with no '..' and no segment starting with '.'.", param))
		}
		return nil
	}
	switch tool {
	case "get_file", "create_or_update_file":
		if p := pathProblem(a.Path); p != "" {
			return a, refuse("args.path " + p + ".")
		}
	}
	switch tool {
	case "list_prs":
		if e := branchArg("base", a.Base); e != nil {
			return a, e
		}
	case "get_file":
		if len(a.Ref) > 255 || strings.ContainsAny(a.Ref, "\x00\\ ") {
			return a, refuse("args.ref must be a branch, a tag or a commit SHA.")
		}
	case "create_issue":
		if len(a.Labels) > 0 {
			if err := json.Unmarshal(a.Labels, &a.labels); err != nil || len(a.labels) > maxLabels {
				return a, refuse("args.labels must be a list of at most 10 label names.")
			}
		}
	case "create_branch":
		if e := branchArg("branch", a.Branch); e != nil {
			return a, e
		}
		if a.Branch == "" {
			return a, refuse("args.branch is required.")
		}
		if a.From != "" && !reSHA.MatchString(a.From) {
			if e := branchArg("from", a.From); e != nil {
				return a, e
			}
		}
	case "create_or_update_file":
		if e := branchArg("branch", a.Branch); e != nil {
			return a, e
		}
		if int64(len(a.Content)) > st.cfg.MaxFileBytes {
			return a, refuse(fmt.Sprintf("args.content is %s; this connector's limit is %s (max_file_bytes).", connector.Bytes(int64(len(a.Content))), connector.Bytes(st.cfg.MaxFileBytes)))
		}
		if strings.TrimSpace(a.Message) == "" {
			return a, refuse("args.message is required: the commit message.")
		}
	case "create_pr":
		if e := branchArg("head", a.Head); e != nil {
			return a, e
		}
		if e := branchArg("base", a.Base); e != nil {
			return a, e
		}
		if a.Head == "" || a.Base == "" {
			return a, refuse("args.head and args.base are required.")
		}
	case "merge_pr":
		if a.ExpectedHeadSHA == "" {
			return a, refuse(msgMergeSHA)
		}
		if !reSHA.MatchString(a.ExpectedHeadSHA) {
			return a, refuse("args.expected_head_sha must be the full 40-character commit SHA of the pull request's head.")
		}
	case "delete_branch":
		if e := branchArg("branch", a.Branch); e != nil {
			return a, e
		}
		if a.Branch == "" {
			return a, refuse("args.branch is required.")
		}
	}
	return a, nil
}

// repoPath is /repos/{owner}/{name}/more….
func repoPath(a args, more ...string) (string, error) {
	return saas.Segments(append([]string{"repos", a.owner, a.name}, more...)...)
}

func badPath(err error) *connector.Error {
	return &connector.Error{Kind: connector.KindInvalidRequest, Code: connector.CodeValidationFailed,
		AgentMessage: "The arguments can't be turned into a GitHub path. Check them and send the call again.", Err: err}
}

// Execute implements connector.Connector.
func (c *Connector) Execute(ctx context.Context, inst *connector.Instance, call connector.Call) (connector.Result, error) {
	st, err := c.state(inst)
	if err != nil {
		return connector.Result{}, misconfigured(err)
	}
	spec, ok := specFor(call.Tool.Name)
	if !ok {
		return connector.Result{}, &connector.Error{Kind: connector.KindInvalidRequest, Code: "tool_unknown",
			AgentMessage: fmt.Sprintf("The connector %s has no tool named %s.", inst.Name, clip(call.Tool.Name, 64))}
	}
	if err := connector.ValidateArgs(spec.InputSchema, call.Args); err != nil {
		return connector.Result{}, err
	}
	a, ce := decodeArgs(st, inst.Name, spec.Name, call.Args)
	if ce != nil {
		return connector.Result{}, ce
	}
	started := c.now()
	s, err := c.open(ctx, inst, st)
	if err != nil {
		return connector.Result{}, err
	}
	defer s.close()
	x := &exec{s: s, a: a, call: call, started: started}
	var res connector.Result
	switch spec.Name {
	case "get_repo":
		res, err = x.getRepo(ctx)
	case "list_issues":
		res, err = x.listIssues(ctx)
	case "get_issue":
		res, err = x.getIssue(ctx)
	case "get_file":
		res, err = x.getFile(ctx)
	case "list_prs":
		res, err = x.listPRs(ctx)
	case "get_pr":
		res, err = x.getPR(ctx)
	case "create_issue":
		res, err = x.createIssue(ctx)
	case "comment":
		res, err = x.comment(ctx)
	case "create_branch":
		res, err = x.createBranch(ctx)
	case "create_or_update_file":
		res, err = x.putFile(ctx)
	case "create_pr":
		res, err = x.createPR(ctx)
	case "merge_pr":
		res, err = x.mergePR(ctx)
	case "delete_branch":
		res, err = x.deleteBranch(ctx)
	case "close_issue":
		res, err = x.closeIssue(ctx)
	}
	return res, err
}

// exec is one call in flight.
type exec struct {
	s       *session
	a       args
	call    connector.Call
	started time.Time
}

func (x *exec) result(resp *saas.Response, v any, ev map[string]string) (connector.Result, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return connector.Result{}, err
	}
	res := connector.Result{Output: b, Meta: meta(resp), Evidence: ev}
	if resp != nil {
		res.Latency = resp.Latency
	}
	return res, nil
}

func (x *exec) get(ctx context.Context, q url.Values, more ...string) (*saas.Response, error) {
	p, err := repoPath(x.a, more...)
	if err != nil {
		return nil, badPath(err)
	}
	return x.s.do(ctx, saas.Request{Method: http.MethodGet, Path: p, Query: q, Idempotent: true})
}

func (x *exec) send(ctx context.Context, method string, body any, more ...string) (*saas.Response, error) {
	p, err := repoPath(x.a, more...)
	if err != nil {
		return nil, badPath(err)
	}
	return x.s.do(ctx, saas.Request{Method: method, Path: p, JSON: body})
}

func decode(resp *saas.Response, v any) error {
	if err := json.Unmarshal(resp.Body, v); err != nil {
		return &connector.Error{Kind: connector.KindUpstream, Code: connector.CodeUpstreamError, HTTPStatus: resp.Status,
			AgentMessage: "GitHub answered with something Deedglass can't read. The call may have run.", Err: err}
	}
	return nil
}

func num(n int64) string { return strconv.FormatInt(n, 10) }

func pageQuery(a args, q url.Values) url.Values {
	per := a.PerPage
	if per == 0 {
		per = 30
	}
	q.Set("per_page", strconv.Itoa(per))
	if a.Page > 0 {
		q.Set("page", strconv.Itoa(a.Page))
	}
	return q
}

// ---- reads ----

func (x *exec) getRepo(ctx context.Context) (connector.Result, error) {
	resp, err := x.get(ctx, nil)
	if err != nil {
		return connector.Result{}, err
	}
	var r ghRepo
	if err := decode(resp, &r); err != nil {
		return connector.Result{}, err
	}
	x.s.st.cacheDefault(x.a.Repo, r.DefaultBranch, x.s.c.now())
	return x.result(resp, projectRepo(r), nil)
}

func (x *exec) listIssues(ctx context.Context) (connector.Result, error) {
	q := url.Values{"state": {nonEmpty(x.a.State, "open")}}
	if l := strings.TrimSpace(string(x.a.Labels)); l != "" {
		var s string
		if json.Unmarshal(x.a.Labels, &s) == nil && s != "" {
			q.Set("labels", s)
		}
	}
	resp, err := x.get(ctx, pageQuery(x.a, q), "issues")
	if err != nil {
		return connector.Result{}, err
	}
	var is []ghIssue
	if err := decode(resp, &is); err != nil {
		return connector.Result{}, err
	}
	out := issueList{Issues: make([]issueItem, 0, len(is)), Page: max(x.a.Page, 1)}
	for _, i := range is {
		out.Issues = append(out.Issues, projectIssueItem(i))
	}
	return x.result(resp, out, nil)
}

func (x *exec) getIssue(ctx context.Context) (connector.Result, error) {
	resp, err := x.get(ctx, nil, "issues", num(x.a.Number))
	if err != nil {
		return connector.Result{}, err
	}
	var i ghIssue
	if err := decode(resp, &i); err != nil {
		return connector.Result{}, err
	}
	return x.result(resp, projectIssue(i), nil)
}

func (x *exec) getFile(ctx context.Context) (connector.Result, error) {
	var q url.Values
	if x.a.Ref != "" {
		q = url.Values{"ref": {x.a.Ref}}
	}
	resp, err := x.get(ctx, q, append([]string{"contents"}, segmentsOf(x.a.Path)...)...)
	if err != nil {
		return connector.Result{}, err
	}
	if t := strings.TrimSpace(string(resp.Body)); strings.HasPrefix(t, "[") {
		var es []ghContent
		if err := decode(resp, &es); err != nil {
			return connector.Result{}, err
		}
		out := fileOut{Path: x.a.Path, Type: "dir", Entries: []dirEntry{}}
		for i, e := range es {
			if i == 100 {
				break
			}
			out.Entries = append(out.Entries, dirEntry{Name: e.Name, Path: e.Path, Type: e.Type})
		}
		return x.result(resp, out, nil)
	}
	var f ghContent
	if err := decode(resp, &f); err != nil {
		return connector.Result{}, err
	}
	return x.result(resp, projectFile(f), nil)
}

func (x *exec) listPRs(ctx context.Context) (connector.Result, error) {
	q := url.Values{"state": {nonEmpty(x.a.State, "open")}}
	if x.a.Base != "" {
		q.Set("base", x.a.Base)
	}
	resp, err := x.get(ctx, pageQuery(x.a, q), "pulls")
	if err != nil {
		return connector.Result{}, err
	}
	var ps []ghPR
	if err := decode(resp, &ps); err != nil {
		return connector.Result{}, err
	}
	out := prList{Pulls: make([]prItem, 0, len(ps)), Page: max(x.a.Page, 1)}
	for _, p := range ps {
		out.Pulls = append(out.Pulls, projectPRItem(p))
	}
	return x.result(resp, out, nil)
}

func (x *exec) getPR(ctx context.Context) (connector.Result, error) {
	resp, err := x.get(ctx, nil, "pulls", num(x.a.Number))
	if err != nil {
		return connector.Result{}, err
	}
	var p ghPR
	if err := decode(resp, &p); err != nil {
		return connector.Result{}, err
	}
	return x.result(resp, projectPR(p), nil)
}

// ---- writes ----

// marker is the action id the body carries, or "" when issue_marker is off.
func (x *exec) marker() string {
	if !x.s.st.cfg.IssueMarker || x.call.ActionID.IsZero() {
		return ""
	}
	return x.call.ActionID.String()
}

// sendCreate sends a create (never retried). With a marker and a deadline, the POST runs under a
// slightly earlier deadline so that a reconciliation lookup still fits in the call's own.
func (x *exec) sendCreate(ctx context.Context, body any, more ...string) (*saas.Response, error) {
	pctx := ctx
	if dl, ok := ctx.Deadline(); ok && x.marker() != "" {
		reserve := min(reconcileReserve, time.Until(dl)/4)
		var cancel context.CancelFunc
		pctx, cancel = context.WithDeadline(ctx, dl.Add(-reserve))
		defer cancel()
	}
	return x.send(pctx, http.MethodPost, body, more...)
}

func (x *exec) since() string {
	return x.started.Add(-time.Minute).UTC().Format(time.RFC3339)
}

// reconcileIssue looks once for an issue (or a pull request, pr) this connector created since the
// call started that carries this call's marker.
func (x *exec) reconcileIssue(ctx context.Context, pr bool) (*ghIssue, *saas.Response) {
	login, err := x.s.login(ctx)
	if err != nil {
		return nil, nil
	}
	q := url.Values{"creator": {login}, "since": {x.since()}, "state": {"all"}, "sort": {"created"}, "direction": {"desc"},
		"per_page": {strconv.Itoa(reconcilePageSize)}}
	p, err := repoPath(x.a, "issues")
	if err != nil {
		return nil, nil
	}
	resp, err := x.s.cl.Do(ctx, saas.Request{Method: http.MethodGet, Path: p, Query: q})
	if err != nil {
		return nil, nil
	}
	var is []ghIssue
	if json.Unmarshal(resp.Body, &is) != nil {
		return nil, nil
	}
	mk := Marker(x.marker())
	for i := range is {
		if (len(is[i].PullRequest) > 0 && string(is[i].PullRequest) != "null") != pr {
			continue
		}
		if b := deref(is[i].Body); strings.Contains(b, mk) {
			return &is[i], resp
		}
	}
	return nil, nil
}

func (x *exec) createIssue(ctx context.Context) (connector.Result, error) {
	body := x.a.Body
	if m := x.marker(); m != "" {
		body = withMarker(body, m)
	}
	req := map[string]any{"title": x.a.Title, "body": body}
	if len(x.a.labels) > 0 {
		req["labels"] = x.a.labels
	}
	resp, err := x.sendCreate(ctx, req, "issues")
	if err != nil {
		if saas.IsSent(err) && x.marker() != "" {
			if i, r := x.reconcileIssue(ctx, false); i != nil {
				res, rerr := x.result(r, createdIssue{Number: i.Number, HTMLURL: i.HTMLURL, State: i.State, Title: i.Title},
					map[string]string{"number": num(i.Number), "marker": x.marker()})
				res.Meta["reconciled"] = true
				return res, rerr
			}
		}
		return connector.Result{}, err
	}
	var i ghIssue
	if err := decode(resp, &i); err != nil {
		return connector.Result{}, err
	}
	return x.result(resp, createdIssue{Number: i.Number, HTMLURL: i.HTMLURL, State: i.State, Title: i.Title},
		map[string]string{"number": num(i.Number), "marker": x.marker()})
}

func (x *exec) comment(ctx context.Context) (connector.Result, error) {
	body := x.a.Body
	if m := x.marker(); m != "" {
		body = withMarker(body, m)
	}
	resp, err := x.sendCreate(ctx, map[string]any{"body": body}, "issues", num(x.a.Number), "comments")
	if err != nil {
		if saas.IsSent(err) && x.marker() != "" {
			if cm, r := x.reconcileComment(ctx); cm != nil {
				res, rerr := x.result(r, createdComment{ID: cm.ID, IssueNumber: x.a.Number, HTMLURL: cm.HTMLURL},
					map[string]string{"number": num(x.a.Number), "comment_id": num(cm.ID), "marker": x.marker()})
				res.Meta["reconciled"] = true
				return res, rerr
			}
		}
		return connector.Result{}, err
	}
	var cm ghComment
	if err := decode(resp, &cm); err != nil {
		return connector.Result{}, err
	}
	return x.result(resp, createdComment{ID: cm.ID, IssueNumber: x.a.Number, HTMLURL: cm.HTMLURL},
		map[string]string{"number": num(x.a.Number), "comment_id": num(cm.ID), "marker": x.marker()})
}

func (x *exec) reconcileComment(ctx context.Context) (*ghComment, *saas.Response) {
	p, err := repoPath(x.a, "issues", num(x.a.Number), "comments")
	if err != nil {
		return nil, nil
	}
	resp, err := x.s.cl.Do(ctx, saas.Request{Method: http.MethodGet, Path: p,
		Query: url.Values{"since": {x.since()}, "per_page": {strconv.Itoa(reconcilePageSize)}}})
	if err != nil {
		return nil, nil
	}
	var cs []ghComment
	if json.Unmarshal(resp.Body, &cs) != nil {
		return nil, nil
	}
	mk := Marker(x.marker())
	for i := range cs {
		if strings.Contains(deref(cs[i].Body), mk) {
			return &cs[i], resp
		}
	}
	return nil, nil
}

// refSHA reads where a branch points.
func (x *exec) refSHA(ctx context.Context, branch string) (string, *saas.Response, error) {
	resp, err := x.get(ctx, nil, append([]string{"git", "ref", "heads"}, segmentsOf(branch)...)...)
	if err != nil {
		return "", resp, err
	}
	var r ghRef
	if err := decode(resp, &r); err != nil {
		return "", resp, err
	}
	return r.Object.SHA, resp, nil
}

func (x *exec) createBranch(ctx context.Context) (connector.Result, error) {
	sha := x.a.From
	if !reSHA.MatchString(sha) {
		from := x.a.From
		if from == "" {
			db, err := x.s.defaultBranch(ctx, x.a)
			if err != nil {
				return connector.Result{}, err
			}
			from = db
		}
		s, _, err := x.refSHA(ctx, from)
		if err != nil {
			return connector.Result{}, err
		}
		sha = s
	}
	ref := "refs/heads/" + x.a.Branch
	ev := map[string]string{"branch": x.a.Branch, "sha": sha}
	resp, err := x.send(ctx, http.MethodPost, map[string]string{"ref": ref, "sha": sha}, "git", "refs")
	if err != nil {
		ce := connector.AsError(err)
		if ce == nil || resp == nil || resp.Status != http.StatusUnprocessableEntity || !strings.Contains(strings.ToLower(string(resp.Body)), "reference already exists") {
			return connector.Result{}, err
		}
		have, r2, gerr := x.refSHA(ctx, x.a.Branch)
		if gerr != nil {
			return connector.Result{}, err
		}
		if have != sha {
			return connector.Result{}, &connector.Error{Kind: connector.KindInvalidRequest, Code: "conflict", HTTPStatus: http.StatusUnprocessableEntity,
				AgentMessage: fmt.Sprintf("The branch %s already exists in %s at another commit. Nothing was changed. Choose another branch name.", x.a.Branch, x.a.Repo)}
		}
		return x.result(r2, createdBranch{Ref: ref, Branch: x.a.Branch, SHA: sha, Existed: true}, ev)
	}
	return x.result(resp, createdBranch{Ref: ref, Branch: x.a.Branch, SHA: sha}, ev)
}

func (x *exec) putFile(ctx context.Context) (connector.Result, error) {
	body := map[string]any{"message": x.a.Message, "content": base64.StdEncoding.EncodeToString([]byte(x.a.Content))}
	if x.a.Branch != "" {
		body["branch"] = x.a.Branch
	}
	if x.a.ExpectedSHA != "" {
		body["sha"] = x.a.ExpectedSHA
	}
	resp, err := x.send(ctx, http.MethodPut, body, append([]string{"contents"}, segmentsOf(x.a.Path)...)...)
	if err != nil {
		return connector.Result{}, err
	}
	var r struct {
		Content ghContent `json:"content"`
		Commit  struct {
			SHA string `json:"sha"`
		} `json:"commit"`
	}
	if err := decode(resp, &r); err != nil {
		return connector.Result{}, err
	}
	return x.result(resp, writtenFile{Path: x.a.Path, Branch: x.a.Branch, BlobSHA: r.Content.SHA, CommitSHA: r.Commit.SHA, HTMLURL: r.Content.HTMLURL},
		map[string]string{"blob_sha": BlobSHA([]byte(x.a.Content)), "branch": x.a.Branch, "path": x.a.Path})
}

func (x *exec) createPR(ctx context.Context) (connector.Result, error) {
	body := x.a.Body
	if m := x.marker(); m != "" {
		body = withMarker(body, m)
	}
	req := map[string]any{"title": x.a.Title, "head": x.a.Head, "base": x.a.Base, "body": body}
	if x.a.Draft {
		req["draft"] = true
	}
	resp, err := x.sendCreate(ctx, req, "pulls")
	if err != nil {
		if saas.IsSent(err) && x.marker() != "" {
			if i, r := x.reconcileIssue(ctx, true); i != nil {
				res, rerr := x.result(r, createdPR{Number: i.Number, HTMLURL: i.HTMLURL, State: i.State, Title: i.Title, Head: x.a.Head, Base: x.a.Base},
					map[string]string{"number": num(i.Number), "marker": x.marker()})
				res.Meta["reconciled"] = true
				return res, rerr
			}
		}
		return connector.Result{}, err
	}
	var p ghPR
	if err := decode(resp, &p); err != nil {
		return connector.Result{}, err
	}
	return x.result(resp, createdPR{Number: p.Number, HTMLURL: p.HTMLURL, State: p.State, Title: p.Title, Head: p.Head.Ref, Base: p.Base.Ref},
		map[string]string{"number": num(p.Number), "marker": x.marker()})
}

func (x *exec) mergePR(ctx context.Context) (connector.Result, error) {
	body := map[string]any{"sha": x.a.ExpectedHeadSHA}
	if x.a.MergeMethod != "" {
		body["merge_method"] = x.a.MergeMethod
	}
	if x.a.CommitTitle != "" {
		body["commit_title"] = x.a.CommitTitle
	}
	resp, err := x.send(ctx, http.MethodPut, body, "pulls", num(x.a.Number), "merge")
	if err != nil {
		return connector.Result{}, err
	}
	var r struct {
		SHA    string `json:"sha"`
		Merged bool   `json:"merged"`
	}
	if err := decode(resp, &r); err != nil {
		return connector.Result{}, err
	}
	return x.result(resp, mergedPR{Number: x.a.Number, Merged: r.Merged, SHA: r.SHA},
		map[string]string{"number": num(x.a.Number), "merge_sha": r.SHA})
}

func (x *exec) deleteBranch(ctx context.Context) (connector.Result, error) {
	db, err := x.s.defaultBranch(ctx, x.a)
	if err != nil {
		return connector.Result{}, err
	}
	if strings.EqualFold(db, x.a.Branch) {
		return connector.Result{}, refuse(msgDefaultBranch)
	}
	resp, err := x.send(ctx, http.MethodDelete, nil, append([]string{"git", "refs", "heads"}, segmentsOf(x.a.Branch)...)...)
	if err != nil {
		return connector.Result{}, err
	}
	return x.result(resp, deletedBranch{Branch: x.a.Branch, Deleted: true}, map[string]string{"branch": x.a.Branch})
}

func (x *exec) closeIssue(ctx context.Context) (connector.Result, error) {
	reason := nonEmpty(x.a.StateReason, "completed")
	resp, err := x.send(ctx, http.MethodPatch, map[string]string{"state": "closed", "state_reason": reason}, "issues", num(x.a.Number))
	if err != nil {
		return connector.Result{}, err
	}
	var i ghIssue
	if err := decode(resp, &i); err != nil {
		return connector.Result{}, err
	}
	return x.result(resp, closedIssue{Number: i.Number, State: i.State, StateReason: deref(i.StateReason), HTMLURL: i.HTMLURL},
		map[string]string{"number": num(x.a.Number), "state_reason": reason})
}

// ---- the default branch (cached per repo for 5 minutes) ----

func (st *state) cacheDefault(repo, branch string, now time.Time) {
	if branch == "" {
		return
	}
	st.mu.Lock()
	st.defaults[strings.ToLower(repo)] = cachedBranch{name: branch, at: now}
	st.mu.Unlock()
}

func (st *state) cachedDefault(repo string, now time.Time) (string, bool) {
	st.mu.Lock()
	defer st.mu.Unlock()
	cb, ok := st.defaults[strings.ToLower(repo)]
	if !ok || now.Sub(cb.at) >= defaultBranchTTL {
		return "", false
	}
	return cb.name, true
}

func (s *session) defaultBranch(ctx context.Context, a args) (string, error) {
	if b, ok := s.st.cachedDefault(a.Repo, s.c.now()); ok {
		return b, nil
	}
	p, err := repoPath(a)
	if err != nil {
		return "", badPath(err)
	}
	resp, err := s.do(ctx, saas.Request{Method: http.MethodGet, Path: p, Idempotent: true})
	if err != nil {
		return "", err
	}
	var r ghRepo
	if err := decode(resp, &r); err != nil {
		return "", err
	}
	if r.DefaultBranch == "" {
		return "", unreadable()
	}
	s.st.cacheDefault(a.Repo, r.DefaultBranch, s.c.now())
	return r.DefaultBranch, nil
}
