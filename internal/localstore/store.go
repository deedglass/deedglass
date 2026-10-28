package localstore

//deedglass:allow-exec the local store runs initdb and pg_ctl for the cluster it manages (deedglass app)

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	cpsql "github.com/deedglass/deedglass/deploy/postgres"
)

// Files under the store's directory.
const (
	StateFile = "store.json" // binaries, port and passwords (0600)
	DataDir   = "pg"         // the cluster
	LogFile   = "pg.log"
	confFile  = "deedglass.conf" // rewritten before each start; postgresql.conf includes it
)

// State is store.json.
type State struct {
	Object           string   `json:"object"` // "deedglass_local_store"
	Version          int      `json:"version"`
	Binaries         Binaries `json:"binaries"`
	Port             int      `json:"port"`
	AdminPassword    string   `json:"admin_password"`
	MigratorPassword string   `json:"migrator_password"`
	AppPassword      string   `json:"app_password"`
	CreatedAt        string   `json:"created_at"`
}

// Store is one local cluster under Dir.
type Store struct {
	Dir    string
	Getenv func(string) string // DEEDGLASS_PG_BIN, DEEDGLASS_PG_BUNDLE, ProgramFiles
	Now    func() time.Time
}

// New returns the store in dir.
func New(dir string, getenv func(string) string) *Store {
	if getenv == nil {
		getenv = os.Getenv
	}
	return &Store{Dir: dir, Getenv: getenv, Now: time.Now}
}

func (s *Store) data() string { return filepath.Join(s.Dir, DataDir) }

// Initialized reports whether the cluster exists.
func (s *Store) Initialized() bool {
	_, err := os.Stat(filepath.Join(s.data(), "PG_VERSION"))
	return err == nil
}

// ReadState reads store.json (nil, nil when there is none).
func (s *Store) ReadState() (*State, error) {
	b, err := os.ReadFile(filepath.Join(s.Dir, StateFile))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var st State
	if err := json.Unmarshal(b, &st); err != nil {
		return nil, fmt.Errorf("%s is damaged: %w", StateFile, err)
	}
	return &st, nil
}

func (s *Store) writeState(st *State) error {
	b, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	return WriteSecret(filepath.Join(s.Dir, StateFile), append(b, '\n'))
}

// WriteSecret writes a 0600 file through a temporary file and a rename.
func WriteSecret(p string, b []byte) error {
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	_ = os.Chmod(tmp, 0o600)
	return os.Rename(tmp, p)
}

// NeedDownload is returned by Binaries when the programs must be downloaded first.
var NeedDownload = errors.New("PostgreSQL needs a one-time download")

// MajorMismatch means the cluster was made by a PostgreSQL that is no longer there.
type MajorMismatch struct {
	Recorded Binaries
	Found    Binaries
}

func (e *MajorMismatch) Error() string {
	return fmt.Sprintf("the local database was made with PostgreSQL %d (%s), which is gone; this computer has PostgreSQL %d", e.Recorded.Major, e.Recorded.Dir, e.Found.Major)
}

// Binaries finds the programs without downloading (the order is in the package comment). It
// returns NeedDownload when only the download is left.
func (s *Store) Binaries(ctx context.Context) (Binaries, error) {
	st, err := s.ReadState()
	if err != nil {
		return Binaries{}, err
	}
	var recorded *Binaries
	if st != nil && st.Binaries.Dir != "" && s.Initialized() {
		recorded = &st.Binaries
		if b, err := probe(ctx, st.Binaries.Dir, st.Binaries.Source); err == nil && b.Major == st.Binaries.Major {
			return b, nil
		}
	}
	accept := func(b Binaries) (Binaries, error) {
		if recorded != nil && b.Major != recorded.Major {
			return Binaries{}, &MajorMismatch{Recorded: *recorded, Found: b}
		}
		return b, nil
	}
	if d := strings.TrimSpace(s.Getenv("DEEDGLASS_PG_BIN")); d != "" {
		b, err := probe(ctx, d, SourceEnv)
		if err != nil {
			return Binaries{}, fmt.Errorf("DEEDGLASS_PG_BIN: %w", err)
		}
		return accept(b)
	}
	if b, err := probe(ctx, filepath.Join(RuntimeDir(s.Dir), "bin"), SourceDownloaded); err == nil {
		return accept(b)
	}
	if (recorded == nil || recorded.Source == SourceSystem) && !off(s.Getenv("DEEDGLASS_PG_SYSTEM")) {
		if b, err := FindSystem(ctx, s.Getenv); err == nil {
			return accept(b)
		}
	}
	if recorded != nil && recorded.Major != 18 {
		return Binaries{}, &MajorMismatch{Recorded: *recorded, Found: Binaries{Major: 18, Source: SourceDownloaded}}
	}
	return Binaries{}, NeedDownload
}

