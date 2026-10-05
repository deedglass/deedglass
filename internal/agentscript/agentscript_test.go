package agentscript

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParseRefusals(t *testing.T) {
	head := "deedglass: agent-script/v1\norg: acme-demo\nagent: claude-code\nscenes:\n  - id: one\n    title: One\n    steps:\n"
	for _, c := range []struct{ name, steps, want string }{
		{"unknown step kind", "      - shell: rm -rf /\n", "unknown field or step kind"},
		{"two kinds in one step", "      - make: demo-trigger-on\n        run: [verify, x]\n", "exactly one of"},
		{"no kind", "      - expect: {status: executed}\n", "exactly one of"},
		{"make outside the allow-list", "      - make: demo-down\n", `make "demo-down" isn't allowed`},
		{"make that shells", "      - make: \"demo-trigger-on; rm -rf /\"\n", "isn't allowed"},
		{"tamper outside .tmp/demo", "      - tamper: {from: .tmp/demo/a.jsonl, to: /etc/passwd, deed: x}\n", "must be a relative path"},
		{"tamper escaping with ..", "      - tamper: {from: .tmp/demo/a.jsonl, to: .tmp/demo/../../x, deed: x}\n", "outside .tmp/demo/"},
		{"tamper in place", "      - tamper: {from: .tmp/demo/a.jsonl, to: .tmp/demo/a.jsonl, deed: x}\n", "must differ"},
		{"run as a shell string", "      - run: \"deedglass verify x | sh\"\n", "never a shell string"},
		{"run with a pipe", "      - run: [verify, \"x|sh\"]\n", "looks like shell"},
		{"run with a command substitution", "      - run: [verify, \"$(id)\"]\n", "looks like shell"},
		{"run of a program", "      - run: [sh, -c, id]\n", "not a program"},
		{"run of deedglass itself", "      - run: [deedglass, verify]\n", "not a program"},
		{"run outside the allow-list", "      - run: [org, delete]\n", "isn't allowed"},
		{"run writing outside .tmp/demo", "      - run: [deeds, export, --out, /tmp/x.jsonl]\n", "--out"},
		{"as someone else", "      - call: {tool: postgres.query}\n        as: root\n", "as must be agent or admin"},
		{"call capturing a regexp", "      - call: {tool: postgres.query}\n        capture: {x: \"act_.*\"}\n", "captures id, deed or approval"},
		{"approve without an email", "      - approve: {approval: apr_x, as: ops}\n", "approver's email"},
		{"unknown need", "      - call: {tool: x}\n", ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, err := Parse("launch.yaml", []byte(head+c.steps))
			if c.want == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("got %v, want an error containing %q", err, c.want)
			}
			if !strings.Contains(err.Error(), "launch.yaml") {
				t.Fatalf("the error doesn't say where: %v", err)
			}
		})
	}
	if _, err := Parse("x", []byte("deedglass: agent-script/v2\norg: a\nagent: b\nscenes: []\n")); err == nil || !strings.Contains(err.Error(), "agent-script/v1") {
		t.Fatalf("version: %v", err)
	}
	if _, err := Parse("x", []byte(head+"      - call: {tool: x}\n    needs: [phone]\n")); err == nil || !strings.Contains(err.Error(), "needs") {
		t.Fatalf("needs: %v", err)
	}
}

func TestLaunchScriptParses(t *testing.T) {
	b, err := os.ReadFile("../../examples/demo/launch.yaml")
	if err != nil {
		t.Fatal(err)
	}
	s, err := Parse("launch.yaml", b)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Scenes) != 7 {
		t.Fatalf("%d scenes, want 7", len(s.Scenes))
	}
	ids := []string{"read", "drop", "ship-17", "ship-42", "undo", "refund", "verify"}
	for i, sc := range s.Scenes {
		if sc.ID != ids[i] {
			t.Fatalf("scene %d is %s, want %s", i+1, sc.ID, ids[i])
		}
		for _, l := range sc.Say {
			if strings.Count(l, "\n") > 0 || len(sc.Say) > 2 {
				t.Fatalf("scene %s: narration is at most two lines", sc.ID)
			}
		}
	}
}

// fake is a Backend that answers from tables.
type fake struct {
	calls   map[string]Result // by tool
	runs    map[string]RunOutput
	awaited map[string]Result
	log     []string
}

func (f *fake) Call(_ context.Context, as, tool string, args json.RawMessage) (Result, error) {
	f.log = append(f.log, "call "+as+" "+tool+" "+string(args))
	r, ok := f.calls[tool]
	if !ok {
		return Result{}, errors.New("no such tool")
	}
	return r, nil
}

func (f *fake) Run(_ context.Context, as string, argv []string) (RunOutput, error) {
	f.log = append(f.log, "run "+as+" "+strings.Join(argv, " "))
	return f.runs[argv[0]], nil
}

