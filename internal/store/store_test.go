package store

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// goFiles returns every non-test Go file under the repo's internal/ and cmd/ trees.
func goFiles(t *testing.T, tests bool) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, root := range []string{"../../internal", "../../cmd"} {
		err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(p, ".go") || (!tests && strings.HasSuffix(p, "_test.go")) {
				return err
			}
			b, err := os.ReadFile(p)
			out[filepath.ToSlash(strings.TrimPrefix(p, "../../"))] = string(b)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	return out
}

// Settings that RLS keys on are always transaction-local, so a pooled connection never carries
// them into the next transaction.
func TestSetConfigIsTransactionLocal(t *testing.T) {
	sessionWide := regexp.MustCompile(`set_config\([^)]*,\s*false\s*\)`)
	rawSet := regexp.MustCompile(`(?i)\bSET\s+(SESSION\s+)?"?deedglass\.`)
	for p, src := range goFiles(t, false) {
		if sessionWide.MatchString(src) {
			t.Errorf("%s: set_config(…, false) makes a setting session-wide; use true", p)
		}
		if rawSet.MatchString(src) {
			t.Errorf("%s: SET deedglass.… is session-wide; use set_config(…, true) inside a transaction", p)
		}
	}
}

// Every SystemTx call outside tests is listed here, so a new cross-org path shows up in review.
// Pre-auth lookups and worker claims use SECURITY DEFINER functions instead (docs/DATA_MODEL.md).
var systemTxCallSites = []string{
	"internal/store/repo.go: SystemTx(ctx, ReasonOrgCreate",                      // CreateOrg
	"internal/store/storetest/seed.go: SystemTx(ctx, store.ReasonMaintenance",    // test harness (integration tag only)
	"cmd/deedglass/devseed.go: SystemTx(ctx, store.ReasonMaintenance",            // dev seed: find the demo org by slug (dev only)
	"cmd/deedglass/devseed.go: SystemTx(ctx, store.ReasonMaintenance",            // dev seed: list orgs to enforce default-deny where none is (dev only, S12)
	"internal/store/idempotency.go: SystemTx(ctx, ReasonMaintenance",             // PruneIdempotencyKeys (S03)
	"cmd/deedglass/ledgercmd.go: SystemTx(ctx, store.ReasonMaintenance",          // DB-direct Ledger commands: list orgs to pick --org (S06)
	"cmd/deedglass/fakeaction.go: SystemTx(ctx, store.ReasonMaintenance",         // dev fake-action: find the demo org by slug (dev only, S06)
	"internal/identity/org.go: SystemTx(ctx, store.ReasonOrgCreate",              // deedglass org create: org, owner, org.created deed, hooks (S07)
	"internal/store/policystore/usage.go: SystemTx(ctx, store.ReasonMaintenance", // Usage.Prune: drops old usage_counters windows across orgs (S11)
	"cmd/deedglass/devseed_demo.go: SystemTx(ctx, store.ReasonMaintenance",       // dev seed --demo: find the acme-demo org by slug (dev only, S15)
	"cmd/deedglass/devseed_demo.go: SystemTx(ctx, store.ReasonMaintenance",       // dev seed --demo --approver: upsert the approver's user row (users is global; dev only, S22)
	"internal/store/connectors.go: SystemTx(ctx, ReasonMaintenance",              // OrgIDs: deedglass vault status|rotate walk orgs, one org tx each (S16)
}

func TestSystemTxCallSites(t *testing.T) {
	call := regexp.MustCompile(`\.SystemTx\(ctx, [A-Za-z.]+`)
	var got []string
	for p, src := range goFiles(t, false) {
		for _, m := range call.FindAllString(src, -1) {
			got = append(got, p+": "+strings.TrimPrefix(m, "."))
		}
	}
	sort.Strings(got)
	want := append([]string(nil), systemTxCallSites...)
	sort.Strings(want)
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("SystemTx call sites changed.\ngot:\n  %s\nwant:\n  %s\nA new cross-org path needs review: add it to systemTxCallSites with its reason, or use a SECURITY DEFINER function.",
			strings.Join(got, "\n  "), strings.Join(want, "\n  "))
	}
}

func TestClampLimit(t *testing.T) {
	for in, want := range map[int]int{0: 20, -5: 1, 1: 1, 50: 50, 100: 100, 101: 100, 1 << 30: 100} {
		if got := ClampLimit(in); got != want {
			t.Errorf("ClampLimit(%d) = %d, want %d", in, got, want)
		}
	}
}

func TestRoleReport(t *testing.T) {
	if r := (RoleReport{User: "deedglass_app"}); !r.Clean() || len(r.Problems()) != 0 {
		t.Error("clean")
	}
	r := RoleReport{User: "postgres", Superuser: true, BypassRLS: true, OwnsSchema: true}
	if r.Clean() || len(r.Problems()) != 3 || !strings.Contains(r.Problems()[0], "Role postgres is a superuser") {
		t.Errorf("%v", r.Problems())
	}
	if !(SystemReason("org_create")).valid() || SystemReason("x").valid() {
		t.Error("reasons")
	}
}
