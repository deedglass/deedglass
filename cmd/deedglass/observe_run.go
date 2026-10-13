package main

//deedglass:allow-exec observe run starts the person's own agent command (argv, never a shell) with inherited stdio, forwards SIGINT and SIGTERM, and passes its exit code through

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"

	"github.com/deedglass/deedglass/internal/observe"
)

// deedglass observe run --connector <name> [--label …] [--agent …] -- <cmd …> (S50 §3.2): open a
// watched session, run the agent's command with the terminal's stdio, close the session when the
// command exits, wait for the seal and print what Postgres committed. The command's exit code is
// passed through (130 after Ctrl-C). The child never sees DEEDGLASS_* variables: the admin key that
// opened the session must not be the agent's.

func runObserveRun(ctx context.Context, e *Env, args []string) error {
	var cmd []string
	for i, a := range args {
		if a == "--" {
			args, cmd = args[:i], args[i+1:]
			break
		}
	}
	pos, err := mixedArgs("deedglass observe run", observeStartFlags, args)
	if err != nil {
		return err
	}
	if len(pos) != 0 || observeStartValues.connector == "" || len(cmd) == 0 {
		return usageErr(errors.New("deedglass observe run needs --connector and the agent's command after --"),
			"deedglass observe run --connector pg-demo -- ./agent.sh")
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

	code, runErr := runChild(ctx, cmd)

	// Close even when the command failed or the person pressed Ctrl-C: the session holds what
	// Postgres committed until now either way.
	closeCtx := context.WithoutCancel(ctx)
	sealed, raw, err := closeSession(closeCtx, c, x.ID, true)
	if err != nil {
		return apiErr(e, c, err)
	}
	if e.Globals.JSON {
		if _, err := e.Stdout.Write(append(raw, '\n')); err != nil {
			return err
		}
	} else {
		printSealed(e.Stderr, e.Style, sealed)
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
	return nil
}

// runChild runs argv with the terminal's stdio and DEEDGLASS_* removed from its environment,
// forwarding SIGINT and SIGTERM. It returns the exit code (128+signal when a signal ended it).
func runChild(ctx context.Context, argv []string) (int, error) {
	path, err := exec.LookPath(argv[0])
	if err != nil {
		return 0, err
	}
	child := exec.Command(path, argv[1:]...)
	child.Stdin, child.Stdout, child.Stderr = os.Stdin, os.Stdout, os.Stderr
	child.Env = childEnv(os.Environ())
	sigs := make(chan os.Signal, 4)
	signal.Notify(sigs, os.Interrupt, syscall.SIGTERM)
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
