package notion

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/deedglass/deedglass/internal/connector"
	"github.com/deedglass/deedglass/internal/connector/egress"
	"github.com/deedglass/deedglass/internal/connector/saas/saastest"
	"github.com/deedglass/deedglass/internal/ids"
	"github.com/deedglass/deedglass/internal/vault"
)

const (
	parentID  = "11111111-2222-4333-8444-555555555555"
	parentHex = "11111111222243338444555555555555"
	pageID    = "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"
	childID   = "99999999-8888-4777-8666-555555555555"
	otherID   = "12345678-1234-4123-8123-123456789abc"
	block1    = "b1b1b1b1-0000-4000-8000-000000000001"
	block2    = "b2b2b2b2-0000-4000-8000-000000000002"
	testAct   = "act_01j9zq3m8x2k7w4c5v6b7n8p9r"
)

var (
	testOrg  = ids.MustParse[ids.OrgKind]("org_01k6c9kb2zcjsyn7b8g2k1vqvp")
	testConn = ids.MustParse[ids.ConnectorKind]("con_01j9zq7c4m8x2v6t0r3n5k1h7d")
	testActs = ids.MustParse[ids.ActionKind](testAct)
)

// sentinelToken is an integration-token shape built at run time; it is no credential.
func sentinelToken() string { return "ntn_" + strings.Repeat("dgSENTINEL7Q2v", 4) }

type opener struct{ fields map[string]string }

func (o *opener) Open(context.Context, vault.Ref) (*vault.Secret, error) {
	if o.fields == nil {
		return nil, &connector.Error{Kind: connector.KindCredentialsMissing, Code: connector.CodeCredentialsMissing}
	}
	return vault.NewSecretForTest(vault.KindBearer, o.fields), nil
}

func devEnv() connector.Env {
	return connector.Env{Name: "dev", Egress: &egress.Policy{Env: egress.EnvDev, AllowPrivate: true}}
}

func setup(t *testing.T, xs ...saastest.Exchange) (*Connector, *connector.Instance, *saastest.Server) {
	t.Helper()
	srv := saastest.New(t, xs...)
	c := New()
	c.sleep = func(context.Context, time.Duration) bool { return true }
	cfg, _ := json.Marshal(map[string]any{"parents": []string{parentHex}, "api_base_url": srv.URL, "max_blocks": 3})
	norm, errs := c.Validate(context.Background(), devEnv(), cfg)
	if len(errs) > 0 {
		t.Fatalf("Validate: %v", errs)
	}
	inst := connector.NewInstance(testConn, testOrg, "notion", Type, norm, "sha256:test", devEnv(), &opener{fields: map[string]string{FieldToken: sentinelToken()}})
	return c, inst, srv
}

func call(t *testing.T, tool, args string) connector.Call {
	t.Helper()
	spec, ok := specFor(tool)
	if !ok {
		t.Fatalf("no tool %s", tool)
	}
	if err := connector.ValidateArgs(spec.InputSchema, json.RawMessage(args)); err != nil {
		t.Fatalf("schema refused %s: %v", args, err)
	}
	return connector.Call{ActionID: testActs, OrgID: testOrg, Tool: spec, Qualified: "notion." + tool, Args: json.RawMessage(args), IdempotencyKey: testAct}
}

func ctx() context.Context { return vault.WithRedactor(context.Background(), vault.NewRedactor()) }

