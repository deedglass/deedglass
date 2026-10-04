package policy

import (
	"bytes"
	"errors"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The patcher goldens (S28 §6): go test ./internal/policy -run TestPatchGoldens -update rewrites
// testdata/patch/*.golden (review the diff: every untouched line must stay byte-identical).

func readPatchFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "patch", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func newRule(id string, d Outcome) RuleSpec {
	return RuleSpec{Comment: []string{"Accepted from Deedglass Insights suggestion sug_test · evidence sha256:7a1c…e9d0"}, ID: id,
		Description: "People approved 401 of 404 of these between 2026-09-25 and 2026-10-09.",
		Match:       MatchSpec{Tool: []string{"postgres.execute"}, Agent: []string{"ops-agent"}},
		When:        `sql.kind == "update" && sql.tables == ["public.orders"] && sql.statements == 1 && sql.has_where`,
		Decision:    d, Verify: L2}
}

func TestPatchGoldens(t *testing.T) {
	money := Money{Amount: 60000, Currency: "usd"}
	calls := int64(240)
	cases := []struct {
		name, file string
		ops        []PatchOp
	}{
		{"insert-middle", "comments.yaml", []PatchOp{InsertRuleBefore{RuleID: "approve-orders", Rule: newRule("allow-order-updates", Allow)}}},
		{"insert-top", "comments.yaml", []PatchOp{InsertRuleBefore{RuleID: "allow-reads", Rule: RuleSpec{ID: "deny-drops",
			Match: MatchSpec{Tool: []string{"postgres.*"}}, When: `"drop" in sql.kinds`, Decision: Deny, Reason: "DROP never runs for agents."}}}},
		{"insert-before-inline-comment", "comments.yaml", []PatchOp{InsertRuleBefore{RuleID: "allow-writes", Rule: RuleSpec{ID: "approve-inventory",
			Match: MatchSpec{Tool: []string{"postgres.execute"}}, When: `"public.inventory" in sql.tables`, Decision: RequireApproval,
			Reason: "Inventory writes wait for a person after 3 mismatches. An admin can approve in Console › Approvals.", Verify: L1}}}},
		{"append-end", "comments.yaml", []PatchOp{AppendRule{Rule: RuleSpec{ID: "explicit-github-merge-pr", Match: MatchSpec{Tool: []string{"github.merge_pr"}},
			Decision: Deny, Reason: "github.merge_pr isn't allowed for agents. Edit this reason in Console › Policies to say why."}}}},
		{"set-verify-replace", "comments.yaml", []PatchOp{SetRuleField{RuleID: "allow-writes", Key: "verify", Value: L2}}},
		{"set-verify-add", "comments.yaml", []PatchOp{SetRuleField{RuleID: "allow-reads", Key: "verify", Value: "L1"}}},
		{"budget-max-spend", "comments.yaml", []PatchOp{SetBudgetLimit{BudgetID: "spend-per-day", MaxSpend: &money}}},
		{"budget-max-calls", "comments.yaml", []PatchOp{SetBudgetLimit{BudgetID: "calls-per-hour", MaxCalls: &calls}}},
		{"tight-insert", "tight.yaml", []PatchOp{InsertRuleBefore{RuleID: "allow-posts", Rule: newRule("allow-order-updates", Allow)}}},
		{"tight-append", "tight.yaml", []PatchOp{AppendRule{Rule: newRule("allow-order-updates", Allow)}}},
		{"four-space-insert", "four-space.yaml", []PatchOp{InsertRuleBefore{RuleID: "allow-posts", Rule: newRule("allow-order-updates", Allow)}}},
		{"crlf-insert", "crlf.yaml", []PatchOp{InsertRuleBefore{RuleID: "allow-reads", Rule: newRule("allow-order-updates", Allow)}}},
		{"several", "comments.yaml", []PatchOp{
			InsertRuleBefore{RuleID: "approve-orders", Rule: newRule("allow-order-updates", Allow)},
			SetRuleField{RuleID: "allow-writes", Key: "verify", Value: L2},
			SetBudgetLimit{BudgetID: "spend-per-day", MaxSpend: &money},
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			src := readPatchFixture(t, c.file)
			base, diags := Compile(src, Options{})
			if base == nil {
				t.Fatal(diags)
			}
			out, err := Patch(src, c.ops...)
			if err != nil {
				t.Fatal(err)
			}
			cand, diags := Compile(out, Options{})
			if cand == nil {
				t.Fatalf("the patched policy doesn't compile: %v\n%s", diags, out)
			}
			if err := CheckPatch(base, cand, c.ops...); err != nil {
				t.Fatalf("CheckPatch: %v\n%s", err, out)
			}
			checkUntouched(t, src, out)
			if bytes.Contains(src, []byte("\r\n")) && bytes.Count(out, []byte("\r\n")) != bytes.Count(out, []byte("\n")) {
				t.Fatal("a CRLF file must stay CRLF on every line")
			}
			golden := filepath.Join("testdata", "patch", c.name+".golden")
			if *update {
				if err := os.WriteFile(golden, out, 0o644); err != nil {
					t.Fatal(err)
				}
				return
			}
			want, err := os.ReadFile(golden)
			if err != nil {
				t.Fatalf("%v (run with -update)", err)
			}
			if !bytes.Equal(out, want) {
				t.Fatalf("patch differs from %s:\n%s", golden, Diff(want, out, 2).Text)
			}
		})
	}
}

// checkUntouched: every line of src appears in out, in order (the patch only inserts lines or
// changes a value in place on lines it names).
func checkUntouched(t *testing.T, src, out []byte) {
	t.Helper()
	u := Diff(src, out, 0)
	for _, h := range u.Hunks {
		for _, l := range h.Lines {
			if l.Op == "-" {
				// A replaced value: only verify, max_calls and max_spend lines may change.
				k := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(l.Text), "- "))
				if !strings.HasPrefix(k, "verify:") && !strings.HasPrefix(k, "max_calls:") && !strings.HasPrefix(k, "max_spend:") {
					t.Fatalf("the patch removed or changed a line it doesn't own: %q", l.Text)
				}
			}
		}
	}
}

