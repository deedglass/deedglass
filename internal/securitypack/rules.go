package securitypack

import (
	"sort"
	"strings"
	"time"
)

// Evidence is what a rule computed for one question: ids, counts, hashes, dates (fields ending
// in _on, as YYYY-MM-DD) and names of the org's own agents, connectors and rules. Values are
// int, string or bool.
type Evidence map[string]any

// env is what every rule reads: the facts plus what the core derived from them.
type env struct {
	f  *Facts
	rf ruleFacts
}

type rule func(e *env) (Status, Evidence)

// The thresholds the rules use, named once (docs/SECURITY_PACK.md quotes them).
const (
	KeyMaxAge         = 90 * 24 * time.Hour
	KeyUnusedAfter    = 30 * 24 * time.Hour
	CredentialMaxAge  = 365 * 24 * time.Hour
	CheckedInPlacePct = 80
	CheckedPartlyPct  = 20
)

// DefaultDenyName is the onboarding policy every org starts with (policy.DefaultDenyName).
const DefaultDenyName = "default-deny"

// rules maps each question id to its deterministic, conservative rule. A rule only runs when every
// collector its question needs was read; otherwise the answer is Can't tell.
var rules = map[string]rule{
	"agents_listed":            ruleAgentsListed,
	"keys_scoped":              ruleKeysScoped,
	"keys_rotated":             ruleKeysRotated,
	"connectors_listed":        ruleConnectorsListed,
	"rules_active":             ruleRulesActive,
	"default_deny":             ruleDefaultDeny,
	"limits_set":               ruleLimits,
	"rules_changes_recorded":   ruleChangesRecorded,
	"approvals_required":       ruleApprovalsRequired,
	"four_eyes":                ruleFourEyes,
	"approvals_expire":         ruleApprovalsExpire,
	"stop_all":                 ruleStopAll,
	"stop_used":                ruleStopUsed,
	"results_checked":          ruleResultsChecked,
	"mismatches_handled":       ruleMismatches,
	"watched_sessions":         ruleWatched,
	"undo_available":           ruleUndoAvailable,
	"undos_done":               ruleUndosDone,
	"secrets_encrypted":        ruleSecretsEncrypted,
	"agents_never_see_secrets": ruleNeverSeeSecrets,
	"egress_limited":           ruleEgress,
	"records_sealed":           ruleRecordsSealed,
	"records_verifiable":       ruleRecordsVerifiable,
	"product_tested":           ruleProductTested,
}

func day(t time.Time) string { return t.UTC().Format("2006-01-02") }

// grade is the common three-way rule: none bad → InPlace, all bad → NotSetUp, else Partly.
func grade(bad, total int) Status {
	switch {
	case bad == 0:
		return InPlace
	case bad >= total:
		return NotSetUp
	}
	return Partly
}

func ruleAgentsListed(e *env) (Status, Evidence) {
	var active, stopped, noOwner int
	for _, a := range e.f.Agents {
		switch a.Status {
		case "active":
			active++
		default:
			stopped++
		}
		if !a.HasOwner && a.Status != "stopped" {
			noOwner++
		}
	}
	ev := Evidence{"agents": len(e.f.Agents), "active": active, "stopped": stopped, "without_owner": noOwner}
	switch {
	case len(e.f.Agents) == 0:
		return NotSetUp, ev
	case noOwner > 0:
		return Partly, ev
	}
	return InPlace, ev
}

func ruleKeysScoped(e *env) (Status, Evidence) {
	var agentKeys, adminKeys, unused int
	for _, k := range e.f.Keys {
		if k.AgentID == "" {
			adminKeys++
			continue
		}
		agentKeys++
		if k.LastUsedAt == nil && e.f.Now.Sub(k.CreatedAt) > KeyUnusedAfter {
			unused++
		}
	}
	ev := Evidence{"agent_keys": agentKeys, "admin_keys": adminKeys, "unused_keys": unused}
	switch {
	case agentKeys == 0:
		return NotSetUp, ev
	case unused > 0:
		return Partly, ev
	}
	return InPlace, ev
}

