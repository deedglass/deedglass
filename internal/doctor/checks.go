package doctor

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/deedglass/deedglass/internal/deed"
	"github.com/deedglass/deedglass/internal/store/migrate"
	"github.com/deedglass/deedglass/internal/vault"
)

// Env is what the checks read. Everything is injected, so each failure can be forced in a test.
type Env struct {
	Dir            string
	Prod           bool
	EnvName        string // "dev" or "prod"
	ConfigErr      error  // config.Load's problems
	DBURL          string // the runtime role's URL (a secret: never printed)
	SealKeyFile    string
	VaultKey       string // the ring (a secret: never printed)
	Server         string
	APIKey         string // an org admin key (a secret: never printed)
	HTTP           *http.Client
	CLIVersion     string
	APIVersion     string
	SlotLagAlertMB int
	Migrations     []migrate.Migration
	Now            func() time.Time
	Statfs         func(path string) (free uint64, err error)
	// Connect opens a read-only session; nil uses pgx with default_transaction_read_only=on.
	Connect func(ctx context.Context, url string) (*pgx.Conn, error)
	// Lockdown (ADR-117): the config's lockdown key, and what doctor checks lockdown against.
	Lockdown      bool
	SealPQKeyFile string
	Timestamps    bool // timestamps.tsa_urls or scitt_url set
	// Unauthenticated is set when the server refused the API key (exit 3).
	Unauthenticated atomic.Bool
}

// Checks are the seventeen, in display order.
func Checks() []Check {
	return []Check{
		{"config.valid", "Config", checkConfig},
		{"state.dir", "Local state", checkStateDir},
		{"db.reachable", "Database", checkDB},
		{"db.role", "Database role", checkRole},
		{"db.migrations", "Migrations", checkMigrations},
		{"seal.key", "Seal key", checkSealKey},
		{"vault.key", "Vault key", checkVault},
		{"server.health", "Server", checkServer},
		{"clock.skew", "Clock", checkClock},
		{"ledger.chain", "Record chain", checkChain},
		{"connectors.postgres", "Postgres connectors", checkConnectors},
		{"decoder.slots", "Decoder slots", checkSlots},
		{"wal.level", "WAL level", checkWAL},
		{"disk.space", "Disk", checkDisk},
		{"version.drift", "Versions", checkVersions},
		{"policy.active", "Policies", checkPolicy},
		{"lockdown", "Lockdown", checkLockdown},
	}
}

func checkConfig(_ context.Context, env *Env) Result {
	if env.ConfigErr != nil {
		msg := strings.SplitN(env.ConfigErr.Error(), "\n", 2)[0]
		return fail(strings.TrimSuffix(msg, "."), "Run deedglass config print to see every problem, then fix the first one.")
	}
	return ok("valid for " + env.EnvName)
}

func checkStateDir(_ context.Context, env *Env) Result {
	d := filepath.Join(env.Dir, ".deedglass")
	fi, err := os.Lstat(d)
	switch {
	case err != nil:
		return skip("no .deedglass in this directory", "Run deedglass init to set up this directory.")
	case fi.Mode()&fs.ModeSymlink != 0:
		return fail(".deedglass is a symlink", "Replace it with a real directory: Deedglass keeps keys there.")
	case fi.Mode().Perm()&0o077 != 0:
		return fail(fmt.Sprintf(".deedglass is mode %04o", fi.Mode().Perm()), "Run chmod 700 .deedglass.")
	}
	keys := 0
	var wide []string
	_ = filepath.WalkDir(d, func(p string, e fs.DirEntry, err error) error {
		if err != nil || e.IsDir() {
			return nil
		}
		if e.Type()&fs.ModeSymlink != 0 {
			wide = append(wide, rel(d, p)+" is a symlink")
			return nil
		}
		n := e.Name()
		secret := strings.HasSuffix(n, ".key") || n == "dev-secrets.json" || n == "stack.json" || strings.HasPrefix(n, "sk_")
		if !secret {
			return nil
		}
		keys++
		if info, err := e.Info(); err == nil && info.Mode().Perm()&0o077 != 0 {
			wide = append(wide, fmt.Sprintf("%s is mode %04o", rel(d, p), info.Mode().Perm()))
		}
		return nil
	})
	if len(wide) > 0 {
		return fail(strings.Join(wide, " · "), "Run chmod 600 on each file named, so only you can read it.")
	}
	return ok(fmt.Sprintf(".deedglass 0700 · %d key %s 0600", keys, plural(keys, "file", "files")))
}