func jb(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

func pageBody(id, parent, title string, trash bool) json.RawMessage {
	return jb(map[string]any{"object": "page", "id": id, "url": "https://www.notion.so/" + strings.ReplaceAll(id, "-", ""), "in_trash": trash,
		"parent":     map[string]any{"type": "page_id", "page_id": parent},
		"properties": map[string]any{"title": map[string]any{"type": "title", "title": []any{map[string]any{"plain_text": title}}}}})
}

func para(id, text string) map[string]any {
	return map[string]any{"object": "block", "id": id, "type": "paragraph", "in_trash": false,
		"paragraph": map[string]any{"rich_text": []any{map[string]any{"plain_text": text}}}}
}

var hdrs = map[string]string{"Notion-Version": APIVersion}

func allMatch(fs []connector.L1Finding) (bool, string) {
	for _, f := range fs {
		if !f.Match {
			return false, f.Field
		}
	}
	return len(fs) > 0, ""
}

func mustErr(t *testing.T, err error) *connector.Error {
	t.Helper()
	ce := connector.AsError(err)
	if ce == nil {
		t.Fatalf("err = %v, want a *connector.Error", err)
	}
	return ce
}

func TestValidate(t *testing.T) {
	c := New()
	for name, tc := range map[string]struct {
		cfg   string
		param string
		prod  bool
	}{
		"ok":            {cfg: `{"parents":["` + parentHex + `"]}`},
		"no parents":    {cfg: `{}`, param: "config.parents"},
		"bad parent":    {cfg: `{"parents":["not-a-page"]}`, param: "config.parents[0]"},
		"unknown key":   {cfg: `{"parents":["` + parentHex + `"],"delete_pages":true}`, param: "config.delete_pages"},
		"max_blocks":    {cfg: `{"parents":["` + parentHex + `"],"max_blocks":101}`, param: "config.max_blocks"},
		"version":       {cfg: `{"parents":["` + parentHex + `"],"api_version":"latest"}`, param: "config.api_version"},
		"prod base url": {cfg: `{"parents":["` + parentHex + `"],"api_base_url":"http://127.0.0.1:9"}`, param: "config.api_base_url", prod: true},
		"not an object": {cfg: `[1]`, param: "config"},
	} {
		t.Run(name, func(t *testing.T) {
			env := devEnv()
			if tc.prod {
				env = connector.Env{Name: "prod"}
			}
			out, errs := c.Validate(context.Background(), env, json.RawMessage(tc.cfg))
			if tc.param == "" {
				if len(errs) > 0 {
					t.Fatal(errs)
				}
				var cfg Config
				_ = json.Unmarshal(out, &cfg)
				if cfg.Parents[0] != parentID || cfg.MaxBlocks != DefaultMaxBlocks || cfg.APIVersion != APIVersion || cfg.APIBaseURL != DefaultAPIBaseURL {
					t.Fatalf("normalized %s", out)
				}
				return
			}
			if len(errs) == 0 || errs[0].Param != tc.param {
				t.Fatalf("errs %v, want %s", errs, tc.param)
			}
		})
	}
	if NormalizeID("AAAAAAAA-BBBB-4CCC-8DDD-EEEEEEEEEEEE") != pageID || NormalizeID("aaaa-bbbbbbbb") != "" || NormalizeID(strings.Repeat("a", 31)) != "" {
		t.Fatal("NormalizeID")
	}
}

func TestCreatePageVerified(t *testing.T) {
	c, inst, srv := setup(t,
		saastest.Exchange{Name: "create", Method: "POST", Path: "/v1/pages", RequireHeaders: hdrs,
			BodySubset: map[string]any{"parent": map[string]any{"type": "page_id", "page_id": parentID}},
			Status:     200, Headers: map[string]string{"X-Request-Id": "req-notion-1"}, Body: pageBody(pageID, parentID, "Weekly notes", false)},
		saastest.Exchange{Name: "page", Method: "GET", Path: "/v1/pages/" + pageID, Status: 200, Body: pageBody(pageID, parentID, "Weekly notes", false)},
		saastest.Exchange{Name: "children", Method: "GET", Path: "/v1/blocks/" + pageID + "/children", Status: 200,
			Body: jb(map[string]any{"results": []any{para(block1, "First."), para(block2, "Second.  ")}, "has_more": false})},
	)
	args := `{"parent_page_id":"` + parentHex + `","title":"Weekly notes","paragraphs":["First.","Second."]}`
	cl := call(t, ToolCreatePage, args)
	res, err := c.Execute(ctx(), inst, cl)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	_ = json.Unmarshal(res.Output, &out)
	if out["id"] != pageID || out["title"] != "Weekly notes" || res.Evidence["page"] != pageID || res.Meta["notion_request_id"] != "req-notion-1" {
		t.Fatalf("output %s meta %v evidence %v", res.Output, res.Meta, res.Evidence)
	}
	obs, fs, err := c.NativeReadBack(ctx(), inst, cl, res)
	if err != nil {
		t.Fatal(err)
	}
	if ok, f := allMatch(fs); !ok || len(fs) != 4 {
		t.Fatalf("findings %+v (first mismatch %s)", fs, f)
	}
	if obs.Target != "page "+pageID+" in Notion" {
		t.Fatalf("target %q", obs.Target)
	}
	r := srv.Requests()[0]
	if r.Header.Get("Authorization") != "Bearer "+sentinelToken() || r.Header.Get("Idempotency-Key") != "" {
		t.Fatalf("headers %v", r.Header)
	}
}

func TestCreatePageParentRefused(t *testing.T) {
	c, inst, srv := setup(t)
	_, err := c.Execute(ctx(), inst, call(t, ToolCreatePage, `{"parent_page_id":"`+otherID+`","title":"x"}`))
	ce := mustErr(t, err)
	if ce.Code != connector.CodeValidationFailed || !strings.Contains(ce.AgentMessage, "isn't one of the connector's parent pages") {
		t.Fatalf("%+v", ce)
	}
	if n := len(srv.Requests()); n != 0 {
		t.Fatalf("%d requests; want 0", n)
	}
}

func TestCreatePageInTrashIsMismatch(t *testing.T) {
	c, inst, _ := setup(t,
		saastest.Exchange{Name: "page", Method: "GET", Path: "/v1/pages/" + pageID, Status: 200, Body: pageBody(pageID, parentID, "Weekly notes", true)},
		saastest.Exchange{Name: "children", Method: "GET", Path: "/v1/blocks/" + pageID + "/children", Status: 200, Body: jb(map[string]any{"results": []any{}})},
	)
	cl := call(t, ToolCreatePage, `{"parent_page_id":"`+parentID+`","title":"Weekly notes"}`)
	_, fs, err := c.NativeReadBack(ctx(), inst, cl, connector.Result{Evidence: map[string]string{"page": pageID}})
	if err != nil {
		t.Fatal(err)
	}
	if ok, f := allMatch(fs); ok || f != "in_trash" {
		t.Fatalf("findings %+v", fs)
	}
}

func TestCreatePageReadBackDifferentTitle(t *testing.T) {
	c, inst, _ := setup(t,
		saastest.Exchange{Name: "page", Method: "GET", Path: "/v1/pages/" + pageID, Status: 200, Body: pageBody(pageID, parentID, "Edited", false)},
		saastest.Exchange{Name: "children", Method: "GET", Path: "/v1/blocks/" + pageID + "/children", Status: 200, Body: jb(map[string]any{"results": []any{para(block1, "Other")}})},
	)
	cl := call(t, ToolCreatePage, `{"parent_page_id":"`+parentID+`","title":"Weekly notes","paragraphs":["First."]}`)
	_, fs, err := c.NativeReadBack(ctx(), inst, cl, connector.Result{Evidence: map[string]string{"page": pageID}})
	if err != nil {
		t.Fatal(err)
	}
	bad := 0
	for _, f := range fs {
		if !f.Match {
			bad++
		}
	}
	if bad != 2 {
		t.Fatalf("findings %+v; want title and paragraphs to differ", fs)
	}
}

func TestAppendBlockVerified(t *testing.T) {
	c, inst, srv := setup(t,
		saastest.Exchange{Name: "page", Method: "GET", Path: "/v1/pages/" + childID, Status: 200, Body: pageBody(childID, parentID, "Child", false)},
		saastest.Exchange{Name: "append", Method: "PATCH", Path: "/v1/blocks/" + childID + "/children",
			BodySubset: map[string]any{"position": map[string]any{"type": "end"}}, Status: 200,
			Body: jb(map[string]any{"object": "list", "results": []any{para(block1, "One."), para(block2, "Two.")}})},
		saastest.Exchange{Name: "b1", Method: "GET", Path: "/v1/blocks/" + block1, Status: 200, Body: jb(para(block1, "One."))},
		saastest.Exchange{Name: "b2", Method: "GET", Path: "/v1/blocks/" + block2, Status: 200, Body: jb(para(block2, "Two.\r\n"))},
	)
	cl := call(t, ToolAppendBlock, `{"page_id":"`+childID+`","paragraphs":["One.","Two.\n"]}`)
	res, err := c.Execute(ctx(), inst, cl)
	if err != nil {
		t.Fatal(err)
	}
	if res.Evidence["blocks"] != block1+","+block2 {
		t.Fatalf("evidence %v", res.Evidence)
	}
	_, fs, err := c.NativeReadBack(ctx(), inst, cl, res)
	if err != nil {
		t.Fatal(err)
	}
	if ok, f := allMatch(fs); !ok || len(fs) != 5 {
		t.Fatalf("findings %+v (first mismatch %s)", fs, f)
	}
	if srv.Count("append") != 1 {
		t.Fatal("append not sent once")
	}
}

func TestAppendBlockParentRefused(t *testing.T) {
	c, inst, srv := setup(t,
		saastest.Exchange{Name: "page", Method: "GET", Path: "/v1/pages/" + childID, Status: 200, Body: pageBody(childID, otherID, "Elsewhere", false)},
	)
	_, err := c.Execute(ctx(), inst, call(t, ToolAppendBlock, `{"page_id":"`+childID+`","paragraphs":["x"]}`))
	ce := mustErr(t, err)
	if ce.Code != connector.CodeValidationFailed || ce.AgentMessage != MsgParentRefused {
		t.Fatalf("%+v", ce)
	}
	if n := len(srv.Requests()); n != 1 {
		t.Fatalf("%d requests; want only the GET", n)
	}
}

func TestAppendBlockLimits(t *testing.T) {
	c, inst, srv := setup(t)
	_, err := c.Execute(ctx(), inst, call(t, ToolAppendBlock, `{"page_id":"`+childID+`","paragraphs":["a","b","c","d"]}`))
	if ce := mustErr(t, err); ce.Code != connector.CodeValidationFailed || !strings.Contains(ce.AgentMessage, "at most 3 paragraphs") {
		t.Fatalf("%+v", ce)
	}
	_, err = c.Execute(ctx(), inst, call(t, ToolAppendBlock, `{"page_id":"`+childID+`","paragraphs":[]}`))
	if ce := mustErr(t, err); ce.Code != connector.CodeValidationFailed {
		t.Fatalf("%+v", ce)
	}
	if len(srv.Requests()) != 0 {
		t.Fatal("a refused call sent a request")
	}
}

func TestRateLimited(t *testing.T) {
	c, inst, _ := setup(t,
		saastest.Exchange{Name: "create", Method: "POST", Path: "/v1/pages", Status: 429, Headers: map[string]string{"Retry-After": "30"},
			Body: jb(map[string]any{"object": "error", "status": 429, "code": "rate_limited", "message": "Slow down."})},
	)
	_, err := c.Execute(ctx(), inst, call(t, ToolCreatePage, `{"parent_page_id":"`+parentID+`","title":"x"}`))
	ce := mustErr(t, err)
	if ce.Code != connector.CodeUpstreamRateLimited || ce.Kind != connector.KindRateLimited || ce.RetryAfter != 30*time.Second {
		t.Fatalf("%+v", ce)
	}
}

func TestErrorsClassified(t *testing.T) {
	for status, want := range map[int]connector.Kind{401: connector.KindAuth, 403: connector.KindAuth, 404: connector.KindNotFound,
		400: connector.KindToolError, 409: connector.KindToolError, 500: connector.KindUpstream} {
		c, inst, _ := setup(t, saastest.Exchange{Name: "create", Method: "POST", Path: "/v1/pages", Status: status,
			Body: jb(map[string]any{"object": "error", "code": "some_code", "message": "Token " + sentinelToken() + " refused."})})
		_, err := c.Execute(ctx(), inst, call(t, ToolCreatePage, `{"parent_page_id":"`+parentID+`","title":"x"}`))
		ce := mustErr(t, err)
		if ce.Kind != want {
			t.Errorf("%d: kind %s want %s", status, ce.Kind, want)
		}
		if strings.Contains(ce.AgentMessage, "ntn_") {
			t.Errorf("%d: the agent message carries the upstream text", status)
		}
	}
}

func TestSearch(t *testing.T) {
	c, inst, _ := setup(t,
		saastest.Exchange{Name: "search", Method: "POST", Path: "/v1/search", BodySubset: map[string]any{"query": "notes", "page_size": float64(2)}, Status: 200,
			Body: jb(map[string]any{"results": []any{json.RawMessage(pageBody(pageID, parentID, "Weekly notes", false)),
				map[string]any{"object": "data_source", "id": parentHex, "title": []any{map[string]any{"plain_text": "Tasks"}}, "parent": map[string]any{"type": "workspace"}}},
				"has_more": true})},
	)
	res, err := c.Execute(ctx(), inst, call(t, ToolSearch, `{"query":"notes","limit":2}`))
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		Results []found `json:"results"`
		HasMore bool    `json:"has_more"`
	}
	_ = json.Unmarshal(res.Output, &out)
	if len(out.Results) != 2 || out.Results[0].Title != "Weekly notes" || out.Results[0].Parent != parentID ||
		out.Results[1].Title != "Tasks" || out.Results[1].Parent != "workspace" || out.Results[1].ID != parentID || !out.HasMore {
		t.Fatalf("output %s", res.Output)
	}
}

