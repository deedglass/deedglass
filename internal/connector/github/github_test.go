package github

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/deedglass/deedglass/internal/connector"
	"github.com/deedglass/deedglass/internal/connector/saas"
	"github.com/deedglass/deedglass/internal/connector/saas/saastest"
)

func run(t *testing.T, c *Connector, inst *connector.Instance, tool, args string) (connector.Result, error) {
	t.Helper()
	return c.Execute(ctxWithRedactor(), inst, call(t, tool, args))
}

func readBack(t *testing.T, c *Connector, inst *connector.Instance, tool, args string, res connector.Result) (connector.Observation, []connector.L1Finding, error) {
	t.Helper()
	return c.NativeReadBack(ctxWithRedactor(), inst, call(t, tool, args), res)
}

// ---- S29 §6 "GitHub (fixtures)" ----

func TestCreateIssueVerified(t *testing.T) {
	c, inst, srv := setup(t, "create_issue", nil)
	args := `{"repo":"arjun/deedglass-sandbox","title":"Flaky checkout test","body":"Fails 1 in 20 runs on CI."}`
	res, err := run(t, c, inst, "create_issue", args)
	if err != nil {
		t.Fatal(err)
	}
	out := output(t, res)
	if out["number"] != float64(42) || out["state"] != "open" || out["title"] != "Flaky checkout test" || len(out) != 4 {
		t.Fatalf("output %s", res.Output)
	}
	if res.Meta["ratelimit_remaining"] != 4987 || res.Meta["http_status"] != 201 {
		t.Fatalf("meta %v", res.Meta)
	}
	var sent struct{ Body string }
	if err := json.Unmarshal(srv.Requests()[0].Body, &sent); err != nil || !strings.HasSuffix(sent.Body, "\n\n<!-- deedglass:"+testAction+" -->") {
		t.Fatalf("sent body %q has no marker", sent.Body)
	}
	if srv.Count("POST issues") != 1 {
		t.Fatalf("POST issues = %d", srv.Count("POST issues"))
	}
	obs, fs, err := readBack(t, c, inst, "create_issue", args, res)
	if err != nil {
		t.Fatal(err)
	}
	if ok, bad := allMatch(fs); !ok {
		t.Fatalf("finding %s failed: %+v", bad, fs)
	}
	if obs.Target != "issue #42 on GitHub" {
		t.Fatalf("target %q", obs.Target)
	}
	fields := []string{}
	for _, f := range fs {
		fields = append(fields, f.Field)
	}
	if strings.Join(fields, ",") != "title,body,marker,author" {
		t.Fatalf("fields %v", fields)
	}
}

func TestCreateIssueEditedBodyMismatch(t *testing.T) {
	c, inst, _ := setup(t, "create_issue_edited", nil)
	long := strings.TrimSpace(strings.Repeat("Fails 1 in 20 runs on CI. ", 12))
	args, _ := json.Marshal(map[string]string{"repo": testRepo, "title": "Flaky checkout test", "body": long})
	res, err := run(t, c, inst, "create_issue", string(args))
	if err != nil {
		t.Fatal(err)
	}
	_, fs, err := readBack(t, c, inst, "create_issue", string(args), res)
	if err != nil {
		t.Fatal(err)
	}
	var body *connector.L1Finding
	for i := range fs {
		if fs[i].Field == "body" {
			body = &fs[i]
		} else if !fs[i].Match {
			t.Fatalf("%s should match: %+v", fs[i].Field, fs[i])
		}
	}
	if body == nil || body.Match {
		t.Fatalf("body finding %+v", body)
	}
	e, _ := body.Expected.(string)
	o, _ := body.Observed.(string)
	if !strings.HasPrefix(e, "sha256:") || !strings.HasPrefix(o, "sha256:") || e == o {
		t.Fatalf("want two digests, got %v / %v", body.Expected, body.Observed)
	}
}

func TestReconcileFound(t *testing.T) {
	c, inst, srv := setup(t, "reconcile_found", nil)
	ctx, cancel := context.WithTimeout(ctxWithRedactor(), 1600*time.Millisecond)
	defer cancel()
	args := `{"repo":"arjun/deedglass-sandbox","title":"Flaky checkout test","body":"Fails 1 in 20 runs on CI."}`
	res, err := c.Execute(ctx, inst, call(t, "create_issue", args))
	if err != nil {
		t.Fatal(err)
	}
	if res.Meta["reconciled"] != true {
		t.Fatalf("meta %v", res.Meta)
	}
	if out := output(t, res); out["number"] != float64(42) {
		t.Fatalf("output %s", res.Output)
	}
	if n := srv.Count("POST issues"); n != 1 {
		t.Fatalf("POST issues = %d, want 1", n)
	}
}