// Install is the one-time download (or DEEDGLASS_PG_BUNDLE), then Binaries.
func (s *Store) Install(ctx context.Context, progress Progress) (Binaries, error) {
	if err := Install(ctx, s.Dir, strings.TrimSpace(s.Getenv("DEEDGLASS_PG_BUNDLE")), progress); err != nil {
		return Binaries{}, err
	}
	return probe(ctx, filepath.Join(RuntimeDir(s.Dir), "bin"), SourceDownloaded)
}

// Running is a started cluster.
type Running struct {
	Port             int
	Binaries         Binaries
	MigratorPassword string
	AppPassword      string
	Created          bool // initdb ran in this Start
}

// AppURL is deedglass_app on the deedglass database (DEEDGLASS_DATABASE_URL).
func (r *Running) AppURL() string { return pgURL("deedglass_app", r.AppPassword, r.Port) }

// MigratorURL is deedglass_migrator (DEEDGLASS_MIGRATE_DATABASE_URL).
func (r *Running) MigratorURL() string {
	return pgURL("deedglass_migrator", r.MigratorPassword, r.Port)
}

func pgURL(user, pw string, port int) string {
	u := url.URL{Scheme: "postgres", User: url.UserPassword(user, pw), Host: "127.0.0.1:" + strconv.Itoa(port), Path: "/deedglass", RawQuery: "sslmode=disable"}
	return u.String()
}

// Start creates the cluster on first use, starts it on 127.0.0.1 (the recorded port when it is
// free, else a free one), and creates the Deedglass roles and database once. A cluster that is
// already running is reused.
func (s *Store) Start(ctx context.Context, b Binaries) (*Running, error) {
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return nil, err
	}
	st, err := s.ReadState()
	if err != nil {
		return nil, err
	}
	created := false
	if st == nil || !s.Initialized() {
		st = &State{Object: "deedglass_local_store", Version: 1, AdminPassword: randHex(24), MigratorPassword: randHex(24),
			AppPassword: randHex(24), CreatedAt: s.Now().UTC().Format(time.RFC3339)}
		if err := s.initdb(ctx, b, st.AdminPassword); err != nil {
			return nil, err
		}
		created = true
	}
	st.Binaries = b
	if s.running(ctx, b) && st.Port > 0 {
		if err := s.writeState(st); err != nil {
			return nil, err
		}
	} else {
		port := st.Port
		if port == 0 || !portFree(port) {
			if port, err = freePort(); err != nil {
				return nil, err
			}
		}
		st.Port = port
		if err := s.writeConf(port); err != nil {
			return nil, err
		}
		if err := s.writeState(st); err != nil {
			return nil, err
		}
		if err := s.pgctl(ctx, b, 90*time.Second, "start", "-w", "-t", "60", "-l", filepath.Join(s.Dir, LogFile)); err != nil {
			return nil, fmt.Errorf("PostgreSQL didn't start (see %s): %w", filepath.Join(s.Dir, LogFile), err)
		}
	}
	if err := s.bootstrap(ctx, st); err != nil {
		// Don't leave a cluster running that nothing will stop.
		sctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		_ = s.pgctl(sctx, b, 60*time.Second, "stop", "-m", "fast", "-w", "-t", "50")
		return nil, err
	}
	return &Running{Port: st.Port, Binaries: b, MigratorPassword: st.MigratorPassword, AppPassword: st.AppPassword, Created: created}, nil
}

// Stop stops the cluster if it is running (fast shutdown: open sessions end, nothing is lost).
func (s *Store) Stop(ctx context.Context) error {
	if !s.Initialized() {
		return nil
	}
	st, err := s.ReadState()
	if err != nil || st == nil {
		return err
	}
	b := st.Binaries
	if pb, err := probe(ctx, b.Dir, b.Source); err == nil {
		b = pb
	} else {
		return fmt.Errorf("can't stop PostgreSQL: %w", err)
	}
	if !s.running(ctx, b) {
		return nil
	}
	return s.pgctl(ctx, b, 60*time.Second, "stop", "-m", "fast", "-w", "-t", "50")
}

// Status reports whether the cluster is running, and its port.
func (s *Store) Status(ctx context.Context) (running bool, port int) {
	st, err := s.ReadState()
	if err != nil || st == nil || !s.Initialized() {
		return false, 0
	}
	return s.running(ctx, st.Binaries), st.Port
}

func (s *Store) running(ctx context.Context, b Binaries) bool {
	if _, err := os.Stat(filepath.Join(s.data(), "postmaster.pid")); err != nil {
		return false
	}
	return s.pgctl(ctx, b, 10*time.Second, "status") == nil
}