func TestPatchRefusals(t *testing.T) {
	money := Money{Amount: 1, Currency: "usd"}
	zero := int64(0)
	comments := readPatchFixture(t, "comments.yaml")
	cases := []struct {
		name string
		src  []byte
		op   PatchOp
		want string
	}{
		{"mixed line endings", readPatchFixture(t, "mixed.yaml"), AppendRule{Rule: newRule("x", Allow)}, "mixes LF and CRLF"},
		{"flow rules list", readPatchFixture(t, "flow-rules.yaml"), AppendRule{Rule: newRule("x", Allow)}, "flow style"},
		{"unknown rule", comments, InsertRuleBefore{RuleID: "nope", Rule: newRule("x", Allow)}, `no rule "nope"`},
		{"unknown budget", comments, SetBudgetLimit{BudgetID: "nope", MaxSpend: &money}, `no budget "nope"`},
		{"both limits", comments, SetBudgetLimit{BudgetID: "spend-per-day"}, "exactly one"},
		{"zero calls", comments, SetBudgetLimit{BudgetID: "calls-per-hour", MaxCalls: &zero}, "at least 1"},
		{"bad key", comments, SetRuleField{RuleID: "allow-reads", Key: "decision", Value: "allow"}, "only verify"},
		{"bad level", comments, SetRuleField{RuleID: "allow-reads", Key: "verify", Value: "L9"}, "L0, L1 or L2"},
		{"bad id", comments, AppendRule{Rule: RuleSpec{ID: "Bad Id", Decision: Allow}}, "isn't a valid id"},
		{"no decision", comments, AppendRule{Rule: RuleSpec{ID: "ok"}}, "no decision"},
		{"newline in when", comments, AppendRule{Rule: RuleSpec{ID: "ok", Decision: Allow, When: "a\nb"}}, "line break"},
		{"newline in comment", comments, AppendRule{Rule: RuleSpec{ID: "ok", Decision: Allow, Comment: []string{"a\nb"}}}, "line break"},
		{"not yaml", []byte("rules: [\n"), AppendRule{Rule: newRule("x", Allow)}, "valid YAML"},
		{"not a mapping", []byte("- a\n- b\n"), AppendRule{Rule: newRule("x", Allow)}, "one YAML mapping"},
		{"no rules", []byte("name: x\n"), AppendRule{Rule: newRule("x", Allow)}, "no rules list"},
		{"empty rules", []byte("name: x\nrules:\n"), AppendRule{Rule: newRule("x", Allow)}, "empty"},
		{"rules not a list", []byte("name: x\nrules: 3\n"), AppendRule{Rule: newRule("x", Allow)}, "isn't a list"},
		{"too large", bytes.Repeat([]byte("#"), MaxSourceBytes+1), AppendRule{Rule: newRule("x", Allow)}, "larger than"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := Patch(c.src, c.op)
			var pe *PatchError
			if err == nil || !errors.As(err, &pe) || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("got %v, want a PatchError containing %q", err, c.want)
			}
		})
	}
}