func TestReconcileAbsent(t *testing.T) {
	c, inst, srv := setup(t, "reconcile_absent", nil)
	ctx, cancel := context.WithTimeout(ctxWithRedactor(), 1600*time.Millisecond)
	defer cancel()
	_, err := c.Execute(ctx, inst, call(t, "create_issue", `{"repo":"arjun/deedglass-sandbox","title":"Flaky checkout test","body":"Fails 1 in 20 runs on CI."}`))
	ce := mustErr(t, err)
	if !saas.IsSent(err) || ce.Kind != connector.KindTimeout || !strings.Contains(ce.AgentMessage, "may have run") {
		t.Fatalf("err = %+v", ce)
	}
	if n := srv.Count("POST issues"); n != 1 {
		t.Fatalf("POST issues = %d, want 1", n)
	}
}

func TestReconcileOffWithoutMarker(t *testing.T) {
	srv := saastest.New(t, saastest.Load(t, "testdata/reconcile_absent.json")[0])
	c := newConn()
	inst := instance(t, c, cfgJSON(srv.URL, map[string]any{"issue_marker": false}), patOpener(sentinelToken()))
	ctx, cancel := context.WithTimeout(ctxWithRedactor(), 600*time.Millisecond)
	defer cancel()
	_, err := c.Execute(ctx, inst, call(t, "create_issue", `{"repo":"arjun/deedglass-sandbox","title":"Flaky checkout test","body":"Fails 1 in 20 runs on CI."}`))
	if !saas.IsSent(err) {
		t.Fatalf("err = %v", err)
	}
	if srv.Count("GET issues since") != 0 || srv.Count("GET user") != 0 {
		t.Fatal("issue_marker: false must not reconcile")
	}
}

func TestReconcileCommentAndPR(t *testing.T) {
	c, inst, srv := setup(t, "reconcile_comment", nil)
	ctx, cancel := context.WithTimeout(ctxWithRedactor(), 1600*time.Millisecond)
	defer cancel()
	res, err := c.Execute(ctx, inst, call(t, "comment", `{"repo":"arjun/deedglass-sandbox","number":42,"body":"Looks good."}`))
	if err != nil || res.Meta["reconciled"] != true || srv.Count("POST comments") != 1 {
		t.Fatalf("res %+v err %v", res, err)
	}
	if out := output(t, res); out["id"] != float64(1001) {
		t.Fatalf("output %s", res.Output)
	}

	c2, inst2, srv2 := setup(t, "reconcile_pr", nil)
	ctx2, cancel2 := context.WithTimeout(ctxWithRedactor(), 1600*time.Millisecond)
	defer cancel2()
	res, err = c2.Execute(ctx2, inst2, call(t, "create_pr", `{"repo":"arjun/deedglass-sandbox","head":"agent/fix-typo","base":"main","title":"Fix typo in README","body":"Fixes the typo."}`))
	if err != nil || res.Meta["reconciled"] != true || srv2.Count("POST pulls") != 1 {
		t.Fatalf("res %+v err %v", res, err)
	}
	if out := output(t, res); out["number"] != float64(7) || out["head"] != "agent/fix-typo" {
		t.Fatalf("output %s", res.Output)
	}
}

func TestReadBackNotFoundIsAGap(t *testing.T) {
	c, inst, srv := setup(t, "readback_404", nil)
	res := connector.Result{Output: json.RawMessage(`{"number":42,"html_url":"x","state":"open","title":"t"}`), Evidence: map[string]string{"number": "42", "marker": testAction}}
	for i := 0; i < 3; i++ {
		_, fs, err := readBack(t, c, inst, "create_issue", `{"repo":"arjun/deedglass-sandbox","title":"t"}`, res)
		if ce := mustErr(t, err); ce.Kind != connector.KindNotFound || len(fs) != 0 {
			t.Fatalf("attempt %d: %+v %v", i, ce, fs)
		}
	}
	if srv.Count("GET issue") != 3 {
		t.Fatalf("reads = %d", srv.Count("GET issue"))
	}
}