func (f *fake) Make(_ context.Context, target string) error {
	f.log = append(f.log, "make "+target)
	return nil
}

func (f *fake) Countersign(_ context.Context, apr, as, _ string) error {
	f.log = append(f.log, "approve "+apr+" "+as)
	return nil
}

func (f *fake) WaitDecided(context.Context, string, time.Duration) (string, error) {
	return "priya@acme.test", nil
}

func (f *fake) Await(_ context.Context, id string) (Result, error) {
	return f.awaited[id], nil
}

const script = `deedglass: agent-script/v1
title: Deedglass demo
org: acme-demo
agent: claude-code
scenes:
  - id: ship
    title: The trigger
    say: ["Same request."]
    steps:
      - make: demo-trigger-on
      - call: {tool: postgres.execute, args: {sql: "UPDATE public.orders SET status = 'shipped' WHERE id = 42"}}
        expect: {status: executed, level: L2, verdict: MISMATCH}
        capture: {ship42: id}
  - id: undo
    title: Put it back
    steps:
      - run: [undo, run, "${ship42}", --json]
        as: admin
        show: none
        expect: {exit: 2, output: pending_approval}
        capture: {apr: "(apr_[0-9a-z]+)", fix: "\"id\": \"(act_[0-9a-z]+)\""}
        print: "◐ pending approval · postgres.undo · a person countersigns first"
      - approve: {approval: "${apr}", as: ops@example.test, reason: Checked, action: "${fix}"}
        expect: {status: executed, verdict: VERIFIED}
  - id: refund
    title: A refund over $50
    needs: [stripe]
    steps:
      - call: {tool: stripe.create_refund}
`

func newFake() *fake {
	return &fake{
		calls: map[string]Result{"postgres.execute": {ID: "act_1", Tool: "postgres.execute", Status: "executed", Level: "L2", Verdict: "MISMATCH",
			Summary: "Postgres changed 301 rows in public.orders. The agent reported 1.", Diff: []string{"expected  UPDATE 1 row", "observed  UPDATE 301 rows"}}},
		runs:    map[string]RunOutput{"undo": {Text: `{"id": "act_2", "status": "pending_approval", "approval": {"id": "apr_9"}}`, Exit: 2}},
		awaited: map[string]Result{"act_2": {ID: "act_2", Tool: "postgres.undo", Status: "executed", Level: "L2", Verdict: "VERIFIED", Summary: "301 rows restored"}},
	}
}

func TestRunOffline(t *testing.T) {
	s, err := Parse("t", []byte(script))
	if err != nil {
		t.Fatal(err)
	}
	f := newFake()
	var out bytes.Buffer
	sum, err := Run(context.Background(), s, f, Options{Out: &out})
	if err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	got := out.String()
	for _, want := range []string{
		"Deedglass demo · acme-demo · offline · 3 scenes",
		"── 2/3 Put it back ───",
		"✕ L2 · MISMATCH · Postgres changed 301 rows in public.orders. The agent reported 1.",
		"    expected  UPDATE 1 row",
		"◐ pending approval · postgres.undo · a person countersigns first",
		"✓ countersigned by ops@example.test",
		"✓ executed · postgres.undo · L2 · VERIFIED · 301 rows restored",
		"○ skipped · needs Stripe test mode · run make demo-live",
		"Demo complete · 2 scenes · 1 skipped · 0 s",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in\n%s", want, got)
		}
	}
	if strings.Contains(got, "Same request.") {
		t.Fatal("narration prints only in --step mode")
	}
	if sum.Passed != 2 || sum.Skipped != 1 || sum.Captured["apr"] != "apr_9" || sum.Captured["fix"] != "act_2" {
		t.Fatalf("summary %+v", sum)
	}
	if strings.Join(f.log, "\n") != strings.Join([]string{
		"make demo-trigger-on",
		`call agent postgres.execute {"sql":"UPDATE public.orders SET status = 'shipped' WHERE id = 42"}`,
		"run admin undo run act_1 --json",
		"approve apr_9 ops@example.test",
	}, "\n") {
		t.Fatalf("log:\n%s", strings.Join(f.log, "\n"))
	}
	for _, l := range strings.Split(got, "\n") {
		if strings.HasPrefix(l, "──") && len([]rune(l)) != RuleWidth {
			t.Fatalf("header %q is %d columns", l, len([]rune(l)))
		}
	}
}

func TestRunUnmetExpectation(t *testing.T) {
	s, _ := Parse("t", []byte(strings.Replace(script, "verdict: MISMATCH", "verdict: VERIFIED", 1)))
	var out bytes.Buffer
	_, err := Run(context.Background(), s, newFake(), Options{Out: &out})
	var se *StepError
	if !errors.As(err, &se) || se.Expected != "VERIFIED" || se.Actual != "MISMATCH" {
		t.Fatalf("got %v", err)
	}
	if !strings.Contains(out.String(), "✕ scene ship, step 2: expected verdict VERIFIED, got MISMATCH") || strings.Contains(out.String(), "Put it back") {
		t.Fatalf("the run must stop at the first unmet expectation:\n%s", out.String())
	}
}

