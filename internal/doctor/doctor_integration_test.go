//go:build integration

package doctor

import (
	"context"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/deedglass/deedglass/internal/store/migrate"
	"github.com/deedglass/deedglass/internal/store/migrations"
	"github.com/deedglass/deedglass/internal/store/storetest"
)

// TestDoctorReadOnly: the database checks on a real control plane, with log_statement=all on the
// runtime role: the server log holds no INSERT, UPDATE, DELETE or DDL from doctor's sessions.
func TestDoctorReadOnly(t *testing.T) {
	e := storetest.New(t, storetest.Options{})
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, e.AdminURL)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close(ctx)
	ms, err := migrate.Load(migrations.FS)
	if err != nil {
		t.Fatal(err)
	}
	var pids []string
	env := &Env{Dir: t.TempDir(), EnvName: "dev", DBURL: e.AppURL, Migrations: ms, Now: time.Now}
	env.Connect = func(ctx context.Context, u string) (*pgx.Conn, error) {
		inner := *env
		inner.Connect = nil
		c, err := connect(ctx, &inner)
		if err == nil {
			var pid int
			_ = c.QueryRow(ctx, "SELECT pg_backend_pid()").Scan(&pid)
			pids = append(pids, "["+strconv.Itoa(pid)+"]")
		}
		return c, err
	}
	conn, _ := connect(ctx, &Env{DBURL: e.AppURL})
	var role string
	_ = conn.QueryRow(ctx, "SELECT current_user").Scan(&role)
	conn.Close(ctx)
	// Scoped to this test's own database (S49). A bare ALTER ROLE … SET log_statement made the
	// shared runtime role log every statement of every package running in parallel on the cluster;
	// the server then wrote the whole suite's queries to its log for the length of this test, which
	// slowed every backend, this test's three doctor connections included, past doctor's 3 s check
	// budget whenever the machine was busy (the S44/S45 "load flake").
	scope := "ALTER ROLE " + pgx.Identifier{role}.Sanitize() + " IN DATABASE " + pgx.Identifier{e.Name}.Sanitize()
	if _, err := admin.Exec(ctx, scope+" SET log_statement = 'all'"); err != nil {
		t.Fatal(err)
	}
	defer admin.Exec(context.Background(), scope+" RESET log_statement") //nolint:errcheck
	rs := byID(Run(ctx, env, Filter(Checks(), "db.*")))
	for _, id := range []string{"db.reachable", "db.role", "db.migrations"} {
		if rs[id].Status != OK && !(id == "db.role" && rs[id].Status == Warn) {
			t.Errorf("%s: %+v", id, rs[id])
		}
	}
	t.Logf("%s · %s · %s", rs["db.reachable"].Detail, rs["db.role"].Detail, rs["db.migrations"].Detail)
	logPath := os.Getenv("DEEDGLASS_IT_PG_LOG")
	if logPath == "" {
		t.Skip("DEEDGLASS_IT_PG_LOG names the server log in brew mode")
	}
	time.Sleep(300 * time.Millisecond)
	b, _ := os.ReadFile(logPath)
	n := 0
	for _, l := range strings.Split(string(b), "\n") {
		i := strings.Index(l, "LOG:  ")
		if i < 0 || !(strings.Contains(l, "statement: ") || strings.Contains(l, "execute ")) {
			continue
		}
		mine := false
		for _, p := range pids {
			mine = mine || strings.Contains(l, p+" LOG:")
		}
		if !mine {
			continue
		}
		n++
		u := strings.ToUpper(l[i:])
		for _, w := range []string{"INSERT ", "UPDATE ", "DELETE ", "CREATE ", "ALTER ", "DROP ", "TRUNCATE"} {
			if strings.Contains(u, ": "+w) {
				t.Errorf("doctor wrote: %s", l)
			}
		}
	}
	if n == 0 {
		t.Fatal("log_statement didn't take effect")
	}
	t.Logf("%d statements from doctor in the server log, none a write", n)
}