func TestRepoOutsideAllowList(t *testing.T) {
	c, inst, srv := setup(t, "get_repo", nil)
	srv.Add() // nothing more
	for tool, args := range map[string]string{"create_issue": `{"repo":"arjun/payments","title":"x"}`, "get_repo": `{"repo":"arjun/payments"}`,
		"delete_branch": `{"repo":"arjun/payments","branch":"x"}`, "merge_pr": `{"repo":"arjun/payments","number":1}`} {
		_, err := run(t, c, inst, tool, args)
		ce := mustErr(t, err)
		want := "arjun/payments isn't in this connector's repos. Add it in Console › Connectors › github, or use an allowed repo."
		if ce.Code != connector.CodeValidationFailed || ce.AgentMessage != want {
			t.Fatalf("%s: %+v", tool, ce)
		}
	}
	if n := len(srv.Requests()); n != 0 {
		t.Fatalf("%d requests sent", n)
	}
	// Satisfy the fixture.
	if _, err := run(t, c, inst, "get_repo", `{"repo":"arjun/deedglass-sandbox"}`); err != nil {
		t.Fatal(err)
	}
}

func TestCreateOrUpdateFileBlobVerified(t *testing.T) {
	c, inst, _ := setup(t, "file", nil)
	args := `{"repo":"arjun/deedglass-sandbox","path":"docs/x.md","branch":"agent/fix-typo","message":"Add x","content":"hello\n"}`
	res, err := run(t, c, inst, "create_or_update_file", args)
	if err != nil {
		t.Fatal(err)
	}
	if res.Evidence["blob_sha"] != "ce013625030ba8dba906f756967f9e9ca394464a" {
		t.Fatalf("evidence %v", res.Evidence)
	}
	obs, fs, err := readBack(t, c, inst, "create_or_update_file", args, res)
	if err != nil {
		t.Fatal(err)
	}
	if ok, bad := allMatch(fs); !ok || fs[0].Field != "blob_sha" {
		t.Fatalf("finding %s: %+v", bad, fs)
	}
	if obs.Target != "docs/x.md on agent/fix-typo on GitHub" {
		t.Fatalf("target %q", obs.Target)
	}
}

func TestFileOnDefaultBranchMismatch(t *testing.T) {
	c, inst, _ := setup(t, "file_default", nil)
	args := `{"repo":"arjun/deedglass-sandbox","path":"docs/x.md","message":"Edit x","content":"hello\n","expected_sha":"0123456789abcdef0123456789abcdef01234567"}`
	res, err := run(t, c, inst, "create_or_update_file", args)
	if err != nil {
		t.Fatal(err)
	}
	obs, fs, err := readBack(t, c, inst, "create_or_update_file", args, res)
	if err != nil || len(fs) != 1 || fs[0].Match {
		t.Fatalf("fs %+v err %v", fs, err)
	}
	if obs.Target != "docs/x.md on the default branch on GitHub" {
		t.Fatalf("target %q", obs.Target)
	}
}

func TestBadPathsRefused(t *testing.T) {
	c, inst, srv := setup(t, "get_repo", nil)
	for _, p := range []string{"../x", ".git/config", "/etc/passwd", `a\b`, "a/./b", "a//b", "docs/.GIT/x", "a\x01b", "é.md", strings.Repeat("a", 1025), ""} {
		for _, tool := range []string{"create_or_update_file", "get_file"} {
			args, _ := json.Marshal(map[string]string{"repo": testRepo, "path": p, "message": "m", "content": "c"})
			_, err := run(t, c, inst, tool, string(args))
			if ce := mustErr(t, err); ce.Code != connector.CodeValidationFailed {
				t.Fatalf("%s %q: %+v", tool, p, ce)
			}
		}
	}
	if n := len(srv.Requests()); n != 0 {
		t.Fatalf("%d requests sent", n)
	}
	if _, err := run(t, c, inst, "get_repo", `{"repo":"arjun/deedglass-sandbox"}`); err != nil {
		t.Fatal(err)
	}
}

