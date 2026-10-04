package sim

import (
	"bytes"
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	simfiles "github.com/deedglass/deedglass/examples/sim"
	"github.com/deedglass/deedglass/internal/policy"
)

// base is a small valid scenario; each invalid case changes one line. Line numbers matter.
const base = `v: 1
org: { slug: acme-sim, name: "Acme (simulated)", timezone: Asia/Kolkata }
policies: { enforce: v1.yaml, shadow: { file: v2.yaml, from_day: 3 } }
approvers: [priya@example.test]
agents:
  - name: support-bot
    hours: "09-19"
    per_day: 220
    user_refs: { pool: 400, prefix: cust_ }
    tools:
      - { tool: stripe.create_refund, weight: 30, args: { amount: { lognormal: { median: 2400, sigma: 0.8 } }, currency: usd } }
      - { tool: postgres.query, weight: 20, sql: ["SELECT id FROM public.orders WHERE id = {id}"] }
    outcomes: { verified: 0.92, unverifiable: 0.05, failed: 0.03 }
  - name: ops-agent
    hours: "10-18"
    per_day: 120
    tools: [ { tool: postgres.execute, weight: 50 } ]
    approvals: { approve: 0.99, wait: { median: 20m, p95: 2h } }
incidents:
  - { day: 5, hour: 11, agent: ops-agent, kind: mismatch, tool: postgres.execute, table: public.inventory, count: 3 }
  - { days: [2, 4], agent: support-bot, kind: budget_pressure, budget: refunds-per-day, target: 0.95 }
`

func testFiles(name string) ([]byte, error) {
	switch name {
	case "v1.yaml":
		return simfiles.AcmeSimV1, nil
	case "v2.yaml":
		return simfiles.AcmeSimV2, nil
	case "broken.yaml":
		return []byte("deedglass: policy/v1\nname: acme-sim\n"), nil
	}
	return nil, fs.ErrNotExist
}

func TestParseBase(t *testing.T) {
	sc, err := Parse("acme.yaml", []byte(base), testFiles)
	if err != nil {
		t.Fatal(err)
	}
	if len(sc.Agents) != 2 || sc.Agents[1].Outcomes != DefaultOutcomes || sc.Agents[1].Levels != DefaultLevels ||
		sc.Agents[0].Approvals != DefaultApprovals || sc.Agents[1].Approvals.WaitP95 != 2*time.Hour {
		t.Fatalf("defaults not applied: %+v", sc.Agents)
	}
	if sc.Policies.ShadowFromDay != 3 || !bytes.Equal(sc.Policies.Shadow, simfiles.AcmeSimV2) || sc.Incidents[1].Days[1] != 4 {
		t.Fatalf("policies or incidents: %+v %+v", sc.Policies.ShadowFile, sc.Incidents)
	}
}