func TestCheckPatchCatchesDrift(t *testing.T) {
	src := readPatchFixture(t, "comments.yaml")
	base, _ := Compile(src, Options{})
	// A candidate that also changed a reason elsewhere must fail the structural check.
	op := InsertRuleBefore{RuleID: "approve-orders", Rule: newRule("allow-order-updates", Allow)}
	out, err := Patch(src, op)
	if err != nil {
		t.Fatal(err)
	}
	out = bytes.Replace(out, []byte("Order updates wait for a person."), []byte("Order updates wait for someone."), 1)
	cand, diags := Compile(out, Options{})
	if cand == nil {
		t.Fatal(diags)
	}
	if err := CheckPatch(base, cand, op); !errors.Is(err, ErrPatchChanged) {
		t.Fatalf("CheckPatch = %v, want ErrPatchChanged", err)
	}
	if err := CheckPatch(nil, cand); !errors.Is(err, ErrPatchChanged) {
		t.Fatal("nil base")
	}
	if err := CheckPatch(base, cand, InsertRuleBefore{RuleID: "nope", Rule: op.Rule}); err == nil {
		t.Fatal("unknown target")
	}
}

// TestPatchProperty: inserting a rule before a random rule of a random starter, or appending one,
// always compiles, keeps every base rule in order with the insert where it was put, and changes
// no line of the original.
func TestPatchProperty(t *testing.T) {
	var files []string
	for _, dir := range []string{"../../examples/policies", "../../examples/sim", "testdata/patch"} {
		m, _ := filepath.Glob(filepath.Join(dir, "*.yaml"))
		for _, f := range m {
			if strings.HasSuffix(f, ".cases.yaml") || strings.Contains(f, "mixed") || strings.Contains(f, "flow-rules") || strings.HasSuffix(f, "acme.yaml") {
				continue
			}
			files = append(files, f)
		}
	}
	if len(files) < 8 {
		t.Fatalf("only %d fixture policies", len(files))
	}
	r := rand.New(rand.NewPCG(28, 28))
	decisions := []Outcome{Allow, Deny, RequireApproval}
	n := 0
	for i := 0; i < 400; i++ {
		f := files[r.IntN(len(files))]
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		base, _ := Compile(src, Options{})
		if base == nil || len(base.Rules) == 0 {
			continue
		}
		spec := RuleSpec{ID: UniqueRuleID(base, fmt.Sprintf("prop-%d", i)), Match: MatchSpec{Tool: []string{"fake.echo"}},
			When: fmt.Sprintf("size(args) > %d", r.IntN(5)), Decision: decisions[r.IntN(3)], Reason: "A property test rule."}
		if spec.Decision == Allow {
			spec.Reason = ""
		}
		var op PatchOp = AppendRule{Rule: spec}
		if r.IntN(3) > 0 {
			op = InsertRuleBefore{RuleID: base.Rules[r.IntN(len(base.Rules))].ID, Rule: spec}
		}
		out, err := Patch(src, op)
		if err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		cand, diags := Compile(out, Options{})
		if cand == nil {
			t.Fatalf("%s: %v\n%s", f, diags, out)
		}
		if err := CheckPatch(base, cand, op); err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		u := Diff(src, out, 0)
		if u.Removed != 0 || u.Added == 0 {
			t.Fatalf("%s: +%d −%d", f, u.Added, u.Removed)
		}
		n++
	}
	if n < 300 {
		t.Fatalf("only %d property cases ran", n)
	}
}

func TestUniqueRuleID(t *testing.T) {
	base, _ := Compile(readPatchFixture(t, "comments.yaml"), Options{})
	if got := UniqueRuleID(base, "allow-reads"); got != "allow-reads-2" {
		t.Fatal(got)
	}
	if got := UniqueRuleID(base, "fresh"); got != "fresh" {
		t.Fatal(got)
	}
	if ids := RuleIDs(base); len(ids) != 3 || ids[0] != "allow-reads" {
		t.Fatal(ids)
	}
}

func TestRenderScalars(t *testing.T) {
	for in, want := range map[string]string{
		"Plain words.":      "Plain words.",
		"yes":               `"yes"`,
		"a: b":              `"a: b"`,
		"has # hash":        `"has # hash"`,
		"Refunds over $30.": "Refunds over $30.",
		"'quoted'":          `"'quoted'"`,
		"Console › Agents.": `"Console › Agents."`,
	} {
		if got := yamlString(in); got != want {
			t.Errorf("yamlString(%q) = %s, want %s", in, got, want)
		}
	}
	if flowItem("role:admin") != "role:admin" || flowItem("*") != `"*"` || flowItem("a b") != `"a b"` {
		t.Fatal("flowItem")
	}
}
