package main

//deedglass:allow-exec observe run starts the person's own agent command (argv, never a shell) with inherited stdio, forwards SIGINT, SIGTERM and SIGHUP, and passes its exit code through

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/deedglass/deedglass/internal/decoder"
	"github.com/deedglass/deedglass/internal/observe"
	"github.com/deedglass/deedglass/internal/observeapi"
	"github.com/deedglass/deedglass/internal/setup"
	"github.com/deedglass/deedglass/pkg/deedglass"
)

// deedglass observe run --connector <name> [--label …] [--agent …] [--undo-sql <file>] -- <cmd …>
// (S50 §3.2): open a watched session, run the agent's command with the terminal's stdio, close the
// session when the command exits, wait for the seal and print what Postgres committed. The
// command's exit code is passed through (130 after Ctrl-C). The child never sees DEEDGLASS_*
// variables: the admin key that opened the session must not be the agent's.
//
// --undo-sql (FEAT-04, watching a coding agent such as `-- claude`) saves the session's free undo
// SQL to a file once the session is sealed, so the end of the run shows what was caught, the
// any-writer note and the file to review and run. Deedglass never runs it. Given a folder, the file
// is undo-<session id>.sql in it, so sessions never share a file.

var observeRunValues struct{ undoSQL string }

func observeRunFlags(fs *flag.FlagSet) {
	observeStartFlags(fs)
	observeRunValues.undoSQL = ""
	fs.StringVar(&observeRunValues.undoSQL, "undo-sql", "", "after the seal, save the undo SQL (free) to this file, or to undo-<session id>.sql in this folder, for you to review and run; Deedglass runs nothing")
}

func runObserveRun(ctx context.Context, e *Env, args []string) error {
	var cmd []string
	for i, a := range args {
		if a == "--" {
			args, cmd = args[:i], args[i+1:]
			break
		}
	}
	pos, err := mixedArgs("deedglass observe run", observeRunFlags, args)
	if err != nil {
		return err
	}
	if len(pos) != 0 || observeStartValues.connector == "" || len(cmd) == 0 {
		return usageErr(errors.New("deedglass observe run needs --connector and the agent's command after --"),
			"deedglass observe run --connector pg-demo -- ./agent.sh")
	}
	undoArg := observeRunValues.undoSQL
	if undoArg != "" {
		if problem := undoPathProblem(undoArg); problem != "" {
			return usageErr(errors.New("--undo-sql: "+problem), "deedglass observe run --connector pg-demo --undo-sql ~/deedglass-undo -- ./agent.sh")
		}
	}
	c, err := apiClient(e)
	if err != nil {
		return err
	}
	x, _, err := openSession(ctx, c, observeStartValues.connector, observeStartValues.label, observeStartValues.agent)
	if err != nil {
		return apiErr(e, c, err)
	}
	fmt.Fprintln(e.Stderr, watchingLine(e.Style, x))
	if p := adminKeyInReach(); p != "" {
		// observe run keeps DEEDGLASS_* out of the agent's environment, but a file the agent can
		// read is in its reach all the same (FEAT-04 review).
		fmt.Fprintln(e.Stderr, "  The agent can read "+clean(p)+": with that admin key it could stop this session or leave its own transactions out. Move the file out of this folder and its parents.")
	}

	code, runErr := runChild(ctx, cmd)

	// Close even when the command failed or the person pressed Ctrl-C: the session holds what
	// Postgres committed until now either way.
	closeCtx := context.WithoutCancel(ctx)
	sealed, raw, err := closeSession(closeCtx, c, x.ID, true)
	if err != nil {
		return apiErr(e, c, err)
	}
	var end runEnd
	var saveErr error
	if undoArg != "" {
		end.undoLine, end.refused, saveErr = saveUndoSQL(closeCtx, c, sealed, undoFile(undoArg, sealed.ID))
	}
	if n := decoderNote(closeCtx, c, observeStartValues.connector); n != "" {
		end.notes = append(end.notes, n)
	}
	if e.Globals.JSON {
		if _, err := e.Stdout.Write(append(raw, '\n')); err != nil {
			return err
		}
		for _, l := range append([]string{end.undoLine}, end.notes...) {
			if l != "" {
				fmt.Fprintln(e.Stderr, strings.TrimSpace(l))
			}
		}
	} else {
		printSealed(e.Stderr, e.Style, sealed, end)
	}
	if runErr != nil {
		return &ExitError{Code: ExitFailure, ErrCode: "command_failed", Err: fmt.Errorf("Couldn't start %s: %s", clean(cmd[0]), lastPart(runErr)),
			Next: "Check the command after --. The session was closed and holds nothing the command didn't do."}
	}
	if code != 0 {
		// The command's own exit code passes through (gendocs reads ExitFailure from the literal).
		e := &ExitError{Code: ExitFailure, ErrCode: "agent_exit", printed: true, Err: fmt.Errorf("the command exited with %d", code)}
		e.Code = code
		return e
	}
	if sealed.State == observe.StateClosing {
		return &ExitError{Code: ExitUsage, ErrCode: "seal_pending", printed: true, Err: errors.New("the session is still closing")}
	}
	if saveErr != nil {
		// In --json mode the envelope goes to stderr, so a script sees undo_sql_not_saved; in text
		// mode the line above already said why and what to run.
		return &ExitError{Code: ExitFailure, ErrCode: "undo_sql_not_saved", printed: !e.Globals.JSON, Err: errors.New("the undo SQL wasn't saved"),
			Next: "Print it instead: deedglass observe show " + clean(x.ID) + " --undo-sql"}
	}
	return nil
}