func rel(base, p string) string {
	r, err := filepath.Rel(base, p)
	if err != nil {
		return p
	}
	return ".deedglass/" + filepath.ToSlash(r)
}

// connect opens the read-only session every database check uses.
func connect(ctx context.Context, env *Env) (*pgx.Conn, error) {
	if env.Connect != nil {
		return env.Connect(ctx, env.DBURL)
	}
	cfg, err := pgx.ParseConfig(env.DBURL)
	if err != nil {
		return nil, err
	}
	cfg.RuntimeParams["default_transaction_read_only"] = "on"
	cfg.RuntimeParams["application_name"] = "deedglass-doctor"
	cfg.RuntimeParams["statement_timeout"] = "2000"
	return pgx.ConnectConfig(ctx, cfg)
}

const noDB = "Set DEEDGLASS_DATABASE_URL, or run deedglass doctor in the directory where you ran deedglass init."

func dbErr(err error) string {
	s := err.Error()
	// pgx errors can carry the host and user; keep only the reason.
	if i := strings.LastIndex(s, ": "); i >= 0 {
		s = s[i+2:]
	}
	if len(s) > 120 {
		s = s[:120]
	}
	return s
}

func checkDB(ctx context.Context, env *Env) Result {
	if env.DBURL == "" {
		return fail("no database URL", noDB)
	}
	t0 := time.Now()
	c, err := connect(ctx, env)
	if err != nil {
		return fail("can't connect: "+dbErr(err), "Start Postgres (deedglass init starts it in Docker), then run deedglass doctor again.")
	}
	defer c.Close(context.WithoutCancel(ctx))
	var ver, role string
	if err := c.QueryRow(ctx, "SELECT current_setting('server_version'), current_user").Scan(&ver, &role); err != nil {
		return fail("the database didn't answer: "+dbErr(err), "Check the database's logs, then run deedglass doctor again.")
	}
	if i := strings.IndexByte(ver, ' '); i > 0 {
		ver = ver[:i]
	}
	return ok(fmt.Sprintf("postgres %s · %d ms · role %s", ver, time.Since(t0).Milliseconds(), role))
}

func checkRole(ctx context.Context, env *Env) Result {
	if env.DBURL == "" {
		return skip("no database URL", noDB)
	}
	c, err := connect(ctx, env)
	if err != nil {
		return skip("database unreachable (see Database)", "Fix the Database line first.")
	}
	defer c.Close(context.WithoutCancel(ctx))
	var role string
	var su, bypass bool
	if err := c.QueryRow(ctx, "SELECT rolname, rolsuper, rolbypassrls FROM pg_catalog.pg_roles WHERE rolname = current_user").Scan(&role, &su, &bypass); err != nil {
		return fail("couldn't read the role: "+dbErr(err), "Check the database's logs, then run deedglass doctor again.")
	}
	if su || bypass {
		what := "a superuser"
		if !su {
			what = "BYPASSRLS"
		}
		d := fmt.Sprintf("%s is %s, so row-level security doesn't apply", role, what)
		f := "Run the server as deedglass_app (deploy/postgres/control-plane-roles.sql creates it)."
		if env.Prod {
			return fail(d, f)
		}
		return warn(d, f)
	}
	return ok(role + " · not a superuser · row-level security applies")
}

