//go:build attack

package attack

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/deedglass/deedglass/attack/harness"
)

// The attack suite (S18): go test -tags attack ./attack/... on a throwaway cluster (make attack).
// One server boots for the whole run; TestAttack runs the declared cases in meta order, and
// TestOrgIsolation sweeps the route table across two orgs.

var (
	bootOnce sync.Once
	env      *harness.Env
	bootErr  error
	started  time.Time
	allCases []harness.Case
	results  []harness.Result

	resultsLeaked bool
)

func register(cs ...harness.Case) { allCases = append(allCases, cs...) }

func TestMain(m *testing.M) {
	started = time.Now().UTC()
	code := m.Run()
	if env != nil {
		writeResults()
		env.Close()
	}
	if resultsLeaked && code == 0 {
		code = 1
	}
	os.Exit(code)
}

func boot(t *testing.T) *harness.Env {
	t.Helper()
	bootOnce.Do(func() { env, bootErr = harness.Boot() })
	if bootErr != nil {
		t.Fatal(bootErr)
	}
	return env
}

func TestAttack(t *testing.T) {
	e := boot(t)
	meta, err := harness.LoadMeta("attack_meta.json")
	if err != nil {
		t.Fatal(err)
	}
	r := &harness.Runner{Env: e, Meta: meta}
	byID := map[string]harness.Case{}
	for _, c := range allCases {
		if _, dup := byID[c.ID]; dup {
			t.Fatalf("case %s is registered twice", c.ID)
		}
		byID[c.ID] = c
	}
	for _, mc := range meta.Cases {
		if !strings.Contains("HJKW", mc.Group) {
			continue // V0 groups run in their own suites (deed_test.go, deed_verify_test.go, S34)
		}
		c, ok := byID[mc.ID]
		if !ok {
			t.Errorf("%s is declared but has no case code", mc.ID)
			continue
		}
		r.RunCase(t, c)
	}
	for id := range byID {
		if meta.Case(id) == nil {
			t.Errorf("case %s isn't declared in attack_meta.json", id)
		}
	}
	results = r.Results()
}

// writeResults writes attack/results/latest.json once the run is over.
func writeResults() {
	if len(results) == 0 {
		return
	}
	n, where := env.SentinelHits()
	if n > 0 {
		fmt.Fprintf(os.Stderr, "attack: the sentinel appeared %d times at the end of the run (%s)\n", n, where)
	}
	chains := "intact"
	for _, o := range env.Orgs {
		if harness.Str(o.Admin.Do("GET", "/v1/ledger/check", nil).Map(), "status") != "intact" {
			chains = "broken"
		}
	}
	run := harness.Run{Object: "attack_run", StartedAt: started.Format(time.RFC3339), Commit: commit(), Postgres: env.PGVersion,
		BootMS: env.BootMS, Cases: results, Summary: harness.Summarize(results),
		Invariants: harness.Invariants{Chains: chains, SecretsSeen: n, LogLines: env.Logs.Lines(), RowsScanned: env.RowsScanned(),
			BodiesSeen: len(env.Bodies.Snapshot())}}
	// The results are swept too: they must not carry the planted credential.
	if raw, err := json.Marshal(run); err == nil && harness.CountSentinel(raw, env.Sentinel) > 0 {
		fmt.Fprintln(os.Stderr, "attack: the results themselves carry the planted credential; they are not written")
		resultsLeaked = true
		return
	}
	path := filepath.Join("results", "latest.json")
	if err := harness.WriteRun(path, run); err != nil {
		fmt.Fprintf(os.Stderr, "attack: write %s: %v\n", path, err)
	}
}

func commit() string {
	out, err := exec.Command("git", "rev-parse", "--short=7", "HEAD").Output()
	if err != nil {
		return "uncommitted"
	}
	if st, _ := exec.Command("git", "status", "--porcelain").Output(); len(st) > 0 {
		return strings.TrimSpace(string(out)) + "+uncommitted"
	}
	return strings.TrimSpace(string(out))
}

// TestInvariantsCatch: the universal invariants can fail (a planted secret, a changed target).
func TestInvariantsCatch(t *testing.T) {
	boot(t).InvariantSelfTest(t)
}