func ruleKeysRotated(e *env) (Status, Evidence) {
	old := 0
	var oldest time.Time
	for _, k := range e.f.Keys {
		if e.f.Now.Sub(k.CreatedAt) > KeyMaxAge {
			old++
		}
		if oldest.IsZero() || k.CreatedAt.Before(oldest) {
			oldest = k.CreatedAt
		}
	}
	ev := Evidence{"live_keys": len(e.f.Keys), "older_than_90_days": old}
	if len(e.f.Keys) == 0 {
		return NotApplicable, ev
	}
	ev["oldest_on"] = day(oldest)
	return grade(old, len(e.f.Keys)), ev
}

func ruleConnectorsListed(e *env) (Status, Evidence) {
	active := 0
	for _, c := range e.f.Connectors {
		if c.Status == "active" {
			active++
		}
	}
	ev := Evidence{"connectors": len(e.f.Connectors), "active_connectors": active, "proxy_actions": e.f.Actions.Proxy}
	switch {
	case len(e.f.Connectors) == 0:
		return NotSetUp, ev
	case e.f.Actions.Proxy > 0:
		return Partly, ev
	}
	return InPlace, ev
}

func ruleRulesActive(e *env) (Status, Evidence) {
	v := e.f.Rules.Enforce
	ev := Evidence{}
	if s := e.f.Rules.Shadow; s != nil {
		ev["shadow"] = s.Name
	}
	if v == nil {
		return NotSetUp, ev
	}
	ev["enforced"], ev["version"], ev["version_id"], ev["version_hash"] = v.Name, v.Version, v.VersionID, v.VersionHash
	ev["activated_by"], ev["activated_on"], ev["activation_deed"], ev["rules"] = v.ActivatedBy, day(v.ActivatedAt), v.DeedID, e.rf.rules
	if v.Name == DefaultDenyName {
		return Partly, ev
	}
	return InPlace, ev
}

func ruleDefaultDeny(e *env) (Status, Evidence) {
	var refused, held int
	var allowed []string
	for _, p := range e.rf.probes {
		switch p.Outcome {
		case "deny":
			refused++
		case "require_approval":
			held++
		default:
			allowed = append(allowed, probeLabel(p.Name))
		}
	}
	ev := Evidence{"probes": len(e.rf.probes), "refused": refused, "held": held, "allowed": len(allowed),
		"allowed_probes": strings.Join(allowed, ", ")}
	if e.f.Rules.Enforce == nil {
		return NotSetUp, ev
	}
	return grade(len(allowed), len(e.rf.probes)), ev
}

func ruleLimits(e *env) (Status, Evidence) {
	activeN, withBudget := 0, 0
	for _, a := range e.f.Agents {
		if a.Status == "active" {
			activeN++
			if a.HasBudget {
				withBudget++
			}
		}
	}
	ev := Evidence{"rule_budgets": e.rf.budgets, "agents_with_budgets": withBudget, "active_agents": activeN}
	switch {
	case e.f.Rules.Enforce != nil && e.rf.budgets > 0:
		return InPlace, ev
	case activeN > 0 && withBudget == activeN:
		// Every agent has its own limit: as good as a rule-level budget for this question.
		return InPlace, ev
	case withBudget > 0:
		return Partly, ev
	}
	return NotSetUp, ev
}

func ruleChangesRecorded(e *env) (Status, Evidence) {
	var sealed, withWho, inPeriod, ok int
	for _, c := range e.f.Rules.Changes {
		if c.DeedID != "" {
			sealed++
		}
		if c.By != "" {
			withWho++
		}
		if c.DeedID != "" && c.By != "" {
			ok++
		}
		if !c.At.Before(e.f.Period.From) {
			inPeriod++
		}
	}
	n := len(e.f.Rules.Changes)
	ev := Evidence{"changes": n, "sealed": sealed, "with_who": withWho, "changes_in_period": inPeriod}
	switch {
	case n > 0 && ok == n:
		return InPlace, ev
	case sealed == 0:
		// Every org has its onboarding activation, so no change at all is not a pass either.
		return NotSetUp, ev
	}
	return Partly, ev
}

func ruleApprovalsRequired(e *env) (Status, Evidence) {
	a := e.f.Approvals
	ev := Evidence{"approval_rules": e.rf.approvalRules, "approvals": a.Total, "approved": a.Approved, "denied": a.Denied}
	if e.rf.approvalRules == 0 {
		return NotSetUp, ev
	}
	return InPlace, ev
}