func checkMigrations(ctx context.Context, env *Env) Result {
	if env.DBURL == "" {
		return skip("no database URL", noDB)
	}
	c, err := connect(ctx, env)
	if err != nil {
		return skip("database unreachable (see Database)", "Fix the Database line first.")
	}
	defer c.Close(context.WithoutCancel(ctx))
	rows, err := migrate.Status(ctx, c, env.Migrations)
	if err != nil {
		return fail("couldn't read the applied migrations: "+dbErr(err), "Run deedglass migrate status to see why.")
	}
	pending, top := 0, 0
	for _, r := range rows {
		if r.State == migrate.StatePending {
			pending++
		} else if r.Version > top {
			top = r.Version
		}
	}
	if err := migrate.Verify(ctx, c, env.Migrations); err != nil {
		return fail("checksums don't match: "+dbErr(err), "Run deedglass migrate verify to see which file changed.")
	}
	if pending > 0 {
		return fail(fmt.Sprintf("schema at %04d · %d pending", top, pending), "Run deedglass migrate up (deedglass init does it for you).")
	}
	return ok(fmt.Sprintf("schema at %04d · checksums match", top))
}

func checkSealKey(ctx context.Context, env *Env) Result {
	if env.SealKeyFile == "" {
		return fail("DEEDGLASS_SEAL_KEY_FILE isn't set", "Run deedglass keys gen, then set DEEDGLASS_SEAL_KEY_FILE to the file it wrote.")
	}
	s, err := deed.LoadKeyFile(env.SealKeyFile, deed.LoadOptions{})
	if err != nil {
		return fail("the seal key file doesn't load: "+strings.TrimSuffix(dbErr(err), "."), "Check the path and that the file is mode 0600, or run deedglass keys gen.")
	}
	id := s.KeyID()
	pub := s.PublicKey()
	tp := fmt.Sprintf("sha256:%x", sha256.Sum256(pub))
	if env.HTTP == nil || env.Server == "" {
		return ok(short(id) + " · loads")
	}
	var set struct {
		Keys []struct {
			ID         string `json:"id"`
			Status     string `json:"status"`
			Thumbprint string `json:"thumbprint"`
		} `json:"keys"`
	}
	if _, err := getJSON(ctx, env, "/.well-known/deedglass/keys.json", false, &set); err != nil {
		return warn(short(id)+" · loads · keys.json unreadable", "Start the server (deedglass serve), then run deedglass doctor again.")
	}
	for _, k := range set.Keys {
		if k.ID == id {
			if k.Thumbprint != tp {
				return fail(short(id)+" · differs from keys.json", "The server publishes another key under this id. Point DEEDGLASS_SEAL_KEY_FILE at the server's key.")
			}
			if k.Status != "active" {
				return fail(short(id)+" · "+k.Status+" in keys.json", "Run deedglass keys rotate to activate a new key.")
			}
			return ok(short(id) + " · matches keys.json")
		}
	}
	return fail(short(id)+" · not in keys.json", "The server seals with another key. Point DEEDGLASS_SEAL_KEY_FILE at the server's key.")
}

func short(id string) string {
	if len(id) > 14 {
		return id[:9] + "…" + id[len(id)-4:]
	}
	return id
}

func checkVault(ctx context.Context, env *Env) Result {
	if env.VaultKey == "" {
		return fail("DEEDGLASS_VAULT_KEY isn't set", "Set it (openssl rand -base64 32); in dev, deedglass init writes .deedglass/dev-secrets.json.")
	}
	r, err := vault.NewRing(env.VaultKey)
	if err != nil {
		return fail("DEEDGLASS_VAULT_KEY doesn't parse", "Set it to 1 to 4 comma-separated base64 keys of exactly 32 bytes.")
	}
	dek := make([]byte, 32)
	_, _ = rand.Read(dek)
	aad := []byte("deedglass/doctor/canary")
	id, wrapped, err := r.Wrap(ctx, dek, aad)
	if err == nil {
		var back []byte
		back, err = r.Unwrap(ctx, id, wrapped, aad)
		if err == nil && !bytes.Equal(back, dek) {
			err = fmt.Errorf("canary mismatch")
		}
	}
	if err != nil {
		return fail("the vault key doesn't decrypt the canary", "Check DEEDGLASS_VAULT_KEY; run deedglass vault status.")
	}
	n := len(r.IDs())
	return ok(fmt.Sprintf("%s active · %d %s in the ring · decrypts the canary", r.ActiveID(), n, plural(n, "key", "keys")))
}

