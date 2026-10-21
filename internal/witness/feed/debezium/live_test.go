//go:build debezium

package debezium

// TestMySQLTriggerLive is TY-05's done check (make debezium-it, never in required CI): MySQL 8.4
// with the trigger scene and Debezium Server 3.3 (deploy/debezium, pinned by digest) in Docker.
// Debezium Server has no file sink, so it posts every event to an HTTP receiver this test runs on
// 127.0.0.1; the events are written as JSON lines and read back through Read and feed.Evaluate.
// -record rewrites testdata/mysql-trigger.jsonl.gz (the CI golden). KEEP=1 keeps the containers.

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/rand"
	"encoding/hex"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

var record = flag.Bool("record", false, "rewrite testdata/mysql-trigger.jsonl.gz from the live run")

// receiver keeps every event body Debezium posts, in arrival order.
type receiver struct {
	mu     sync.Mutex
	events [][]byte
}

func (rc *receiver) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b, err := io.ReadAll(io.LimitReader(r.Body, MaxLine))
	if err != nil {
		http.Error(w, "read", http.StatusBadRequest)
		return
	}
	rc.mu.Lock()
	rc.events = append(rc.events, bytes.TrimSpace(b))
	rc.mu.Unlock()
	w.WriteHeader(http.StatusOK)
}

func (rc *receiver) snapshot() [][]byte {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	return append([][]byte(nil), rc.events...)
}

// ends counts END events whose transaction holds a change to table.
func (rc *receiver) ends(from int, table string) int {
	n := 0
	for _, b := range rc.snapshot()[from:] {
		if bytes.Contains(b, []byte(`"status":"END"`)) && bytes.Contains(b, []byte(`"`+table+`"`)) {
			n++
		}
	}
	return n
}

func freePort(t *testing.T) string {
	t.Helper()
	for range 10 {
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		p := l.Addr().(*net.TCPAddr).Port
		l.Close()
		if p != 8080 && p != 55432 && p != 8090 {
			return fmt.Sprint(p)
		}
	}
	t.Fatal("no free port")
	return ""
}

func secret(t *testing.T) string {
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(b)
}

func TestMySQLTriggerLive(t *testing.T) {
	compose := os.Getenv("DEEDGLASS_DEBEZIUM_COMPOSE")
	if compose == "" {
		t.Skip("set DEEDGLASS_DEBEZIUM_COMPOSE to deploy/debezium/compose.yaml (make debezium-it)")
	}
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not found")
	}
	rc := &receiver{}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: rc, MaxHeaderBytes: 1 << 20, ReadHeaderTimeout: 10 * time.Second}
	go srv.Serve(l)
	t.Cleanup(func() { srv.Close() })

	project := fmt.Sprintf("deedglass-debezium-%d", os.Getpid())
	rootPW := secret(t)
	env := append(os.Environ(),
		"DEBEZIUM_PROJECT="+project,
		"DEBEZIUM_MYSQL_ROOT_PASSWORD="+rootPW,
		"DEBEZIUM_MYSQL_PASSWORD="+secret(t),
		"DEBEZIUM_MYSQL_PORT="+freePort(t),
		"DEBEZIUM_HEALTH_PORT="+freePort(t),
		fmt.Sprintf("DEBEZIUM_SINK_URL=http://host.docker.internal:%d/events", l.Addr().(*net.TCPAddr).Port),
	)
	dc := func(ctx context.Context, stdin string, args ...string) (string, error) {
		cmd := exec.CommandContext(ctx, "docker", append([]string{"compose", "-f", compose}, args...)...)
		cmd.Env = env
		if stdin != "" {
			cmd.Stdin = strings.NewReader(stdin)
		}
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	t.Cleanup(func() {
		if os.Getenv("KEEP") == "1" {
			t.Logf("containers kept: docker compose -p %s down -v", project)
			return
		}
		if out, err := dc(context.Background(), "", "down", "-v", "--remove-orphans"); err != nil {
			t.Logf("compose down: %v\n%s", err, out)
		}
	})
	if out, err := dc(ctx, "", "up", "-d", "--wait"); err != nil {
		t.Fatalf("compose up: %v\n%s", err, out)
	}
	healthPort := envValue(env, "DEBEZIUM_HEALTH_PORT")
	waitFor(t, 3*time.Minute, "Debezium Server healthy", func() bool {
		resp, err := http.Get("http://127.0.0.1:" + healthPort + "/q/health")
		if err != nil {
			return false
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode == 200 && bytes.Contains(b, []byte(`"UP"`))
	})
	mysql := func(sql string) {
		t.Helper()
		out, err := dc(ctx, sql, "exec", "-T", "-e", "MYSQL_PWD="+rootPW, "mysql", "mysql", "-uroot", "shop")
		if err != nil {
			t.Fatalf("mysql: %v\n%s", err, out)
		}
	}
	// A warm-up write shows the connector is streaming; the recording starts after its END.
	waitFor(t, 2*time.Minute, "the warm-up write in the feed", func() bool {
		mysql("INSERT INTO deedglass_ledger (action_id, org_id, agent_id, sql_digest) VALUES (CONCAT('warmup_', UUID()), 'warmup', 'warmup', 'warmup');")
		time.Sleep(2 * time.Second)
		return rc.ends(0, "shop.deedglass_ledger") > 0
	})
	time.Sleep(3 * time.Second) // any later warm-up write lands before the recording starts
	start := len(rc.snapshot())

	mysql(sceneSQL)
	waitFor(t, 2*time.Minute, "the scene's transactions in the feed", func() bool { return rc.ends(start, "shop.orders") >= 3 })

	var buf bytes.Buffer
	for _, b := range rc.snapshot()[start:] {
		buf.Write(b)
		buf.WriteByte('\n')
	}
	path := filepath.Join(t.TempDir(), "feed.jsonl")
	if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Logf("%d events, %d bytes in %s", len(rc.snapshot())-start, buf.Len(), path)
	if *record {
		var gz bytes.Buffer
		zw, _ := gzip.NewWriterLevel(&gz, gzip.BestCompression)
		zw.Write(buf.Bytes())
		zw.Close()
		if err := os.WriteFile(filepath.Join("testdata", "mysql-trigger.jsonl.gz"), gz.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	f, err := Read(bytes.NewReader(buf.Bytes()), sceneOptions())
	if err != nil {
		t.Fatal(err)
	}
	checkScene(t, f)
}

func envValue(env []string, k string) string {
	for _, kv := range env {
		if v, ok := strings.CutPrefix(kv, k+"="); ok {
			return v
		}
	}
	return ""
}

func waitFor(t *testing.T, d time.Duration, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(time.Second)
	}
	t.Fatalf("timed out waiting for %s", what)
}
