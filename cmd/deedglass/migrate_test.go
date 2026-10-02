package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/deedglass/deedglass/internal/config"
	"github.com/deedglass/deedglass/internal/store/migrate"
	"github.com/deedglass/deedglass/internal/store/migrations"
)

// fakeMigrator replays canned results through the same callbacks the real runner uses.
type fakeMigrator struct {
	ms        []migrate.Migration
	applied   int // how many are applied before Up
	changed   int // a version whose checksum differs (0 = none)
	ahead     int // an unknown applied version (0 = none)
	upErr     error
	warnings  []string
	gotURL    string
	gotOpts   migrate.UpOptions
	upCalls   int
	durations []time.Duration
}

var appliedAt = time.Date(2026, 10, 1, 11, 2, 0, 0, time.UTC)

func (f *fakeMigrator) Target() Target {
	return Target{Host: "127.0.0.1", Port: 55432, Database: "deedglass", User: "deedglass_migrator"}
}

func (f *fakeMigrator) Status(context.Context) ([]migrate.Row, error) {
	var rows []migrate.Row
	for _, m := range f.ms {
		r := migrate.Row{Version: m.Version, Name: m.Name, State: migrate.StatePending, Checksum: m.Checksum}
		if m.Version <= f.applied {
			r.State, r.AppliedAt, r.AppliedChecksum = migrate.StateApplied, appliedAt, m.Checksum
			if m.Version == f.changed {
				r.State, r.AppliedChecksum = migrate.StateChanged, "sha256:3f9a"+strings.Repeat("0", 56)+"c21e"
			}
		}
		rows = append(rows, r)
	}
	if f.ahead > 0 {
		rows = append(rows, migrate.Row{Version: f.ahead, Name: "future", State: migrate.StateUnknown, AppliedAt: appliedAt, AppliedChecksum: "sha256:" + strings.Repeat("e", 64)})
	}
	return rows, nil
}

func (f *fakeMigrator) Verify(ctx context.Context) error {
	rows, _ := f.Status(ctx)
	for _, r := range rows {
		switch r.State {
		case migrate.StateChanged:
			return &migrate.ChecksumError{Version: r.Version, Name: r.Name, Applied: r.AppliedChecksum, File: r.Checksum}
		case migrate.StateUnknown:
			return &migrate.AheadError{Version: r.Version, Known: len(f.ms)}
		}
	}
	return nil
}

func (f *fakeMigrator) Up(ctx context.Context, o migrate.UpOptions) (migrate.Result, error) {
	f.gotOpts = o
	f.upCalls++
	if f.upErr != nil {
		return migrate.Result{}, f.upErr
	}
	if err := f.Verify(ctx); err != nil {
		return migrate.Result{}, err
	}
	res := migrate.Result{Current: f.applied, Warnings: f.warnings}
	pending := f.ms[f.applied:]
	if o.DryRun {
		res.Pending = pending
		return res, nil
	}
	if o.OnStart != nil {
		o.OnStart(pending)
	}
	for i, m := range pending {
		d := 11 * time.Millisecond
		if i < len(f.durations) {
			d = f.durations[i]
		}
		a := migrate.Applied{Migration: m, Duration: d}
		res.Applied = append(res.Applied, a)
		res.Duration += d
		res.Current = m.Version
		if o.OnApply != nil {
			o.OnApply(a)
		}
	}
	res.Duration += 1 * time.Millisecond
	return res, nil
}

func (f *fakeMigrator) Close() {}

func withFake(t *testing.T, f *fakeMigrator, openErr error) {
	t.Helper()
	ms, err := migrate.Load(migrations.FS)
	if err != nil {
		t.Fatal(err)
	}
	f.ms = ms
	saved := openMigrator
	openMigrator = func(_ context.Context, url config.Secret) (migrator, error) {
		f.gotURL = url.Reveal()
		if openErr != nil {
			return nil, openErr
		}
		return f, nil
	}
	t.Cleanup(func() { openMigrator = saved })
}

const migURL = "postgres://deedglass_migrator:mig-secret@127.0.0.1:55432/deedglass"
const appURL = "postgres://deedglass_app:app-secret@127.0.0.1:55432/deedglass"

