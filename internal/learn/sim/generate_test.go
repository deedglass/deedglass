package sim

import (
	"context"
	"encoding/json"
	"math"
	"slices"
	"strings"
	"testing"
	"time"

	simfiles "github.com/deedglass/deedglass/examples/sim"
	"github.com/deedglass/deedglass/internal/policy"
)

// testEnd is 10:30 in Kolkata, so the window ends at 10:00 local and day spans cross midnight.
var testEnd = time.Date(2026, 10, 4, 5, 0, 0, 0, time.UTC)

func defaultPlan(t testing.TB, seed uint64, days int) (*Scenario, Window, []PlannedAction) {
	t.Helper()
	sc := Default()
	w, err := Span(sc, days, testEnd)
	if err != nil {
		t.Fatal(err)
	}
	p, err := Plan(sc, seed, w)
	if err != nil {
		t.Fatal(err)
	}
	return sc, w, p
}

func TestSpan(t *testing.T) {
	sc := Default()
	w, err := Span(sc, 7, testEnd)
	if err != nil {
		t.Fatal(err)
	}
	wantEnd := time.Date(2026, 10, 4, 10, 0, 0, 0, sc.Org.Location)
	if !w.End.Equal(wantEnd) || !w.Start.Equal(wantEnd.Add(-7*24*time.Hour)) || w.Days != 7 || w.End.Location() != time.UTC {
		t.Fatalf("span: %+v", w)
	}
	for _, d := range []int{0, 91} {
		if _, err := Span(sc, d, testEnd); err == nil {
			t.Errorf("days %d accepted", d)
		}
	}
}

func TestPlanDeterministic(t *testing.T) {
	_, _, a := defaultPlan(t, 7, 7)
	_, _, b := defaultPlan(t, 7, 7)
	if PlanDigest(a) != PlanDigest(b) {
		t.Fatal("the same seed gave two plans")
	}
	ja, _ := json.Marshal(a)
	jb, _ := json.Marshal(b)
	if string(ja) != string(jb) {
		t.Fatal("the same seed gave different bytes")
	}
	_, _, c := defaultPlan(t, 8, 7)
	if PlanDigest(a) == PlanDigest(c) {
		t.Fatal("a different seed gave the same plan")
	}
	if !strings.HasPrefix(PlanDigest(a), "sha256:") || len(PlanDigest(a)) != 71 {
		t.Fatalf("digest form: %s", PlanDigest(a))
	}
}

// TestIncidentsKeepBaseline: removing every incident leaves the baseline traffic unchanged.
func TestIncidentsKeepBaseline(t *testing.T) {
	sc, w, full := defaultPlan(t, 7, 7)
	bare := *sc
	bare.Incidents = nil
	p, err := Plan(&bare, 7, w)
	if err != nil {
		t.Fatal(err)
	}
	key := func(a PlannedAction) string {
		a.Seq = 0
		b, _ := json.Marshal(a)
		return string(b)
	}
	// The new_user_refs incident takes over the hour's usual calls (new refs, same calls), so a
	// baseline action may come back with only its ref and incident changed.
	norm := func(a PlannedAction) string {
		a.UserRef, a.Incident = "", ""
		return key(a)
	}
	j, converted := 0, 0
	for _, a := range full {
		switch {
		case j < len(p) && a.Incident == "" && key(a) == key(p[j]):
			j++
		case j < len(p) && a.Incident == KindNewUserRefs && norm(a) == norm(p[j]):
			j, converted = j+1, converted+1
		case a.Incident == "":
			t.Fatalf("baseline action %d differs:\n%s\n%s", j, key(a), key(p[j]))
		}
	}
	if j != len(p) || converted == 0 {
		t.Fatalf("matched %d of %d baseline actions (%d taken over by new_user_refs)", j, len(p), converted)
	}
}