const noServer = "Start the server (deedglass serve, or deedglass init), then run deedglass doctor again."

// getJSON is a GET to the server, with the admin key when auth is true. It returns the response.
func getJSON(ctx context.Context, env *Env, path string, auth bool, out any) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimSuffix(env.Server, "/")+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	if auth {
		req.Header.Set("Authorization", "Bearer "+env.APIKey)
	}
	resp, err := env.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized && auth {
		env.Unauthenticated.Store(true)
		return resp, errUnauth
	}
	if resp.StatusCode/100 != 2 {
		return resp, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	if out != nil {
		if err := json.NewDecoder(http.MaxBytesReader(nil, resp.Body, 4<<20)).Decode(out); err != nil {
			return resp, err
		}
	}
	return resp, nil
}

var errUnauth = fmt.Errorf("the server refused the API key")

func needAPI(env *Env) (Result, bool) {
	if env.HTTP == nil || env.Server == "" {
		return skip("no server", noServer), false
	}
	if env.APIKey == "" {
		return skip("no admin API key", "Set DEEDGLASS_API_KEY to an org admin key (deedglass init writes .deedglass/admin.key)."), false
	}
	return Result{}, true
}

func apiFail(err error, what string) Result {
	if err == errUnauth {
		return fail("the server refused the API key", "Set DEEDGLASS_API_KEY to a current org admin key.")
	}
	return fail(what+": "+dbErr(err), noServer)
}

func checkServer(ctx context.Context, env *Env) Result {
	if env.HTTP == nil || env.Server == "" {
		return fail("no server URL", noServer)
	}
	var h struct {
		Status string `json:"status"`
	}
	if _, err := getJSON(ctx, env, "/v1/health", false, &h); err != nil {
		return fail("no answer from "+env.Server, noServer)
	}
	var v struct {
		Version     string   `json:"version"`
		APIVersions []string `json:"api_versions"`
	}
	if _, err := getJSON(ctx, env, "/v1/version", false, &v); err != nil {
		return fail("/v1/version didn't answer", noServer)
	}
	known := false
	for _, a := range v.APIVersions {
		known = known || a == env.APIVersion
	}
	if !known {
		return fail("the server doesn't speak API "+env.APIVersion, "Update the CLI or the server so both speak the same API version.")
	}
	d := fmt.Sprintf("%s · health %s · API %s", env.Server, h.Status, env.APIVersion)
	if h.Status != "ok" {
		return warn(d, "Run curl "+env.Server+"/v1/health to see which check is degraded.")
	}
	return ok(d)
}

func checkClock(ctx context.Context, env *Env) Result {
	if env.HTTP == nil || env.Server == "" {
		return skip("no server to compare with", noServer)
	}
	now := env.Now
	if now == nil {
		now = time.Now
	}
	t0 := now()
	resp, err := getJSON(ctx, env, "/v1/version", false, nil)
	t1 := now()
	if err != nil || resp == nil || resp.Header.Get("Date") == "" {
		return skip("the server sent no Date", noServer)
	}
	st, err := http.ParseTime(resp.Header.Get("Date"))
	if err != nil {
		return skip("the server's Date is unreadable", noServer)
	}
	mid := t0.Add(t1.Sub(t0) / 2)
	// Date has one-second resolution: anything inside it is no skew.
	skew := mid.Sub(st.Add(500 * time.Millisecond))
	if skew < 0 {
		skew = -skew
	}
	skew -= 500 * time.Millisecond
	if skew < 0 {
		skew = 0
	}
	d := fmt.Sprintf("%.1f s from the server", skew.Seconds())
	switch {
	case skew > 30*time.Second:
		return fail(d, "Fix this machine's clock (System Settings › Date & Time). Signed intents allow ±60 s; records use the database's clock.")
	case skew > 2*time.Second:
		return warn(d, "Sync this machine's clock. Signed intents allow ±60 s of skew.")
	}
	return ok(d)
}