func TestMigrateGoldens(t *testing.T) {
	env := func() map[string]string { return map[string]string{"DEEDGLASS_MIGRATE_DATABASE_URL": migURL} }
	durations := []time.Duration{14, 38, 21, 9, 52, 33, 17, 12, 11}
	for i := range durations {
		durations[i] *= time.Millisecond
	}
	cases := []struct {
		name    string
		f       fakeMigrator
		openErr error
		env     map[string]string
		tty     bool
		args    []string
	}{
		{name: "migrate_up_fresh", f: fakeMigrator{durations: durations}, args: []string{"migrate", "up"}},
		{name: "migrate_up_fresh_color", f: fakeMigrator{durations: durations}, tty: true, args: []string{"migrate", "up"}},
		{name: "migrate_up_current", f: fakeMigrator{applied: 9}, args: []string{"migrate", "up"}},
		{name: "migrate_up_one", f: fakeMigrator{applied: 8}, args: []string{"migrate", "up"}},
		{name: "migrate_up_dry_run", f: fakeMigrator{applied: 6}, args: []string{"migrate", "up", "--dry-run"}},
		{name: "migrate_up_dry_run_current", f: fakeMigrator{applied: 9}, args: []string{"migrate", "up", "--dry-run"}},
		{name: "migrate_up_json", f: fakeMigrator{applied: 7, durations: durations}, args: []string{"migrate", "up", "--json"}},
		{name: "migrate_up_dev_fallback", f: fakeMigrator{applied: 9, warnings: []string{"Migrating as a superuser. Dev allows it; prod refuses. Run migrations as deedglass_migrator."}},
			env: map[string]string{"DEEDGLASS_DATABASE_URL": appURL}, args: []string{"migrate", "up"}},
		{name: "migrate_up_prod_no_url", env: map[string]string{"DEEDGLASS_ENV": "prod", "DEEDGLASS_DATABASE_URL": appURL,
			"DEEDGLASS_BASE_URL": "https://dg.example.test", "DEEDGLASS_VAULT_KEY": key32, "DEEDGLASS_API_KEY_PEPPER": key32}, args: []string{"migrate", "up"}},
		{name: "migrate_up_not_control_plane", f: fakeMigrator{upErr: &migrate.NotControlPlaneError{Found: "deedglass.ledger"}}, args: []string{"migrate", "up"}},
		{name: "migrate_up_missing_role", f: fakeMigrator{upErr: migrate.ErrMissingRole}, args: []string{"migrate", "up"}},
		{name: "migrate_up_too_old", f: fakeMigrator{upErr: &migrate.ServerTooOldError{Version: "15.8"}}, args: []string{"migrate", "up"}},
		{name: "migrate_up_locked", f: fakeMigrator{upErr: migrate.ErrLockTimeout}, args: []string{"migrate", "up"}},
		{name: "migrate_up_superuser_prod", f: fakeMigrator{upErr: migrate.ErrSuperuser}, args: []string{"migrate", "up"}},
		{name: "migrate_up_failed", f: fakeMigrator{upErr: fmt.Errorf("migration 0004_connectors.sql failed: %w", errors.New("ERROR: relation \"deedglass.orgs\" does not exist (SQLSTATE 42P01)"))}, args: []string{"migrate", "up"}},
		{name: "migrate_up_unreachable", openErr: &connectError{target: Target{Host: "127.0.0.1", Port: 55432, User: "deedglass_migrator"}, reason: "connection refused"}, args: []string{"migrate", "up"}},
		{name: "migrate_up_unreachable_json", openErr: &connectError{target: Target{Host: "127.0.0.1", Port: 55432, User: "deedglass_migrator"}, reason: "connection refused"}, args: []string{"migrate", "up", "--json"}},
		{name: "migrate_status_mixed", f: fakeMigrator{applied: 8}, args: []string{"migrate", "status"}},
		{name: "migrate_status_current", f: fakeMigrator{applied: 9}, args: []string{"migrate", "status"}},
		{name: "migrate_status_changed", f: fakeMigrator{applied: 9, changed: 3}, tty: true, args: []string{"migrate", "status"}},
		{name: "migrate_status_json", f: fakeMigrator{applied: 8, ahead: 99}, args: []string{"migrate", "status", "--json"}},
		{name: "migrate_verify_ok", f: fakeMigrator{applied: 9}, args: []string{"migrate", "verify"}},
		{name: "migrate_verify_changed", f: fakeMigrator{applied: 9, changed: 3}, args: []string{"migrate", "verify"}},
		{name: "migrate_verify_changed_json", f: fakeMigrator{applied: 9, changed: 3}, args: []string{"migrate", "verify", "--json"}},
		{name: "migrate_verify_ahead", f: fakeMigrator{applied: 9, ahead: 99}, args: []string{"migrate", "verify"}},
		{name: "migrate_verify_args", args: []string{"migrate", "verify", "extra"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := c.f
			withFake(t, &f, c.openErr)
			e := c.env
			if e == nil {
				e = env()
			}
			r := cli(t, e, c.tty, c.args...)
			golden(t, c.name, r)
			if strings.Contains(r.stdout+r.stderr, "secret") {
				t.Error("a password reached the output")
			}
		})
	}
}