func TestPlanProperties(t *testing.T) {
	sc := Default()
	for seed := uint64(1); seed <= 20; seed++ {
		w, _ := Span(sc, 7, testEnd.Add(time.Duration(seed)*37*time.Minute))
		p, err := Plan(sc, seed, w)
		if err != nil {
			t.Fatal(err)
		}
		for i, a := range p {
			if a.Seq != i+1 {
				t.Fatalf("seed %d: seq %d at index %d", seed, a.Seq, i)
			}
			if a.At.Before(w.Start) || !a.At.Before(w.End) || a.At.Location() != time.UTC || a.At.Nanosecond()%int(time.Millisecond) != 0 {
				t.Fatalf("seed %d: action %d at %v outside [%v, %v) or not UTC ms", seed, a.Seq, a.At, w.Start, w.End)
			}
			if i > 0 && !a.At.After(p[i-1].At) {
				t.Fatalf("seed %d: action %d is not after action %d", seed, a.Seq, a.Seq-1)
			}
			if d := int(a.At.Sub(w.Start)/(24*time.Hour)) + 1; d != a.Day {
				t.Fatalf("seed %d: action %d on day %d, labelled %d", seed, a.Seq, d, a.Day)
			}
			if a.ConnectorType != connectorOf(a.Tool) || !json.Valid(a.Args) || a.Approval.Wait < 30*time.Second ||
				!slices.Contains(sc.Approvers, a.Approval.Approver) {
				t.Fatalf("seed %d: malformed action %+v", seed, a)
			}
			if a.Outcome.Evidence == EvidenceContradicts && a.Incident != KindMismatch {
				t.Fatalf("seed %d: a MISMATCH outside the mismatch incident: %+v", seed, a)
			}
		}
	}
}

func TestVolume(t *testing.T) {
	sc, _, p := defaultPlan(t, 7, 7)
	base := map[string]int{}
	inc := map[string]int{}
	for _, a := range p {
		if a.Incident == "" {
			base[a.Agent]++
		} else {
			inc[a.Incident]++
		}
	}
	total := 0
	for _, ag := range sc.Agents {
		want := float64(ag.PerDay * 7)
		if got := float64(base[ag.Name]); math.Abs(got-want) > 0.15*want {
			t.Errorf("%s: %v baseline actions, want %v ± 15%%", ag.Name, got, want)
		}
		total += base[ag.Name]
	}
	if total < 5300 || total > 5900 {
		t.Errorf("baseline %d actions, want about 5,600", total)
	}
	if inc[KindMismatch] != 3 || inc[KindFirstUse] != 2 || inc[KindOffHours] != 12 || inc[KindNewUserRefs] != 25 {
		t.Errorf("incident counts: %v", inc)
	}
	if inc[KindSpike] < 40 || inc[KindBudgetPressure] < 30 {
		t.Errorf("spike or pressure too small: %v", inc)
	}
	if len(p) < 5600 || len(p) > 6600 {
		t.Errorf("%d actions in a week", len(p))
	}
	t.Logf("seed 7, 7 days: %d actions; baseline %v; incidents %v", len(p), base, inc)
}

func TestHoursAndOutcomes(t *testing.T) {
	sc, _, p := defaultPlan(t, 7, 7)
	loc := sc.Org.Location
	for _, ag := range sc.Agents {
		in, n := 0, 0
		var verified, unverifiable, failed float64
		for _, a := range p {
			if a.Agent != ag.Name || a.Incident != "" {
				continue
			}
			n++
			if h := a.At.In(loc).Hour(); h >= ag.HourFrom && h < ag.HourTo {
				in++
			}
			switch {
			case a.Outcome.Exec == ExecFailed:
				failed++
				if a.Outcome.ErrorCode != ErrUpstream {
					t.Fatalf("failed without upstream_error: %+v", a.Outcome)
				}
			case a.Outcome.Evidence == EvidenceGap:
				unverifiable++
				if a.Outcome.Exec == ExecUnknown && a.Outcome.ErrorCode != ErrTimeout {
					t.Fatalf("unknown without timeout: %+v", a.Outcome)
				}
			case a.Outcome.Evidence == EvidenceClean:
				verified++
			default:
				t.Fatalf("unexpected outcome %+v", a.Outcome)
			}
			if a.Tool == refundTool && a.Outcome.Exec == ExecSucceeded && (a.Outcome.CostMinor <= 0 || a.Outcome.Currency != "usd") {
				t.Fatalf("refund without a cost: %+v", a)
			}
			if a.Tool != refundTool && a.Outcome.CostMinor != 0 {
				t.Fatalf("cost on %s", a.Tool)
			}
		}
		if share := float64(in) / float64(n); math.Abs(share-inHours) > 0.01 {
			t.Errorf("%s: %.3f of calls inside its hours, want 0.97", ag.Name, share)
		}
		fn := float64(n)
		if math.Abs(verified/fn-ag.Outcomes.Verified) > 0.03 || math.Abs(unverifiable/fn-ag.Outcomes.Unverifiable) > 0.03 ||
			math.Abs(failed/fn-ag.Outcomes.Failed) > 0.03 {
			t.Errorf("%s: outcomes %.3f/%.3f/%.3f, want %+v", ag.Name, verified/fn, unverifiable/fn, failed/fn, ag.Outcomes)
		}
	}
}