func checkChain(ctx context.Context, env *Env) Result {
	if r, ok := needAPI(env); !ok {
		return r
	}
	var list struct {
		Data []struct {
			Body struct {
				Seq int64 `json:"seq"`
			} `json:"body"`
		} `json:"data"`
	}
	if _, err := getJSON(ctx, env, "/v1/deeds?order=desc&limit=1", true, &list); err != nil {
		return apiFail(err, "couldn't read the newest record")
	}
	if len(list.Data) == 0 {
		return ok("no records yet")
	}
	from := list.Data[0].Body.Seq - 999
	if from < 1 {
		from = 1
	}
	var c struct {
		Status  string `json:"status"`
		Checked int    `json:"checked"`
	}
	if _, err := getJSON(ctx, env, "/v1/ledger/check?from_seq="+strconv.FormatInt(from, 10), true, &c); err != nil {
		return apiFail(err, "the chain check didn't answer")
	}
	d := fmt.Sprintf("last %s %s · chain %s", thousands(c.Checked), plural(c.Checked, "record", "records"), c.Status)
	if c.Status != "intact" {
		return fail(d, "Run deedglass ledger check to see where it breaks, and keep your last good export.")
	}
	return ok(d)
}

type conn struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Type   string `json:"type"`
	Health *struct {
		Status string `json:"status"`
		Reason string `json:"reason"`
	} `json:"health"`
}

func postgresConnectors(ctx context.Context, env *Env) ([]conn, error) {
	var l struct {
		Data []conn `json:"data"`
	}
	if _, err := getJSON(ctx, env, "/v1/connectors?limit=100", true, &l); err != nil {
		return nil, err
	}
	var out []conn
	for _, c := range l.Data {
		if c.Type == "postgres" {
			out = append(out, c)
		}
	}
	return out, nil
}

func checkConnectors(ctx context.Context, env *Env) Result {
	if r, ok := needAPI(env); !ok {
		return r
	}
	cs, err := postgresConnectors(ctx, env)
	if err != nil {
		return apiFail(err, "couldn't list connectors")
	}
	if len(cs) == 0 {
		return ok("no Postgres connectors")
	}
	var parts []string
	worst := OK
	fix := ""
	for _, c := range cs {
		h := ""
		if c.Health != nil {
			h = c.Health.Status
		}
		switch h {
		case "ok", "pass", "healthy":
			parts = append(parts, c.Name+" ready")
		case "warn":
			// Notes only (S17: ready when no check fails): optional hardening, never a problem.
			parts = append(parts, c.Name+" ready · notes in deedglass connector test "+c.Name)
		case "fail", "failing", "down":
			parts = append(parts, c.Name+" failing")
			worst, fix = Fail, "Run deedglass connector postgres check --connector "+c.Name+" to see the failing check."
		default:
			parts = append(parts, c.Name+" not tested yet")
			if worst == OK {
				worst, fix = Warn, "Run deedglass connector test "+c.Name+"."
			}
		}
	}
	return Result{Status: worst, Detail: strings.Join(parts, " · "), Fix: fix}
}

type decoderObj struct {
	Name   string  `json:"name"`
	Slot   string  `json:"slot"`
	Status string  `json:"status"`
	Reason *string `json:"reason"`
	Lag    struct {
		Bytes *int64 `json:"bytes"`
	} `json:"lag"`
}

func decoders(ctx context.Context, env *Env) ([]decoderObj, error) {
	cs, err := postgresConnectors(ctx, env)
	if err != nil {
		return nil, err
	}
	var out []decoderObj
	for _, c := range cs {
		var d decoderObj
		if _, err := getJSON(ctx, env, "/v1/connectors/"+c.ID+"/decoder", true, &d); err != nil {
			if err == errUnauth {
				return nil, err
			}
			continue
		}
		out = append(out, d)
	}
	return out, nil
}

func reason(d decoderObj) string {
	if d.Reason == nil {
		return ""
	}
	return *d.Reason
}

