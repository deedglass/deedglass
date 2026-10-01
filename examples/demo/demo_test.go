//go:build demo

// TestDemoScript plays examples/demo/SCRIPT.md against the running demo stack (make demo-up, then
// make demo-smoke): the official Go SDK as the MCP client, launching
// deedglass mcp wrap --connector orders --connector-type postgres -- docker run … postgres-mcp,
// exactly as the Claude Desktop config does. The video is the test.
package demo_test

import (
	"bufio"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/deedglass/deedglass/internal/mcp/snippet"
)

func readKV(t *testing.T, path string) map[string]string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("%s: %v (run make demo-up first)", path, err)
	}
	defer f.Close()
	out := map[string]string{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if k, v, ok := strings.Cut(strings.TrimSpace(sc.Text()), "="); ok && !strings.HasPrefix(k, "#") {
			out[k] = v
		}
	}
	return out
}

func text(r *mcp.CallToolResult) string {
	var b strings.Builder
	for _, c := range r.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	return b.String()
}

func TestDemoScript(t *testing.T) {
	if os.Getenv("DEEDGLASS_DEMO") != "1" {
		t.Skip("needs the demo stack: make demo-up, then make demo-smoke")
	}
	env := readKV(t, ".env")
	keys := readKV(t, ".demo-keys")
	port := env["DEMO_PORT"]
	if port == "" {
		port = "8080"
	}
	root, _ := filepath.Abs(filepath.Join("..", ".."))
	bin := filepath.Join(root, "bin", "deedglass")
	docker, err := exec.LookPath("docker")
	if err != nil {
		t.Fatal("docker isn't on PATH")
	}
	dsn := "postgresql://acme_owner:" + env["ACME_OWNER_PASSWORD"] + "@localhost:55434/acme"
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	cmd := exec.Command(bin, "mcp", "wrap", "--connector", "orders", "--connector-type", "postgres", "--no-log-file", "--",
		docker, "run", "-i", "--rm", "--init", "-e", "DATABASE_URI", snippet.PostgresMCPImage, "--access-mode=unrestricted")
	cmd.Env = append(os.Environ(), "DEEDGLASS_API_KEY="+keys["claude-code"], "DEEDGLASS_SERVER=http://127.0.0.1:"+port, "DATABASE_URI="+dsn)
	cmd.Dir = t.TempDir() // no .env or deedglass.yaml of the repo's
	c := mcp.NewClient(&mcp.Implementation{Name: "demo-script", Version: "1"}, nil)
	s, err := c.Connect(ctx, &mcp.CommandTransport{Command: cmd}, &mcp.ClientSessionOptions{ProtocolVersion: "2025-11-25"})
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer s.Close()

	lt, err := s.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, x := range lt.Tools {
		names = append(names, x.Name)
	}
	t.Logf("tools/list → %s", strings.Join(names, ", "))
	joined := "," + strings.Join(names, ",") + ","
	if !strings.Contains(joined, ",execute_sql,") || !strings.Contains(joined, ",deedglass.check_approval,") || strings.Contains(joined, ",analyze_db_health,") {
		t.Fatalf("tools: %v", names)
	}

	sql := func(beat, q string) *mcp.CallToolResult {
		t.Helper()
		r, err := s.CallTool(ctx, &mcp.CallToolParams{Name: "execute_sql", Arguments: map[string]any{"sql": q}})
		if err != nil {
			t.Fatalf("%s: %v", beat, err)
		}
		t.Logf("%s\n  sql: %s\n  isError: %v\n  text: %s", beat, q, r.IsError, strings.ReplaceAll(text(r), "\n", "\n        "))
		return r
	}
	// Beat 1: the five newest orders.
	r := sql("Beat 1 · show me the 5 newest orders", "SELECT id, status, total_cents, created_at FROM orders ORDER BY created_at DESC LIMIT 5")
	if r.IsError || !strings.Contains(text(r), "500") || !strings.Contains(text(r), "496") {
		t.Fatalf("beat 1: %s", text(r))
	}
	// Beat 2: DROP is blocked with the brand's sentence.
	r = sql("Beat 2 · drop the refunds table", "DROP TABLE refunds")
	if !r.IsError || !strings.HasPrefix(text(r), `Blocked by policy "block-destructive-sql": DROP is never allowed for agents. Nothing was run.`) {
		t.Fatalf("beat 2: %s", text(r))
	}
	// Beat 3: deleting the cancelled orders is blocked.
	r = sql("Beat 3 · delete all the cancelled orders", "DELETE FROM orders WHERE status = 'cancelled'")
	if !r.IsError || !strings.HasPrefix(text(r), `Blocked by policy "no-deletes-on-orders": Agents never delete orders. Set the status to 'cancelled' instead.`) {
		t.Fatalf("beat 3: %s", text(r))
	}
	// Beat 4: marking order 42 as shipped waits for a person.
	r = sql("Beat 4 · mark order 42 as shipped", "UPDATE orders SET status = 'shipped' WHERE id = 42")
	if !r.IsError || !strings.HasPrefix(text(r), `Waiting for approval under policy "orders-writes-need-approval": Writes to the orders database need a person to countersign. Nothing has run yet. Approval apr_`) {
		t.Fatalf("beat 4: %s", text(r))
	}

	// Postgres agrees: nothing destructive ran.
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	var refunds, cancelled int
	var status42 string
	if err := conn.QueryRow(ctx, `SELECT (SELECT count(*) FROM public.refunds), (SELECT count(*) FROM public.orders WHERE status = 'cancelled'),
	    (SELECT status FROM public.orders WHERE id = 42)`).Scan(&refunds, &cancelled, &status42); err != nil {
		t.Fatal(err)
	}
	t.Logf("psql → refunds %d rows · %d orders cancelled · order 42 %s", refunds, cancelled, status42)
	if refunds != 60 || cancelled != 37 || status42 != "paid" {
		t.Fatalf("the database changed: refunds %d, cancelled %d, order 42 %s", refunds, cancelled, status42)
	}

	// Beat 5: the deeds of the last 10 minutes verify, chain intact.
	out, err := exec.CommandContext(ctx, "scripts/demo.sh", "verify").CombinedOutput()
	t.Logf("Beat 5 · make demo-verify → %s", strings.TrimSpace(string(out)))
	if err != nil || !strings.Contains(string(out), "chain intact") {
		t.Fatalf("verify: %v %s", err, out)
	}
}