func ruleFourEyes(e *env) (Status, Evidence) {
	ev := Evidence{"approval_rules": e.rf.approvalRules, "two_person_rules": e.rf.twoPersonRules,
		"two_person_approvals": e.f.Approvals.MultiApprover}
	switch {
	case e.rf.approvalRules == 0:
		return NotApplicable, ev
	case e.rf.twoPersonRules == 0:
		return Partly, ev
	}
	return InPlace, ev
}

func ruleApprovalsExpire(e *env) (Status, Evidence) {
	a := e.f.Approvals
	ev := Evidence{"approvals": a.Total, "expired": a.Expired, "overdue": a.PendingOverdue}
	if a.MedianDecideSeconds >= 0 {
		ev["median_minutes"] = int((a.MedianDecideSeconds + 59) / 60)
	} else {
		ev["median_minutes"] = 0
	}
	switch {
	case e.rf.approvalRules == 0 && a.Total == 0:
		return NotApplicable, ev
	case a.PendingOverdue > 0:
		return Partly, ev
	}
	return InPlace, ev
}

func ruleStopAll(e *env) (Status, Evidence) {
	m := e.f.Members
	ev := Evidence{"people": m.OwnersAdmins, "admin_keys": m.AdminKeys}
	switch {
	case m.OwnersAdmins >= 2:
		return InPlace, ev
	case m.OwnersAdmins == 1:
		return Partly, ev
	}
	return NotSetUp, ev
}

func ruleStopUsed(e *env) (Status, Evidence) {
	s := e.f.Stop
	ev := Evidence{"stops_in_period": s.InPeriod}
	if s.LastAt != nil {
		ev["last_on"], ev["last_deed"] = day(*s.LastAt), s.LastDeed
	}
	switch {
	case s.InPeriod > 0 && s.LastAt != nil:
		return InPlace, ev
	case s.LastAt != nil:
		return Partly, ev
	}
	return NotSetUp, ev
}

// percent is the floor of 100·n/d, so 79.9% reads 79 and never rounds up into a better status.
func percent(n, d int) int {
	if d == 0 {
		return 0
	}
	return n * 100 / d
}

func ruleResultsChecked(e *env) (Status, Evidence) {
	a := e.f.Actions
	share := percent(a.Checked+a.Proven, a.Writes)
	ev := Evidence{"writes": a.Writes, "checked": a.Checked, "proven": a.Proven, "claimed": a.Writes - a.Checked - a.Proven, "share_percent": share}
	switch {
	case a.Writes == 0:
		return NotApplicable, ev
	case share >= CheckedInPlacePct:
		return InPlace, ev
	case share >= CheckedPartlyPct:
		return Partly, ev
	}
	return NotSetUp, ev
}

func ruleMismatches(e *env) (Status, Evidence) {
	a := e.f.Actions
	open := append([]string(nil), a.MismatchOpen...)
	sort.Strings(open)
	ev := Evidence{"mismatches": a.Mismatch, "followed_up": a.MismatchFollowed, "open": strings.Join(open, ", ")}
	if a.Mismatch == 0 {
		return NotApplicable, ev
	}
	return grade(a.Mismatch-a.MismatchFollowed, a.Mismatch), ev
}

func ruleWatched(e *env) (Status, Evidence) {
	pg, watched := 0, 0
	for _, c := range e.f.Connectors {
		if c.Type == "postgres" {
			pg++
			if c.Observe {
				watched++
			}
		}
	}
	s := e.f.Sessions
	ev := Evidence{"postgres_connectors": pg, "watched": watched, "sessions": s.Total, "caught": s.Caught}
	if pg == 0 {
		return NotApplicable, ev
	}
	return grade(pg-watched, pg), ev
}

func ruleUndoAvailable(e *env) (Status, Evidence) {
	var writes, undo int
	var without []string
	for _, c := range e.f.Connectors {
		if !c.Writes {
			continue
		}
		writes++
		if c.Undo {
			undo++
		} else {
			without = append(without, c.Name)
		}
	}
	sort.Strings(without)
	ev := Evidence{"write_connectors": writes, "undo_connectors": undo, "without_undo": strings.Join(without, ", ")}
	if writes == 0 {
		return NotApplicable, ev
	}
	return grade(writes-undo, writes), ev
}