func checkSlots(ctx context.Context, env *Env) Result {
	if r, ok := needAPI(env); !ok {
		return r
	}
	ds, err := decoders(ctx, env)
	if err != nil {
		return apiFail(err, "couldn't read the decoders")
	}
	if len(ds) == 0 {
		return ok("no decoder slots")
	}
	limit := int64(env.SlotLagAlertMB)
	if limit <= 0 {
		limit = 512
	}
	worst, fix := OK, ""
	var parts []string
	for _, d := range ds {
		lag := int64(0)
		if d.Lag.Bytes != nil {
			lag = *d.Lag.Bytes
		}
		mb := lag >> 20
		parts = append(parts, fmt.Sprintf("%s %s · lag %d MB", short(d.Slot), d.Status, mb))
		switch {
		case reason(d) == "slot_invalidated" || reason(d) == "slot_missing":
			worst, fix = Fail, "Run deedglass decoder drop --connector "+d.Name+" --confirm "+d.Name+"; the server creates a new slot (frames in between are missing)."
		case d.Status == "halted" && worst != Fail:
			worst, fix = Fail, "Run deedglass decoder status --connector "+d.Name+" to see why it halted."
		case mb >= limit && worst == OK:
			worst, fix = Warn, fmt.Sprintf("Run deedglass decoder status, or raise DEEDGLASS_SLOT_LAG_ALERT_MB (alert at %d MB).", limit)
		case d.Status != "running" && worst == OK:
			worst, fix = Warn, "Run deedglass decoder status --connector "+d.Name+"."
		}
	}
	return Result{Status: worst, Detail: strings.Join(parts, " · "), Fix: fix}
}

func checkWAL(ctx context.Context, env *Env) Result {
	if r, ok := needAPI(env); !ok {
		return r
	}
	ds, err := decoders(ctx, env)
	if err != nil {
		return apiFail(err, "couldn't read the decoders")
	}
	if len(ds) == 0 {
		return skip("no Proven targets", "Add a Postgres connector with witness.enabled to check at Proven (L2).")
	}
	var bad []string
	for _, d := range ds {
		if reason(d) == "wal_level" {
			bad = append(bad, d.Name)
		}
	}
	if len(bad) > 0 {
		return fail(strings.Join(bad, ", ")+" has wal_level below logical; Proven checks need logical",
			"Set wal_level=logical and restart Postgres (docs.deedglass.com/doctor#wal-level).")
	}
	return ok(fmt.Sprintf("logical on %d Proven %s", len(ds), plural(len(ds), "target", "targets")))
}

func checkDisk(_ context.Context, env *Env) Result {
	if env.Statfs == nil {
		return skip("disk space isn't readable here", "")
	}
	free, err := env.Statfs(env.Dir)
	if err != nil {
		return skip("disk space isn't readable here", "")
	}
	gib := float64(free) / (1 << 30)
	d := fmt.Sprintf("%.1f GiB free", gib)
	switch {
	case free < 500<<20:
		return fail(d, "Free some disk space: Postgres stops writing when the disk fills.")
	case free < 2<<30:
		return warn(d, "Free some disk space; Deedglass wants at least 2 GiB.")
	}
	return ok(d)
}

// minor is "0.1" of "0.1.0-dev".
func minor(v string) string {
	v = strings.TrimPrefix(v, "v")
	parts := strings.SplitN(v, ".", 3)
	if len(parts) < 2 {
		return v
	}
	return parts[0] + "." + parts[1]
}

func checkVersions(ctx context.Context, env *Env) Result {
	if env.HTTP == nil || env.Server == "" {
		return skip("no server", noServer)
	}
	var v struct {
		Version string `json:"version"`
	}
	if _, err := getJSON(ctx, env, "/v1/version", false, &v); err != nil {
		return skip("/v1/version didn't answer", noServer)
	}
	d := fmt.Sprintf("CLI %s · server %s", env.CLIVersion, v.Version)
	if minor(env.CLIVersion) != minor(v.Version) {
		return warn(d, "Use the same minor version for the CLI and the server.")
	}
	return ok(d)
}

