package main

//deedglass:allow-exec deedglass app runs this same binary for its one-shot setup steps (migrate, keys gen, org create, login-link) and opens the browser

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/deedglass/deedglass/internal/config"
	"github.com/deedglass/deedglass/internal/connector/egress"
	"github.com/deedglass/deedglass/internal/localstore"
	"github.com/deedglass/deedglass/internal/setup"
)

// deedglass app (DL-143): the local app. It starts the local store (internal/localstore, ADR-120),
// runs serve in this process on 127.0.0.1, signs the owner in with a one-time link opened in the
// browser, and prints three lines. Bare deedglass in an interactive terminal runs it too; piped,
// in CI or with --json, bare deedglass keeps printing the welcome, so scripts and agents don't
// start a server by accident.

// Files under the app's data directory.
const (
	appStateFile = "app.json"   // the running app: pid, address (no secret)
	appLocalFile = "local.json" // the first-run state the Console reads (GET /v1/local)
	appAdminKey  = "admin.key"  // the org admin key, for the CLI against the app (0600)
	appServeLog  = "serve.log"
	appKeysDir   = "keys"
	appOrgName   = "My team"
	appOrgSlug   = "local"
	appPort      = 4180 // the address DL-143 names; a free port when it is taken
)

type appOpts struct {
	stop, reset, status, yes, noOpen bool
	port                             int
}

var appFlagValues appOpts

func appFlags(fs *flag.FlagSet) {
	appFlagValues = appOpts{}
	v := &appFlagValues
	fs.BoolVar(&v.stop, "stop", false, "stop the running app and its database (your data stays)")
	fs.BoolVar(&v.status, "status", false, "say whether the app is running, where, and where its data is")
	fs.BoolVar(&v.reset, "reset", false, "delete the app's data (asks first; --yes skips the question)")
	fs.BoolVar(&v.yes, "yes", false, "with --reset: don't ask")
	fs.BoolVar(&v.noOpen, "no-open", false, "don't open the browser; print the sign-in link instead")
	fs.IntVar(&v.port, "port", 0, "the port to serve on (default 4180, or a free one)")
}

// appState is app.json.
type appState struct {
	Object    string `json:"object"` // "deedglass_app"
	Version   int    `json:"version"`
	PID       int    `json:"pid"`
	Server    string `json:"server"`
	PGPort    int    `json:"pg_port"`
	StartedAt string `json:"started_at"`
}

// appLocal is local.json: what the first-run screens remember.
type appLocal struct {
	Object   string  `json:"object"` // "local"
	FirstRun bool    `json:"first_run"`
	Path     *string `json:"path"`
	Org      string  `json:"org,omitempty"`
}

// appDir is the app's data directory: DEEDGLASS_APP_DIR, else Deedglass under the user's config
// directory (macOS ~/Library/Application Support/Deedglass, Linux ~/.config/deedglass, Windows
// %AppData%\Deedglass).
func appDir(e *Env) (string, error) {
	if d := strings.TrimSpace(e.Getenv("DEEDGLASS_APP_DIR")); d != "" {
		return filepath.Abs(d)
	}
	home := e.Getenv("HOME")
	switch runtime.GOOS {
	case "windows":
		if d := e.Getenv("AppData"); d != "" {
			return filepath.Join(d, "Deedglass"), nil
		}
		if d := e.Getenv("USERPROFILE"); d != "" {
			return filepath.Join(d, "AppData", "Roaming", "Deedglass"), nil
		}
	case "darwin":
		if home != "" {
			return filepath.Join(home, "Library", "Application Support", "Deedglass"), nil
		}
	default:
		if d := e.Getenv("XDG_CONFIG_HOME"); d != "" && filepath.IsAbs(d) {
			return filepath.Join(d, "deedglass"), nil
		}
		if home != "" {
			return filepath.Join(home, ".config", "deedglass"), nil
		}
	}
	return "", errors.New("no home directory to keep the app's data in")
}

