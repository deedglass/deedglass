package main

import (
	"context"
	"encoding/json"
	"net"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/deedglass/deedglass/internal/cli/ui"
	"github.com/deedglass/deedglass/internal/setup"
)

// noDocker makes deedglass start take the in-process path, whatever this machine has.
func noDocker(t *testing.T, why string) {
	t.Helper()
	saved := startDocker
	startDocker = func(context.Context, *setup.ProcRunner, string) (bool, string) { return false, why }
	t.Cleanup(func() { startDocker = saved })
}

var reSealKey = regexp.MustCompile(`sk_[0-9a-z]{8}…`)

// Without Docker, deedglass start plays the scene in-process and ends on the catch and Next
// (first-run-v1). The seal key is made per run, so its id is normalised.
func TestStartNoDockerGolden(t *testing.T) {
	noDocker(t, "Docker isn't running")
	r := cli(t, nil, false, "start")
	r.stdout = reSealKey.ReplaceAllString(r.stdout, "sk_00000000…")
	golden(t, "start_no_docker", r)
}

func TestStartNoDockerLayout(t *testing.T) {
	noDocker(t, "only 1.2 GiB of disk is free (the live version needs 2 GiB)")
	for _, c := range []struct {
		name string
		env  map[string]string
		tty  bool
		args []string
	}{
		{"piped", nil, false, []string{"start"}},
		{"NO_COLOR on a TTY", map[string]string{"NO_COLOR": "1", "DEEDGLASS_NO_ANIM": "1"}, true, []string{"start", "--fast"}},
		{"--no-color on a TTY", map[string]string{"DEEDGLASS_NO_ANIM": "1"}, true, []string{"start", "--fast", "--no-color"}},
		{"colour on a TTY", map[string]string{"DEEDGLASS_NO_ANIM": "1"}, true, []string{"start", "--fast"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := cli(t, c.env, c.tty, c.args...)
			if r.code != 0 {
				t.Fatalf("exit %d: %s", r.code, r.stderr)
			}
			colour := c.name == "colour on a TTY"
			if !colour && strings.Contains(r.stdout, "\x1b") {
				t.Errorf("escape codes with colour off:\n%s", r.stdout)
			}
			for _, l := range strings.Split(r.stdout, "\n") {
				if w := ui.Width(ui.StripANSI(l)); w > 80 {
					t.Errorf("line of %d columns: %q", w, l)
				}
			}
			plain := ui.StripANSI(r.stdout)
			for _, want := range []string{"○ Only 1.2 GiB of disk is free", "Doesn't match: Postgres changed 301 rows", "✓ Caught. The agent reported 1 row; Postgres changed 301.",
				"Next\n  deedglass start ", "deedglass protect", "deedglass health pilot"} {
				if !strings.Contains(plain, want) {
					t.Errorf("output lacks %q:\n%s", want, plain)
				}
			}
			if strings.Contains(plain, "Play complete") {
				t.Error("start prints its own ending, not play's")
			}
		})
	}
}

func TestStartNoDockerJSON(t *testing.T) {
	noDocker(t, "Docker isn't installed")
	r := cli(t, nil, false, "start", "--json")
	if r.code != 0 {
		t.Fatalf("exit %d: %s", r.code, r.stderr)
	}
	var got struct {
		Object, Mode, Reason string
		Play                 struct {
			Records int  `json:"records"`
			Chain   bool `json:"chain_intact"`
		}
		Next []string
	}
	if err := json.Unmarshal([]byte(r.stdout), &got); err != nil {
		t.Fatalf("not one JSON object: %v\n%s", err, r.stdout)
	}
	if got.Object != "start_result" || got.Mode != "replay" || got.Reason != "Docker isn't installed" || got.Play.Records != 4 || !got.Play.Chain {
		t.Errorf("%+v", got)
	}
	if len(got.Next) != 3 || got.Next[0] != "deedglass start" || got.Next[1] != "deedglass protect" {
		t.Errorf("next %v", got.Next)
	}
}

// Under npx the commands it prints say npx deedglass.
func TestStartNoDockerNpx(t *testing.T) {
	noDocker(t, "Docker isn't running")
	r := cli(t, map[string]string{"npm_config_user_agent": "npm/11", "npm_command": "exec"}, false, "start")
	if !strings.Contains(r.stdout, "  npx deedglass protect") || strings.Contains(r.stdout, "\n  deedglass protect") {
		t.Errorf("want npx commands:\n%s", r.stdout)
	}
}