func TestConnectionTest(t *testing.T) {
	c, inst, _ := setup(t,
		saastest.Exchange{Name: "me", Method: "GET", Path: "/v1/users/me", Status: 200, Body: jb(map[string]any{"type": "bot", "name": "Deedglass", "bot": map[string]any{"workspace_name": "Acme"}})},
		saastest.Exchange{Name: "parent", Method: "GET", Path: "/v1/pages/" + parentID, Status: 200, Body: pageBody(parentID, "", "Agent notes", false)},
	)
	rep, err := c.Test(ctx(), inst)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, ch := range rep.Checks {
		got = append(got, ch.ID+":"+string(ch.Status))
	}
	want := "config:pass credentials:pass destination:pass auth:pass parents:pass writes:skip"
	if strings.Join(got, " ") != want {
		t.Fatalf("checks %v", got)
	}
}

func TestConnectionTestParentNotShared(t *testing.T) {
	c, inst, _ := setup(t,
		saastest.Exchange{Name: "me", Method: "GET", Path: "/v1/users/me", Status: 200, Body: jb(map[string]any{"type": "bot"})},
		saastest.Exchange{Name: "parent", Method: "GET", Path: "/v1/pages/" + parentID, Status: 404, Body: jb(map[string]any{"code": "object_not_found"})},
	)
	rep, _ := c.Test(ctx(), inst)
	if ch := rep.Checks[4]; ch.ID != "parents" || ch.Status != connector.CheckFail || !strings.Contains(ch.Fix, "Connections") {
		t.Fatalf("%+v", ch)
	}
}