// interactive reports whether bare deedglass should open the app: a person at a terminal (stdin
// and stdout), not CI, not --json, and not turned off with DEEDGLASS_NO_APP=1.
func interactive(e *Env) bool {
	return e.IsTTY && e.StdinTTY && !e.Globals.JSON && e.Getenv("CI") == "" && !truthy(e.Getenv("DEEDGLASS_NO_APP"))
}

func runApp(ctx context.Context, e *Env, args []string) error {
	if len(args) > 0 {
		return usageErr(fmt.Errorf("deedglass app takes flags only (got %q)", args[0]), "Run deedglass app, or deedglass app --stop.")
	}
	v := appFlagValues
	n := 0
	for _, b := range []bool{v.stop, v.reset, v.status} {
		if b {
			n++
		}
	}
	if n > 1 {
		return usageErr(errors.New("pick one of --stop, --status and --reset"), "Run deedglass help app.")
	}
	dir, err := appDir(e)
	if err != nil {
		return failNext(e, ExitFailure, "app_failed", err.Error(), "Set DEEDGLASS_APP_DIR to a folder for the app's data.")
	}
	switch {
	case v.stop:
		return appStop(ctx, e, dir)
	case v.status:
		return appStatus(ctx, e, dir)
	case v.reset:
		return appReset(ctx, e, dir, v.yes)
	}
	return appStart(ctx, e, dir, v)
}

// --- start ---