func TestIncidentsLand(t *testing.T) {
	sc, w, p := defaultPlan(t, 7, 7)
	loc := sc.Org.Location
	for _, inc := range sc.Incidents {
		n := 0
		for _, a := range p {
			if a.Incident != inc.Kind || a.Agent != inc.Agent {
				continue
			}
			n++
			if !slices.Contains(inc.Days, a.Day) {
				t.Errorf("%s on day %d, want %v", inc.Kind, a.Day, inc.Days)
			}
			dayStart := w.Start.Add(time.Duration(a.Day-1) * 24 * time.Hour)
			if a.At.Before(dayStart) || !a.At.Before(dayStart.Add(24*time.Hour)) {
				t.Errorf("%s at %v outside day %d", inc.Kind, a.At, a.Day)
			}
			if inc.Kind != KindBudgetPressure {
				if h := a.At.In(loc).Hour(); h != inc.Hour {
					t.Errorf("%s at %v, org-local hour %d, want %d", inc.Kind, a.At.In(loc), h, inc.Hour)
				}
			}
			switch inc.Kind {
			case KindMismatch:
				if a.Outcome.Level != "L2" || a.Outcome.Evidence != EvidenceContradicts || a.Outcome.Detail["table"] != "public.inventory" ||
					!strings.HasPrefix(string(a.Args), `{"sql":"UPDATE public.inventory SET reserved = reserved - 1 WHERE sku = 'SKU-`) {
					t.Errorf("mismatch action: %+v", a)
				}
			case KindFirstUse:
				if a.Tool != inc.Tool || a.Outcome.Level != "L0" || a.Outcome.Evidence != EvidenceClean {
					t.Errorf("first_use action: %+v", a)
				}
			case KindSpike:
				if a.Tool != inc.Tool {
					t.Errorf("spike tool %s", a.Tool)
				}
			case KindNewUserRefs:
				if !strings.HasPrefix(a.UserRef, "cust_n") || len(a.UserRef) != len("cust_n0000") {
					t.Errorf("new ref %q", a.UserRef)
				}
			case KindBudgetPressure:
				if a.Tool != refundTool {
					t.Errorf("pressure tool %s", a.Tool)
				}
			}
		}
		if n == 0 {
			t.Errorf("%s produced nothing", inc.Kind)
		}
	}
	// The spike hour carries about factor × the agent's usual volume for that hour.
	spike := sc.Incidents[slices.IndexFunc(sc.Incidents, func(i Incident) bool { return i.Kind == KindSpike })]
	ag := sc.Agents[slices.IndexFunc(sc.Agents, func(a Agent) bool { return a.Name == spike.Agent })]
	inHour := 0
	for _, a := range p {
		if a.Agent == ag.Name && a.Day == spike.Days[0] && a.At.In(loc).Hour() == spike.Hour {
			inHour++
		}
	}
	want := spike.Factor * float64(ag.PerDay) * hourShare(&ag, spike.Hour)
	if math.Abs(float64(inHour)-want) > 0.25*want {
		t.Errorf("spike hour holds %d calls, want about %.0f", inHour, want)
	}
	// New refs are never in the pool, and every pool ref is.
	pool := map[string]bool{}
	for _, a := range p {
		if a.Agent == "support-bot" && a.Incident != KindNewUserRefs {
			pool[a.UserRef] = true
			if !strings.HasPrefix(a.UserRef, "cust_") || strings.HasPrefix(a.UserRef, "cust_n") {
				t.Fatalf("pool ref %q", a.UserRef)
			}
		}
	}
	if len(pool) > 400 {
		t.Errorf("%d distinct pool refs, pool is 400", len(pool))
	}
}