func TestConnectionTestNoCredentials(t *testing.T) {
	c := New()
	cfg, _ := c.Validate(context.Background(), devEnv(), json.RawMessage(`{"parents":["`+parentHex+`"]}`))
	inst := connector.NewInstance(testConn, testOrg, "notion", Type, cfg, "sha256:test", devEnv(), &opener{})
	rep, _ := c.Test(ctx(), inst)
	if rep.Checks[1].Status != connector.CheckFail || !strings.Contains(rep.Checks[1].Fix, "NOTION_TOKEN") {
		t.Fatalf("%+v", rep.Checks[1])
	}
}

func TestScrubNotionTokens(t *testing.T) {
	legacy := "secret_" + strings.Repeat("A1b2C3d4E5", 4) + "xyz"
	s := vault.Scrub("token " + sentinelToken() + " and " + legacy + " end")
	if strings.Contains(s, "ntn_dg") || strings.Contains(s, "secret_A1") {
		t.Fatalf("not scrubbed: %s", s)
	}
}

func TestInfoAndSummary(t *testing.T) {
	info := New().Info()
	if info.Type != Type || len(info.Tools) != 3 || info.Levels[1] != "L1" {
		t.Fatalf("%+v", info)
	}
	for _, tl := range info.Tools {
		if tl.SideEffect != connector.SideEffectRead && tl.ReadBack == "" {
			t.Errorf("%s has no read-back sentence", tl.Name)
		}
	}
	spec, _ := specFor(ToolCreatePage)
	s := New().Summarize(connector.SummaryInput{Agent: "notes-bot", Tool: spec, Args: json.RawMessage(`{"parent_page_id":"` + parentID + `","title":"Weekly notes","paragraphs":["a"]}`)})
	if got := s.Plain(); got != "notes-bot wants to create the Notion page Weekly notes under "+parentID+" with 1 paragraph." {
		t.Fatalf("%q", got)
	}
	spec, _ = specFor(ToolAppendBlock)
	s = New().Summarize(connector.SummaryInput{Tool: spec, Args: json.RawMessage(`{"page_id":"` + childID + `","paragraphs":["a","b"]}`)})
	if got := s.Plain(); got != "An agent wants to append 2 paragraphs to the Notion page "+childID+"." {
		t.Fatalf("%q", got)
	}
}