func appStart(ctx context.Context, e *Env, dir string, v appOpts) error {
	s := e.Style
	inv := invocation(e)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return failNext(e, ExitFailure, "app_failed", "The app's folder couldn't be created: "+err.Error()+".", "Set DEEDGLASS_APP_DIR to a folder you can write to.")
	}
	_ = os.Chmod(dir, 0o700)
	// Already running: sign in again in a new tab.
	if st := readAppState(dir); st != nil && processAlive(st.PID) && appHealthy(ctx, st.Server) {
		link, err := appLoginLink(ctx, dir, appEnvFor(dir, st.Server, st.PGPort))
		if err != nil {
			return appFail(e, "Deedglass is running at "+st.Server+", but a sign-in link couldn't be made: "+err.Error(), inv+" app --stop, then "+inv+" app")
		}
		if e.Globals.JSON {
			return writeJSON(e, map[string]any{"object": "app", "running": true, "server": st.Server, "dir": dir, "already_running": true})
		}
		opened := !v.noOpen && openLink(link)
		fmt.Fprintln(e.Stdout, s.Bold("Deedglass is already running at "+st.Server))
		if !opened {
			fmt.Fprintln(e.Stdout, "Open this link to sign in (it works once, on this computer): "+link)
		}
		fmt.Fprintln(e.Stdout, s.Dim("To stop it: "+inv+" app --stop"))
		return nil
	}

	// 1. The store.
	ls := localstore.New(dir, e.Getenv)
	bins, err := ls.Binaries(ctx)
	var mm *localstore.MajorMismatch
	switch {
	case errors.As(err, &mm):
		return appFail(e, "The app's database was made with PostgreSQL "+strconv.Itoa(mm.Recorded.Major)+", which isn't on this computer any more", "Reinstall PostgreSQL "+strconv.Itoa(mm.Recorded.Major)+", or start over with "+inv+" app --reset (deletes the app's data).")
	case errors.Is(err, localstore.NeedDownload):
		if bins, err = appInstall(ctx, e, ls); err != nil {
			return err
		}
	case err != nil:
		return appFail(e, "PostgreSQL couldn't be found: "+err.Error(), "Unset DEEDGLASS_PG_BIN, or point it at a folder with initdb, pg_ctl and postgres.")
	}
	sp := newSpinner(e)
	sp.Start("Starting the local database", "")
	run, err := ls.Start(ctx, bins)
	sp.Stop()
	if err != nil {
		return appFail(e, "The local database didn't start: "+err.Error(), "Read "+filepath.Join(dir, localstore.LogFile)+", or run "+inv+" app --status.")
	}
	stopStore := func() {
		sctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		_ = ls.Stop(sctx)
	}

	// 2. The address: 4180, the --port asked for, or a free one.
	port := v.port
	if port == 0 {
		port = appPort
		if !localstore.PortFree(port) {
			if port, err = localstore.FreePort(); err != nil {
				stopStore()
				return err
			}
		}
	} else if !localstore.PortFree(port) {
		stopStore()
		return usageErr(fmt.Errorf("port %d is in use", port), "Pick another with --port, or leave it out for a free one.")
	}
	server := "http://127.0.0.1:" + strconv.Itoa(port)
	env := appEnvFor(dir, server, run.Port)

	// 3. Keys, schema and the owner, once.
	sp.Start("Getting your workspace ready", "")
	err = appPrepare(ctx, dir, env)
	sp.Stop()
	if err != nil {
		stopStore()
		return appFail(e, "The app couldn't be set up: "+err.Error(), "Run "+inv+" app again; if it fails the same way, run "+inv+" app --status and file an issue with both outputs.")
	}

	// 4. serve, in this process, logging to serve.log.
	logf, err := os.OpenFile(filepath.Join(dir, appServeLog), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		stopStore()
		return err
	}
	defer logf.Close()
	sctx, cancelServe := context.WithCancel(ctx)
	defer cancelServe()
	done := make(chan error, 1)
	go func() {
		se := *e
		se.Stdout, se.Stderr = logf, logf
		se.IsTTY, se.Style.Color = false, false
		se.Globals = Globals{}
		se.LoadOptions = func() config.LoadOptions {
			return config.LoadOptions{Env: env, FS: os.DirFS(dir), Dir: dir}
		}
		serveFlagValues = serveOpts{}
		appLocalInfo = &localInfo{dir: dir, address: server}
		done <- runServe(sctx, &se, nil)
	}()
	deadline := time.Now().Add(60 * time.Second)
	for !appHealthy(ctx, server) {
		select {
		case err := <-done:
			stopStore()
			if err == nil {
				err = errors.New("it stopped")
			}
			return appFail(e, "Deedglass didn't start: "+plainError(err.Error()), "Read "+filepath.Join(dir, appServeLog)+".")
		case <-time.After(150 * time.Millisecond):
		}
		if time.Now().After(deadline) {
			cancelServe()
			<-done
			stopStore()
			return appFail(e, "Deedglass didn't answer on "+server+" within a minute", "Read "+filepath.Join(dir, appServeLog)+".")
		}
	}
	_ = writeAppState(dir, &appState{Object: "deedglass_app", Version: 1, PID: os.Getpid(), Server: server, PGPort: run.Port,
		StartedAt: time.Now().UTC().Format(time.RFC3339)})

	// 5. Sign the owner in and say three things.
	link, lerr := appLoginLink(ctx, dir, env)
	opened := lerr == nil && !v.noOpen && openLink(link)
	if e.Globals.JSON {
		_ = writeJSON(e, map[string]any{"object": "app", "running": true, "server": server, "dir": dir, "pid": os.Getpid()})
	} else {
		fmt.Fprintln(e.Stdout, s.Bold("Deedglass is running at "+server))
		switch {
		case lerr != nil:
			fmt.Fprintln(e.Stdout, s.Amber("The sign-in link couldn't be made ("+lerr.Error()+"). Run "+inv+" app again in another terminal."))
		case !opened:
			fmt.Fprintln(e.Stdout, "Open this link to sign in (it works once, on this computer): "+link)
		}
		fmt.Fprintln(e.Stdout, "Press Ctrl+C to stop, or run "+inv+" app --stop.")
		fmt.Fprintln(e.Stdout, s.Dim("Prefer the terminal? "+inv+" help"))
	}

	// 6. Run until Ctrl+C, app --stop (SIGTERM) or the server stops.
	sigs := make(chan os.Signal, 2)
	signal.Notify(sigs, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sigs)
	var serveErr error
	select {
	case <-sigs:
		cancelServe()
		serveErr = <-done
	case <-ctx.Done():
		cancelServe()
		serveErr = <-done
	case serveErr = <-done:
	}
	removeAppState(dir, os.Getpid())
	stopStore()
	if !e.Globals.JSON {
		fmt.Fprintln(e.Stdout, "Stopped. Your data is kept in "+dir+".")
	}
	if serveErr != nil && !errors.Is(serveErr, context.Canceled) {
		return appFail(e, "Deedglass stopped: "+plainError(serveErr.Error()), "Read "+filepath.Join(dir, appServeLog)+".")
	}
	return nil
}