func (s *Store) initdb(ctx context.Context, b Binaries, adminPassword string) error {
	_ = os.RemoveAll(s.data())
	pw := filepath.Join(s.Dir, ".pwfile")
	if err := WriteSecret(pw, []byte(adminPassword+"\n")); err != nil {
		return err
	}
	defer os.Remove(pw)
	ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, b.path("initdb"), "-D", s.data(), "-U", "postgres", "--auth=scram-sha-256",
		"--pwfile="+pw, "-E", "UTF8", "--locale=C", "--no-instructions")
	cmd.Env = childEnv(nil)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("initdb failed: %s", lastLine(string(out), err))
	}
	f, err := os.OpenFile(filepath.Join(s.data(), "postgresql.conf"), os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, err = f.WriteString("\n# deedglass app: the settings below are rewritten at every start\ninclude_if_exists = '" + confFile + "'\n")
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

// writeConf writes the settings the store needs: loopback only, no Unix socket, the change log
// Deedglass can read (wal_level logical), and a small footprint for a desktop.
func (s *Store) writeConf(port int) error {
	conf := fmt.Sprintf(`# Written by deedglass app before each start. Edits here are replaced.
listen_addresses = '127.0.0.1'
port = %d
unix_socket_directories = ''
wal_level = logical
max_connections = 60
shared_buffers = '64MB'
max_wal_size = '256MB'
logging_collector = off
`, port)
	return os.WriteFile(filepath.Join(s.data(), confFile), []byte(conf), 0o600)
}

func (s *Store) pgctl(ctx context.Context, b Binaries, timeout time.Duration, args ...string) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	full := append([]string{args[0], "-D", s.data()}, args[1:]...)
	cmd := exec.CommandContext(ctx, b.path("pg_ctl"), full...)
	cmd.Env = childEnv(nil)
	cmd.SysProcAttr = detachAttr()
	out, err := cmd.CombinedOutput()
	if err != nil {
		return errors.New(lastLine(string(out), err))
	}
	return nil
}

// bootstrap creates deploy/postgres's control-plane roles and database once, with this store's
// passwords (random hex, so quoting them as literals is safe).
func (s *Store) bootstrap(ctx context.Context, st *State) error {
	cfg, err := pgx.ParseConfig(pgURL("postgres", st.AdminPassword, st.Port))
	if err != nil {
		return err
	}
	cfg.Database = "postgres"
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	var conn *pgx.Conn
	for i := 0; ; i++ {
		conn, err = pgx.ConnectConfig(ctx, cfg)
		if err == nil || i >= 20 {
			break
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
	if err != nil {
		return fmt.Errorf("connecting to the local PostgreSQL: %w", err)
	}
	defer conn.Close(context.Background())
	var n int
	if err := conn.QueryRow(ctx, "SELECT count(*) FROM pg_catalog.pg_roles WHERE rolname = 'deedglass_migrator'").Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	stmts, err := controlPlaneStatements(st.MigratorPassword, st.AppPassword)
	if err != nil {
		return err
	}
	for _, q := range stmts {
		if _, err := conn.Exec(ctx, q); err != nil {
			return fmt.Errorf("creating the Deedglass roles: %w", err)
		}
	}
	return nil
}

var hexOnly = func(s string) bool {
	if len(s) < 16 {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}

// controlPlaneStatements turns control-plane-roles.sql into statements, with the two psql
// variables replaced by the given passwords (hex only).
func controlPlaneStatements(migratorPW, appPW string) ([]string, error) {
	if !hexOnly(migratorPW) || !hexOnly(appPW) {
		return nil, errors.New("the store's passwords must be hex")
	}
	var out []string
	for _, line := range strings.Split(cpsql.ControlPlaneRoles, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "--") || strings.HasPrefix(line, `\`) {
			continue
		}
		line = strings.TrimSuffix(line, ";")
		line = strings.ReplaceAll(line, ":'migrator_password'", "'"+migratorPW+"'")
		line = strings.ReplaceAll(line, ":'app_password'", "'"+appPW+"'")
		if strings.Contains(line, ":'") {
			return nil, fmt.Errorf("control-plane-roles.sql has an unknown variable: %s", line)
		}
		out = append(out, line)
	}
	if len(out) == 0 {
		return nil, errors.New("control-plane-roles.sql is empty")
	}
	return out, nil
}

func randHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

func lastLine(out string, err error) string {
	out = strings.TrimSpace(out)
	if out == "" {
		return err.Error()
	}
	if i := strings.LastIndexByte(out, '\n'); i >= 0 {
		return strings.TrimSpace(out[i+1:])
	}
	return out
}

// portFree reports whether 127.0.0.1:port can be listened on.
func portFree(port int) bool {
	l, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(port))
	if err != nil {
		return false
	}
	_ = l.Close()
	return true
}

// freePort asks the kernel for a free loopback port.
func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

// FreePort is freePort for callers that pick the app's port.
func FreePort() (int, error) { return freePort() }

// PortFree is portFree for callers.
func PortFree(port int) bool { return portFree(port) }

// off reports whether a switch is set to off (DEEDGLASS_PG_SYSTEM=off skips an installed
// PostgreSQL, so the store always uses its own copy).
func off(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "0", "off", "false", "no":
		return true
	}
	return false
}