// runEnd is what observe run adds to the sealed session's lines.
type runEnd struct {
	undoLine string   // the free way back --undo-sql took: the file it saved, or why it saved none
	refused  bool     // the record can't undo the session, so no one-step undo is offered either
	notes    []string // more notes, such as the decoder having stopped
}

// undoFile is where --undo-sql's SQL goes: arg itself, or undo-<id>.sql when arg is a folder.
func undoFile(arg, id string) string {
	if st, err := os.Stat(arg); err == nil && st.IsDir() {
		return filepath.Join(arg, "undo-"+id+".sql")
	}
	return arg
}

// adminKeyInReach is the admin key file deedglass init wrote, when it is in .deedglass/ of the
// working directory (the agent's, too) or a parent; "" when there is none.
func adminKeyInReach() string {
	dir, err := os.Getwd()
	if err != nil {
		return ""
	}
	for i := 0; i < 32; i++ {
		if p := setup.Path(dir, setup.AdminKeyFile); fileExists(p) {
			return p
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return ""
}

// decoderNote says when the connector's decoder stopped or saw its publication change, because
// rows committed after that aren't in the session: a DROP TABLE, DROP SCHEMA or an ORM's reset
// takes a watched table out of the publication and writes no row change of its own. "" when the
// decoder is fine or its status can't be read.
func decoderNote(ctx context.Context, c *deedglass.Client, connector string) string {
	v, err := getDecoder(ctx, c, connector, 0)
	if err != nil {
		return ""
	}
	reason := ""
	if v.Reason != nil {
		reason = *v.Reason
	}
	next := " Next: deedglass decoder status --connector " + clean(connector)
	switch {
	case v.Status == decoder.StatusHalted && strings.HasPrefix(reason, "publication_"):
		what := "the publication changed"
		if m := detailList(v.Detail, "missing"); m != "" {
			what = m + " left the publication"
		} else if reason == decoder.ReasonPublicationMissing {
			what = "the publication is gone"
		}
		return "  The decoder stopped: " + what + " (a DROP TABLE, DROP SCHEMA or an ORM's reset does this). Rows it hadn't read by then aren't in this session, and schema changes never are." + next
	case v.Status == decoder.StatusHalted:
		return "  The decoder stopped (" + clean(reason) + "): rows it hadn't read by then aren't in this session." + next
	case reason == decoder.ReasonPublicationChanged:
		return "  The publication changed during this window: a table was dropped, recreated or added." + next
	}
	return ""
}

// detailList is a decoder detail's list of names, joined ("" when absent).
func detailList(d map[string]any, key string) string {
	xs, _ := d[key].([]any)
	var out []string
	for _, x := range xs {
		if s, ok := x.(string); ok && s != "" {
			out = append(out, s)
		}
	}
	return clean(strings.Join(out, ", "))
}

// saveUndoSQL writes a sealed session's free undo SQL to path and returns the line that says so
// (or why nothing was saved), and whether the record can't undo the session. The error is set only
// when something went wrong on the way (the preview couldn't be read, the file couldn't be
// written); a session with nothing to undo, or one the record can't undo, is a line, not an error.
func saveUndoSQL(ctx context.Context, c *deedglass.Client, x *observeapi.SessionJSON, path string) (string, bool, error) {
	later := "(umask 077; deedglass observe show " + clean(x.ID) + " --undo-sql > " + pathArg(path) + ")"
	// A file already at path is from an earlier session: say so whether it is kept or replaced.
	earlier := fileExists(path)
	kept := ""
	if earlier {
		kept = " The " + clean(path) + " already there is from an earlier session; it was left as it was."
	}
	switch {
	case x.State == observe.StateClosing:
		return "  The undo SQL isn't saved: the session is still closing." + kept + " Next: " + later, false, nil
	case x.Summary.Transactions == 0:
		return "  No undo SQL saved: the change log holds no row changes on the watched tables. Schema changes (a DROP TABLE, an ORM's reset) never appear in it." + kept, false, nil
	}
	pv, _, err := undoPreview(ctx, c, x.ID)
	if err != nil {
		return "  Couldn't read the undo SQL: " + why(err) + "." + kept + " Next: " + later, false, err
	}
	if pv.SQL == nil {
		return "  No undo SQL saved: " + strings.TrimSuffix(clean(undoRefusals(pv)[0]), ".") + "." + kept, true, nil
	}
	if err := writeFileReplacing(path, []byte(*pv.SQL)); err != nil {
		return "  Couldn't save the undo SQL to " + clean(path) + ": " + why(err) + "." + kept + " Next: " + later, false, err
	}
	saved := "  Undo SQL saved to " + clean(path) + " (free)."
	if earlier {
		saved = "  Undo SQL saved to " + clean(path) + " (free), replacing an earlier file."
	}
	return saved + " Deedglass doesn't run it: review it, then run it yourself:\n" +
		"    psql \"$DATABASE_URL\" -v ON_ERROR_STOP=1 -f " + pathArg(path) + "\n" +
		"  It holds row values: keep it out of version control and out of the agent's folder.", false, nil
}

// fileExists reports whether anything (a symlink too) is at p.
func fileExists(p string) bool {
	_, err := os.Lstat(p)
	return err == nil
}

// pathArg is path as one shell word that no command reads as an option.
func pathArg(path string) string {
	p := clean(path)
	if strings.HasPrefix(p, "-") {
		p = "./" + p
	}
	return shellQuote(p)
}

// undoPathProblem says why arg can't take the undo SQL ("" when it can). It runs before the agent
// does, so a long session doesn't end without its undo SQL: arg is a folder that takes a new file,
// or a file name in one.
func undoPathProblem(arg string) string {
	if arg == "-" {
		return "name a file or a folder; to print the undo SQL, run deedglass observe show <session> --undo-sql"
	}
	dir := filepath.Dir(arg)
	if st, err := os.Stat(arg); err == nil && st.IsDir() {
		dir = arg
	} else if st, err := os.Stat(dir); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "the folder " + clean(dir) + " doesn't exist"
		}
		return "can't use " + clean(dir) + ": " + why(err)
	} else if !st.IsDir() {
		return clean(dir) + " isn't a folder"
	}
	f, err := os.CreateTemp(dir, ".deedglass-undo-*.sql")
	if err != nil {
		return "can't write in " + clean(dir) + ": " + why(err)
	}
	name := f.Name()
	_ = f.Close()
	_ = os.Remove(name)
	return ""
}