func TestMoreRefusalsAndEdges(t *testing.T) {
	c, inst, _ := setup(t,
		saastest.Exchange{Name: "trashed", Method: "GET", Path: "/v1/pages/" + parentID, Status: 200, Body: pageBody(parentID, otherID, "Old", true)},
		saastest.Exchange{Name: "me401", Method: "GET", Path: "/v1/users/me", Status: 401, Body: jb(map[string]any{"code": "unauthorized"})},
	)
	// Appending to a parent page itself is allowed, but not while it is in the trash.
	_, err := c.Execute(ctx(), inst, call(t, ToolAppendBlock, `{"page_id":"`+parentHex+`","paragraphs":["x"]}`))
	if ce := mustErr(t, err); !strings.Contains(ce.AgentMessage, "trash") {
		t.Fatalf("%+v", ce)
	}
	_, err = c.Execute(ctx(), inst, rawCall(ToolCreatePage, `{"parent_page_id":"`+parentID+`","title":"`+strings.Repeat("t", 201)+`"}`))
	if ce := mustErr(t, err); ce.Code != connector.CodeValidationFailed {
		t.Fatalf("%+v", ce)
	}
	_, err = c.Execute(ctx(), inst, rawCall(ToolCreatePage, `{"parent_page_id":"`+parentID+`","title":"x","paragraphs":["`+strings.Repeat("p", 2001)+`"]}`))
	if ce := mustErr(t, err); !strings.Contains(ce.AgentMessage, "2,000") {
		t.Fatalf("%+v", ce)
	}
	_, err = c.Execute(ctx(), inst, rawCall(ToolSearch, `{"query":"`+strings.Repeat("q", 201)+`"}`))
	if ce := mustErr(t, err); ce.Code != connector.CodeValidationFailed {
		t.Fatalf("%+v", ce)
	}
	_, _, err = c.NativeReadBack(ctx(), inst, call(t, ToolCreatePage, `{"parent_page_id":"`+parentID+`","title":"x"}`), connector.Result{})
	var skip *connector.ErrReadBackSkipped
	if !errorsAs(err, &skip) {
		t.Fatalf("err %v", err)
	}
	_, _, err = c.NativeReadBack(ctx(), inst, call(t, ToolAppendBlock, `{"page_id":"`+childID+`","paragraphs":["x"]}`), connector.Result{})
	if !errorsAs(err, &skip) {
		t.Fatalf("err %v", err)
	}
	_, _, err = c.NativeReadBack(ctx(), inst, call(t, ToolSearch, `{}`), connector.Result{})
	if !errorsAs(err, &skip) {
		t.Fatalf("err %v", err)
	}
	tools, err := c.Tools(context.Background(), inst)
	if err != nil || len(tools) != 3 {
		t.Fatal(tools, err)
	}
	rep, _ := c.Test(ctx(), inst)
	if ch := rep.Checks[3]; ch.ID != "auth" || ch.Status != connector.CheckFail || !strings.Contains(ch.Fix, "NOTION_TOKEN") {
		t.Fatalf("%+v", ch)
	}
}