func TestParseInvalid(t *testing.T) {
	cases := []struct {
		name, old, new, want string
	}{
		{"unknown key with a suggestion", "    per_day: 120", "    per_hour: 120",
			`acme.yaml:16: unknown key "per_hour" in agents[1]; did you mean "per_day"?`},
		{"unknown top-level key", "incidents:", "incidnets:",
			`acme.yaml:19: unknown key "incidnets" at the top level; did you mean "incidents"?`},
		{"unknown key without a suggestion", "    approvals: {", "    zzz: 1\n    approvals: {",
			`acme.yaml:18: unknown key "zzz" in agents[1]; use name, hours, per_day, user_refs, tools, outcomes, levels or approvals.`},
		{"version", "v: 1\n", "v: 2\n",
			`acme.yaml:1: v is 2, and this Deedglass reads scenario format 1; set v: 1.`},
		{"time zone", "timezone: Asia/Kolkata", "timezone: Asia/Kolkatta",
			`acme.yaml:2: org.timezone "Asia/Kolkatta" is not a known IANA time zone; use a name like "Asia/Kolkata" or "UTC".`},
		{"hours backwards", `hours: "09-19"`, `hours: "19-09"`,
			`acme.yaml:7: agents[0].hours "19-09" must start before it ends, within 00 to 24; write it like "09-19".`},
		{"hours one digit", `hours: "09-19"`, `hours: "9-19"`,
			`acme.yaml:7: agents[0].hours "9-19" must be two org-local hours, like "09-19"; write both with two digits.`},
		{"per_day zero", "per_day: 220", "per_day: 0",
			`acme.yaml:8: agents[0].per_day is 0; use a whole number from 1 to 100,000.`},
		{"weight zero", "postgres.query, weight: 20", "postgres.query, weight: 0",
			`acme.yaml:12: agents[0].tools[1].weight is 0; use a number above 0, like 30.`},
		{"outcomes do not add up", "failed: 0.03", "failed: 0.04",
			`acme.yaml:13: agents[0].outcomes add up to 1.01; make verified, unverifiable and failed add up to 1.`},
		{"outcome out of range", "verified: 0.92", "verified: 1.5",
			`acme.yaml:13: agents[0].outcomes.verified is 1.5; use a fraction from 0 to 1.`},
		{"unknown connector", "tool: stripe.create_refund", "tool: shopify.create_refund",
			`acme.yaml:11: agents[0].tools[0].tool "shopify.create_refund" uses the connector "shopify"; use postgres, stripe, gmail, slack, notion or github.`},
		{"misspelt connector", "tool: stripe.create_refund", "tool: strpe.create_refund",
			`acme.yaml:11: agents[0].tools[0].tool "strpe.create_refund" uses the connector "strpe"; did you mean "stripe.create_refund"?`},
		{"tool without a dot", "tool: postgres.execute, weight: 50", "tool: postgres, weight: 50",
			`acme.yaml:17: agents[1].tools[0].tool "postgres" must be <connector>.<tool> in lowercase, like "stripe.create_refund"; fix the name.`},
		{"sql on a stripe tool", "currency: usd } }", "currency: usd }, sql: [\"SELECT 1\"] }",
			`acme.yaml:11: agents[0].tools[0].sql applies only to postgres tools; remove it, or use args for stripe.create_refund.`},
		{"unknown placeholder", "WHERE id = {id}", "WHERE id = {order}",
			`acme.yaml:12: agents[0].tools[1].sql[0] has the placeholder {order}; use {id} or {sku}.`},
		{"lognormal typo", "lognormal: { median: 2400, sigma: 0.8 }", "lognormal: { median: 2400, sigma: 0.8, mean: 1 }",
			`acme.yaml:11: unknown key "mean" in agents[0].tools[0].args.amount.lognormal; did you mean "median"?`},
		{"p95 under the median", "p95: 2h", "p95: 10m",
			`acme.yaml:18: agents[1].approvals.wait.p95 (10m) is shorter than the median (20m); set p95 at or above the median.`},
		{"bad duration", "median: 20m", "median: 20 min",
			`acme.yaml:18: agents[1].approvals.wait.median "20 min" is not a duration; write it like "20m" or "2h".`},
		{"approver outside .test", "[priya@example.test]", "[priya@example.com]",
			`acme.yaml:4: approvers[0] "priya@example.com" must be a lowercase address under the reserved .test domain, like "priya@example.test"; change the domain.`},
		{"duplicate agent", "  - name: ops-agent", "  - name: support-bot",
			`acme.yaml:14: agents[1] is named "support-bot" like an earlier agent; give each agent its own name.`},
		{"incident kind typo", "kind: mismatch", "kind: mismatches",
			`acme.yaml:20: incidents[0].kind "mismatches" is not an incident kind; did you mean "mismatch"?`},
		{"incident agent typo", "agent: ops-agent, kind", "agent: ops-bot, kind",
			`acme.yaml:20: incidents[0].agent "ops-bot" is not one of the agents; did you mean "ops-agent"?`},
		{"mismatch without a table", ", table: public.inventory", "",
			`acme.yaml:20: incidents[0] is a mismatch incident with no table; add tool, table and count.`},
		{"field that does not apply", "count: 3 }", "count: 3, factor: 2 }",
			`acme.yaml:20: incidents[0].factor does not apply to a mismatch incident; remove it.`},
		{"hour out of range", "hour: 11,", "hour: 24,",
			`acme.yaml:20: incidents[0].hour is 24; use a whole number from 0 to 23.`},
		{"day and days", "{ day: 5, hour: 11", "{ day: 5, days: [5], hour: 11",
			`acme.yaml:20: incidents[0] has both day and days; keep one of them.`},
		{"no day", "{ day: 5, hour: 11, ", "{ hour: 11, ",
			`acme.yaml:20: incidents[0] has no day; add day: <n> or days: [<n>, ...].`},
		{"mismatch on a stripe tool", "kind: mismatch, tool: postgres.execute", "kind: mismatch, tool: stripe.create_refund",
			`acme.yaml:20: incidents[0].tool "stripe.create_refund" can't show a mismatch, because the Witness compares table changes for postgres tools only; use postgres.execute.`},
		{"unqualified table", "table: public.inventory", "table: inventory",
			`acme.yaml:20: incidents[0].table "inventory" must be a schema-qualified table, like "public.inventory"; add the schema.`},
		{"budget typo", "budget: refunds-per-day", "budget: refunds-daily",
			`acme.yaml:21: incidents[1].budget "refunds-daily" is not a budget in v1.yaml; did you mean "refunds-per-day"?`},
		{"budget pressure with an hour", "{ days: [2, 4], agent", "{ days: [2, 4], hour: 3, agent",
			`acme.yaml:21: incidents[1].hour does not apply to a budget_pressure incident, which spreads over the agent's hours; remove it.`},
		{"target over 1", "target: 0.95", "target: 1.5",
			`acme.yaml:21: incidents[1].target is 1.5; use a fraction above 0 and at most 1, like 0.95.`},
		{"off hours inside the hours", "  - { days: [2, 4]", "  - { day: 6, hour: 12, agent: support-bot, kind: off_hours, count: 12 }\n  - { days: [2, 4]",
			`acme.yaml:21: incidents[1].hour 12 is inside support-bot's hours (09-19); pick an hour outside them.`},
		{"first use of a known tool", "  - { days: [2, 4]", "  - { day: 5, hour: 15, agent: ops-agent, kind: first_use, tool: postgres.execute, count: 2 }\n  - { days: [2, 4]",
			`acme.yaml:21: incidents[1].tool "postgres.execute" is already one of ops-agent's tools; pick a tool the agent has never used.`},
		{"new refs without user_refs", "  - { days: [2, 4]", "  - { day: 7, hour: 16, agent: ops-agent, kind: new_user_refs, count: 60 }\n  - { days: [2, 4]",
			`acme.yaml:21: incidents[1] needs user_refs on ops-agent to make new ones; add user_refs to the agent.`},
		{"missing policy file", "enforce: v1.yaml", "enforce: missing.yaml",
			`acme.yaml:3: policies.enforce "missing.yaml" can't be read (file does not exist); check the path, relative to the scenario file.`},
		{"policy that does not compile", "enforce: v1.yaml", "enforce: broken.yaml",
			"acme.yaml:3: policies.enforce \"broken.yaml\" does not compile (line 1: This policy has no `defaults`); run deedglass policy lint on it to see every problem."},
		{"missing agents", "agents:\n  - name: support-bot", "agentz:\n  - name: support-bot",
			`acme.yaml:5: unknown key "agentz" at the top level; did you mean "agents"?`},
		{"duplicate key", "    per_day: 120", "    per_day: 120\n    per_day: 130",
			`acme.yaml:17: key "per_day" appears twice in agents[1]; keep one of them.`},
		{"anchor", "approvers: [priya@example.test]", "approvers: &a [priya@example.test]",
			`acme.yaml:4: anchors and aliases are not supported; write the value out in full.`},
		{"two documents", "v: 1\n", "v: 1\n---\nv: 1\n",
			`acme.yaml:2: the file holds more than one YAML document; keep one scenario per file.`},
		{"YAML syntax", "    per_day: 120", "    per_day: 120: 3",
			`acme.yaml:16: the YAML can't be read (mapping values are not allowed in this context); check the syntax on this line.`},
		{"text where a number goes", "per_day: 220", `per_day: "220"`,
			`acme.yaml:8: agents[0].per_day must be a whole number from 1 to 100,000; write it without quotes.`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if !strings.Contains(base, tc.old) {
				t.Fatalf("the base scenario has no %q", tc.old)
			}
			src := strings.Replace(base, tc.old, tc.new, 1)
			_, err := Parse("acme.yaml", []byte(src), testFiles)
			if err == nil {
				t.Fatalf("parsed; want %s", tc.want)
			}
			if err.Error() != tc.want {
				t.Fatalf("\n got: %s\nwant: %s", err, tc.want)
			}
			msg := err.Error()
			if strings.Contains(msg, "!") || strings.Contains(msg, "—") || !(strings.HasSuffix(msg, ".") || strings.HasSuffix(msg, "?")) {
				t.Errorf("message breaks the voice rules: %s", msg)
			}
		})
	}
}