// appInstall is the one-time download, said plainly before it starts.
func appInstall(ctx context.Context, e *Env, ls *localstore.Store) (localstore.Binaries, error) {
	inv := invocation(e)
	b, ok := localstore.CurrentBundle()
	if !ok {
		return localstore.Binaries{}, appFail(e, "There's no ready-made PostgreSQL for "+runtime.GOOS+"/"+runtime.GOARCH,
			"Install PostgreSQL 16 or newer, then run "+inv+" app again (or set DEEDGLASS_PG_BIN to its bin folder).")
	}
	bundle := strings.TrimSpace(e.Getenv("DEEDGLASS_PG_BUNDLE"))
	if bundle == "" && truthy(e.Getenv("DEEDGLASS_NO_NETWORK")) {
		return localstore.Binaries{}, appFail(e, "The app needs PostgreSQL, and DEEDGLASS_NO_NETWORK=1 stops the one-time download",
			"Download "+b.URL()+" on another computer, then set DEEDGLASS_PG_BUNDLE to that file.")
	}
	if !e.Globals.JSON {
		if bundle != "" {
			fmt.Fprintln(e.Stdout, "First run: installing PostgreSQL "+strings.TrimSuffix(localstore.PGVersion, ".0")+" from "+bundle+".")
		} else {
			fmt.Fprintf(e.Stdout, "First run: downloading PostgreSQL %s for this computer (%d MB, once).\n", strings.TrimSuffix(localstore.PGVersion, ".0"), b.MB())
			fmt.Fprintln(e.Stdout, e.Style.Dim("It stays on this computer; after this the app works offline."))
		}
	}
	var progress localstore.Progress
	live := e.IsTTY && !e.Globals.JSON && e.Getenv("TERM") != "dumb"
	if live && bundle == "" {
		progress = func(done, total int64) {
			fmt.Fprintf(e.Stdout, "\r  %3d%%  %d of %d MB", done*100/max(total, 1), done/1_000_000, total/1_000_000)
		}
	}
	bins, err := ls.Install(ctx, progress)
	if progress != nil {
		fmt.Fprint(e.Stdout, "\r\033[K")
	}
	var de *localstore.DownloadError
	switch {
	case errors.As(err, &de) && de.Offline:
		return bins, appFail(e, "PostgreSQL couldn't be downloaded: this computer seems to be offline",
			"Connect to the internet and run "+inv+" app again, or set DEEDGLASS_PG_BUNDLE to a copy of "+b.FileName()+".")
	case err != nil:
		return bins, appFail(e, "PostgreSQL couldn't be installed: "+err.Error(), "Run "+inv+" app again; a proxy can be set with HTTPS_PROXY.")
	}
	return bins, nil
}

// appEnvFor is the configuration serve and the one-shot steps run with: dev mode on loopback, the
// local store's database, the app's seal key, and nothing from the person's own DEEDGLASS_*
// variables.
func appEnvFor(dir, server string, pgPort int) map[string]string {
	ls := localstore.New(dir, nil)
	st, _ := ls.ReadState()
	env := map[string]string{
		"DEEDGLASS_ENV":       "dev",
		"DEEDGLASS_LISTEN":    strings.TrimPrefix(server, "http://"),
		"DEEDGLASS_BASE_URL":  server,
		"DEEDGLASS_LOG_LEVEL": "info",
		// Connections to a FHIR server or database on the clinic's own network (Connect).
		"DEEDGLASS_ALLOW_PRIVATE_NETWORKS": "1",
	}
	if st != nil {
		r := localstore.Running{Port: pgPort, MigratorPassword: st.MigratorPassword, AppPassword: st.AppPassword}
		env["DEEDGLASS_DATABASE_URL"] = r.AppURL()
		env["DEEDGLASS_MIGRATE_DATABASE_URL"] = r.MigratorURL()
	}
	if k := newestSealKey(filepath.Join(dir, appKeysDir)); k != "" {
		env["DEEDGLASS_SEAL_KEY_FILE"] = k
	}
	return env
}

