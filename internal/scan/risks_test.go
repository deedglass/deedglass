package scan

import (
	"encoding/json"
	"strings"
	"testing"
)

// fakeToken builds a vendor-shaped token at run time, so no credential-shaped string sits in the
// repository (secret scanners would flag it).
func fakeToken(prefix string, n int) string {
	const alphabet = "aB3dE5fG7hJ9kL1mN3pQ5rS7tU9vW1xY3zA5cD7eF9gH2iK4"
	var b strings.Builder
	b.WriteString(prefix)
	for i := 0; i < n; i++ {
		b.WriteByte(alphabet[(i*7+3)%len(alphabet)])
	}
	return b.String()
}

func mcpJSON(t *testing.T, servers map[string]any) string {
	t.Helper()
	b, err := json.Marshal(map[string]any{"mcpServers": servers})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func findingIDs(r *Report) map[string]int {
	out := map[string]int{}
	for _, f := range r.Findings {
		out[f.ID]++
	}
	return out
}

// TestRiskFindings: the three risks the agent-permissions study found under an A grade each cost
// points, and their safe counterparts cost nothing.
func TestRiskFindings(t *testing.T) {
	gh := fakeToken("gh"+"p_", 36)
	cases := []struct {
		name    string
		files   map[string]string
		home    map[string]string
		want    map[string]int // finding id → count
		notA    bool
		noValue string // a value that must never appear in any output
	}{
		{name: "mysql-no-read-only", files: map[string]string{".mcp.json": mcpJSON(t, map[string]any{
			"mysql": map[string]any{"command": "npx", "args": []string{"-y", "@benborla29/mcp-server-mysql"}, "env": map[string]any{"MYSQL_HOST": "db.internal"}}})},
			want: map[string]int{"database_server_not_read_only": 1}, notA: true},
		{name: "mysql-read-only", files: map[string]string{".mcp.json": mcpJSON(t, map[string]any{
			"mysql": map[string]any{"command": "npx", "args": []string{"-y", "@benborla29/mcp-server-mysql"}, "env": map[string]any{"MYSQL_READONLY": "true"}}})},
			want: map[string]int{"database_server_not_read_only": 0}},
		{name: "supabase-read-only-flag", files: map[string]string{".mcp.json": mcpJSON(t, map[string]any{
			"supabase": map[string]any{"command": "npx", "args": []string{"-y", "@supabase/mcp-server-supabase", "--read-only"}}})},
			want: map[string]int{"database_server_not_read_only": 0}},
		{name: "db-routed", files: map[string]string{".mcp.json": mcpJSON(t, map[string]any{
			"pg": map[string]any{"command": "deedglass", "args": []string{"mcp", "wrap", "--connector", "pg", "--", "npx", "-y", "@benborla29/mcp-server-mysql"}}})},
			want: map[string]int{"database_server_not_read_only": 0}},
		{name: "not-a-database", files: map[string]string{".mcp.json": mcpJSON(t, map[string]any{
			"fs": map[string]any{"command": "npx", "args": []string{"-y", "@modelcontextprotocol/server-filesystem", "."}}})},
			want: map[string]int{"database_server_not_read_only": 0, "plaintext_secret_in_config": 0}},
		{name: "github-token-in-env", files: map[string]string{".mcp.json": mcpJSON(t, map[string]any{
			"github": map[string]any{"command": "npx", "args": []string{"-y", "@modelcontextprotocol/server-github"}, "env": map[string]any{"GITHUB_PERSONAL_ACCESS_TOKEN": gh}}})},
			want: map[string]int{"plaintext_secret_in_config": 1}, notA: true, noValue: gh},
		{name: "token-reference", files: map[string]string{".mcp.json": mcpJSON(t, map[string]any{
			"github": map[string]any{"command": "npx", "args": []string{"-y", "@modelcontextprotocol/server-github"}, "env": map[string]any{"GITHUB_PERSONAL_ACCESS_TOKEN": "${GITHUB_TOKEN}"}}})},
			want: map[string]int{"plaintext_secret_in_config": 0}},
		{name: "placeholder", files: map[string]string{".mcp.json": mcpJSON(t, map[string]any{
			"x": map[string]any{"command": "npx", "args": []string{"-y", "some-mcp"}, "env": map[string]any{"API_KEY": "your-api-key-here-0000000000"}}})},
			want: map[string]int{"plaintext_secret_in_config": 0}},
		{name: "bearer-header", files: map[string]string{".mcp.json": mcpJSON(t, map[string]any{
			"remote": map[string]any{"type": "http", "url": "https://mcp.example.com/mcp", "headers": map[string]any{"Authorization": "Bearer " + gh}}})},
			want: map[string]int{"plaintext_secret_in_config": 1}, noValue: gh},
		{name: "mysql-url-password-in-env", files: map[string]string{".mcp.json": mcpJSON(t, map[string]any{
			"db": map[string]any{"command": "npx", "args": []string{"-y", "mcp-db-tool", "--read-only"}, "env": map[string]any{"DB_URL": "mysql://app:Xk9vQ2mL7pR4@db.internal:3306/app"}}})},
			want: map[string]int{"plaintext_secret_in_config": 1, "database_server_not_read_only": 0}, noValue: "Xk9vQ2mL7pR4"},
		{name: "local-dev-password", files: map[string]string{".mcp.json": mcpJSON(t, map[string]any{
			"db": map[string]any{"command": "npx", "args": []string{"-y", "mcp-db-tool", "--read-only"}, "env": map[string]any{"DB_URL": "mysql://root:postgres@localhost:3306/app"}}})},
			want: map[string]int{"plaintext_secret_in_config": 0}},
		{name: "bash-allow-all-no-hook", files: map[string]string{".claude/settings.json": `{"permissions":{"allow":["Bash","Edit"]}}`},
			want: map[string]int{"shell_no_guard_hook": 1}, notA: true},
		{name: "bash-allow-all-with-guard", files: map[string]string{".claude/settings.json": `{"permissions":{"allow":["Bash"]},"hooks":{"PreToolUse":[{"matcher":"Bash","hooks":[{"type":"command","command":"deedglass guard shell"}]}]}}`},
			want: map[string]int{"shell_no_guard_hook": 0}},
		{name: "bash-allow-all-user-hook", files: map[string]string{".claude/settings.json": `{"permissions":{"allow":["Bash(npm run *)"]}}`},
			home: map[string]string{".claude/settings.json": `{"hooks":{"PreToolUse":[{"hooks":[{"type":"command","command":"guard"}]}]}}`},
			want: map[string]int{"shell_no_guard_hook": 0}},
		{name: "bash-allow-all-deny", files: map[string]string{".claude/settings.json": `{"permissions":{"allow":["Bash"],"deny":["Bash(rm -rf *)"]}}`},
			want: map[string]int{"shell_no_guard_hook": 0}},
		{name: "bash-narrow", files: map[string]string{".claude/settings.json": `{"permissions":{"allow":["Bash(npm test)","Bash(git status)"]}}`},
			want: map[string]int{"shell_no_guard_hook": 0}},
		{name: "user-bypass", home: map[string]string{".claude/settings.json": `{"permissions":{"defaultMode":"bypassPermissions"}}`},
			want: map[string]int{"shell_no_guard_hook": 1}, notA: true},
		{name: "hook-other-tool-only", files: map[string]string{".claude/settings.json": `{"permissions":{"allow":["Bash"]},"hooks":{"PreToolUse":[{"matcher":"Edit|Write","hooks":[{"type":"command","command":"fmt"}]}]}}`},
			want: map[string]int{"shell_no_guard_hook": 1}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := shellFixture(t, c.name, c.files, c.home)
			got := findingIDs(r)
			for id, n := range c.want {
				if got[id] != n {
					t.Errorf("%s: %d, want %d (findings %v)", id, got[id], n, got)
				}
			}
			if c.notA && r.Score.Grade == "A" {
				t.Errorf("grade A (%d points) with %v", r.Score.Points, got)
			}
			if c.noValue != "" {
				b, _ := json.Marshal(r)
				md := RenderMarkdown(r)
				if strings.Contains(string(b), c.noValue) || strings.Contains(md, c.noValue) {
					t.Errorf("the secret's value appears in the report")
				}
			}
		})
	}
}

// TestRiskFindingsInShareCatalog: every new id is in the closed share catalog.
func TestRiskFindingsInShareCatalog(t *testing.T) {
	cat := ShareCatalog()["findings"].([]ShareEntry)
	have := map[string]bool{}
	for _, e := range cat {
		have[e.ID] = true
	}
	for _, id := range []string{"database_server_not_read_only", "plaintext_secret_in_config", "shell_no_guard_hook"} {
		if !have[id] {
			t.Errorf("%s missing from ShareCatalog", id)
		}
	}
}