func TestParseEmpty(t *testing.T) {
	for _, src := range []string{"", "# only a comment\n"} {
		_, err := Parse("acme.yaml", []byte(src), testFiles)
		if err == nil || err.Error() != "acme.yaml: the scenario is empty; start from examples/sim/acme.yaml." {
			t.Errorf("%q: %v", src, err)
		}
	}
}

func TestPlanChecksIncidentDays(t *testing.T) {
	sc := Default()
	w, err := Span(sc, 3, time.Date(2026, 10, 4, 10, 30, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	_, err = Plan(sc, 7, w)
	want := "acme.yaml:38: incidents[0] is on day 5, but the window has 3 days; simulate at least 5 days, or move the incident."
	if err == nil || err.Error() != want {
		t.Fatalf("\n got: %v\nwant: %s", err, want)
	}
}

func TestDefault(t *testing.T) {
	sc := Default()
	if sc.Org.Slug != "acme-sim" || sc.Org.Location.String() != "Asia/Kolkata" || len(sc.Agents) != 4 || len(sc.Incidents) != 6 {
		t.Fatalf("unexpected default: %+v", sc.Org)
	}
	if sc.Policies.EnforceFile != "acme-sim.v1.yaml" || sc.Policies.ShadowFile != "acme-sim.v2.yaml" || sc.Policies.ShadowFromDay != 3 {
		t.Fatalf("policies: %+v", sc.Policies)
	}
	// The off_hours incident is on day 6: the detector needs five days of history first.
	for _, inc := range sc.Incidents {
		if inc.Kind == KindOffHours && inc.Days[0] < 6 {
			t.Errorf("off_hours on day %d; the detector needs at least five earlier days", inc.Days[0])
		}
	}
}

// TestLoadMatchesDefault reads examples/sim from disk and checks it is the embedded scenario.
func TestLoadMatchesDefault(t *testing.T) {
	dir := filepath.Join("..", "..", "..", "examples", "sim")
	sc, err := Load(filepath.Join(dir, "acme.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	for name, b := range map[string][]byte{"acme.yaml": simfiles.AcmeScenario, "acme-sim.v1.yaml": simfiles.AcmeSimV1,
		"acme-sim.v2.yaml": simfiles.AcmeSimV2, "acme-sim.v3.yaml": simfiles.AcmeSimV3} {
		disk, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil || !bytes.Equal(disk, b) {
			t.Errorf("%s: the embedded bytes differ from the file (%v)", name, err)
		}
		if fsb, err := simfiles.FS.ReadFile(name); err != nil || !bytes.Equal(fsb, b) {
			t.Errorf("%s: simfiles.FS differs (%v)", name, err)
		}
	}
	end := time.Date(2026, 10, 4, 10, 30, 0, 0, time.UTC)
	w, _ := Span(sc, 7, end)
	a, err := Plan(sc, 7, w)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := Plan(Default(), 7, w)
	if PlanDigest(a) != PlanDigest(b) {
		t.Error("the plan from disk differs from the embedded default")
	}
	if _, err := Load(filepath.Join(dir, "nope.yaml")); err == nil || !strings.HasSuffix(err.Error(), "check the path and try again.") {
		t.Errorf("missing file: %v", err)
	}
}

// TestPolicyCases runs each version's cases file the way deedglass policy test does, with every
// rule and budget covered and the voice check on.
func TestPolicyCases(t *testing.T) {
	dir := filepath.Join("..", "..", "..", "examples", "sim")
	for _, v := range []string{"acme-sim.v1", "acme-sim.v2", "acme-sim.v3"} {
		src, err := os.ReadFile(filepath.Join(dir, v+".yaml"))
		if err != nil {
			t.Fatal(err)
		}
		c, diags := policy.Compile(src, policy.Options{File: v + ".yaml", Voice: true})
		if c == nil || len(c.Warnings) > 0 {
			t.Fatalf("%s: %v %v", v, diags, c)
		}
		if c.Name != "acme-sim" || !strings.Contains(c.Description, "Simulated") {
			t.Errorf("%s: name %q, description %q", v, c.Name, c.Description)
		}
		raw, err := os.ReadFile(filepath.Join(dir, v+".cases.yaml"))
		if err != nil {
			t.Fatal(err)
		}
		cf, cd := policy.ParseCasesYAML(raw, v+".cases.yaml")
		if cf == nil {
			t.Fatalf("%s: %v", v, cd)
		}
		r := policy.RunCases(context.Background(), c, cf, policy.TestOptions{})
		if !r.OK || len(r.Coverage.Uncovered) > 0 || r.BudgetsCovered(c) != len(c.Budgets) {
			t.Errorf("%s: ok %v, uncovered %v, budgets %d of %d", v, r.OK, r.Coverage.Uncovered, r.BudgetsCovered(c), len(c.Budgets))
		}
	}
}
