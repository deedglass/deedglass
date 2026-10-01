package main

import (
	"os"
	"os/exec"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/deedglass/deedglass/internal/httpx"
	"github.com/deedglass/deedglass/internal/policy"
)

var (
	microsRE  = regexp.MustCompile(`[0-9][0-9,]* µs`)
	evalUSRE  = regexp.MustCompile(`"evaluated_in_us": [0-9]+`)
	retryInRE = regexp.MustCompile(`Try again in [0-9]+ s`)
)

// normalizeEval removes the evaluation time, the only part of the output that varies.
func normalizeEval(r result) result {
	r.stdout = microsRE.ReplaceAllString(r.stdout, "N µs")
	r.stdout = evalUSRE.ReplaceAllString(r.stdout, `"evaluated_in_us": N`)
	return r
}

// TestPolicyEvaluateGoldens is S11 §5: the demo layout, every decision kind, --json, and every
// exit code. Each case fixes --time, so budget messages are exact.
func TestPolicyEvaluateGoldens(t *testing.T) {
	const ps = "../../examples/policies/postgres-safe.yaml"
	at := "--time=2026-10-04T04:30:46Z"
	cases := []struct {
		name string
		tty  bool
		args []string
	}{
		{"evaluate_demo", false, []string{"policy", "evaluate", "--policy", ps, "--tool", "postgres.execute", "--sql", "/* tidy up */ DrOp TABLE public.orders", "--explain", at}},
		{"evaluate_demo_color", true, []string{"policy", "evaluate", "--policy", ps, "--tool", "postgres.execute", "--sql", "/* tidy up */ DrOp TABLE public.orders", "--explain", at}},
		{"evaluate_stacked", false, []string{"policy", "evaluate", "--policy", ps, "--tool", "postgres.execute", "--sql", "COMMIT; DROP TABLE orders", at}},
		{"evaluate_approval", false, []string{"policy", "evaluate", "--policy", ps, "--tool", "postgres.execute", "--sql", "UPDATE public.orders SET status = 'shipped' WHERE id = 42", at}},
		{"evaluate_allow", false, []string{"policy", "evaluate", "--policy", ps, "--tool", "postgres.query", "--sql", "SELECT id FROM orders LIMIT 5", at}},
		{"evaluate_budget", false, []string{"policy", "evaluate", "--policy", ps, "--tool", "postgres.query", "--sql", "SELECT 1", "--usage", "calls_minute=60", at}},
		{"evaluate_default", false, []string{"policy", "evaluate", "--policy", ps, "--tool", "slack.post_message", "--args", `{"text":"hi"}`, "--explain", at}},
		{"evaluate_lookalike", false, []string{"policy", "evaluate", "--policy", ps, "--tool", "postgres.execute", "--sql", "DRОP TABLE orders", "--explain", at}},
		{"evaluate_json", false, []string{"policy", "evaluate", "--policy", ps, "--tool", "postgres.execute", "--sql", "DELETE FROM orders", "--json", at, "--agent", "support-bot", "--label", "env=prod", "--tz", "Asia/Kolkata"}},
		{"evaluate_invalid_policy", false, []string{"policy", "evaluate", "--policy", "testdata/policy/refunds.yaml", "--tool", "stripe.refund", at}},
		{"evaluate_no_policy", false, []string{"policy", "evaluate", "--tool", "postgres.query"}},
		{"evaluate_no_tool", false, []string{"policy", "evaluate", "--policy", ps}},
		{"evaluate_bad_time", false, []string{"policy", "evaluate", "--policy", ps, "--tool", "postgres.query", "--time", "yesterday"}},
		{"evaluate_bad_usage", false, []string{"policy", "evaluate", "--policy", ps, "--tool", "postgres.query", "--usage", "calls_minute=lots", at}},
		{"evaluate_bad_args", false, []string{"policy", "evaluate", "--policy", ps, "--tool", "postgres.query", "--args", "[1]", at}},
		{"evaluate_input_file", false, []string{"policy", "evaluate", "--policy", ps, "--input", "testdata/policy/evaluate_input.json", "--explain"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			golden(t, "policy/"+c.name, normalizeEval(cli(t, nil, c.tty, c.args...)))
		})
	}
}

// TestPolicyEvaluateAcceptance runs the three S11 §9 lines on the in-process CLI.
func TestPolicyEvaluateAcceptance(t *testing.T) {
	const ps = "../../examples/policies/postgres-safe.yaml"
	r := cli(t, nil, false, "policy", "evaluate", "--policy", ps, "--tool", "postgres.execute", "--sql", "/* tidy up */ DrOp TABLE public.orders", "--explain")
	if r.code != 0 || !strings.HasPrefix(r.stdout, "✕ deny · block-destructive-sql") {
		t.Fatalf("%+v", r)
	}
	r = cli(t, nil, false, "policy", "evaluate", "--policy", ps, "--tool", "postgres.execute", "--sql", "COMMIT; DROP TABLE orders")
	if r.code != 0 || !strings.HasPrefix(r.stdout, "✕ deny · one-statement-only") {
		t.Fatalf("%+v", r)
	}
	r = cli(t, nil, false, "policy", "evaluate", "--policy", ps, "--tool", "postgres.query", "--sql", "SELECT 1", "--usage", "calls_minute=60")
	if r.code != 0 || !strings.Contains(r.stdout, `Budget "agent-rate" reached: 60 of 60 calls this minute.`) || !retryInRE.MatchString(r.stdout) {
		t.Fatalf("%+v", r)
	}
}

// TestStopMessagesMatchCatalog keeps the evaluator's Stop messages equal to S08's error catalog,
// and policy_unavailable's message equal to the catalog's (the policy package can't import httpx).
func TestStopMessagesMatchCatalog(t *testing.T) {
	org, stopped, suspended := policy.StopMessages("support-bot")
	if e := httpx.ErrOrgStopped(""); e.Message != org {
		t.Errorf("org: %q vs %q", e.Message, org)
	}
	if e := httpx.ErrAgentStopped("support-bot"); e.Message != stopped {
		t.Errorf("stopped: %q vs %q", e.Message, stopped)
	}
	if e := httpx.ErrAgentSuspended("support-bot"); e.Message != suspended {
		t.Errorf("suspended: %q vs %q", e.Message, suspended)
	}
	if c, ok := httpx.Lookup(httpx.CodePolicyUnavailable); !ok || c.Message != policy.MessageUnavailable {
		t.Errorf("policy_unavailable: %+v", c)
	}
}

// TestTimeZoneDatabase: the binary embeds time/tzdata, so Asia/Kolkata loads with no zoneinfo on
// the host (distroless). It runs the check in a child process with ZONEINFO pointing nowhere.
func TestTimeZoneDatabase(t *testing.T) {
	if os.Getenv("DEEDGLASS_DEV_TZ_CHILD") == "1" {
		if _, err := time.LoadLocation("Asia/Kolkata"); err != nil {
			t.Fatal(err)
		}
		return
	}
	exe, err := os.Executable()
	if err != nil {
		t.Skip(err)
	}
	cmd := exec.Command(exe, "-test.run", "^TestTimeZoneDatabase$")
	cmd.Env = append(os.Environ(), "DEEDGLASS_DEV_TZ_CHILD=1", "ZONEINFO=/nonexistent", "TZ=UTC")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
}
