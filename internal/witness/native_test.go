package witness

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func nativeSubject() Subject {
	return Subject{ActionID: "act_1", Org: "org_1", Agent: "agt_1", Tool: "github.create_issue", ConnectorType: "github",
		Mode: ModeGateway, SideEffect: "write", Args: json.RawMessage(`{"repo":"a/b","title":"t"}`),
		Result:   ResultView{Status: StatusSucceeded, Output: json.RawMessage(`{"number":42}`)},
		Spec:     ToolWitnessSpec{Connector: "github", Tool: "create_issue", Native: true, NativeL1: "GitHub shows the issue."},
		Required: L1}
}

func findings(fs ...Finding) func(context.Context, int) (Observation, error) {
	return func(context.Context, int) (Observation, error) {
		return Observation{Status: 200, Target: "issue #42 on GitHub", Findings: fs, LatencyMS: 5}, nil
	}
}

func TestNativeReadBack(t *testing.T) {
	ctx := context.Background()
	s := nativeSubject()
	if p := PlanFor(s); !p.L1 || p.Kind != KindNative {
		t.Fatalf("plan = %+v", p)
	}
	t.Run("match", func(t *testing.T) {
		obs := &fakeObserver{fn: findings(Finding{Field: "title", Expected: "t", Observed: "t", Match: true},
			Finding{Field: "body", Expected: "b", Observed: "b", Match: true}, Finding{Field: "marker", Expected: "act_1", Observed: "act_1", Match: true})}
		r := EvaluateL1(ctx, s, obs, true)
		if len(r.Reasons) != 1 || r.Reasons[0].Code != CodeReadbackMatched || r.Method != "readback.github.create_issue" {
			t.Fatalf("%+v", r)
		}
		if want := "Read back issue #42 on GitHub: title, body and marker match."; r.Reasons[0].Message != want {
			t.Fatalf("message %q", r.Reasons[0].Message)
		}
		if !obs.reqs[0].Native || r.Readback.Target != "issue #42 on GitHub" || r.Readback.Attempts != 1 {
			t.Fatalf("req %+v detail %+v", obs.reqs[0], r.Readback)
		}
		d := Decide(L1, map[Level][]Reason{L0: {R(L0, CodeToolReportedSuccess, "status", nil)}, L1: r.Reasons}, nil)
		if d.Verdict != Verified || d.Level != L1 {
			t.Fatalf("decide %+v", d)
		}
	})
	t.Run("one field", func(t *testing.T) {
		r := EvaluateL1(ctx, s, &fakeObserver{fn: findings(Finding{Field: "branch", Match: true})}, true)
		if !strings.HasSuffix(r.Reasons[0].Message, "branch matches.") {
			t.Fatal(r.Reasons[0].Message)
		}
		r = EvaluateL1(ctx, s, &fakeObserver{fn: findings()}, true)
		if !strings.HasSuffix(r.Reasons[0].Message, "the write is there.") {
			t.Fatal(r.Reasons[0].Message)
		}
	})
	t.Run("mismatch retried then violation", func(t *testing.T) {
		obs := &fakeObserver{fn: findings(Finding{Field: "title", Expected: "Fix typo", Observed: "Fix tpyo", Match: false},
			Finding{Field: "body", Expected: "sha256:aa", Observed: "sha256:bb", Match: false})}
		cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		r := EvaluateL1(cctx, s, obs, true)
		if obs.calls() != NativeAttempts {
			t.Fatalf("attempts %d", obs.calls())
		}
		x := r.Reasons[0]
		if x.Code != CodePostconditionFailed || x.Class != Violation || len(x.Diff) != 2 || x.Diff[1].Expected != "sha256:aa" {
			t.Fatalf("%+v", x)
		}
		if want := `github.create_issue reported success, but issue #42 on GitHub says title is "Fix tpyo", not "Fix typo".`; x.Message != want {
			t.Fatalf("message %q", x.Message)
		}
	})
	t.Run("not found is a gap after three tries", func(t *testing.T) {
		obs := &fakeObserver{fn: fail(ErrNotFound)}
		r := EvaluateL1(ctx, s, obs, true)
		if obs.calls() != 3 || r.Reasons[0].Code != CodeReadbackNotFound || r.Reasons[0].Class != Gap {
			t.Fatalf("calls %d %+v", obs.calls(), r.Reasons)
		}
	})
	t.Run("not found then found", func(t *testing.T) {
		n := 0
		obs := &fakeObserver{fn: func(c context.Context, a int) (Observation, error) {
			n++
			if n < 2 {
				return Observation{}, ErrNotFound
			}
			return findings(Finding{Field: "state", Match: true})(c, a)
		}}
		r := EvaluateL1(ctx, s, obs, true)
		if r.Reasons[0].Code != CodeReadbackMatched || r.Readback.Attempts != 2 {
			t.Fatalf("%+v", r)
		}
	})
	t.Run("rate limit is a gap", func(t *testing.T) {
		r := EvaluateL1(ctx, s, &fakeObserver{fn: fail(&ReadError{Code: "upstream_rate_limited"})}, true)
		x := r.Reasons[0]
		if x.Code != CodeReadbackFailed || x.Class != Gap || !strings.Contains(x.Message, "upstream_rate_limited") {
			t.Fatalf("%+v", x)
		}
		r = EvaluateL1(ctx, s, &fakeObserver{fn: fail(context.Canceled)}, true)
		if r.Reasons[0].Code != CodeReadbackFailed {
			t.Fatalf("%+v", r.Reasons)
		}
	})
	t.Run("skip is a note", func(t *testing.T) {
		r := EvaluateL1(ctx, s, &fakeObserver{fn: fail(&SkipError{Why: "the comment was deleted by policy"})}, true)
		if r.Reasons[0].Code != CodeReadbackSkipped || r.Reasons[0].Class != Note {
			t.Fatalf("%+v", r.Reasons)
		}
	})
	t.Run("budget", func(t *testing.T) {
		cctx, cancel := WithBudget(ctx, time.Millisecond)
		defer cancel()
		time.Sleep(2 * time.Millisecond)
		r := EvaluateL1(cctx, s, &fakeObserver{fn: fail(context.DeadlineExceeded)}, false)
		if !r.Deferred {
			t.Fatalf("%+v", r)
		}
		r = EvaluateL1(cctx, s, &fakeObserver{fn: fail(context.DeadlineExceeded)}, true)
		if r.Reasons[0].Code != CodeReadbackTimeout {
			t.Fatalf("%+v", r.Reasons)
		}
	})
	t.Run("unknown outcome is never read back", func(t *testing.T) {
		u := nativeSubject()
		u.Result.Status = StatusUnknown
		if PlanFor(u).L1 {
			t.Fatal("an unknown outcome planned a read-back")
		}
	})
}

func TestJoinAnd(t *testing.T) {
	for in, want := range map[string]string{"": "", "a": "a", "a,b": "a and b", "a,b,c": "a, b and c"} {
		var xs []string
		if in != "" {
			xs = strings.Split(in, ",")
		}
		if got := joinAnd(xs); got != want {
			t.Errorf("%q → %q", in, got)
		}
	}
}