func TestMismatchOnlyFromIncident(t *testing.T) {
	_, _, p := defaultPlan(t, 7, 7)
	n := 0
	for _, a := range p {
		if a.Outcome.Evidence == EvidenceContradicts {
			n++
			if a.Incident != KindMismatch {
				t.Errorf("MISMATCH from %q", a.Incident)
			}
		}
	}
	if n != 3 {
		t.Fatalf("%d MISMATCH actions, want exactly 3", n)
	}
}

// TestBudgetPressure evaluates every refund under the compiled v1 policy and sums the
// refunds-per-day reservations by budget window (the org's calendar day).
func TestBudgetPressure(t *testing.T) {
	for _, seed := range []uint64{7, 11, 42} {
		sc, w, p := defaultPlan(t, seed, 7)
		c, diags := policy.Compile(simfiles.AcmeSimV1, policy.Options{})
		if c == nil {
			t.Fatal(diags)
		}
		b := c.Budgets[slices.IndexFunc(c.Budgets, func(b policy.Budget) bool { return b.ID == "refunds-per-day" })]
		limit := float64(b.MaxSpend.Amount)
		totals := map[time.Time]int64{}
		for _, a := range p {
			if a.Tool != refundTool {
				continue
			}
			d := policy.Evaluate(context.Background(), c, policyInput(sc, a), policy.EvalOptions{})
			for _, r := range d.Reservations {
				if r.Budget == b.ID {
					totals[r.WindowStart.UTC()] += r.Amount
				}
			}
		}
		g := &gen{sc: sc, w: w, loc: sc.Org.Location}
		pressure := map[time.Time]bool{}
		inc := sc.Incidents[slices.IndexFunc(sc.Incidents, func(i Incident) bool { return i.Kind == KindBudgetPressure })]
		for _, d := range inc.Days {
			_, _, ws := g.pressureRange(d)
			pressure[ws] = true
			got := float64(totals[ws]) / limit
			if math.Abs(got-inc.Target) > 0.02 {
				t.Errorf("seed %d: day %d (%s) reaches %.4f of the budget, want %.2f ± 0.02", seed, d, ws.In(sc.Org.Location).Format("2006-01-02"), got, inc.Target)
			}
		}
		for ws, v := range totals {
			if !pressure[ws] && float64(v) >= 0.8*limit {
				t.Errorf("seed %d: %s reaches %.3f of the budget; other days stay under 0.8", seed, ws.In(sc.Org.Location).Format("2006-01-02"), float64(v)/limit)
			}
		}
		if seed == 7 {
			t.Logf("v1 refunds-per-day limit %d minor; totals by day: %v", b.MaxSpend.Amount, totals)
		}
	}
}

func TestPlan30DaysFast(t *testing.T) {
	sc := Default()
	w, err := Span(sc, 30, testEnd)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	p, err := Plan(sc, 7, w)
	if err != nil {
		t.Fatal(err)
	}
	if el := time.Since(start); el > time.Second {
		t.Fatalf("30 days took %v, budget 1 s", el)
	}
	if len(p) < 24000 {
		t.Fatalf("%d actions in 30 days", len(p))
	}
}

