package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"

	demopolicies "github.com/deedglass/deedglass/examples/demo/policies"
	"github.com/deedglass/deedglass/internal/config"
	"github.com/deedglass/deedglass/internal/identity"
	"github.com/deedglass/deedglass/internal/policy"
	"github.com/deedglass/deedglass/internal/store"
	"github.com/deedglass/deedglass/internal/store/policystore"
)

// deedglass dev seed --demo (S15): the Acme demo org, idempotent. Org "Acme Demo" (acme-demo),
// owner founder@example.test, agents claude-desktop, claude-code and cursor with one dg_test_ key
// each (shown once: written to --keys-file, mode 0600, git-ignored), and the acme-demo policy
// enforced with its activation deed.

var devSeedValues struct {
	demo     bool
	keysFile string
}

func devSeedFlags(fs *flag.FlagSet) {
	devSeedValues.demo, devSeedValues.keysFile = false, ""
	fs.BoolVar(&devSeedValues.demo, "demo", false, "load the Acme demo org, agents, keys and policy instead (S15)")
	fs.StringVar(&devSeedValues.keysFile, "keys-file", filepath.Join("examples", "demo", ".demo-keys"), "with --demo: where the agents' keys are written (0600)")
}

// The demo org, agents and the reason on the activation deed.
var (
	demoOrg    = struct{ Name, Slug, Owner string }{"Acme Demo", "acme-demo", "founder@example.test"}
	demoAgents = []struct{ Name, Description string }{
		{"claude-desktop", "Claude Desktop, for the Acme demo."},
		{"claude-code", "Claude Code, for the Acme demo."},
		{"cursor", "Cursor, for the Acme demo."},
	}
)

const demoActivateReason = "Acme demo (deedglass dev seed --demo)"