func TestStartRefusesArgs(t *testing.T) {
	r := cli(t, nil, false, "start", "now")
	if r.code != 2 || !strings.Contains(r.stderr, "deedglass start takes no arguments") {
		t.Errorf("exit %d: %s", r.code, r.stderr)
	}
}

// Busy default ports are swapped for the next free ones, with one line each; free ones stay.
func TestPickPorts(t *testing.T) {
	busy := map[string]bool{}
	free := func(a string) bool { return !busy[a] }
	l, pg, notes := pickPorts("127.0.0.1:8080", 55432, free)
	if l != "127.0.0.1:8080" || pg != 55432 || len(notes) != 0 {
		t.Fatalf("all free: %s %d %v", l, pg, notes)
	}
	busy["127.0.0.1:8080"], busy["127.0.0.1:8081"], busy["127.0.0.1:55442"] = true, true, true
	l, pg, notes = pickPorts("127.0.0.1:8080", 55432, free)
	if l != "127.0.0.1:8082" || pg != 55433 || len(notes) != 2 {
		t.Fatalf("busy: %s %d %v", l, pg, notes)
	}
	if notes[0] != "Port 8080 is busy, so the server uses 8082." || notes[1] != "Port 55432 or 55442 is busy, so Postgres uses 55433 and 55443." {
		t.Errorf("notes %q", notes)
	}
}

// pickPorts against real listeners: a port this test holds is never picked.
func TestPickPortsRealListeners(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skip(err)
	}
	defer ln.Close()
	held := ln.Addr().String()
	l, _, notes := pickPorts(held, 1, func(a string) bool {
		if a == held {
			return portFree(a)
		}
		return true
	})
	if l == held || len(notes) == 0 {
		t.Errorf("picked the held port %s (%v)", l, notes)
	}
	_, port, _ := net.SplitHostPort(held)
	p, _ := strconv.Atoi(port)
	if l != "127.0.0.1:"+strconv.Itoa(p+1) {
		t.Errorf("want the next port, got %s", l)
	}
}

// The words a new user tries first point at the right command.
func TestFirstRunWords(t *testing.T) {
	for _, c := range []struct{ word, want string }{
		{"check", "deedglass doctor"},
		{"test", "deedglass verify"},
		{"quickstart", "The first run is deedglass start."},
		{"demo", "The first run is deedglass start."},
		{"setup", "Run deedglass start"},
	} {
		r := cli(t, nil, false, c.word)
		if r.code != 2 || !strings.Contains(r.stderr, c.want) {
			t.Errorf("%s: exit %d, stderr %q", c.word, r.code, r.stderr)
		}
	}
}

// The welcome stays within its five lines of content and 80 columns, and says nothing about
// Docker it can't know.
func TestWelcomeShape(t *testing.T) {
	r := cli(t, nil, false)
	var content int
	for _, l := range strings.Split(strings.TrimRight(r.stdout, "\n"), "\n") {
		if ui.Width(l) > 80 {
			t.Errorf("line of %d columns: %q", ui.Width(l), l)
		}
		if strings.TrimSpace(l) != "" {
			content++
		}
	}
	if content != 5 {
		t.Errorf("want 5 lines of content, got %d:\n%s", content, r.stdout)
	}
}

// Fast: on a terminal the replayed scene's pauses add up to under 30 s and none is longer than
// 2 s (first-run-v1: nothing silent for more than 2 s; a first success in under 2 minutes).
func TestStartReplayPacing(t *testing.T) {
	noDocker(t, "Docker isn't running")
	var total, longest time.Duration
	saved := pace
	pace = func(_ context.Context, d time.Duration) error {
		total += d
		longest = max(longest, d)
		return nil
	}
	t.Cleanup(func() { pace = saved })
	r := cli(t, map[string]string{"DEEDGLASS_NO_ANIM": "1"}, true, "start")
	if r.code != 0 {
		t.Fatalf("exit %d: %s", r.code, r.stderr)
	}
	if total == 0 || total >= 30*time.Second || longest > 2*time.Second {
		t.Errorf("pauses: %v in all, longest %v (want > 0, < 30 s, each ≤ 2 s)", total, longest)
	}
	t.Logf("paced replay: %v of pauses, longest %v", total, longest)
}