// TestPolicyDecisions runs every planned action of seed 7 through the three policy versions.
func TestPolicyDecisions(t *testing.T) {
	sc, _, p := defaultPlan(t, 7, 7)
	compile := func(src []byte) *policy.Compiled {
		c, diags := policy.Compile(src, policy.Options{})
		if c == nil {
			t.Fatal(diags)
		}
		return c
	}
	v1, v2, v3 := compile(simfiles.AcmeSimV1), compile(simfiles.AcmeSimV2), compile(simfiles.AcmeSimV3)
	ctx := context.Background()
	counts := map[string]int{}
	for _, a := range p {
		in := policyInput(sc, a)
		d1 := policy.Evaluate(ctx, v1, in, policy.EvalOptions{})
		d2 := policy.Evaluate(ctx, v2, in, policy.EvalOptions{})
		d3 := policy.Evaluate(ctx, v3, in, policy.EvalOptions{})
		sql := ""
		var args map[string]any
		_ = json.Unmarshal(a.Args, &args)
		if s, ok := args["sql"].(string); ok {
			sql = s
		}
		switch {
		case a.Agent == "ops-agent" && a.Tool == "postgres.execute" && strings.HasPrefix(sql, "UPDATE public.orders"):
			expect(t, "v1 orders update", d1, policy.RequireApproval, "approve-order-updates")
			expect(t, "v3 orders update", d3, policy.Allow, "allow-order-status-updates")
			counts["orders"]++
		case a.Agent == "ops-agent" && a.Tool == "postgres.execute" && strings.HasPrefix(sql, "UPDATE public.inventory"):
			expect(t, "v1 inventory update", d1, policy.Allow, "allow-db-writes")
			counts["inventory"]++
		case a.Tool == "github.merge_pr":
			if d1.Outcome != policy.Deny || d1.Source != policy.SourceDefault {
				t.Errorf("merge_pr: %s by %s", d1.Outcome, d1.Source)
			}
			counts["merge"]++
		case a.Agent == "nightly-etl":
			expect(t, "nightly read", d1, policy.Allow, "allow-db-reads")
			counts["etl"]++
		case a.Tool == "slack.post_message":
			expect(t, "v1 slack", d1, policy.Allow, "allow-slack-posts")
			if args["channel"] == "#random" {
				expect(t, "v2 #random", d2, policy.Deny, "slack-allowlist")
				counts["random"]++
			} else {
				expect(t, "v2 slack", d2, policy.Allow, "allow-slack-posts")
			}
		case a.Tool == refundTool:
			amt := int64(args["amount"].(float64))
			switch {
			case amt > 10000:
				expect(t, "v1 large refund", d1, policy.Deny, "refund-limit")
				expect(t, "v2 large refund", d2, policy.Deny, "refund-limit")
			case amt > 3000:
				expect(t, "v1 refund", d1, policy.Allow, "allow-refunds")
				expect(t, "v2 refund over $30", d2, policy.RequireApproval, "refund-approval-over-30")
				counts["over30"]++
			default:
				expect(t, "v2 small refund", d2, policy.Allow, "allow-refunds")
			}
			if amt > 7500 && amt <= 10000 {
				expect(t, "v3 refund over $75", d3, policy.Deny, "refund-limit-strict")
				counts["v3stricter"]++
			}
		}
	}
	for _, k := range []string{"orders", "inventory", "merge", "etl", "random", "over30", "v3stricter"} {
		if counts[k] == 0 {
			t.Errorf("no %s actions in the plan", k)
		}
	}
	t.Logf("checked: %v", counts)
}

func expect(t *testing.T, what string, d policy.Decision, o policy.Outcome, rule string) {
	t.Helper()
	if d.Outcome != o || d.RuleID != rule {
		t.Fatalf("%s: %s by %q, want %s by %q (%s)", what, d.Outcome, d.RuleID, o, rule, d.Reason)
	}
}

func BenchmarkPlan30Days(b *testing.B) {
	sc := Default()
	w, _ := Span(sc, 30, testEnd)
	for b.Loop() {
		if _, err := Plan(sc, 7, w); err != nil {
			b.Fatal(err)
		}
	}
}