func TestEnrichWorkflowPath(t *testing.T) {
	c, inst, srv := setup(t, "enrich", nil)
	spec, _ := specFor("create_or_update_file")
	m, err := c.Enrich(ctxWithRedactor(), inst, spec, json.RawMessage(`{"repo":"arjun/deedglass-sandbox","path":".github/workflows/ci.yml","message":"m","content":"x"}`))
	if err != nil {
		t.Fatal(err)
	}
	g := m["github"].(map[string]any)
	want := map[string]any{"repo": testRepo, "owner": "arjun", "name": "deedglass-sandbox", "default_branch": "main", "branch": "main", "base": "",
		"head": "", "path": ".github/workflows/ci.yml", "protected_branch": true, "is_default_branch": true, "touches_workflows": true, "enriched": true}
	for k, v := range want {
		if g[k] != v {
			t.Errorf("%s = %v, want %v", k, g[k], v)
		}
	}
	if len(g) != len(want) {
		t.Fatalf("keys %v", g)
	}
	// Cached: a second call makes no request.
	spec, _ = specFor("create_pr")
	m, _ = c.Enrich(ctxWithRedactor(), inst, spec, json.RawMessage(`{"repo":"arjun/deedglass-sandbox","head":"agent/x","base":"release/2.0","title":"t"}`))
	g = m["github"].(map[string]any)
	if g["protected_branch"] != true || g["is_default_branch"] != false || g["base"] != "release/2.0" || srv.Count("GET repo") != 1 {
		t.Fatalf("pr facts %v, GET repo %d", g, srv.Count("GET repo"))
	}
	spec, _ = specFor("create_branch")
	m, _ = c.Enrich(ctxWithRedactor(), inst, spec, json.RawMessage(`{"repo":"arjun/deedglass-sandbox","branch":"agent/x"}`))
	if g = m["github"].(map[string]any); g["protected_branch"] != false || g["branch"] != "agent/x" {
		t.Fatalf("branch facts %v", g)
	}
	// Refused arguments give no facts.
	if m, err := c.Enrich(ctxWithRedactor(), inst, spec, json.RawMessage(`{"repo":"arjun/payments","branch":"x"}`)); m != nil || err != nil {
		t.Fatalf("facts for a refused repo: %v %v", m, err)
	}
}

func TestEnrichFailureNeverBlocks(t *testing.T) {
	c, inst, _ := setup(t, "enrich_fail", nil)
	spec, _ := specFor("create_or_update_file")
	m, err := c.Enrich(ctxWithRedactor(), inst, spec, json.RawMessage(`{"repo":"arjun/deedglass-sandbox","path":"a.md","message":"m","content":"x"}`))
	if err != nil {
		t.Fatal(err)
	}
	g := m["github"].(map[string]any)
	if g["enriched"] != false || g["protected_branch"] != true {
		t.Fatalf("facts %v", g)
	}
	// No credential: still facts, enriched false.
	c2 := newConn()
	inst2 := instance(t, c2, cfgJSON("http://127.0.0.1:1", nil), nil)
	spec, _ = specFor("merge_pr")
	m, _ = c2.Enrich(ctxWithRedactor(), inst2, spec, json.RawMessage(`{"repo":"arjun/deedglass-sandbox","number":7,"expected_head_sha":"9f2c4e6a8b0d1f3a5c7e9b1d3f5a7c9e1b3d5f7a"}`))
	if g = m["github"].(map[string]any); g["enriched"] != false || g["protected_branch"] != true {
		t.Fatalf("facts %v", g)
	}
}

func TestEnrichMergePR(t *testing.T) {
	c, inst, _ := setup(t, "enrich_merge", nil)
	spec, _ := specFor("merge_pr")
	m, _ := c.Enrich(ctxWithRedactor(), inst, spec, json.RawMessage(`{"repo":"arjun/deedglass-sandbox","number":7,"expected_head_sha":"9f2c4e6a8b0d1f3a5c7e9b1d3f5a7c9e1b3d5f7a"}`))
	g := m["github"].(map[string]any)
	if g["base"] != "main" || g["head"] != "agent/fix-typo" || g["protected_branch"] != true || g["is_default_branch"] != true || g["enriched"] != true {
		t.Fatalf("facts %v", g)
	}
}

func TestDeleteDefaultBranchRefused(t *testing.T) {
	c, inst, srv := setup(t, "delete_branch_default", nil)
	// Cold cache: the default branch is read (a GET), and no DELETE is sent.
	_, err := run(t, c, inst, "delete_branch", `{"repo":"arjun/deedglass-sandbox","branch":"main"}`)
	if ce := mustErr(t, err); ce.Code != connector.CodeValidationFailed || ce.AgentMessage != "Deedglass never deletes a repository's default branch." {
		t.Fatalf("%+v", ce)
	}
	before := len(srv.Requests())
	// Warm cache (Enrich ran before the decision): 0 requests.
	_, err = run(t, c, inst, "delete_branch", `{"repo":"arjun/deedglass-sandbox","branch":"MAIN"}`)
	if ce := mustErr(t, err); ce.AgentMessage != msgDefaultBranch {
		t.Fatalf("%+v", ce)
	}
	if after := len(srv.Requests()); after != before || before != 1 {
		t.Fatalf("requests %d then %d", before, after)
	}
}

