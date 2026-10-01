package policy

import (
	"reflect"
	"strings"
	"testing"
)

// head is a valid policy prefix; each case appends one rule (or replaces everything).
const head = "deedglass: policy/v1\nname: acme-test\ndefaults:\n  decision: deny\n  reason: No rule allows this action.\n"

func codesOf(ds []Diagnostic) []string {
	var out []string
	for _, d := range ds {
		out = append(out, d.Code)
	}
	return out
}

func TestCompileCases(t *testing.T) {
	rule := func(body string) string { return head + "rules:\n  - id: rule-one\n" + body }
	when := func(w string) string { return rule("    decision: allow\n    when: " + w + "\n") }
	cases := []struct {
		name, src string
		want      []string
	}{
		// CEL.
		{"and", when("'sql.multi and sql.writes'"), []string{"cel_syntax"}},
		{"or", when("'sql.multi or sql.writes'"), []string{"cel_syntax"}},
		{"single equals", when("'args.x = 1'"), []string{"cel_syntax"}},
		{"unexpected", when("'args.x == )'"), []string{"cel_syntax"}},
		{"now", when("'now() > time.now'"), []string{"cel_undeclared"}},
		{"nested no field", when("'tool.name.size() > 1 && agent.statsu == \"x\"'"), []string{"cel_no_field"}},
		{"overload", when("'sql.kind + 1 == 2'"), []string{"cel_type"}},
		{"overload same", when("'sql.tables < sql.kinds'"), []string{"cel_type"}},
		{"size bool", when("'size(true) > 0'"), []string{"cel_type"}},
		{"not bool string", when(`'"yes"'`), []string{"cel_not_bool"}},
		{"not bool int", when("'1'"), []string{"cel_not_bool"}},
		{"not bool list", when("'[true]'"), []string{"cel_not_bool"}},
		{"not bool map", when("'{\"a\": true}'"), []string{"cel_not_bool"}},
		{"not bool time", when("time.now"), []string{"cel_not_bool"}},
		{"not bool duration", when("'duration(\"1h\")'"), []string{"cel_not_bool"}},
		{"not bool null", when("'null'"), []string{"cel_not_bool"}},
		{"not bool object", when("sql"), []string{"cel_not_bool"}},
		{"bad duration literal", when("'duration(\"1x\") > duration(\"1h\")'"), []string{"cel_type"}},
		{"mixed list", when("'args.x in [1, \"a\"]'"), []string{"cel_type"}},
		{"glob bad pattern", when("'glob(tool.name, \"a b\")'"), []string{"invalid_glob"}},
		{"minor bad", when("'args.x > minor(\"5.555 usd\")'"), []string{"money_precision"}},
		{"minor var", when("'args.x > minor(args.y)'"), []string{"cel_constant_required"}},
		{"regex long", when("'tool.name.matches(\"" + strings.Repeat("a", 257) + "\")'"), []string{"cel_regex"}},
		{"regex global", when("'matches(tool.name, \"(\")'"), []string{"cel_regex"}},
		{"nesting", when("'sql.tables.all(a, sql.tables.all(b, sql.tables.all(c, a == b)))'"), []string{"cel_nesting"}},
		{"cost", when("'args.a.all(x, args.b.all(y, x == y))'"), []string{"cel_cost"}},
		{"too long", when("'" + strings.Repeat("true && ", 600) + "true'"), []string{"cel_too_long"}},
		{"deep", when("'" + strings.Repeat("(", 70) + "true" + strings.Repeat(")", 70) + "'"), []string{"cel_syntax"}},
		{"empty when", when("''"), []string{"wrong_type"}},
		{"when list", when("[a, b]"), []string{"wrong_type"}},
		{"bidi in when", when("\"args.x == '\\u202e'\""), []string{"control_character"}},
		{"quoted number reversed", when(`'"10" < args.n'`), []string{"cel_type"}},
		{"quoted number vs string ok", when(`'sql.kind >= "5"'`), nil},
		{"string compare ok", when(`'args.name == "5000"'`), nil},
		{"glob ok", when(`'glob(tool.name, "stripe.*") && sql.tables.exists(t, glob(t, "public.*"))'`), nil},
		{"has guard ok", when("'has(args.amount) && args.amount > 1.5'"), nil},
		{"strings ext ok", when("'tool.name.lowerAscii().startsWith(\"stripe\") && \"a,b\".split(\",\").size() == 2'"), nil},
		{"sets ext ok", when("'sets.contains(sql.kinds, [\"select\"])'"), nil},
		{"lists ext ok", when("'sql.tables.slice(0, 1).size() <= 1 && lists.range(3).size() == 3'"), nil},
		{"usage ok", when("'usage.budgets[\"agent-rate\"] < 10 && usage.calls_today < 100 && !anomaly.active && user.ref != \"\" && session.ref != \"\" && org.timezone == \"Asia/Kolkata\"'"), nil},
		{"measure bool", head + "budgets:\n  - id: spend\n    window: day\n    max_spend: \"5.00 usd\"\n    measure: 'true'\n", []string{"cel_not_int"}},
		{"measure cost", head + "budgets:\n  - id: spend\n    window: day\n    max_spend: \"5.00 usd\"\n    measure: 'args.a.map(x, args.b.filter(y, y == x).size()).size()'\n", []string{"cel_cost"}},
		{"measure without spend", head + "budgets:\n  - id: spend\n    window: day\n    max_calls: 5\n    measure: args.amount\n", []string{"conflicting_keys"}},
		{"budget no limit", head + "budgets:\n  - id: spend\n    window: day\n", []string{"missing_key"}},
		{"budget no id no window", head + "budgets:\n  - max_calls: 5\n", []string{"missing_key", "missing_key"}},
		{"budget zero", head + "budgets:\n  - id: spend\n    window: day\n    max_spend: \"0.00 usd\"\n", []string{"invalid_money"}},
		{"budget calls range", head + "budgets:\n  - id: spend\n    window: day\n    max_calls: 0\n", []string{"out_of_range"}},
		{"budget calls string", head + "budgets:\n  - id: spend\n    window: day\n    max_calls: \"5\"\n", []string{"wrong_type"}},
		{"budget per", head + "budgets:\n  - id: spend\n    window: day\n    per: team\n    max_calls: 5\n", []string{"invalid_value"}},
		{"budget typo", head + "budgets:\n  - id: spend\n    windw: day\n    max_calls: 5\n", []string{"unknown_key"}},
		{"budget not mapping", head + "budgets:\n  - spend\n", []string{"wrong_type"}},
		{"budgets mapping", head + "budgets:\n  spend: 5\n", []string{"wrong_type"}},
		{"money spend list", head + "budgets:\n  - id: spend\n    window: day\n    max_spend: [1]\n", []string{"wrong_type"}},
		// Structure and values.
		{"rule not mapping", head + "rules:\n  - allow\n", []string{"wrong_type"}},
		{"rule no id", head + "rules:\n  - decision: allow\n", []string{"missing_key"}},
		{"moved key", rule("    decision: require_approval\n    reason: Waits.\n    approvers: [role:admin]\n"), []string{"unknown_key"}},
		{"decision list", rule("    decision: [allow]\n"), []string{"wrong_type"}},
		{"decision typo", rule("    decision: alow\n"), []string{"invalid_decision"}},
		{"decision unknown", rule("    decision: maybe\n"), []string{"invalid_decision"}},
		{"empty reason", rule("    decision: deny\n    reason: \"\"\n"), []string{"reason_missing"}},
		{"approval not mapping", rule("    decision: require_approval\n    reason: Waits.\n    approval: admins\n"), []string{"wrong_type"}},
		{"approval typo", rule("    decision: require_approval\n    reason: Waits.\n    approval: {approver: [role:admin]}\n"), []string{"unknown_key"}},
		{"approver email", rule("    decision: require_approval\n    reason: Waits.\n    approval: {approvers: [priya@acme.com]}\n"), []string{"invalid_approvers"}},
		{"approver bad user", rule("    decision: require_approval\n    reason: Waits.\n    approval: {approvers: [\"user:priya\"]}\n"), []string{"invalid_approvers"}},
		{"approver role", rule("    decision: require_approval\n    reason: Waits.\n    approval: {approvers: [\"role:member\"]}\n"), []string{"invalid_approvers"}},
		{"approvers null", rule("    decision: require_approval\n    reason: Waits.\n    approval: {approvers: }\n"), []string{"wrong_type"}},
		{"expires list", rule("    decision: require_approval\n    reason: Waits.\n    approval: {expires_in: [1m]}\n"), []string{"wrong_type"}},
		{"approval ok defaults", rule("    decision: require_approval\n    reason: Waits.\n    approval: {approvers: [\"user:Priya@Acme.com\", \"user:sam@acme.com\"], required: 2, expires_in: 90s}\n"), nil},
		{"defaults approval unsatisfiable", strings.Replace(head, "  reason:", "  approval: {approvers: [\"user:priya@acme.com\"], required: 2}\n  reason:", 1), []string{"approvals_unsatisfiable"}},
		{"defaults typo", strings.Replace(head, "  decision:", "  decison:", 1), []string{"unknown_key"}},
		{"defaults list", "deedglass: policy/v1\nname: acme-test\ndefaults: [deny]\n", []string{"wrong_type"}},
		{"defaults no reason", "deedglass: policy/v1\nname: acme-test\ndefaults: {decision: require_approval}\n", []string{"reason_missing"}},
		{"defaults no decision", "deedglass: policy/v1\nname: acme-test\ndefaults: {reason: No.}\n", []string{"missing_key"}},
		{"defaults notify verify", strings.Replace(head, "  reason:", "  notify: [email, slack, pager]\n  verify: L3\n  reason:", 1), []string{"invalid_value", "invalid_value"}},
		{"no defaults", "deedglass: policy/v1\nname: acme-test\n", []string{"missing_key"}},
		{"no name", "deedglass: policy/v1\ndefaults: {decision: deny, reason: No.}\n", []string{"missing_key"}},
		{"name int", "deedglass: policy/v1\nname: [x]\ndefaults: {decision: deny, reason: No.}\n", []string{"wrong_type"}},
		{"marker list", "deedglass: [policy/v1]\nname: acme-test\ndefaults: {decision: deny, reason: No.}\n", []string{"wrong_type"}},
		{"marker other", "deedglass: config/v1\nname: acme-test\ndefaults: {decision: deny, reason: No.}\n", []string{"unsupported_format"}},
		{"marker typo", "deedglas: policy/v1\nname: acme-test\ndefaults: {decision: deny, reason: No.}\n", []string{"unknown_key"}},
		{"root list", "- deedglass: policy/v1\n", []string{"wrong_type"}},
		{"description list", strings.Replace(head, "name: acme-test\n", "name: acme-test\ndescription: [a]\n", 1), []string{"wrong_type"}},
		{"description bidi", strings.Replace(head, "name: acme-test\n", "name: acme-test\ndescription: \"a\\u200bb\"\n", 1), []string{"control_character"}},
		{"name bidi", strings.Replace(head, "name: acme-test\n", "name: \"acme\\u202e\"\n", 1), []string{"control_character"}},
		{"match not mapping", rule("    decision: allow\n    match: stripe.*\n"), []string{"wrong_type"}},
		{"match typo", rule("    decision: allow\n    match: {tools: x}\n"), []string{"unknown_key"}},
		{"match empty field", rule("    decision: allow\n    match: {tool: }\n"), []string{"wrong_type"}},
		{"match list of lists", rule("    decision: allow\n    match: {tool: [[a]]}\n"), []string{"wrong_type"}},
		{"match map value", rule("    decision: allow\n    match: {tool: {a: b}}\n"), []string{"wrong_type"}},
		{"glob empty", rule("    decision: allow\n    match: {tool: \"\"}\n"), []string{"invalid_glob"}},
		{"glob long", rule("    decision: allow\n    match: {tool: " + strings.Repeat("a", 201) + "}\n"), []string{"invalid_glob"}},
		{"glob bidi", rule("    decision: allow\n    match: {tool: \"a\\u202eb\"}\n"), []string{"control_character"}},
		{"agent id bad", rule("    decision: allow\n    match: {agent: agt_123}\n"), []string{"invalid_glob"}},
		{"connector glob", rule("    decision: allow\n    match: {connector_type: \"mcp*\"}\n"), nil},
		{"side effect typo", rule("    decision: allow\n    match: {side_effect: wirte}\n"), []string{"invalid_side_effect"}},
		{"labels list", rule("    decision: allow\n    match: {labels: [env]}\n"), []string{"wrong_type"}},
		{"label value list", rule("    decision: allow\n    match: {labels: {env: [prod]}}\n"), []string{"wrong_type"}},
		{"label value long", rule("    decision: allow\n    match: {labels: {env: " + strings.Repeat("p", 129) + "}}\n"), []string{"invalid_label"}},
		{"label value bidi", rule("    decision: allow\n    match: {labels: {env: \"p\\u202e\"}}\n"), []string{"invalid_label"}},
		{"labels too many", rule("    decision: allow\n    match:\n      labels: {" + labelPairs(33) + "}\n"), []string{"too_many_values"}},
		{"label types ok", rule("    decision: allow\n    match: {labels: {tier: 1, beta: true}}\n"), nil},
		{"notify on allow", rule("    decision: allow\n    notify: [slack]\n"), []string{"ignored_field"}},
		{"verify on deny", rule("    decision: deny\n    reason: No.\n    verify: L2\n"), []string{"ignored_field"}},
		{"verify bad", rule("    decision: allow\n    verify: L9\n"), []string{"invalid_value"}},
		{"notify typo", rule("    decision: deny\n    reason: No.\n    notify: [emial]\n"), []string{"invalid_value"}},
		{"secret aws", strings.Replace(head, "name: acme-test\n", "name: acme-test\n# "+"AKIA"+"ABCDEFGHIJKLMNOP\n", 1), []string{"secret_in_policy"}},
		{"unreachable labels", rule("    decision: allow\n    match: {labels: {env: prod}}\n  - id: rule-two\n    decision: deny\n    reason: No.\n    match: {labels: {env: prod, team: a}, side_effect: [read]}\n"), []string{"rule_unreachable"}},
		{"unreachable side effects", rule("    decision: allow\n    match: {side_effect: [read, write]}\n  - id: rule-two\n    decision: deny\n    reason: No.\n    match: {side_effect: [read]}\n"), []string{"rule_unreachable"}},
		{"reachable side effects", rule("    decision: allow\n    match: {side_effect: [read]}\n  - id: rule-two\n    decision: deny\n    reason: No.\n"), nil},
		{"unreachable agents", rule("    decision: allow\n    match: {agent: [support-*, agt_01k67c04kxevyex3pjyqe0t45d]}\n  - id: rule-two\n    decision: deny\n    reason: No.\n    match: {agent: [support-bot, agt_01k67c04kxevyex3pjyqe0t45d]}\n"), []string{"rule_unreachable"}},
		{"reachable agents", rule("    decision: allow\n    match: {agent: [support-*]}\n  - id: rule-two\n    decision: deny\n    reason: No.\n    match: {agent: [agt_01k67c04kxevyex3pjyqe0t45d]}\n"), nil},
		{"reachable labels", rule("    decision: allow\n    match: {labels: {env: prod}}\n  - id: rule-two\n    decision: deny\n    reason: No.\n"), nil},
		{"voice lower", head + "rules:\n  - id: rule-one\n    decision: deny\n    reason: never runs.\n", nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, ds := Compile([]byte(c.src), Options{File: "case.yaml"})
			if got := codesOf(ds); !reflect.DeepEqual(got, c.want) {
				t.Errorf("got %v, want %v\n%+v", got, c.want, ds)
			}
			for _, d := range ds {
				if d.Message == "" || !strings.HasSuffix(d.Message, ".") || d.DocURL != DocBase+d.Code {
					t.Errorf("bad diagnostic shape: %+v", d)
				}
				if strings.ContainsAny(d.Message+d.Hint, "—–!") && d.Code != "cel_syntax" && !strings.Contains(d.Message+d.Hint, "`!") && !strings.Contains(d.Message+d.Hint, "!=") {
					t.Errorf("dash or exclamation in copy: %+v", d)
				}
			}
		})
	}
}

