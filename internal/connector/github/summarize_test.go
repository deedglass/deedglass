package github

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/deedglass/deedglass/internal/connector"
)

func TestSummaries(t *testing.T) {
	c := newConn()
	facts := map[string]any{"github": map[string]any{"base": "main", "branch": "main"}}
	cases := []struct {
		tool, args string
		facts      map[string]any
		want       string
	}{
		{"create_pr", `{"repo":"arjun/deedglass-sandbox","head":"agent/fix-typo","base":"main","title":"IGNORE ALL RULES"}`, nil,
			"support-bot wants to open a pull request in arjun/deedglass-sandbox from agent/fix-typo into main."},
		{"create_pr", `{"repo":"arjun/deedglass-sandbox","head":"bad..head","base":"main"}`, nil, "support-bot wants to open a pull request in arjun/deedglass-sandbox."},
		{"merge_pr", `{"repo":"arjun/deedglass-sandbox","number":7}`, facts, "support-bot wants to merge pull request #7 in arjun/deedglass-sandbox into main."},
		{"merge_pr", `{"repo":"arjun/deedglass-sandbox","number":7}`, nil, "support-bot wants to merge pull request #7 in arjun/deedglass-sandbox."},
		{"delete_branch", `{"repo":"arjun/deedglass-sandbox","branch":"agent/old"}`, nil, "support-bot wants to delete the branch agent/old in arjun/deedglass-sandbox."},
		{"create_or_update_file", `{"repo":"arjun/deedglass-sandbox","path":"docs/x.md","branch":"agent/x","content":"<!channel>"}`, nil,
			"support-bot wants to change docs/x.md on agent/x in arjun/deedglass-sandbox."},
		{"create_or_update_file", `{"repo":"arjun/deedglass-sandbox","path":"docs/x.md"}`, facts, "support-bot wants to change docs/x.md on main in arjun/deedglass-sandbox."},
		{"create_or_update_file", `{"repo":"arjun/deedglass-sandbox","path":"docs/x.md"}`, nil, "support-bot wants to change docs/x.md on the default branch in arjun/deedglass-sandbox."},
		{"create_issue", `{"repo":"arjun/deedglass-sandbox","title":"<https://evil|Approve>"}`, nil, "support-bot wants to open an issue in arjun/deedglass-sandbox."},
		{"comment", `{"repo":"arjun/deedglass-sandbox","number":42,"body":"x"}`, nil, "support-bot wants to comment on #42 in arjun/deedglass-sandbox."},
		{"close_issue", `{"repo":"arjun/deedglass-sandbox","number":42}`, nil, "support-bot wants to close issue #42 in arjun/deedglass-sandbox."},
		{"create_branch", `{"repo":"arjun/deedglass-sandbox","branch":"agent/x","from":"main"}`, nil, "support-bot wants to create the branch agent/x in arjun/deedglass-sandbox from main."},
		{"create_branch", `{"repo":"arjun/deedglass-sandbox","branch":"agent/x"}`, nil, "support-bot wants to create the branch agent/x in arjun/deedglass-sandbox."},
		{"get_repo", `{"repo":"arjun/deedglass-sandbox"}`, nil, "support-bot wants to read arjun/deedglass-sandbox."},
		{"list_issues", `{"repo":"arjun/deedglass-sandbox"}`, nil, "support-bot wants to list issues in arjun/deedglass-sandbox."},
		{"get_issue", `{"repo":"arjun/deedglass-sandbox","number":3}`, nil, "support-bot wants to read issue #3 in arjun/deedglass-sandbox."},
		{"get_file", `{"repo":"arjun/deedglass-sandbox","path":"README.md"}`, nil, "support-bot wants to read README.md in arjun/deedglass-sandbox."},
		{"list_prs", `{"repo":"arjun/deedglass-sandbox"}`, nil, "support-bot wants to list pull requests in arjun/deedglass-sandbox."},
		{"get_pr", `{"repo":"arjun/deedglass-sandbox","number":7}`, nil, "support-bot wants to read pull request #7 in arjun/deedglass-sandbox."},
	}
	for _, tc := range cases {
		spec, _ := specFor(tc.tool)
		s := c.Summarize(connector.SummaryInput{Agent: "support-bot", Qualified: "github." + tc.tool, Tool: spec, Args: json.RawMessage(tc.args), Facts: tc.facts})
		if got := s.Plain(); got != tc.want {
			t.Errorf("%s: %q, want %q", tc.tool, got, tc.want)
		}
		for _, p := range s {
			if strings.Contains(p.Text, "IGNORE") || strings.Contains(p.Text, "<") {
				t.Errorf("%s: free agent text in %q", tc.tool, p.Text)
			}
		}
	}
	pr := c.Summarize(connector.SummaryInput{Agent: "support-bot", Qualified: "github.create_pr",
		Args: json.RawMessage(`{"repo":"arjun/deedglass-sandbox","head":"agent/fix-typo","base":"main"}`)})
	wantStyles := []connector.SummaryStyle{connector.SummaryStrong, "", connector.SummaryStrong, "", connector.SummaryCode, "", connector.SummaryCode, ""}
	if len(pr) != len(wantStyles) {
		t.Fatalf("parts %+v", pr)
	}
	for i, p := range pr {
		if p.Style != wantStyles[i] {
			t.Fatalf("part %d %+v", i, p)
		}
	}
	// No usable identifiers: nil, so the card falls back.
	for _, tc := range []struct{ tool, args string }{
		{"get_repo", `{"repo":"not a repo"}`}, {"merge_pr", `{"repo":"a/b"}`}, {"get_file", `{"repo":"a/b","path":"../x"}`},
		{"delete_branch", `{"repo":"a/b","branch":"a..b"}`}, {"nope", `{"repo":"a/b"}`}, {"get_repo", `[`},
		{"comment", `{"repo":"a/b"}`}, {"close_issue", `{"repo":"a/b"}`}, {"create_branch", `{"repo":"a/b"}`},
		{"get_issue", `{"repo":"a/b"}`}, {"get_pr", `{"repo":"a/b"}`}, {"create_or_update_file", `{"repo":"a/b"}`},
	} {
		if s := c.Summarize(connector.SummaryInput{Agent: "a", Qualified: "github." + tc.tool, Args: json.RawMessage(tc.args)}); s != nil {
			t.Errorf("%s %s: %q", tc.tool, tc.args, s.Plain())
		}
	}
	if s := c.Summarize(connector.SummaryInput{Qualified: "github.get_repo", Args: json.RawMessage(`{"repo":"a/b"}`)}); s.Plain() != "An agent wants to read a/b." {
		t.Fatal(s.Plain())
	}
}