// appPrepare makes the seal key, applies migrations and creates the org with its owner, each once.
func appPrepare(ctx context.Context, dir string, env map[string]string) error {
	keys := filepath.Join(dir, appKeysDir)
	if newestSealKey(keys) == "" {
		if err := os.MkdirAll(keys, 0o700); err != nil {
			return err
		}
		if _, err := selfRun(ctx, dir, env, "keys", "gen", "--dir", keys); err != nil {
			return fmt.Errorf("the seal key: %w", err)
		}
		env["DEEDGLASS_SEAL_KEY_FILE"] = newestSealKey(keys)
	}
	if _, err := selfRun(ctx, dir, env, "--json", "migrate", "up"); err != nil {
		return fmt.Errorf("the database schema: %w", err)
	}
	loc := readAppLocal(dir)
	if k := setup.ReadKeyFile(filepath.Join(dir, appAdminKey)); k != "" && loc.Org != "" {
		return nil
	}
	out, err := selfRun(ctx, dir, env, "--json", "org", "create", "--name", appOrgName, "--slug", appOrgSlug, "--owner-email", setup.DefaultOwner, "--admin-key")
	var created struct {
		Org struct {
			ID string `json:"id"`
		} `json:"org"`
		AdminKey struct {
			Secret string `json:"secret"`
		} `json:"admin_key"`
	}
	if err == nil && json.Unmarshal([]byte(out), &created) == nil && created.AdminKey.Secret != "" {
		loc.Org = created.Org.ID
	} else {
		// The org exists (an earlier run stopped before saving its key): a new admin key for it.
		kout, kerr := selfRun(ctx, dir, env, "--json", "org", "keys", "create", "--org", appOrgSlug, "--name", "app-admin", "--mode", "test")
		var k struct {
			Secret string `json:"secret"`
			Org    string `json:"org"`
		}
		if kerr != nil || json.Unmarshal([]byte(kout), &k) != nil || k.Secret == "" {
			if err == nil {
				err = kerr
			}
			return fmt.Errorf("the owner: %v", err)
		}
		created.AdminKey.Secret, loc.Org = k.Secret, k.Org
	}
	if err := localstore.WriteSecret(filepath.Join(dir, appAdminKey), []byte(created.AdminKey.Secret+"\n")); err != nil {
		return err
	}
	return writeAppLocal(dir, loc)
}

// appLoginLink issues a one-time sign-in link for the owner; local=1 lets the confirm page sign in
// without the extra click, and only on a loopback address.
func appLoginLink(ctx context.Context, dir string, env map[string]string) (string, error) {
	out, err := selfRun(ctx, dir, env, "--json", "org", "login-link", "--email", setup.DefaultOwner)
	if err != nil {
		return "", err
	}
	var l struct {
		Link string `json:"link"`
	}
	if err := json.Unmarshal([]byte(out), &l); err != nil || l.Link == "" {
		return "", errors.New("no link in the answer")
	}
	return l.Link + "&local=1", nil
}

// selfRun runs this binary in dir with exactly env plus the few variables a program needs to run.
func selfRun(ctx context.Context, dir string, env map[string]string, args ...string) (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, exe, args...)
	cmd.Dir = dir
	cmd.Env = childBaseEnv(env)
	var out, errb strings.Builder
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(errb.String())
		if i := strings.LastIndexByte(msg, '\n'); i >= 0 {
			msg = msg[i+1:]
		}
		if msg == "" {
			msg = err.Error()
		}
		return out.String(), errors.New(plainError(msg))
	}
	return out.String(), nil
}

// childBaseEnv keeps what a program needs on each system, never the person's DEEDGLASS_* values.
func childBaseEnv(extra map[string]string) []string {
	var env []string
	for _, k := range []string{"PATH", "HOME", "TMPDIR", "USER", "LANG", "TZ", "SystemRoot", "SYSTEMROOT", "USERPROFILE", "AppData", "APPDATA",
		"LOCALAPPDATA", "TEMP", "TMP", "ComSpec", "PATHEXT", "ProgramFiles"} {
		if v := os.Getenv(k); v != "" {
			env = append(env, k+"="+v)
		}
	}
	env = append(env, "NO_COLOR=1", "DEEDGLASS_NO_BANNER=1", "DEEDGLASS_NO_APP=1")
	for k, v := range extra {
		env = append(env, k+"="+v)
	}
	return env
}