func TestRunExitCode(t *testing.T) {
	s, _ := Parse("t", []byte(strings.Replace(script, "exit: 2", "exit: 0", 1)))
	_, err := Run(context.Background(), s, newFake(), Options{})
	var se *StepError
	if !errors.As(err, &se) || se.Field != "exit code" || se.Expected != "0" || !strings.HasPrefix(se.Actual, "2") {
		t.Fatalf("got %v", err)
	}
}

func TestRunStepAndOnly(t *testing.T) {
	s, _ := Parse("t", []byte(script))
	var out bytes.Buffer
	_, err := Run(context.Background(), s, newFake(), Options{Out: &out, Step: true, In: strings.NewReader("\n\n\n")})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Same request.") || !strings.Contains(out.String(), "Press Enter for scene 2 · Put it back") {
		t.Fatalf("step mode:\n%s", out.String())
	}
	out.Reset()
	if _, err := Run(context.Background(), s, newFake(), Options{Out: &out, Only: "refund"}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "The trigger") || !strings.Contains(out.String(), "── 3/3 A refund over $50") {
		t.Fatalf("only:\n%s", out.String())
	}
	if _, err := Run(context.Background(), s, newFake(), Options{Only: "nope"}); err == nil || !strings.Contains(err.Error(), "ship, undo, refund") {
		t.Fatalf("unknown scene: %v", err)
	}
	// Live: every need is met, and approve waits for a person.
	out.Reset()
	f := newFake()
	f.calls["stripe.create_refund"] = Result{Tool: "stripe.create_refund", Status: "pending_approval", Message: "Waiting for a person."}
	if _, err := Run(context.Background(), s, f, Options{Out: &out, Live: true}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "countersigned by priya@acme.test") || !strings.Contains(out.String(), "◐ pending approval · stripe.create_refund") {
		t.Fatalf("live:\n%s", out.String())
	}
}

func TestUncapturedVariable(t *testing.T) {
	s, _ := Parse("t", []byte(strings.Replace(script, `"${ship42}"`, `"${nope}"`, 1)))
	_, err := Run(context.Background(), s, newFake(), Options{})
	if err == nil || !strings.Contains(err.Error(), "${nope} isn't captured") {
		t.Fatalf("got %v", err)
	}
}

func TestTamperFile(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, ".tmp", "demo")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	orig := `{"body":{"deed_id":"deed_a","seq":1,"execution":null},"id":"deed_a"}` + "\n" +
		`{"body":{"deed_id":"deed_b","execution":{"latency_ms":129,"status":"succeeded"},"seq":2},"id":"deed_b"}` + "\n" +
		`{"body":{"deed_id":"deed_c","execution":{"latency_ms":9},"seq":3},"id":"deed_c"}` + "\n"
	src := filepath.Join(dir, "deeds.jsonl")
	if err := os.WriteFile(src, []byte(orig), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ deed, seq, want string }{{"deed_b", "2", `"latency_ms":120`}, {"deed_c", "3", `"latency_ms":0`}} {
		seq, err := TamperFile(root, ".tmp/demo/deeds.jsonl", ".tmp/demo/changed.jsonl", c.deed)
		if err != nil || seq != c.seq {
			t.Fatalf("%s: %q %v", c.deed, seq, err)
		}
		got, _ := os.ReadFile(filepath.Join(dir, "changed.jsonl"))
		if len(got) != len(orig) {
			t.Fatal("the length changed")
		}
		diff := 0
		for i := range got {
			if got[i] != orig[i] {
				diff++
			}
		}
		if diff != 1 || !strings.Contains(string(got), c.want) {
			t.Fatalf("%s: %d bytes differ:\n%s", c.deed, diff, got)
		}
		if now, _ := os.ReadFile(src); string(now) != orig {
			t.Fatal("the original export changed")
		}
	}
	if _, err := TamperFile(root, ".tmp/demo/deeds.jsonl", ".tmp/demo/x", "deed_a"); err == nil || !strings.Contains(err.Error(), "no execution.latency_ms") {
		t.Fatalf("no latency: %v", err)
	}
	if _, err := TamperFile(root, ".tmp/demo/deeds.jsonl", "../x", "deed_b"); err == nil {
		t.Fatal("a path outside .tmp/demo/ was written")
	}
}

func TestDuration(t *testing.T) {
	for d, want := range map[time.Duration]string{41 * time.Second: "41 s", 192 * time.Second: "3 m 12 s", 60 * time.Second: "1 m 00 s"} {
		if got := Duration(d); got != want {
			t.Errorf("%v: %q", d, got)
		}
	}
}
