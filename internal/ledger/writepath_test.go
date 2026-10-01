package ledger

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// One write path (S06 §7, "must never happen"): outside tests, only internal/ledger writes
// action_events and admin_events, only internal/ledger and the Sealer's ChainStore
// (internal/store/deeds.go, called through ledger) insert deeds, and only ledger.Publish notifies
// deedglass_events. The identity channel (S08), the policy channel (S12) and the jobs channel (S13)
// have their own doors, store.NotifyIdentity, store.NotifyPolicy and store.NotifyJobs, in
// internal/store/notify.go, which may name no other channel.
func TestOneWritePath(t *testing.T) {
	root := filepath.Join("..", "..")
	writes := regexp.MustCompile(`(?i)(INSERT\s+INTO|UPDATE|DELETE\s+FROM)\s+deedglass\.(action_events|admin_events|deeds)\b`)
	notify := regexp.MustCompile(`pg_notify\(`)
	allowedWrites := []string{"internal/ledger/write.go", "internal/store/deeds.go"}
	allowedNotify := []string{"internal/ledger/events.go", "internal/store/notify.go"}
	channel := regexp.MustCompile(`pg_notify\('([a-z_]+)'`)
	seen := map[string]bool{}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			if rel != "." && strings.HasPrefix(d.Name(), ".") || d.Name() == "node_modules" || rel == "web" || rel == "testdata" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") || strings.HasPrefix(rel, "internal/store/storetest/") {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		seen[rel] = seen[rel] || writes.Match(b) || notify.Match(b)
		if writes.Match(b) && !slices.Contains(allowedWrites, rel) {
			t.Errorf("%s writes a Ledger table; only internal/ledger may (S06)", rel)
		}
		if notify.Match(b) && !slices.Contains(allowedNotify, rel) {
			t.Errorf("%s calls pg_notify; use ledger.Publish (or store.NotifyIdentity / store.NotifyPolicy)", rel)
		}
		if rel == "internal/store/notify.go" {
			for _, m := range channel.FindAllSubmatch(b, -1) {
				if string(m[1]) != "deedglass_identity" && string(m[1]) != "deedglass_policy" && string(m[1]) != "deedglass_jobs" {
					t.Errorf("%s notifies %s; it is the door for deedglass_identity, deedglass_policy and deedglass_jobs only", rel, m[1])
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range append(allowedWrites, allowedNotify...) {
		if !seen[f] {
			t.Errorf("the guard saw no write in %s: the pattern no longer matches the code", f)
		}
	}
}
