package main

import (
	"bytes"
	"fmt"
	"strings"
	"time"

	"github.com/deedglass/deedglass/attack/harness"
)

// layers are the page's tables, in order: the V7 groups by what they attack, then V0.
var layers = []struct {
	title, intro string
	ids          func(c harness.MetaCase) bool
}{
	{"Gate and SQL", "A manipulated model sends SQL that destroys, smuggles or hides a write. The policy denies it with a sealed deed; under a rule that allows every Postgres call, the connector refuses it on its own.",
		func(c harness.MetaCase) bool { return c.Group == "H" && c.ID <= "H07" }},
	{"Identity, intents and budgets", "A stolen or misused agent key, a replayed signed intent, and a burst past the agent's budget.",
		func(c harness.MetaCase) bool { return c.Group == "J" }},
	{"Connectors, egress and secrets", "A hostile connector config, a hostile or careless upstream, and an MCP server that renames its tools.",
		func(c harness.MetaCase) bool { return c.Group == "K" }},
	{"Witness (L2)", "Postgres committed something the agent didn't report. The L2 witness (S21) reads Postgres's change log and seals the difference.",
		func(c harness.MetaCase) bool { return c.Group == "H" && c.ID >= "H08" }},
	{"Witness (L2): expected and observed", "S21's group W: triggers that widen, rewrite or delete, forged and replayed evidence, missing old rows, statements too complex to predict, and a decoder that is late, loses its slot or loses its publication. A gap is UNVERIFIABLE, never MISMATCH; late evidence is an amendment deed, never an edit.",
		func(c harness.MetaCase) bool { return c.Group == "W" }},
	{"Approvals, links and webhooks", "S22's group L. A person's countersign is the control here, so each case tries to get one without the right person: a replayed email link, a second vote from the same person, the person the action runs for, an agent key, a vote after the approval expired, a vote on an out-of-date view. Then a webhook aimed at cloud metadata, and a forged webhook signature.",
		func(c harness.MetaCase) bool { return c.Group == "L" }},
	{"Undo", "S23's group U. An undo writes to the customer's database on a person's request, so each case tries to make it write something Postgres didn't record, or to reach it without a person: rows that change under it, values smuggled into the request, a forged marker naming the action, an agent calling the internal tool, another org's action.",
		func(c harness.MetaCase) bool { return c.Group == "U" }},
	{"People, sessions and the live stream", "S34's group P. Each case reaches for a decision or a view it shouldn't have: an approval link used against another approval, a forged or replayed Slack click, the person the action runs for approving it in Slack, a double vote, a viewer reaching for admin actions, a cross-site request, another org's event stream, and calls that race a Stop.",
		func(c harness.MetaCase) bool { return c.Group == "P" }},
	{"Hostile MCP servers and upstreams", "S34's group Q. The upstream is the attacker: tool descriptions that carry instructions, a tool that changes after it was listed, two servers with the same tool name, huge or deeply nested results, a server that is down or hangs, a server that asks the client's model for text, a live Stripe key, and a webhook host that turns into cloud metadata.",
		func(c harness.MetaCase) bool { return c.Group == "Q" }},
	{"Resources, records and output", "S34's group R. Inputs built to exhaust or mislead: policy and CEL bombs, a forked deed chain, a signed intent from the wrong time, injection through the optional AI wording of suggestions, oversized and deeply nested JSON, and control characters aimed at terminals and logs.",
		func(c harness.MetaCase) bool { return c.Group == "R" }},
}

// notCovered is the honest list under "What this suite doesn't cover", shared by the page and the
// docs site's data (DocsData).
var notCovered = []string{
	"How often models fall for injections. These cases start after the model was fooled.",
	"A compromised Deedglass host, database superuser or seal key.",
	"Instructions in tool descriptions are found by a heuristic (phrases, hidden Unicode, instruction tags), not by understanding intent; a reworded instruction can pass (Q01). Poisoned tool results and slow-drip responses are not scanned.",
	"Denial of service at scale. J04 checks one agent's budget, not a flood.",
	"The supply chain beyond `govulncheck` (S35 adds the SBOM and container scanning).",
	"The V0 cases marked retired: each names the V7 test that checks the control instead.",
	"What L2 can't see: effects outside Postgres, writes to tables outside the publication, and proxy-mode SQL servers (no marker to bind). `docs/WITNESS.md` lists them.",
	"Production network rules for private ranges: the suite runs in dev with the private-network override so it can reach its own loopback servers. Metadata, link-local and cross-host targets stay blocked even then (K01, K02); the prod-only rules are covered by the egress corpus (`internal/connector/egress`).",
}