func labelPairs(n int) string {
	var xs []string
	for i := 0; i < n; i++ {
		xs = append(xs, "k"+itoa(i)+": v")
	}
	return strings.Join(xs, ", ")
}

func TestVoice(t *testing.T) {
	for reason, want := range map[string]string{
		"never runs.":                          "starts with a lowercase letter",
		"It never runs":                        "ends without a period",
		"Blocked!":                             "exclamation mark",
		"This is a " + "seam" + "less refund.": "which Deedglass copy never uses", // a banned word, built at run time
		"It never runs.":                       "",
	} {
		src := head + "rules:\n  - id: rule-one\n    decision: deny\n    reason: " + reason + "\n"
		_, ds := Compile([]byte(src), Options{Voice: true})
		got := ""
		for _, d := range ds {
			if d.Code == "reason_style" {
				got += d.Message
			}
		}
		if (want == "") != (got == "") || !strings.Contains(got, want) {
			t.Errorf("%q: got %q, want %q", reason, got, want)
		}
	}
}

// TestPrograms runs the compiled programs once, so the bindings (glob, minor) and the
// estimator's view of real values are exercised. S11 owns evaluation; this only checks wiring.
func TestPrograms(t *testing.T) {
	src := head + "budgets:\n  - id: spend\n    window: day\n    max_spend: \"5.00 usd\"\n    measure: 'has(args.amount) ? args.amount : 0'\n" +
		"rules:\n  - id: rule-one\n    decision: allow\n    when: 'glob(tool.name, \"stripe.*\") && args.amount <= minor(\"50.00 usd\")'\n"
	c, ds := Compile([]byte(src), Options{})
	if c == nil {
		t.Fatal(ds)
	}
	act := map[string]any{"tool": ToolFacts{Name: "stripe.create_refund"}, "args": map[string]any{"amount": int64(4000)},
		"request": RequestFacts{}, "sql": SQLFacts{}, "agent": AgentFacts{}, "user": UserFacts{}, "session": SessionFacts{},
		"org": OrgFacts{}, "time": TimeFacts{}, "usage": UsageFacts{}, "anomaly": AnomalyFacts{}}
	out, _, err := c.Rules[0].When.Eval(act)
	if err != nil || out.Value() != true {
		t.Fatalf("when: %v %v", out, err)
	}
	m, _, err := c.Budgets[0].Measure.Eval(act)
	if err != nil || m.Value() != int64(4000) {
		t.Fatalf("measure: %v %v", m, err)
	}
	if r := celGlob(stringVal("x"), intVal(1)); r.Type().TypeName() != "error" {
		t.Error("glob type check")
	}
	if r := celMinor(intVal(1)); r.Type().TypeName() != "error" {
		t.Error("minor type check")
	}
	if r := celMinor(stringVal("5 xyz")); r.Type().TypeName() != "error" {
		t.Error("minor bad money")
	}
	if r := celGlob(stringVal("x"), stringVal("a b")); r.Type().TypeName() != "error" {
		t.Error("glob bad pattern")
	}
	if c.Rules[0].Match.String() != "" || (&Matcher{Tool: []Glob{mustGlob("a*")}}).String() != "a*" {
		t.Error("Matcher.String")
	}
}

func mustGlob(p string) Glob { g, _ := CompileGlob(p); return g }

func stringVal(s string) refVal { return celString(s) }
func intVal(i int64) refVal     { return celInt(i) }
