package scan

import (
	"fmt"
	"strings"

	"github.com/deedglass/deedglass/examples/policies"
)

// suggest picks three policies to add first (S32 §3.13.3), quoted from the embedded S12 starters
// with only the table name substituted: block-destructive-sql when any table allows DELETE or
// TRUNCATE (or, without a database, always: it costs nothing); writes-need-approval narrowed to the
// top-ranked table; and the postgres-safe rate budget, or stripe-refunds' daily budget when a money
// tool was found. Each one is a whole policy that compiles, and denies DROP TABLE.
func suggest(r *Report) []Suggestion {
	var out []Suggestion
	pg := r.Postgres
	anyDelete := pg == nil
	top := ""
	if pg != nil {
		for _, t := range pg.Tables {
			anyDelete = anyDelete || t.CanDelete()
		}
		if len(pg.Tables) > 0 {
			top = pg.Tables[0].Name()
		}
	}
	if anyDelete {
		if y, err := rulePolicy("postgres-safe", "block-destructive-sql", "", ""); err == nil {
			out = append(out, Suggestion{Starter: "postgres-safe", Rule: "block-destructive-sql",
				Why: "DROP and TRUNCATE never run, whatever the agent sends.", YAML: y})
		}
	}
	why := "Writes wait for a person in Console › Approvals."
	narrow := ""
	if top != "" {
		narrow = fmt.Sprintf(" && %q in sql.tables", top)
		why = fmt.Sprintf("Writes to %s wait for a person.", top)
	}
	if y, err := rulePolicy("postgres-safe", "writes-need-approval", `when: sql.category == "write"`, `when: sql.category == "write"`+narrow); err == nil {
		out = append(out, Suggestion{Starter: "postgres-safe", Rule: "writes-need-approval", Why: why, YAML: y})
	}
	money := false
	for _, t := range r.Tools {
		money = money || t.SideEffect == "money"
	}
	if money {
		if y, err := budgetPolicy("stripe-refunds", "refunds-per-day"); err == nil {
			out = append(out, Suggestion{Starter: "stripe-refunds", Rule: "refunds-per-day",
				Why: "More than $500.00 of refunds in a day waits until the next day.", YAML: y})
		}
	} else if y, err := budgetPolicy("postgres-safe", "agent-rate"); err == nil {
		out = append(out, Suggestion{Starter: "postgres-safe", Rule: "agent-rate",
			Why: "More than 60 calls a minute from one agent waits for the next minute.", YAML: y})
	}
	if out == nil {
		out = []Suggestion{}
	}
	return out
}

const header = `deedglass: policy/v1
name: %s
description: Suggested by deedglass scan from the %s starter.

defaults:
  decision: deny
  reason: No rule allows this action. Add a rule in Console › Policies to allow it.

`

// block returns the YAML list item under key ("rules:" or "budgets:") whose id is id, as written.
func block(src, key, id string) (string, error) {
	lines := strings.Split(src, "\n")
	in := false
	start := -1
	for i, l := range lines {
		if strings.HasPrefix(l, key) {
			in = true
			continue
		}
		if in && len(l) > 0 && l[0] != ' ' && l[0] != '#' {
			in = false
		}
		if !in {
			continue
		}
		if strings.HasPrefix(l, "  - id: ") {
			if start >= 0 {
				return strings.TrimRight(strings.Join(lines[start:i], "\n"), "\n "), nil
			}
			if strings.TrimSpace(strings.TrimPrefix(l, "  - id: ")) == id {
				start = i
			}
		}
	}
	if start >= 0 {
		end := len(lines)
		for i := start + 1; i < len(lines); i++ {
			if len(lines[i]) > 0 && lines[i][0] != ' ' && lines[i][0] != '#' {
				end = i
				break
			}
		}
		return strings.TrimRight(strings.Join(lines[start:end], "\n"), "\n "), nil
	}
	return "", fmt.Errorf("scan: %s has no %s %s", key, strings.TrimSuffix(key, ":"), id)
}

func rulePolicy(starter, id, old, repl string) (string, error) {
	src, err := policies.Source(starter)
	if err != nil {
		return "", err
	}
	b, err := block(string(src), "rules:", id)
	if err != nil {
		return "", err
	}
	if old != "" {
		b = strings.Replace(b, old, repl, 1)
	}
	return fmt.Sprintf(header, "scan-"+id, starter) + "rules:\n" + b + "\n", nil
}

func budgetPolicy(starter, id string) (string, error) {
	src, err := policies.Source(starter)
	if err != nil {
		return "", err
	}
	b, err := block(string(src), "budgets:", id)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf(header, "scan-"+id, starter) + "budgets:\n" + b + "\n", nil
}