func runDevSeedDemo(ctx context.Context, e *Env) error {
	cfg, db, led, people, done, err := openPeople(ctx, e)
	if err != nil {
		return err
	}
	defer done()
	if cfg.Env == config.Prod {
		return &ExitError{Code: ExitFailure, ErrCode: "dev_only", Err: errors.New("deedglass dev seed --demo is for dev databases only"),
			Next: "Unset DEEDGLASS_ENV=prod, and point DEEDGLASS_DATABASE_URL at the demo's database."}
	}
	// 1. The org (its creation seals org.created and enforces default-deny).
	var org store.Org
	createdOrg := false
	err = db.SystemTx(ctx, store.ReasonMaintenance, func(ctx context.Context, tx pgx.Tx) error {
		org, err = store.GetOrgBySlug(ctx, tx, demoOrg.Slug)
		return err
	})
	if errors.Is(err, store.ErrNotFound) {
		c, cerr := identity.CreateOrg(ctx, db, led, demoOrg.Name, demoOrg.Slug, demoOrg.Owner)
		if cerr != nil {
			return seedErr(cerr)
		}
		org, createdOrg = c.Org, true
	} else if err != nil {
		return seedErr(err)
	}
	// 2. The agents and their keys. A key is shown once, so an agent already in the keys file keeps
	// its key; one missing from it gets a new key.
	keys, err := readDemoKeys(devSeedValues.keysFile)
	if err != nil {
		return err
	}
	createdAgents, createdKeys := 0, 0
	for _, a := range demoAgents {
		var id string
		err := db.OrgTx(ctx, org.ID, func(ctx context.Context, tx pgx.Tx) error {
			ag, err := store.GetAgentByName(ctx, tx, org.ID, a.Name)
			if err == nil {
				id = ag.ID.String()
			}
			return err
		})
		if errors.Is(err, store.ErrNotFound) {
			r, cerr := people.CreateAgent(ctx, identity.Operator, org.ID, identity.CreateAgentInput{Name: a.Name, Description: a.Description,
				Labels: map[string]string{"demo": "acme"}})
			if cerr != nil {
				return seedErr(cerr)
			}
			id, createdAgents = r.ID, createdAgents+1
		} else if err != nil {
			return seedErr(err)
		}
		if keys[a.Name] != "" {
			continue
		}
		k, kerr := people.CreateAgentKey(ctx, identity.Operator, org.ID, id, identity.CreateKeyInput{Name: "acme-demo", Mode: identity.ModeTest})
		if kerr != nil || k.Secret == nil {
			return seedErr(fmt.Errorf("a key for %s: %v", a.Name, kerr))
		}
		keys[a.Name] = *k.Secret
		createdKeys++
	}
	if createdKeys > 0 {
		if err := writeDemoKeys(devSeedValues.keysFile, keys); err != nil {
			return &ExitError{Code: ExitFailure, ErrCode: "keys_file", Err: fmt.Errorf("The keys couldn't be saved to %s: %v", devSeedValues.keysFile, err),
				Next: "Check the folder exists and is writable, then run make demo-reset HARD=1."}
		}
	}
	// 3. The policy: push acme-demo (a new version only when the file changed) and enforce it.
	svc := policy.NewService(policystore.NewRepo(db, led))
	caller := policy.Caller{Org: org.ID.String(), Actor: policy.SystemActor}
	req := policy.PushRequest{Source: demopolicies.AcmeDemo, Origin: policy.OriginManual, Notes: "The Acme demo policy (examples/demo/policies/acme-demo.yaml)."}
	version := 0
	if _, err := svc.Get(ctx, caller, "acme-demo"); err == nil {
		v, _, aerr := svc.AddVersion(ctx, caller, "acme-demo", req)
		if aerr != nil {
			return seedErr(aerr)
		}
		version = v.Version
	} else {
		_, v, cerr := svc.Create(ctx, caller, req)
		if cerr != nil {
			return seedErr(cerr)
		}
		version = v.Version
	}
	activated := false
	p, err := svc.Get(ctx, caller, "acme-demo")
	if err != nil {
		return seedErr(err)
	}
	if p.ActiveVersion == nil || *p.ActiveVersion != version {
		if _, err := svc.Activate(ctx, caller, "acme-demo", policy.VersionRef{Number: version},
			policy.ActivateRequest{Mode: policy.ModeEnforce, Reason: demoActivateReason}); err != nil {
			return seedErr(err)
		}
		activated = true
	}
	if e.Globals.JSON {
		return writeJSON(e, map[string]any{"object": "demo_seed", "org": org.ID.String(), "slug": org.Slug, "policy_version": version,
			"created":   map[string]any{"org": createdOrg, "agents": createdAgents, "keys": createdKeys, "activated": activated},
			"keys_file": devSeedValues.keysFile})
	}
	s := e.Style
	names := make([]string, len(demoAgents))
	for i, a := range demoAgents {
		names[i] = a.Name
	}
	fmt.Fprintln(e.Stdout, s.OKLine(fmt.Sprintf("org %s %s policy acme-demo v%d active (enforce)", org.Slug, "·", version)))
	keysNote := "keys saved to " + devSeedValues.keysFile + " (0600)"
	if createdKeys == 0 {
		keysNote = "keys already in " + devSeedValues.keysFile
	}
	fmt.Fprintln(e.Stdout, s.OKLine("agents "+strings.Join(names, ", ")+" · "+keysNote))
	return nil
}

// readDemoKeys reads NAME=dg_test_… lines; a missing file is empty.
func readDemoKeys(path string) (map[string]string, error) {
	out := map[string]string{}
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return out, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if k, v, ok := strings.Cut(line, "="); ok && strings.HasPrefix(v, "dg_") {
			out[k] = v
		}
	}
	return out, sc.Err()
}

// writeDemoKeys writes the keys file with mode 0600, atomically.
func writeDemoKeys(path string, keys map[string]string) error {
	names := make([]string, 0, len(keys))
	for k := range keys {
		names = append(names, k)
	}
	sort.Strings(names)
	var b strings.Builder
	b.WriteString("# Acme demo agent keys (deedglass dev seed --demo). Local test keys: never commit or share them.\n")
	for _, n := range names {
		b.WriteString(n + "=" + keys[n] + "\n")
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(b.String()), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