// why is an error's last part as a clause: cleaned, without its closing period.
func why(err error) string { return strings.TrimSuffix(clean(lastPart(err)), ".") }

// writeFileReplacing writes b to path with mode 0600 through a new file in the same directory and
// a rename, so an existing file is replaced whole and a symlink at path is replaced, never
// followed.
func writeFileReplacing(path string, b []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".deedglass-undo-*.sql")
	if err != nil {
		return err
	}
	tmp := f.Name()
	_, err = f.Write(b)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Chmod(tmp, 0o600)
	}
	if err == nil {
		err = os.Rename(tmp, path)
	}
	if err != nil {
		_ = os.Remove(tmp)
	}
	return err
}

// runChild runs argv with the terminal's stdio and DEEDGLASS_* removed from its environment,
// forwarding SIGINT, SIGTERM and SIGHUP (a closed terminal tab), so the session is still closed. It returns the exit code (128+signal when a signal ended it).
func runChild(ctx context.Context, argv []string) (int, error) {
	path, err := exec.LookPath(argv[0])
	if err != nil {
		return 0, err
	}
	child := exec.Command(path, argv[1:]...)
	child.Stdin, child.Stdout, child.Stderr = os.Stdin, os.Stdout, os.Stderr
	child.Env = childEnv(os.Environ())
	sigs := make(chan os.Signal, 4)
	signal.Notify(sigs, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer signal.Stop(sigs)
	if err := child.Start(); err != nil {
		return 0, err
	}
	done := make(chan error, 1)
	go func() { done <- child.Wait() }()
	for {
		select {
		case s := <-sigs:
			_ = child.Process.Signal(s)
		case <-ctx.Done():
			_ = child.Process.Signal(syscall.SIGTERM)
		case err := <-done:
			return exitCode(err), nil
		}
	}
}

// childEnv drops every DEEDGLASS_ variable (the admin key above all).
func childEnv(env []string) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		if strings.HasPrefix(strings.ToUpper(kv), "DEEDGLASS_") {
			continue
		}
		out = append(out, kv)
	}
	return out
}

// exitCode is a finished command's code: its own, or 128+signal when a signal ended it.
func exitCode(err error) int {
	if err == nil {
		return 0
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		if ws, ok := ee.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
			return 128 + int(ws.Signal())
		}
		return ee.ExitCode()
	}
	return 1
}