func TestMigrateURLChoice(t *testing.T) {
	f := &fakeMigrator{applied: 9}
	withFake(t, f, nil)
	r := cli(t, map[string]string{"DEEDGLASS_MIGRATE_DATABASE_URL": migURL, "DEEDGLASS_DATABASE_URL": appURL}, false, "migrate", "up", "--database-url", "postgres://x:y@127.0.0.1:1/z")
	if r.code != 0 || f.gotURL != "postgres://x:y@127.0.0.1:1/z" {
		t.Fatalf("%d %q %s", r.code, f.gotURL, r.stderr)
	}
	cli(t, map[string]string{"DEEDGLASS_MIGRATE_DATABASE_URL": migURL, "DEEDGLASS_DATABASE_URL": appURL}, false, "migrate", "status")
	if f.gotURL != migURL {
		t.Fatalf("%q", f.gotURL)
	}
	// The dry-run flag doesn't leak into the next run, and prod is passed to the runner.
	cli(t, map[string]string{"DEEDGLASS_MIGRATE_DATABASE_URL": migURL}, false, "migrate", "up", "--dry-run")
	cli(t, map[string]string{"DEEDGLASS_MIGRATE_DATABASE_URL": migURL}, false, "migrate", "up")
	if f.gotOpts.DryRun || f.gotOpts.Env != config.Dev || f.gotOpts.AppVersion != "0.1.0-dev" {
		t.Errorf("%+v", f.gotOpts)
	}
	r = cli(t, map[string]string{"DEEDGLASS_MIGRATE_DATABASE_URL": migURL}, false, "migrate", "status", "--dry-run")
	if r.code != ExitUsage {
		t.Errorf("status --dry-run: %d", r.code)
	}
}

func TestMigrateJSONShapes(t *testing.T) {
	f := &fakeMigrator{applied: 7}
	withFake(t, f, nil)
	r := cli(t, map[string]string{"DEEDGLASS_MIGRATE_DATABASE_URL": migURL}, false, "migrate", "up", "--json")
	var up struct {
		Object  string `json:"object"`
		Current int    `json:"current"`
		Applied []struct {
			Object   string `json:"object"`
			Version  int    `json:"version"`
			Checksum string `json:"checksum"`
		} `json:"applied"`
		Target Target `json:"target"`
	}
	if err := json.Unmarshal([]byte(r.stdout), &up); err != nil || up.Object != "migration_run" || up.Current != 19 || len(up.Applied) != 12 ||
		up.Applied[0].Version != 8 || !strings.HasPrefix(up.Applied[0].Checksum, "sha256:") || up.Target.User != "deedglass_migrator" {
		t.Fatalf("%v %s", err, r.stdout)
	}
}

func TestShortSumAndReason(t *testing.T) {
	if got := shortSum("sha256:3f9a" + strings.Repeat("0", 56) + "c21e"); got != "sha256:3f9a…c21e" {
		t.Error(got)
	}
	if shortSum("x") != "x" {
		t.Error("short input")
	}
	err := errors.New("failed to connect to `user=deedglass_migrator database=deedglass`: 127.0.0.1:1 (127.0.0.1): dial error: dial tcp 127.0.0.1:1: connect: connection refused")
	if got := connectReason(err, ""); got != "connection refused" {
		t.Error(got)
	}
	if got := connectReason(errors.New("password hunter2 rejected: nope"), "hunter2"); strings.Contains(got, "hunter2") {
		t.Error(got)
	}
	if got := connectReason(errors.New("the password hunter2 is wrong"), "hunter2"); got != "the password **** is wrong" {
		t.Error(got)
	}
}

// The real opener refuses a bad URL without echoing it, and reports an unreachable server with its
// host, port and user, never the password.
func TestOpenMigratorReal(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := openMigrator(ctx, config.NewSecret("postgres://%zz")); err == nil || strings.Contains(err.Error(), "%zz") {
		t.Errorf("bad URL: %v", err)
	}
	_, err := openMigrator(ctx, config.NewSecret("postgres://deedglass_migrator:pw-canary@127.0.0.1:1/deedglass?connect_timeout=2"))
	var ce *connectError
	if !errors.As(err, &ce) || strings.Contains(err.Error(), "pw-canary") || !strings.Contains(err.Error(), "127.0.0.1:1 as deedglass_migrator") {
		t.Errorf("unreachable: %v", err)
	}
}
