package wrap

import (
	"flag"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"
)

var update = flag.Bool("update", false, "rewrite testdata/mcp/stderr")

var (
	reStderrIDs  = regexp.MustCompile(`(act|apr|deed|pv|agt)_[0-9a-z]{26}`)
	reStderrMS   = regexp.MustCompile(`[0-9]+ ms`)
	reStderrPID  = regexp.MustCompile(`pid [0-9]+`)
	reStderrURL  = regexp.MustCompile(`http://127\.0\.0\.1:[0-9]+`)
	reStderrArgv = regexp.MustCompile(`· /[^ ]+ ·`)
)

// TestStderrGolden pins the wrapper's log lines for one session (testdata/mcp/stderr; lines are
// sorted, because the startup check runs in parallel with the relay).
func TestStderrGolden(t *testing.T) {
	h := start(t, "legacy")
	h.handshake()
	h.request("tools/list", "")
	h.call("echo", `{}`)
	h.call("write_file", `{"path":"/tmp/deedglass-demo/outside.txt"}`)
	h.call("write_file", `{"path":"/tmp/deedglass-demo/allowed/a.txt","approve":true}`)
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(h.stderr.String(), "Gate reachable") && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	h.close()
	var lines []string
	for _, l := range strings.Split(strings.TrimSpace(h.stderr.String()), "\n") {
		l = reStderrIDs.ReplaceAllString(l, "${1}_…")
		l = reStderrMS.ReplaceAllString(l, "N ms")
		l = reStderrPID.ReplaceAllString(l, "pid N")
		l = reStderrURL.ReplaceAllString(l, "http://127.0.0.1:PORT")
		l = reStderrArgv.ReplaceAllString(l, "· <fake server> ·")
		lines = append(lines, l)
	}
	sort.Strings(lines)
	got := strings.Join(lines, "\n") + "\n"
	p := filepath.Join("..", "..", "..", "testdata", "mcp", "stderr", "session.txt")
	if *update {
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(p)
	if err != nil || string(want) != got {
		t.Errorf("stderr differs from %s (go test ./internal/mcp/wrap -run TestStderrGolden -update):\n%s", p, got)
	}
}