func statusText(r *harness.Result) string {
	switch {
	case r == nil:
		return "○ not run"
	case r.Status == harness.StatusMatched:
		return "✓ matched"
	case r.Status == harness.StatusDiffers:
		return "✕ differs"
	}
	return "○ waiting on " + r.Waiting
}

// v0Status is where a V0 case stands in V7 (S34).
func v0Status(c harness.MetaCase) string {
	switch {
	case c.Status == "superseded":
		return "runs as " + c.SupersededBy
	case c.Status == "retired":
		return "retired: " + c.Retired
	case c.Status == "implemented" && strings.Contains("DE", c.Group):
		return "decoder suite (S20)"
	case c.Status == "implemented":
		return "attack/deed tests (S04, S05)"
	}
	return "V0 suite"
}

func cell(s string) string { return strings.ReplaceAll(strings.ReplaceAll(s, "|", "\\|"), "\n", " ") }

// Generate renders docs/ATTACK_SUITE.md. It reads only the meta and the stored results, and leaves
// out timings and log counts, so two runs with the same outcomes give the same page.
func Generate(meta *harness.Meta, run *harness.Run) []byte {
	byID := map[string]*harness.Result{}
	for i := range run.Cases {
		byID[run.Cases[i].ID] = &run.Cases[i]
	}
	day := run.StartedAt
	if t, err := time.Parse(time.RFC3339, run.StartedAt); err == nil {
		if ist, err := time.LoadLocation("Asia/Kolkata"); err == nil {
			t = t.In(ist)
		}
		day = t.Format("2006-01-02")
	}
	var b bytes.Buffer
	w := func(format string, args ...any) { fmt.Fprintf(&b, format, args...) }
	w("<!-- Generated by `make attack-report` from attack/attack_meta.json and attack/results/latest.json. Don't edit it by hand: run make attack, then make attack-report. -->\n\n")
	w("# Attack suite\n\n")
	w("These cases assume the model has already been manipulated. They test what Deedglass does with the calls that come out of it. They don't measure how often a model falls for an injection.\n\n")
	w("Every case's expected outcome was written in `attack/attack_meta.json` before its code existed, and `attack/meta_test.go` pins a digest of the expectations (ADR-040). A case that turns out different is shown as differs; its expectation is never edited to pass.\n\n")
	w("**Last run:** %s · commit `%s` · PostgreSQL %s · a real `deedglass serve` process, a real Postgres, loopback upstreams.\n\n", day, run.Commit, pgVersion(run.Postgres))
	s := run.Summary
	w("**%d matched · %d differ · %d waiting on a later session.** After every case: the deed chains of both orgs are %s, the target database is unchanged wherever nothing should have run, every action is sealed, and the planted credential was seen %d times across the server's logs, every HTTP response, and the recorded actions, events, deeds, admin events, connector configs and credential hints.\n\n",
		s.Matched, s.Differs, s.Waiting, run.Invariants.Chains, run.Invariants.SecretsSeen)
	w("Run it: `make attack` (a throwaway PostgreSQL 18 cluster, about three and a half minutes on a laptop: L05 waits out a real one-minute approval expiry and P10 runs ten Stop-all rounds), then `make attack-report`. One case: `DEEDGLASS_IT_ATTACK_RUN='TestAttack/H03' make attack`.\n\n")
	for _, l := range layers {
		w("## %s\n\n%s\n\n", l.title, l.intro)
		w("| Case | Attack | Expected | Actual | Status |\n|---|---|---|---|---|\n")
		for _, c := range meta.Cases {
			if !l.ids(c) {
				continue
			}
			r := byID[c.ID]
			actual := "not run"
			if r != nil && r.Status != harness.StatusWaiting {
				actual = r.Detail
			}
			w("| %s | %s | %s | %s | %s |\n", c.ID, cell(c.Title), cell(c.ExpectedDetail), cell(actual), statusText(r))
		}
		w("\n")
	}
	w("## V0 controlled-write cases (A to G)\n\n")
	w("The 66 cases frozen on 2026-09-28 for the V0 controlled-function design. A13 and F01 to F04 run in `attack/deed_test.go` and `attack/deed_verify_test.go`. The decoder's half of D01 to D04, D17, E01 to E05, E07, E08, E10 and E11 (what is stored, bound, alerted or halted) runs in `internal/decoder`'s integration suite on PostgreSQL 16, 17 and 18 (S20); their L2 verdicts are the W group's (S21). S34 accounted for the rest: a case whose attack a V7 case runs points to it, and a case that no longer applies to the V7 architecture says why and names the test that checks the V7 control instead. Their expected outcomes are unchanged.\n\n")
	w("| Case | Attack | Expected | Status |\n|---|---|---|---|\n")
	for _, c := range meta.Cases {
		if strings.Contains("ABCDEFG", c.Group) {
			w("| %s | %s | %s | %s |\n", c.ID, cell(c.Title), cell(c.ExpectedDetail), cell(v0Status(c)))
		}
	}
	w("\n## Outcome words\n\n")
	w("| Outcome | Meaning |\n|---|---|\n")
	for _, k := range []string{"DENIED", "REJECTED", "BLOCKED", "REDACTED", "DETECTED", "MISMATCH", "UNVERIFIABLE", "VERIFIED", "NO EFFECT", "OUTSIDE SCOPE"} {
		if v, ok := meta.Outcomes[k]; ok {
			w("| %s | %s |\n", k, cell(v))
		}
	}
	w("\nADR-021 renamed the V0 words to the Deedglass verdict words: VALID is now VERIFIED, INVALID is MISMATCH, and INCONCLUSIVE is UNVERIFIABLE. Reason codes such as `SIG_INVALID` keep their spelling. `TestADR021WordsOnly` maps the words back and lands on the frozen V0 digest.\n\n")
	w("## Group letters\n\nEach group set has its own pinned digest, so declaring new cases never changes an earlier pin.\n\n")
	w("| Letters | Owner | Constant |\n|---|---|---|\n")
	w("| A to G | V0 | `expectedDigest` |\n| H, J, K | S18 | `expectedDigestV7` |\n| N | S19 (L0 and L1 read-back), if it declares any | `expectedDigestN` |\n")
	w("| W | S21 (the L2 witness, W01 to W13) | `expectedDigestW` |\n| L | S22 (approvals, links, webhooks) | `expectedDigestL` |\n| U | S23 (undo) | `expectedDigestU` |\n| P, Q, R | S34 | `expectedDigestS34` |\n\n")
	w("I, M, O, S, T, V, X, Y and Z are unassigned. A session that needs a group takes the next free letter and adds a row here.\n\n")
	w("## What this suite doesn't cover\n\n")
	for _, line := range notCovered {
		w("- %s\n", line)
	}
	return b.Bytes()
}

