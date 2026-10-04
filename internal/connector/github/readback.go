package github

import (
	"context"
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

// NativeReadBack implements connector.NativeReadBacker (S29 §3.2 "Read-back checks"). It makes
// one read per call; the Witness retries a 404 and a disagreement within its budget. A 404 is a
// KindNotFound error (the gap readback_not_found), never a mismatch, except for delete_branch,
// where a 404 is the pass. A rate limit is an ordinary error: a gap.
func (c *Connector) NativeReadBack(ctx context.Context, inst *connector.Instance, call connector.Call, res connector.Result) (connector.Observation, []connector.L1Finding, error) {
	st, err := c.state(inst)
	if err != nil {
		return connector.Observation{}, nil, misconfigured(err)
	}
	spec, ok := specFor(call.Tool.Name)
	if !ok || spec.L1 == "" {
		return connector.Observation{}, nil, &connector.ErrReadBackSkipped{Why: "the tool " + clip(call.Tool.Name, 64) + " has no GitHub read-back"}
	}
	a, ce := decodeArgs(st, inst.Name, spec.Name, call.Args)
	if ce != nil {
		return connector.Observation{}, nil, &connector.ErrReadBackSkipped{Why: "the call's arguments don't name a repository this connector reads"}
	}
	s, err := c.open(ctx, inst, st)
	if err != nil {
		return connector.Observation{}, nil, err
	}
	defer s.close()
	rb := &readback{s: s, a: a, call: call, res: res, ev: evidence(res)}
	start := time.Now()
	var obs connector.Observation
	var fs []connector.L1Finding
	switch spec.Name {
	case "create_issue":
		obs, fs, err = rb.issue(ctx)
	case "comment":
		obs, fs, err = rb.comment(ctx)
	case "create_branch":
		obs, fs, err = rb.branch(ctx)
	case "create_or_update_file":
		obs, fs, err = rb.file(ctx)
	case "create_pr":
		obs, fs, err = rb.pr(ctx)
	case "merge_pr":
		obs, fs, err = rb.merged(ctx)
	case "delete_branch":
		obs, fs, err = rb.deleted(ctx)
	case "close_issue":
		obs, fs, err = rb.closed(ctx)
	}
	obs.LatencyMS = time.Since(start).Milliseconds()
	if s.lastResp != nil {
		if obs.Status == 0 {
			obs.Status = s.lastResp.Status
		}
		obs.Meta = meta(s.lastResp)
	}
	return obs, fs, err
}

type readback struct {
	s    *session
	a    args
	call connector.Call
	res  connector.Result
	ev   map[string]string
}

// evidence is the result's evidence, filled from its output when the evidence is missing.
func evidence(res connector.Result) map[string]string {
	ev := map[string]string{}
	for k, v := range res.Evidence {
		ev[k] = v
	}
	if _, ok := ev["number"]; !ok && len(res.Output) > 0 {
		var o struct {
			Number      json.Number `json:"number"`
			IssueNumber json.Number `json:"issue_number"`
			ID          json.Number `json:"id"`
			SHA         string      `json:"sha"`
		}
		if json.Unmarshal(res.Output, &o) == nil {
			if o.Number != "" {
				ev["number"] = o.Number.String()
			} else if o.IssueNumber != "" {
				ev["number"] = o.IssueNumber.String()
			}
			if _, ok := ev["comment_id"]; !ok && o.ID != "" {
				ev["comment_id"] = o.ID.String()
			}
		}
	}
	return ev
}

func (rb *readback) number() (int64, error) {
	if n, err := strconv.ParseInt(rb.ev["number"], 10, 64); err == nil && n > 0 {
		return n, nil
	}
	if rb.a.Number > 0 {
		return rb.a.Number, nil
	}
	return 0, &connector.ErrReadBackSkipped{Why: "the result doesn't name what GitHub created"}
}

func (rb *readback) get(ctx context.Context, q url.Values, more ...string) (*saas.Response, error) {
	p, err := repoPath(rb.a, more...)
	if err != nil {
		return nil, badPath(err)
	}
	return rb.s.do(ctx, saas.Request{Method: http.MethodGet, Path: p, Query: q})
}

// marker compares the marker the body carries with the one the call sent (when it sent one).
func (rb *readback) markerFindings(observedBody string) (string, []connector.L1Finding) {
	want := rb.ev["marker"]
	if _, ok := rb.ev["marker"]; !ok && rb.s.st.cfg.IssueMarker && !rb.call.ActionID.IsZero() {
		want = rb.call.ActionID.String()
	}
	body, id, found := stripMarker(observedBody)
	if want == "" {
		return observedBody, nil
	}
	if !found {
		id = "(none)"
	}
	return body, []connector.L1Finding{connector.Compare("marker", want, id)}
}

func (rb *readback) issue(ctx context.Context) (connector.Observation, []connector.L1Finding, error) {
	n, err := rb.number()
	if err != nil {
		return connector.Observation{}, nil, err
	}
	obs := connector.Observation{Target: fmt.Sprintf("issue #%d on GitHub", n)}
	resp, err := rb.get(ctx, nil, "issues", num(n))
	if err != nil {
		return obs, nil, err
	}
	var i ghIssue
	if err := decode(resp, &i); err != nil {
		return obs, nil, err
	}
	login, err := rb.s.login(ctx)
	if err != nil {
		return obs, nil, err
	}
	body, mf := rb.markerFindings(deref(i.Body))
	fs := []connector.L1Finding{connector.Compare("title", rb.a.Title, i.Title), connector.Compare("body", rb.a.Body, body)}
	fs = append(fs, mf...)
	fs = append(fs, connector.Compare("author", login, i.User.Login))
	return obs, fs, nil
}

func (rb *readback) comment(ctx context.Context) (connector.Observation, []connector.L1Finding, error) {
	n, err := rb.number()
	if err != nil {
		return connector.Observation{}, nil, err
	}
	obs := connector.Observation{Target: fmt.Sprintf("a comment on issue #%d on GitHub", n)}
	id, err := strconv.ParseInt(rb.ev["comment_id"], 10, 64)
	if err != nil || id <= 0 {
		return obs, nil, &connector.ErrReadBackSkipped{Why: "the result doesn't name the comment GitHub created"}
	}
	resp, err := rb.get(ctx, nil, "issues", "comments", num(id))
	if err != nil {
		return obs, nil, err
	}
	var cm ghComment
	if err := decode(resp, &cm); err != nil {
		return obs, nil, err
	}
	onIssue := int64(0)
	if i := strings.LastIndex(cm.IssueURL, "/"); i >= 0 {
		onIssue, _ = strconv.ParseInt(cm.IssueURL[i+1:], 10, 64)
	}
	body, mf := rb.markerFindings(deref(cm.Body))
	fs := []connector.L1Finding{connector.Compare("issue", n, onIssue), connector.Compare("body", rb.a.Body, body)}
	return obs, append(fs, mf...), nil
}

func (rb *readback) branch(ctx context.Context) (connector.Observation, []connector.L1Finding, error) {
	obs := connector.Observation{Target: fmt.Sprintf("branch %s on GitHub", rb.a.Branch)}
	want := rb.ev["sha"]
	if want == "" && reSHA.MatchString(rb.a.From) {
		want = rb.a.From
	}
	if want == "" {
		return obs, nil, &connector.ErrReadBackSkipped{Why: "the result doesn't name the commit the branch was created at"}
	}
	resp, err := rb.get(ctx, nil, append([]string{"git", "ref", "heads"}, segmentsOf(rb.a.Branch)...)...)
	if err != nil {
		return obs, nil, err
	}
	var r ghRef
	if err := decode(resp, &r); err != nil {
		return obs, nil, err
	}
	return obs, []connector.L1Finding{connector.Compare("sha", want, r.Object.SHA)}, nil
}

func (rb *readback) file(ctx context.Context) (connector.Observation, []connector.L1Finding, error) {
	branch := nonEmpty(rb.ev["branch"], rb.a.Branch)
	obs := connector.Observation{Target: fmt.Sprintf("%s on %s on GitHub", rb.a.Path, nonEmpty(branch, "the default branch"))}
	var q url.Values
	if branch != "" {
		q = url.Values{"ref": {branch}}
	}
	resp, err := rb.get(ctx, q, append([]string{"contents"}, segmentsOf(rb.a.Path)...)...)
	if err != nil {
		return obs, nil, err
	}
	var f ghContent
	if err := decode(resp, &f); err != nil {
		return obs, nil, err
	}
	want := BlobSHA([]byte(rb.a.Content))
	return obs, []connector.L1Finding{connector.Compare("blob_sha", want, f.SHA)}, nil
}

func (rb *readback) pr(ctx context.Context) (connector.Observation, []connector.L1Finding, error) {
	n, err := rb.number()
	if err != nil {
		return connector.Observation{}, nil, err
	}
	obs := connector.Observation{Target: fmt.Sprintf("pull request #%d on GitHub", n)}
	resp, err := rb.get(ctx, nil, "pulls", num(n))
	if err != nil {
		return obs, nil, err
	}
	var p ghPR
	if err := decode(resp, &p); err != nil {
		return obs, nil, err
	}
	body, mf := rb.markerFindings(deref(p.Body))
	fs := []connector.L1Finding{
		connector.Compare("state", "open", p.State),
		connector.Compare("head", rb.a.Head, p.Head.Ref),
		connector.Compare("base", rb.a.Base, p.Base.Ref),
		connector.Compare("title", rb.a.Title, p.Title),
		connector.Compare("body", rb.a.Body, body),
	}
	return obs, append(fs, mf...), nil
}

func (rb *readback) merged(ctx context.Context) (connector.Observation, []connector.L1Finding, error) {
	n, err := rb.number()
	if err != nil {
		return connector.Observation{}, nil, err
	}
	obs := connector.Observation{Target: fmt.Sprintf("pull request #%d on GitHub", n)}
	resp, err := rb.get(ctx, nil, "pulls", num(n))
	if err != nil {
		return obs, nil, err
	}
	var p ghPR
	if err := decode(resp, &p); err != nil {
		return obs, nil, err
	}
	fs := []connector.L1Finding{connector.Compare("merged", true, p.Merged)}
	if p.Head.SHA != "" {
		fs = append(fs, connector.Compare("head_sha", rb.a.ExpectedHeadSHA, p.Head.SHA))
	}
	mc := deref(p.MergeCommitSHA)
	if !p.Merged || mc == "" || p.Base.Ref == "" {
		return obs, append(fs, connector.Compare("merge_commit_in_base", "identical or behind", "not merged")), nil
	}
	base := p.Base.Ref
	if strings.Contains(base, "/") {
		// A path segment can't hold "/": compare against the base branch's current commit.
		rr, err := rb.get(ctx, nil, append([]string{"git", "ref", "heads"}, segmentsOf(base)...)...)
		if err != nil {
			return obs, nil, err
		}
		var r ghRef
		if err := decode(rr, &r); err != nil {
			return obs, nil, err
		}
		base = r.Object.SHA
	}
	cr, err := rb.get(ctx, nil, "compare", base+"..."+mc)
	if err != nil {
		return obs, nil, err
	}
	var cmp struct {
		Status string `json:"status"`
	}
	if err := decode(cr, &cmp); err != nil {
		return obs, nil, err
	}
	in := cmp.Status
	if in == "identical" || in == "behind" {
		in = "identical or behind"
	}
	return obs, append(fs, connector.Compare("merge_commit_in_base", "identical or behind", in)), nil
}

func (rb *readback) deleted(ctx context.Context) (connector.Observation, []connector.L1Finding, error) {
	obs := connector.Observation{Target: fmt.Sprintf("branch %s on GitHub", rb.a.Branch)}
	resp, err := rb.get(ctx, nil, append([]string{"git", "ref", "heads"}, segmentsOf(rb.a.Branch)...)...)
	if ce := connector.AsError(err); ce != nil && ce.Kind == connector.KindNotFound {
		obs.Status = http.StatusNotFound
		return obs, []connector.L1Finding{connector.Compare("branch", "no longer resolves", "no longer resolves")}, nil
	}
	if err != nil {
		return obs, nil, err
	}
	var r ghRef
	_ = json.Unmarshal(resp.Body, &r)
	observed := "still resolves"
	if r.Object.SHA != "" {
		observed += " at " + r.Object.SHA
	}
	return obs, []connector.L1Finding{connector.Compare("branch", "no longer resolves", observed)}, nil
}

func (rb *readback) closed(ctx context.Context) (connector.Observation, []connector.L1Finding, error) {
	n, err := rb.number()
	if err != nil {
		return connector.Observation{}, nil, err
	}
	obs := connector.Observation{Target: fmt.Sprintf("issue #%d on GitHub", n)}
	resp, err := rb.get(ctx, nil, "issues", num(n))
	if err != nil {
		return obs, nil, err
	}
	var i ghIssue
	if err := decode(resp, &i); err != nil {
		return obs, nil, err
	}
	return obs, []connector.L1Finding{
		connector.Compare("state", "closed", i.State),
		connector.Compare("state_reason", nonEmpty(rb.a.StateReason, "completed"), deref(i.StateReason)),
	}, nil
}