func TestDeleteBranchVerifiedAndMismatch(t *testing.T) {
	args := `{"repo":"arjun/deedglass-sandbox","branch":"agent/fix-typo"}`
	c, inst, _ := setup(t, "delete_branch", nil)
	res, err := run(t, c, inst, "delete_branch", args)
	if err != nil {
		t.Fatal(err)
	}
	obs, fs, err := readBack(t, c, inst, "delete_branch", args, res)
	if ok, _ := allMatch(fs); err != nil || !ok || obs.Target != "branch agent/fix-typo on GitHub" || obs.Status != 404 {
		t.Fatalf("obs %+v fs %+v err %v", obs, fs, err)
	}

	c, inst, _ = setup(t, "delete_branch_still", nil)
	res, err = run(t, c, inst, "delete_branch", args)
	if err != nil {
		t.Fatal(err)
	}
	_, fs, err = readBack(t, c, inst, "delete_branch", args, res)
	if err != nil || len(fs) != 1 || fs[0].Match || !strings.HasPrefix(fs[0].Observed.(string), "still resolves") {
		t.Fatalf("fs %+v err %v", fs, err)
	}
}

func TestMergeWithoutExpectedHeadSHA(t *testing.T) {
	c, inst, srv := setup(t, "get_repo", nil)
	_, err := run(t, c, inst, "merge_pr", `{"repo":"arjun/deedglass-sandbox","number":7}`)
	if ce := mustErr(t, err); ce.Code != connector.CodeValidationFailed || ce.AgentMessage != "Send expected_head_sha so the merge can't include commits pushed after approval." {
		t.Fatalf("%+v", ce)
	}
	if len(srv.Requests()) != 0 {
		t.Fatal("a request was sent")
	}
	_, _ = run(t, c, inst, "get_repo", `{"repo":"arjun/deedglass-sandbox"}`)
}

func TestRateLimited403(t *testing.T) {
	c, inst, srv := setup(t, "ratelimit_403", nil)
	_, err := run(t, c, inst, "create_issue", `{"repo":"arjun/deedglass-sandbox","title":"t"}`)
	ce := mustErr(t, err)
	if ce.Code != connector.CodeUpstreamRateLimited || ce.Kind != connector.KindRateLimited || ce.RetryAfter != 120*time.Second {
		t.Fatalf("%+v", ce)
	}
	if srv.Count("POST issues") != 1 {
		t.Fatal("a write was retried")
	}
	if !strings.Contains(ce.AgentMessage, "Wait 2 min") {
		t.Fatalf("message %q", ce.AgentMessage)
	}
}

func TestSecondaryRateLimit(t *testing.T) {
	c, inst, _ := setup(t, "secondary_limit", nil)
	_, err := run(t, c, inst, "create_issue", `{"repo":"arjun/deedglass-sandbox","title":"t"}`)
	if ce := mustErr(t, err); ce.Code != connector.CodeUpstreamRateLimited || ce.RetryAfter != 60*time.Second {
		t.Fatalf("%+v", ce)
	}
}

func TestRead429RetriesOnce(t *testing.T) {
	c, inst, srv := setup(t, "retry_429", nil)
	var waited []time.Duration
	c.sleep = func(_ context.Context, d time.Duration) bool { waited = append(waited, d); return true }
	res, err := run(t, c, inst, "get_repo", `{"repo":"arjun/deedglass-sandbox"}`)
	if err != nil {
		t.Fatal(err)
	}
	if len(waited) != 1 || waited[0] != 3*time.Second || len(srv.Requests()) != 2 {
		t.Fatalf("waited %v, requests %d", waited, len(srv.Requests()))
	}
	if output(t, res)["default_branch"] != "main" {
		t.Fatalf("output %s", res.Output)
	}
}

func TestPermission403(t *testing.T) {
	c, inst, _ := setup(t, "perm_403", nil)
	_, err := run(t, c, inst, "create_issue", `{"repo":"arjun/deedglass-sandbox","title":"t"}`)
	ce := mustErr(t, err)
	want := "GitHub refused this token for POST /repos/arjun/deedglass-sandbox/issues. It needs Issues: write. Update the fine-grained token, then run deedglass connector test github."
	if ce.Code != connector.CodeUpstreamAuthFailed || ce.AdminDetail != want {
		t.Fatalf("%+v", ce)
	}
}

func TestRepo404AndUnauthorized(t *testing.T) {
	c, inst, _ := setup(t, "repo_404", nil)
	_, err := run(t, c, inst, "get_repo", `{"repo":"arjun/deedglass-sandbox"}`)
	if ce := mustErr(t, err); ce.Code != connector.CodeUpstreamNotFound || !strings.Contains(ce.AdminDetail, "GitHub answers 404 when a token can't see a private repository") {
		t.Fatalf("%+v", ce)
	}
	c, inst, _ = setup(t, "unauthorized", nil)
	_, err = run(t, c, inst, "get_repo", `{"repo":"arjun/deedglass-sandbox"}`)
	if ce := mustErr(t, err); ce.Code != connector.CodeUpstreamAuthFailed || !strings.Contains(ce.AdminDetail, "401") {
		t.Fatalf("%+v", ce)
	}
}

