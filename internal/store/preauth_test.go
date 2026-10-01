package store

import (
	"regexp"
	"strings"
	"testing"
)

// The pre-auth, maintenance and claim functions (migrations 0012, 0014, 0016) and setting the RLS
// org are the only
// ways across row-level security, so they are named in internal/store and nowhere else: every
// other package goes through store.PreAuth, OrgTx or SystemTx. The test also checks it still finds
// them, so it can't pass by matching nothing.
func TestPreAuthNamesStayInStore(t *testing.T) {
	re := regexp.MustCompile(`\b(preauth_(session|login_token|user_by_email|code_token|api_key)|maint_prune_(identity|intent_nonces)|maint_release_action_keys|claim_(witness_jobs|stale_actions))\b|set_config\('deedglass\.org_id'`)
	found := 0
	for p, src := range goFiles(t, false) {
		if !re.MatchString(src) {
			continue
		}
		if strings.HasPrefix(p, "internal/store/") {
			found++
			continue
		}
		t.Errorf("%s names a pre-auth function or sets deedglass.org_id; go through store.PreAuth or store.OrgTx", p)
	}
	if found < 2 {
		t.Fatalf("found the names in %d store files; the pattern no longer matches anything", found)
	}
}