func newestSealKey(dir string) string {
	m, _ := filepath.Glob(filepath.Join(dir, "sk_*.json"))
	sort.Strings(m) // ids are time-ordered ULIDs
	if len(m) == 0 {
		return ""
	}
	return m[len(m)-1]
}

// --- stop, status, reset ---

func appStop(ctx context.Context, e *Env, dir string) error {
	st := readAppState(dir)
	stopped := false
	if st != nil && st.PID > 0 && st.PID != os.Getpid() && processAlive(st.PID) {
		if p, err := os.FindProcess(st.PID); err == nil {
			if runtime.GOOS == "windows" {
				_ = p.Kill()
			} else {
				_ = p.Signal(syscall.SIGTERM)
			}
			for i := 0; i < 600 && processAlive(st.PID); i++ { // up to 60 s: serve drains, then the database stops
				time.Sleep(100 * time.Millisecond)
			}
			stopped = true
		}
	}
	ls := localstore.New(dir, e.Getenv)
	if running, _ := ls.Status(ctx); running {
		if err := ls.Stop(ctx); err != nil {
			return appFail(e, "The local database didn't stop: "+err.Error(), "Run "+invocation(e)+" app --stop again.")
		}
		stopped = true
	}
	removeAppState(dir, 0)
	if e.Globals.JSON {
		return writeJSON(e, map[string]any{"object": "app", "running": false, "stopped": stopped, "dir": dir})
	}
	if stopped {
		fmt.Fprintln(e.Stdout, e.Style.OKLine("Deedglass stopped. Your data is kept in "+dir+"."))
	} else {
		fmt.Fprintln(e.Stdout, "Deedglass isn't running.")
	}
	return nil
}

func appStatus(ctx context.Context, e *Env, dir string) error {
	st := readAppState(dir)
	running := st != nil && processAlive(st.PID) && appHealthy(ctx, st.Server)
	ls := localstore.New(dir, e.Getenv)
	pgRunning, pgPort := ls.Status(ctx)
	ss, _ := ls.ReadState()
	out := map[string]any{"object": "app", "running": running, "dir": dir, "database_running": pgRunning, "set_up": ls.Initialized()}
	if running {
		out["server"], out["pid"] = st.Server, st.PID
	}
	if ss != nil {
		out["postgres"] = map[string]any{"version": ss.Binaries.Version, "source": ss.Binaries.Source, "dir": ss.Binaries.Dir, "port": pgPort}
	}
	if e.Globals.JSON {
		return writeJSON(e, out)
	}
	s := e.Style
	inv := invocation(e)
	switch {
	case running:
		fmt.Fprintln(e.Stdout, s.OKLine("Deedglass is running at "+st.Server))
	case !ls.Initialized():
		fmt.Fprintln(e.Stdout, "Deedglass isn't set up on this computer yet. Run "+inv+" app to start it.")
		return nil
	default:
		fmt.Fprintln(e.Stdout, "Deedglass isn't running. Run "+inv+" app to start it.")
	}
	rows := []string{"data      " + dir}
	if ss != nil {
		where := "installed on this computer"
		if ss.Binaries.Source == localstore.SourceDownloaded {
			where = "downloaded for the app"
		}
		rows = append(rows, "database  PostgreSQL "+ss.Binaries.Version+", "+where)
	}
	for _, r := range rows {
		fmt.Fprintln(e.Stdout, "  "+r)
	}
	return nil
}