func TestReadBackPaginates(t *testing.T) {
	c, inst, _ := setup(t,
		saastest.Exchange{Name: "page", Method: "GET", Path: "/v1/pages/" + pageID, Status: 200, Body: pageBody(pageID, parentID, "T", false)},
		saastest.Exchange{Name: "c1", Method: "GET", Path: "/v1/blocks/" + pageID + "/children", Status: 200,
			Body: jb(map[string]any{"results": []any{para(block1, "a")}, "has_more": true, "next_cursor": "cur2"})},
		saastest.Exchange{Name: "c2", Method: "GET", Path: "/v1/blocks/" + pageID + "/children", Query: map[string]string{"start_cursor": "cur2"}, Status: 200,
			Body: jb(map[string]any{"results": []any{para(block2, "b")}, "has_more": false})},
	)
	_, fs, err := c.NativeReadBack(ctx(), inst, call(t, ToolCreatePage, `{"parent_page_id":"`+parentID+`","title":"T","paragraphs":["a","b"]}`),
		connector.Result{Evidence: map[string]string{"page": pageID}})
	if err != nil {
		t.Fatal(err)
	}
	if ok, f := allMatch(fs); !ok {
		t.Fatalf("mismatch %s", f)
	}
}

func rawCall(tool, args string) connector.Call {
	spec, _ := specFor(tool)
	return connector.Call{ActionID: testActs, OrgID: testOrg, Tool: spec, Qualified: "notion." + tool, Args: json.RawMessage(args)}
}

func errorsAs(err error, target any) bool { return errors.As(err, target) }
