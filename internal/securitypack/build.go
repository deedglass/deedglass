package securitypack

import (
	_ "embed"
	"encoding/json"
	"strings"
)

// attack_summary.json is the header of attack/results/latest.json (TestAttackSummaryCurrent keeps
// the two equal): the attack suite's last recorded run, quoted by product_tested. The pack never
// runs the suite.
//
//go:embed attack_summary.json
var attackSummaryJSON []byte

// EmbeddedAttack is the attack summary this binary carries, or nil.
func EmbeddedAttack() *AttackSummary {
	var a AttackSummary
	if err := json.Unmarshal(attackSummaryJSON, &a); err != nil || a.Version == "" {
		return nil
	}
	a.Version = strings.TrimPrefix(a.Version, "v")
	return &a
}

// BuildFactsFor is the build collector: the running version and the embedded attack summary.
func BuildFactsFor(version, commit string) BuildFacts {
	return BuildFacts{Version: strings.TrimPrefix(version, "v"), Commit: commit, Attack: EmbeddedAttack()}
}