func appReset(ctx context.Context, e *Env, dir string, yes bool) error {
	inv := invocation(e)
	if _, err := os.Stat(dir); errors.Is(err, fs.ErrNotExist) {
		fmt.Fprintln(e.Stdout, "There's nothing to reset: the app has no data on this computer.")
		return nil
	}
	if !yes {
		if !e.StdinTTY {
			return usageErr(errors.New("--reset deletes every record, rule and key the app holds, so it asks first"), "Run it in a terminal, or add --yes: "+inv+" app --reset --yes")
		}
		fmt.Fprintf(e.Stdout, "This deletes every record, rule, connection and key in %s.\nType reset to confirm: ", dir)
		line, _ := bufio.NewReader(e.stdin()).ReadString('\n')
		if strings.TrimSpace(line) != "reset" {
			fmt.Fprintln(e.Stdout, "Nothing was deleted.")
			return nil
		}
	}
	saved := e.Globals.JSON
	e.Globals.JSON = true
	err := appStop(ctx, &Env{Stdout: io.Discard, Stderr: io.Discard, Getenv: e.Getenv, Globals: e.Globals, Style: e.Style}, dir)
	e.Globals.JSON = saved
	if err != nil {
		return err
	}
	// Keep the downloaded PostgreSQL (runtime/) so a reset doesn't download it again.
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, en := range entries {
		if en.Name() == "runtime" {
			continue
		}
		if err := os.RemoveAll(filepath.Join(dir, en.Name())); err != nil {
			return appFail(e, "Some of the app's data couldn't be deleted: "+err.Error(), "Close anything using "+dir+", then run "+inv+" app --reset again.")
		}
	}
	if e.Globals.JSON {
		return writeJSON(e, map[string]any{"object": "app", "reset": true, "dir": dir})
	}
	fmt.Fprintln(e.Stdout, e.Style.OKLine("The app's data is deleted. Run "+inv+" app to start fresh."))
	return nil
}

// --- small helpers ---

func appFail(e *Env, msg, next string) error {
	return failNext(e, ExitFailure, "app_failed", sentence(msg), next)
}

func readAppState(dir string) *appState {
	b, err := os.ReadFile(filepath.Join(dir, appStateFile))
	if err != nil {
		return nil
	}
	var st appState
	if json.Unmarshal(b, &st) != nil || st.Server == "" {
		return nil
	}
	return &st
}

func writeAppState(dir string, st *appState) error {
	b, _ := json.MarshalIndent(st, "", "  ")
	return localstore.WriteSecret(filepath.Join(dir, appStateFile), append(b, '\n'))
}

// removeAppState deletes app.json, only when it names pid (0: whatever it names).
func removeAppState(dir string, pid int) {
	if st := readAppState(dir); st != nil && pid != 0 && st.PID != pid {
		return
	}
	_ = os.Remove(filepath.Join(dir, appStateFile))
}

func readAppLocal(dir string) appLocal {
	l := appLocal{Object: "local", FirstRun: true}
	if b, err := os.ReadFile(filepath.Join(dir, appLocalFile)); err == nil {
		_ = json.Unmarshal(b, &l)
	}
	l.Object = "local"
	return l
}

func writeAppLocal(dir string, l appLocal) error {
	l.Object = "local"
	b, _ := json.MarshalIndent(l, "", "  ")
	return localstore.WriteSecret(filepath.Join(dir, appLocalFile), append(b, '\n'))
}

func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	if runtime.GOOS == "windows" {
		return true // FindProcess opens the process on Windows: it exists
	}
	return p.Signal(syscall.Signal(0)) == nil
}

func appHealthy(ctx context.Context, server string) bool {
	c := egress.FirstParty(egress.FirstPartyOptions{Timeout: 2 * time.Second})
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, server+"/v1/health", nil)
	if err != nil {
		return false
	}
	res, err := c.Do(req)
	if err != nil {
		return false
	}
	res.Body.Close()
	return res.StatusCode == http.StatusOK
}

// openLink opens a URL in the default browser; false when it can't (then the link is printed).
var openLink = func(u string) bool {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", u)
	case "linux":
		if os.Getenv("DISPLAY") == "" && os.Getenv("WAYLAND_DISPLAY") == "" {
			return false
		}
		cmd = exec.Command("xdg-open", u)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", u)
	default:
		return false
	}
	return cmd.Start() == nil
}

// localInfo is set when serve runs inside deedglass app: GET /v1/local answers only then.
type localInfo struct{ dir, address string }

var appLocalInfo *localInfo