func checkPolicy(ctx context.Context, env *Env) Result {
	if r, ok := needAPI(env); !ok {
		return r
	}
	var a struct {
		Enforce *struct {
			Name    string `json:"name"`
			Version int    `json:"version"`
		} `json:"enforce"`
		Shadow *struct {
			Name string `json:"name"`
		} `json:"shadow"`
	}
	if _, err := getJSON(ctx, env, "/v1/policies/active", true, &a); err != nil {
		return apiFail(err, "couldn't read the active policies")
	}
	if a.Enforce == nil {
		return warn("no policy enforced", "Run deedglass policy activate <name> --mode enforce (postgres-safe is a good start).")
	}
	d := fmt.Sprintf("%s v%d enforced", a.Enforce.Name, a.Enforce.Version)
	if a.Shadow != nil {
		d += " · " + a.Shadow.Name + " in shadow"
	}
	return ok(d)
}

func thousands(n int) string {
	s := strconv.Itoa(n)
	var b strings.Builder
	for i, c := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(c)
	}
	return b.String()
}

// checkLockdown (ADR-117) shows whether lockdown is on (the config's lockdown key, or the org's
// setting from /v1/whoami) and lists what weakens it. Off is OK: lockdown is a choice.
func checkLockdown(ctx context.Context, env *Env) Result {
	var org struct {
		Org struct {
			Lockdown bool `json:"lockdown"`
			PHIMode  bool `json:"phi_mode"`
		} `json:"org"`
	}
	api := env.HTTP != nil && env.Server != "" && env.APIKey != ""
	if api {
		if _, err := getJSON(ctx, env, "/v1/whoami", true, &org); err != nil {
			return apiFail(err, "couldn't read the org")
		}
	}
	source := ""
	switch {
	case env.Lockdown && org.Org.Lockdown:
		source = "the config and the org"
	case env.Lockdown:
		source = "lockdown: true in the config"
	case org.Org.Lockdown:
		source = "the org (deedglass lockdown on)"
	default:
		if !api {
			return ok("off in the config · the org's setting needs the server (deedglass lockdown status on the server)")
		}
		return ok("off")
	}
	var weak, fixes []string
	if env.SealPQKeyFile == "" {
		weak = append(weak, "no ML-DSA-65 seal key")
		fixes = append(fixes, "Run deedglass keys rotate --alg ML-DSA-65 and set DEEDGLASS_SEAL_PQ_KEY_FILE.")
	}
	if !env.Timestamps {
		weak = append(weak, "no TSAs, so no outside timestamps")
		fixes = append(fixes, "Set timestamps.tsa_urls in deedglass.yaml (docs/TIMESTAMPS.md).")
	}
	if api {
		var agents struct {
			Data []struct {
				Name   string            `json:"name"`
				Status string            `json:"status"`
				Labels map[string]string `json:"labels"`
			} `json:"data"`
		}
		if _, err := getJSON(ctx, env, "/v1/agents?limit=100", true, &agents); err != nil {
			return apiFail(err, "couldn't list the agents")
		}
		var bare []string
		for _, a := range agents.Data {
			if a.Status != "stopped" && a.Labels["connector"] == "" {
				bare = append(bare, a.Name)
			}
		}
		if len(bare) > 0 {
			weak = append(weak, fmt.Sprintf("%d %s without a connector label (refused)", len(bare), plural(len(bare), "agent", "agents")))
			fixes = append(fixes, "Give "+bare[0]+" its one connector as the label connector=<name> (PATCH /v1/agents/{id}, or deedglass agent create --label for a new agent).")
		}
		var conns struct {
			Data []conn `json:"data"`
		}
		if _, err := getJSON(ctx, env, "/v1/connectors?limit=100", true, &conns); err != nil {
			return apiFail(err, "couldn't list the connectors")
		}
		for _, c := range conns.Data {
			if c.Type == "fhir" && !org.Org.PHIMode {
				weak = append(weak, "a FHIR connector with PHI mode off (refused)")
				fixes = append(fixes, "Run deedglass lockdown on on the server, which turns PHI mode on.")
				break
			}
		}
	}
	if len(weak) == 0 {
		return ok("on · set by " + source + " · dual seal · outside timestamps")
	}
	return warn("on · set by "+source+" · weakened by "+strings.Join(weak, ", "), fixes[0])
}