func ruleUndosDone(e *env) (Status, Evidence) {
	u := e.f.Undos
	ev := Evidence{"undos": u.Actions, "matched": u.Matched, "session_undos": u.SessionsDone}
	switch {
	case u.Actions == 0 && u.Sessions == 0:
		return NotApplicable, ev
	case u.Matched < u.Actions || u.SessionsDone < u.Sessions:
		return Partly, ev
	}
	return InPlace, ev
}

func ruleSecretsEncrypted(e *env) (Status, Evidence) {
	var n, stale int
	algs := map[string]bool{}
	for _, c := range e.f.Connectors {
		cr := c.Credential
		if cr == nil {
			continue
		}
		n++
		algs[cr.Alg] = true
		last := cr.UpdatedAt
		if cr.RotatedAt != nil && cr.RotatedAt.After(last) {
			last = *cr.RotatedAt
		}
		if e.f.Now.Sub(last) > CredentialMaxAge {
			stale++
		}
	}
	names := make([]string, 0, len(algs))
	for a := range algs {
		names = append(names, a)
	}
	sort.Strings(names)
	ev := Evidence{"credentials": n, "alg": strings.Join(names, ", "), "stale": stale}
	switch {
	case n == 0:
		return NotApplicable, ev
	case len(names) != 1 || names[0] != VaultAlg:
		// The schema allows only one algorithm; anything else means the facts are wrong.
		return CantTell, ev
	case stale > 0:
		return Partly, ev
	}
	return InPlace, ev
}

// VaultAlg is the only credential algorithm the vault writes (migration 0010's CHECK).
const VaultAlg = "A256GCM-ENV-1"

func ruleNeverSeeSecrets(e *env) (Status, Evidence) {
	a := e.f.Actions
	ev := Evidence{"gateway_actions": a.Gateway, "proxy_actions": a.Proxy}
	if a.Gateway+a.Proxy == 0 {
		return NotApplicable, ev
	}
	return grade(a.Proxy, a.Gateway+a.Proxy), ev
}

func ruleEgress(e *env) (Status, Evidence) {
	g := e.f.Egress
	if g == nil {
		return CantTell, Evidence{}
	}
	private := "refused"
	if g.Env == "dev" && g.AllowPrivate {
		private = "allowed in dev"
	} else if g.AllowCIDRs > 0 {
		private = "listed ranges only"
	}
	ev := Evidence{"env": g.Env, "private_networks": private, "allowed_ranges": g.AllowCIDRs}
	if private != "refused" {
		return Partly, ev
	}
	return InPlace, ev
}

func ruleRecordsSealed(e *env) (Status, Evidence) {
	r, a := e.f.Records, e.f.Actions
	ev := Evidence{"deeds_checked": int(r.Checked), "chain_head": r.ChainHead, "finished": a.Finished, "unsealed": a.Unsealed}
	if !r.ChainIntact {
		ev["chain"] = r.FirstBroken
		return NotSetUp, ev
	}
	ev["chain"] = "intact"
	if a.Unsealed > 0 {
		return Partly, ev
	}
	return InPlace, ev
}

func ruleRecordsVerifiable(e *env) (Status, Evidence) {
	r := e.f.Records
	revoked := 0
	ids := make([]string, 0, len(r.SealKeys))
	for _, k := range r.SealKeys {
		ids = append(ids, k.ID)
		if k.Status == "revoked" {
			revoked++
		}
	}
	ev := Evidence{"seal_keys": strings.Join(ids, ", "), "signer": r.Signer, "revoked": revoked}
	switch {
	case r.Signer == "":
		return NotSetUp, ev
	case revoked > 0:
		return Partly, ev
	}
	return InPlace, ev
}

func ruleProductTested(e *env) (Status, Evidence) {
	b := e.f.Build
	ev := Evidence{"running": b.Version}
	if b.Attack == nil {
		return NotApplicable, ev
	}
	ev["tested_version"], ev["matched"], ev["differs"], ev["run_on"] = b.Attack.Version, b.Attack.Matched, b.Attack.Differs, b.Attack.RunOn
	switch {
	case b.Attack.Version != b.Version || b.Attack.Differs > 0 || b.Attack.Waiting > 0:
		return Partly, ev
	}
	return InPlace, ev
}
