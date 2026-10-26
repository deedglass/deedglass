package healthpilot

import (
	"fmt"
	"strings"
	"time"
)

// report is REPORT.md: what the pilot did, in words an IT lead can forward. Every number in it comes
// from this run.
func (r *run) report() []byte {
	var b strings.Builder
	w := func(format string, a ...any) { fmt.Fprintf(&b, format, a...) }
	res := r.res
	w("# Deedglass health pilot report\n\n")
	w("Run on %s with `deedglass health pilot`", r.o.Now().UTC().Format("2 January 2006, 15:04 MST"))
	if res.Region == RegionIndia {
		w(" `--region india`")
	}
	w(".\n\n")
	w("**Synthetic data only.** Every patient, observation, order and claim in this run was made up for the\n")
	w("pilot and carries an identifier in the system `%s`. No patient data was used, and nothing was sent to Deedglass.\n", tagSystem)
	w("Deedglass is not clinical software: it decides whether an agent's request runs, waits for a person\n")
	w("or is refused, records it, and compares what a system stored with what the agent reported.\n\n")

	w("## Setup\n\n")
	w("| | |\n|---|---|\n")
	w("| Policy pack | `%s` %s, enforced (carried in the binary, files checked against the pack index) |\n", res.Pack.Name, res.Pack.Version)
	w("| PHI mode | on |\n| Lockdown | on: every write waits for a person, approvals expire within 15 minutes |\n")
	if res.Store == StoreSandbox {
		w("| FHIR server | your sandbox at %s (scene 1); the in-memory synthetic store (scene 2) |\n", hostOf(r.o.FHIRURL))
	} else {
		w("| FHIR server | an in-memory FHIR R4 store inside the binary (synthetic) |\n")
	}
	w("| Agent | `%s`, labelled `connector=%s` |\n\n", agentName, connName)

	w("## What happened\n\n")
	w("| Scene | Request | Decision | Outcome |\n|---|---|---|---|\n")
	for _, s := range res.Scenes {
		switch s.N {
		case 4:
			chain := "chain intact"
			if !res.ChainOK {
				chain = "chain did not check out"
			}
			tam := "a one-byte change was caught"
			if !res.TamperSeen {
				tam = "a one-byte change was NOT caught"
			}
			w("| 4. %s | %d records | sealed | %s; %s |\n", s.Title, res.Records, chain, tam)
		default:
			w("| %d. %s | `%s` %s | %s | %s |\n", s.N, s.Title, s.Tool, s.Resource, decisionText(s), outcomeText(s))
		}
	}
	w("\n")
	for _, s := range res.Scenes {
		if s.Verdict == "MISMATCH" {
			w("**Scene %d, Doesn't match.** The server answered %d, but the read-back at %s differed:\n\n", s.N, s.HTTPStatus, s.Target)
			for _, f := range s.Fields {
				if !f.Match {
					w("- `%s`: sent `%s`, stored `%s`\n", f.Field, jsonText(f.Expected), jsonText(f.Observed))
				}
			}
			w("\nThe HTTP status alone would have reported success.\n\n")
		}
		if s.N == 3 {
			w("**Scene 3, held.** Rule `%s` routed the request to %s (%d required); it expires in %s.\n",
				s.Rule, strings.Join(s.Approvers, ", "), s.Required, minutes(time.Duration(s.ExpiresIn)*time.Second))
			w("On a server, the approvers get this Console link: %s. Nothing reached the record system.\n\n", s.ConsoleURL)
		}
	}

	w("## Check the records yourself\n\n")
	w("The sealed records and the key that sealed them are next to this report. With the `deedglass` CLI:\n\n")
	w("```sh\ndeedglass verify deeds.jsonl --keys keys.json\n```\n\n")
	w("Records hold ids, digests and outcomes, not clinical content.\n\n")

	w("## What this pilot does not show\n\n")
	w("- A real Console, email or Slack approval: the pilot has no server, so a stand-in admin countersigned\n")
	w("  scenes 1 and 2 and scene 3 stayed held.\n")
	w("- Proven (L2) checks, which read a FHIR server's own database change log (HAPI FHIR JPA and Medplum on\n")
	w("  Postgres); see docs/connectors/fhir.md.\n")
	w("- Whether a clinical change is right. That stays with the clinician who approves it.\n\n")

	w("## Next steps\n\n")
	if res.Store == StoreSandbox {
		w("1. Put the gate in front of your agent on a test server: docs/guides/fhir-server.md.\n")
	} else {
		w("1. Point it at your FHIR sandbox (synthetic data, auth none): `deedglass health pilot --fhir-url https://<your-sandbox>/fhir`\n")
	}
	w("2. Read the IT review pack next to this report (IT_REVIEW_PACK.md) with your security team.\n")
	w("3. Plan a six-week pilot on a test server, measured by the success measures in that pack.\n")
	return []byte(b.String())
}

func decisionText(s Scene) string {
	switch s.Decision {
	case "require_approval":
		return fmt.Sprintf("waits for a person (`%s`)", s.Rule)
	case "allow":
		return fmt.Sprintf("allowed (`%s`)", s.Rule)
	}
	return fmt.Sprintf("%s (`%s`)", s.Decision, s.Rule)
}

func outcomeText(s Scene) string {
	if !s.Executed {
		return "held: nothing reached the record system"
	}
	v := map[string]string{"VERIFIED": "Checked · Matches", "MISMATCH": "Checked · Doesn't match", "UNVERIFIABLE": "Can't check"}[s.Verdict]
	return fmt.Sprintf("countersigned by %s; server %d; %s", s.ApprovedBy, s.HTTPStatus, v)
}