// ---- every tool's fixture ----

func TestReadTools(t *testing.T) {
	cases := []struct {
		fixture, tool, args string
		check               func(t *testing.T, out map[string]any)
	}{
		{"get_repo", "get_repo", `{"repo":"arjun/deedglass-sandbox"}`, func(t *testing.T, o map[string]any) {
			if o["full_name"] != testRepo || o["can_push"] != true || o["private"] != true {
				t.Fatal(o)
			}
		}},
		{"list_issues", "list_issues", `{"repo":"arjun/deedglass-sandbox","labels":"bug","per_page":10,"page":2}`, func(t *testing.T, o map[string]any) {
			is := o["issues"].([]any)
			if len(is) != 2 || is[1].(map[string]any)["is_pull_request"] != true || o["page"] != float64(2) {
				t.Fatal(o)
			}
		}},
		{"get_issue", "get_issue", `{"repo":"arjun/deedglass-sandbox","number":42}`, func(t *testing.T, o map[string]any) {
			if o["number"] != float64(42) || o["user"] != testLogin || o["labels"].([]any)[0] != "bug" {
				t.Fatal(o)
			}
		}},
		{"get_file", "get_file", `{"repo":"arjun/deedglass-sandbox","path":"README.md","ref":"main"}`, func(t *testing.T, o map[string]any) {
			if o["content"] != "# Deedglass Sandbox\n" || o["encoding"] != "text" {
				t.Fatal(o)
			}
		}},
		{"get_file_dir", "get_file", `{"repo":"arjun/deedglass-sandbox","path":"docs"}`, func(t *testing.T, o map[string]any) {
			if o["type"] != "dir" || len(o["entries"].([]any)) != 2 {
				t.Fatal(o)
			}
		}},
		{"list_prs", "list_prs", `{"repo":"arjun/deedglass-sandbox","state":"all","base":"main"}`, func(t *testing.T, o map[string]any) {
			if ps := o["pulls"].([]any); len(ps) != 1 || ps[0].(map[string]any)["head"] != "agent/fix-typo" {
				t.Fatal(o)
			}
		}},
		{"get_pr", "get_pr", `{"repo":"arjun/deedglass-sandbox","number":7}`, func(t *testing.T, o map[string]any) {
			if o["head_sha"] != "9f2c4e6a8b0d1f3a5c7e9b1d3f5a7c9e1b3d5f7a" || o["mergeable"] != "yes" {
				t.Fatal(o)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.fixture, func(t *testing.T) {
			c, inst, _ := setup(t, tc.fixture, nil)
			res, err := run(t, c, inst, tc.tool, tc.args)
			if err != nil {
				t.Fatal(err)
			}
			checkProjection(t, tc.tool, res)
			tc.check(t, output(t, res))
		})
	}
}

// checkProjection: the output passes its OutputSchema and echoes no email, node id or API URL.
func checkProjection(t *testing.T, tool string, res connector.Result) {
	t.Helper()
	spec, _ := specFor(tool)
	if err := connector.ValidateArgs(spec.OutputSchema, res.Output); err != nil {
		t.Fatalf("%s output %s fails its schema: %v", tool, res.Output, err)
	}
	for _, bad := range []string{"@example.com", "node_id", "api.github.com", "R_kgDO", "U_bot"} {
		if strings.Contains(string(res.Output), bad) {
			t.Fatalf("%s output echoes %q: %s", tool, bad, res.Output)
		}
	}
}

func TestWriteTools(t *testing.T) {
	cases := []struct {
		fixture, tool, args, target string
	}{
		{"create_issue", "create_issue", `{"repo":"arjun/deedglass-sandbox","title":"Flaky checkout test","body":"Fails 1 in 20 runs on CI."}`, "issue #42 on GitHub"},
		{"comment", "comment", `{"repo":"arjun/deedglass-sandbox","number":42,"body":"Looks good."}`, "a comment on issue #42 on GitHub"},
		{"create_branch", "create_branch", `{"repo":"arjun/deedglass-sandbox","branch":"agent/fix-typo"}`, "branch agent/fix-typo on GitHub"},
		{"create_branch_exists", "create_branch", `{"repo":"arjun/deedglass-sandbox","branch":"agent/fix-typo","from":"5b1f3a9c2e7d4b6a8f0c1e3d5a7b9c2e4f6a8b0c"}`, ""},
		{"file", "create_or_update_file", `{"repo":"arjun/deedglass-sandbox","path":"docs/x.md","branch":"agent/fix-typo","message":"Add x","content":"hello\n"}`, "docs/x.md on agent/fix-typo on GitHub"},
		{"create_pr", "create_pr", `{"repo":"arjun/deedglass-sandbox","head":"agent/fix-typo","base":"main","title":"Fix typo in README","body":"Fixes the typo."}`, "pull request #7 on GitHub"},
		{"merge_pr", "merge_pr", `{"repo":"arjun/deedglass-sandbox","number":7,"expected_head_sha":"9f2c4e6a8b0d1f3a5c7e9b1d3f5a7c9e1b3d5f7a","merge_method":"squash"}`, "pull request #7 on GitHub"},
		{"delete_branch", "delete_branch", `{"repo":"arjun/deedglass-sandbox","branch":"agent/fix-typo"}`, "branch agent/fix-typo on GitHub"},
		{"close_issue", "close_issue", `{"repo":"arjun/deedglass-sandbox","number":42,"state_reason":"not_planned"}`, "issue #42 on GitHub"},
	}
	for _, tc := range cases {
		t.Run(tc.fixture, func(t *testing.T) {
			c, inst, _ := setup(t, tc.fixture, nil)
			res, err := run(t, c, inst, tc.tool, tc.args)
			if err != nil {
				t.Fatal(err)
			}
			checkProjection(t, tc.tool, res)
			if tc.target == "" {
				if output(t, res)["existed"] != true {
					t.Fatalf("output %s", res.Output)
				}
				return
			}
			obs, fs, err := readBack(t, c, inst, tc.tool, tc.args, res)
			if err != nil {
				t.Fatal(err)
			}
			if ok, bad := allMatch(fs); !ok {
				t.Fatalf("%s mismatched: %+v", bad, fs)
			}
			if obs.Target != tc.target {
				t.Fatalf("target %q", obs.Target)
			}
		})
	}
}

func TestCreateBranchConflict(t *testing.T) {
	c, inst, _ := setup(t, "create_branch_conflict", nil)
	_, err := run(t, c, inst, "create_branch", `{"repo":"arjun/deedglass-sandbox","branch":"agent/fix-typo","from":"5b1f3a9c2e7d4b6a8f0c1e3d5a7b9c2e4f6a8b0c"}`)
	if ce := mustErr(t, err); ce.Code != "conflict" || !strings.Contains(ce.AgentMessage, "already exists") {
		t.Fatalf("%+v", ce)
	}
}

func TestMergeRefusals(t *testing.T) {
	args := `{"repo":"arjun/deedglass-sandbox","number":7,"expected_head_sha":"9f2c4e6a8b0d1f3a5c7e9b1d3f5a7c9e1b3d5f7a"}`
	for _, f := range []string{"merge_405", "merge_409"} {
		c, inst, _ := setup(t, f, nil)
		_, err := run(t, c, inst, "merge_pr", args)
		ce := mustErr(t, err)
		if ce.Code != connector.CodeUpstreamError || !strings.Contains(ce.AgentMessage, "GitHub refused the request") || !strings.Contains(ce.AdminDetail, "GitHub said") {
			t.Fatalf("%s: %+v", f, ce)
		}
	}
}

func TestMergeReadBackDiverged(t *testing.T) {
	c, inst, _ := setup(t, "merge_pr_diverged", nil)
	res := connector.Result{Evidence: map[string]string{"number": "7"}}
	_, fs, err := readBack(t, c, inst, "merge_pr", `{"repo":"arjun/deedglass-sandbox","number":7,"expected_head_sha":"9f2c4e6a8b0d1f3a5c7e9b1d3f5a7c9e1b3d5f7a"}`, res)
	if err != nil {
		t.Fatal(err)
	}
	last := fs[len(fs)-1]
	if last.Field != "merge_commit_in_base" || last.Match || last.Observed != "diverged" {
		t.Fatalf("fs %+v", fs)
	}
}

func TestToolsCatalog(t *testing.T) {
	c := newConn()
	ts, err := c.Tools(context.Background(), nil)
	if err != nil || len(ts) != 14 {
		t.Fatalf("%d tools, %v", len(ts), err)
	}
	want := []string{"get_repo", "list_issues", "get_issue", "get_file", "list_prs", "get_pr", "create_issue", "comment", "create_branch",
		"create_or_update_file", "create_pr", "merge_pr", "delete_branch", "close_issue"}
	for i, ts := range ts {
		if ts.Name != want[i] || !connector.ReNativeTool.MatchString(ts.Name) {
			t.Fatalf("tool %d = %s", i, ts.Name)
		}
		if (ts.SideEffect == connector.SideEffectRead) != (ts.L1 == "") {
			t.Fatalf("%s: side effect %s with L1 %q", ts.Name, ts.SideEffect, ts.L1)
		}
		if len(ts.Description) > 1024 || len(ts.OutputSchema) == 0 {
			t.Fatalf("%s: description or output schema", ts.Name)
		}
		if err := connector.ValidateArgs(ts.InputSchema, json.RawMessage(`{"repo":"arjun/deedglass-sandbox","zzz":1}`)); err == nil {
			t.Fatalf("%s: the input schema isn't closed", ts.Name)
		}
		var m map[string]any
		if json.Unmarshal(ts.OutputSchema, &m) != nil || m["additionalProperties"] != false {
			t.Fatalf("%s: the output schema isn't closed", ts.Name)
		}
	}
	sides := map[string]connector.SideEffect{"merge_pr": connector.SideEffectDestructive, "delete_branch": connector.SideEffectDestructive,
		"close_issue": connector.SideEffectDestructive, "create_pr": connector.SideEffectWrite, "get_file": connector.SideEffectRead}
	for n, s := range sides {
		if sp, _ := specFor(n); sp.SideEffect != s {
			t.Fatalf("%s = %s", n, sp.SideEffect)
		}
	}
	info := c.Info()
	if info.Type != "github" || info.Title != "GitHub" || len(info.Levels) != 2 || len(info.CredentialKinds) != 2 || info.DocsURL != "https://docs.deedglass.com/connectors/github" || !json.Valid(info.ConfigSchema) {
		t.Fatalf("info %+v", info)
	}
	if c.Type() != "github" {
		t.Fatal(c.Type())
	}
}

func TestArgumentRefusalsSendNothing(t *testing.T) {
	c, inst, srv := setup(t, "get_repo", nil)
	big := strings.Repeat("x", 1<<20)
	cases := map[string]string{
		"create_branch":         `{"repo":"arjun/deedglass-sandbox","branch":"a..b"}`,
		"list_prs":              `{"repo":"arjun/deedglass-sandbox","base":"a//b"}`,
		"get_file":              `{"repo":"arjun/deedglass-sandbox","path":"a.md","ref":"a b"}`,
		"create_pr":             `{"repo":"arjun/deedglass-sandbox","head":".x","base":"main","title":"t"}`,
		"merge_pr":              `{"repo":"arjun/deedglass-sandbox","number":7,"expected_head_sha":"abc"}`,
		"delete_branch":         `{"repo":"arjun/deedglass-sandbox","branch":"x.lock"}`,
		"create_issue":          `{"repo":"arjun/deedglass-sandbox","title":"t","labels":["a","b","c","d","e","f","g","h","i","j","k"]}`,
		"create_or_update_file": `{"repo":"arjun/deedglass-sandbox","path":"a.md","message":" ","content":"x"}`,
	}
	for tool, args := range cases {
		if _, err := run(t, c, inst, tool, args); mustErr(t, err).Kind != connector.KindInvalidRequest {
			t.Fatalf("%s: %v", tool, err)
		}
	}
	// Over max_file_bytes (a 1 KiB cap here).
	c2, inst2, _ := setup(t, "get_issue", map[string]any{"max_file_bytes": 1024})
	args, _ := json.Marshal(map[string]string{"repo": testRepo, "path": "a.md", "message": "m", "content": big[:2048]})
	if _, err := run(t, c2, inst2, "create_or_update_file", string(args)); !strings.Contains(mustErr(t, err).AgentMessage, "1 KiB") {
		t.Fatal(err)
	}
	if _, err := run(t, c2, inst2, "get_issue", `{"repo":"arjun/deedglass-sandbox","number":42}`); err != nil {
		t.Fatal(err)
	}
	if len(srv.Requests()) != 0 {
		t.Fatal("a request was sent")
	}
	if _, err := run(t, c, inst, "get_repo", `{"repo":"arjun/deedglass-sandbox"}`); err != nil {
		t.Fatal(err)
	}
}

func TestProjectFileDigest(t *testing.T) {
	bin := projectFile(ghContent{Path: "a.bin", SHA: "x", Encoding: "base64", Content: "AAEC"})
	if bin.Encoding != "digest" || !strings.HasPrefix(bin.Digest, "sha256:") || bin.Content != nil {
		t.Fatalf("%+v", bin)
	}
	if none := projectFile(ghContent{Path: "big", Encoding: "none"}); none.Encoding != "digest" || none.Digest != "" {
		t.Fatalf("%+v", none)
	}
}