// DocsCase is one row of the docs site's attack-suite page.
type DocsCase struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Expected string `json:"expected"`
	Actual   string `json:"actual"`
	Status   string `json:"status"` // matched | differs | waiting | not_run
}

// DocsLayer is one table of the page.
type DocsLayer struct {
	Title string     `json:"title"`
	Intro string     `json:"intro"`
	Cases []DocsCase `json:"cases"`
}

// DocsPage is what docs-site/src/data/attack.json holds (S33 §3.4): the same layers, results and
// limits as docs/ATTACK_SUITE.md, without timings or log counts.
type DocsPage struct {
	Object     string          `json:"object"`
	Day        string          `json:"day"`
	Commit     string          `json:"commit"`
	Postgres   string          `json:"postgres"`
	Summary    harness.Summary `json:"summary"`
	Chains     string          `json:"chains"`
	Secrets    int             `json:"secrets_seen"`
	Layers     []DocsLayer     `json:"layers"`
	NotCovered []string        `json:"not_covered"`
}

// DocsData renders the docs site's view of the run.
func DocsData(meta *harness.Meta, run *harness.Run) DocsPage {
	byID := map[string]*harness.Result{}
	for i := range run.Cases {
		byID[run.Cases[i].ID] = &run.Cases[i]
	}
	day := run.StartedAt
	if t, err := time.Parse(time.RFC3339, run.StartedAt); err == nil {
		if ist, err := time.LoadLocation("Asia/Kolkata"); err == nil {
			t = t.In(ist)
		}
		day = t.Format("2006-01-02")
	}
	p := DocsPage{Object: "attack_page", Day: day, Commit: run.Commit, Postgres: pgVersion(run.Postgres), Summary: run.Summary,
		Chains: run.Invariants.Chains, Secrets: run.Invariants.SecretsSeen, NotCovered: notCovered}
	for _, l := range layers {
		dl := DocsLayer{Title: l.title, Intro: l.intro}
		for _, c := range meta.Cases {
			if !l.ids(c) {
				continue
			}
			r := byID[c.ID]
			dc := DocsCase{ID: c.ID, Title: c.Title, Expected: c.ExpectedDetail, Actual: "not run", Status: "not_run"}
			if r != nil {
				switch r.Status {
				case harness.StatusMatched:
					dc.Status, dc.Actual = "matched", r.Detail
				case harness.StatusDiffers:
					dc.Status, dc.Actual = "differs", r.Detail
				default:
					dc.Status, dc.Actual = "waiting", "waiting on "+r.Waiting
				}
			}
			dl.Cases = append(dl.Cases, dc)
		}
		p.Layers = append(p.Layers, dl)
	}
	return p
}
