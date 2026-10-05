package wrap

//deedglass:allow-exec mcp wrap starts and supervises the MCP server named after --

import (
	"bufio"
	"context"
	"errors"
	"io"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/deedglass/deedglass/internal/mcp/jsonrpc"
)

// upstream supervises the MCP server child process (S14 §3.8): process-group signals, a stripped
// environment, stderr to the log, restart with backoff after a crash (replaying a legacy
// initialize), and a stop after five crashes in a minute.

var (
	errDead       = errors.New("upstream keeps crashing")
	errBufferFull = errors.New("upstream restarting and the buffer is full")
)

type upState int

const (
	upStarting upState = iota
	upRunning
	upRestarting
	upDead
	upClosed
)

// maxBuffered is how many messages wait for a restarting server.
const maxBuffered = 64

type queued struct {
	msg []byte
	p   *pending // nil for notifications and responses
}

type upstream struct {
	w   *Wrapper
	mu  sync.Mutex
	st  upState
	gen int
	cmd *exec.Cmd
	in  io.WriteCloser
	out *jsonrpc.Writer
	buf []queued

	crashes []time.Time
	exited  chan struct{} // closed when the current child has exited
	started time.Time
}

func newUpstream(w *Wrapper) *upstream { return &upstream{w: w} }

// start launches the first child. A launch failure counts as a crash, so the relay stays up and
// answers every call with a calm message.
func (u *upstream) start() {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.st = upStarting
	u.launchLocked()
}

// launchLocked starts a child; on success it replays the legacy handshake (if any) and releases
// the buffer, else it schedules the next restart.
func (u *upstream) launchLocked() {
	argv := u.w.cfg.Upstream
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Env = ChildEnv(u.w.cfg.Env)
	setProcessGroup(cmd)
	stdin, err1 := cmd.StdinPipe()
	stdout, err2 := cmd.StdoutPipe()
	stderr, err3 := cmd.StderrPipe()
	if err := errors.Join(err1, err2, err3); err != nil {
		u.w.log.sym(symDenied, "upstream could not start", err.Error())
		u.crashLocked(-1)
		return
	}
	if err := cmd.Start(); err != nil {
		u.w.log.sym(symDenied, "upstream could not start", strings.Join(argv, " "), err.Error())
		u.crashLocked(-1)
		return
	}
	u.gen++
	gen := u.gen
	u.cmd, u.in = cmd, stdin
	u.out = jsonrpc.NewWriter(stdin)
	u.exited = make(chan struct{})
	u.started = time.Now()
	u.w.log.sym(symOK, "upstream started", strings.Join(argv, " "), "pid "+itoa(cmd.Process.Pid))
	exited := u.exited
	go u.pumpStderr(stderr)
	go u.readLoop(gen, stdout, cmd, exited)

	init, initialized := u.w.legacyHandshake()
	if init != nil && gen > 1 {
		// Replay the client's initialize to the new child before anything else; its answer is
		// swallowed (the client already has one).
		u.st = upRestarting
		go u.replay(gen, init, initialized)
		return
	}
	u.st = upRunning
	u.flushLocked()
}

func (u *upstream) replay(gen int, init jsonrpc.Envelope, initialized []byte) {
	done := make(chan struct{})
	p := u.w.internalPending("initialize", func(jsonrpc.Envelope) { close(done) })
	e := cloneEnvelope(init)
	e["id"] = jsonrpc.Int(p.upID)
	u.mu.Lock()
	if u.gen != gen || u.st != upRestarting {
		u.mu.Unlock()
		return
	}
	p.gen = gen
	_ = u.out.Send(e.Marshal())
	u.mu.Unlock()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		u.w.log.sym(symDenied, "upstream didn't answer the replayed initialize in 10 s")
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.gen != gen || u.st != upRestarting {
		return
	}
	if initialized != nil {
		_ = u.out.Send(initialized)
	}
	u.st = upRunning
	u.flushLocked()
}

// flushLocked writes the buffered messages to the running child.
func (u *upstream) flushLocked() {
	for _, q := range u.buf {
		if q.p != nil {
			q.p.gen = u.gen
		}
		_ = u.out.Send(q.msg)
	}
	u.buf = nil
}

// send writes msg to the child, or buffers it while the child restarts. p (a forwarded request)
// learns which child it went to, so a crash fails exactly the requests that child held.
func (u *upstream) send(msg []byte, p *pending) error {
	u.mu.Lock()
	defer u.mu.Unlock()
	switch u.st {
	case upRunning:
		if p != nil {
			p.gen = u.gen
		}
		return u.out.Send(msg)
	case upStarting, upRestarting:
		if len(u.buf) >= maxBuffered {
			return errBufferFull
		}
		u.buf = append(u.buf, queued{msg: msg, p: p})
		return nil
	case upDead:
		return errDead
	}
	return jsonrpc.ErrClosed
}

func (u *upstream) pumpStderr(r io.Reader) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		u.w.log.line("upstream │ " + sc.Text())
	}
}

// readLoop reads one child's stdout until it ends, then reports the exit.
func (u *upstream) readLoop(gen int, stdout io.Reader, cmd *exec.Cmd, exited chan struct{}) {
	r := jsonrpc.NewReader(stdout)
	r.MaxLine = u.w.cfg.MaxMessage
	for {
		e, line, err := r.Next()
		if err != nil && !isFraming(err) {
			break
		}
		u.w.fromUpstream(gen, e, line, err, r)
	}
	err := cmd.Wait()
	close(exited)
	u.exitedChild(gen, err)
}

func isFraming(err error) bool {
	return errors.Is(err, jsonrpc.ErrTooLarge) || errors.Is(err, jsonrpc.ErrParse) || errors.Is(err, jsonrpc.ErrBatch) ||
		errors.Is(err, jsonrpc.ErrTooDeep) || errors.Is(err, jsonrpc.ErrNotUTF8) || errors.Is(err, jsonrpc.ErrShape)
}

// exitedChild handles the end of child gen: expected during shutdown, a crash otherwise.
func (u *upstream) exitedChild(gen int, err error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.gen != gen || u.st == upClosed {
		return
	}
	msg := "exited"
	if err != nil {
		msg = err.Error()
	}
	u.w.log.sym(symDenied, "upstream stopped", msg)
	u.crashLocked(gen)
}

// crashLocked fails what the dead child held, then restarts it with backoff or gives up after
// five crashes in a minute.
func (u *upstream) crashLocked(gen int) {
	now := time.Now()
	recent := u.crashes[:0]
	for _, t := range u.crashes {
		if now.Sub(t) < time.Minute {
			recent = append(recent, t)
		}
	}
	u.crashes = append(recent, now)
	u.w.failInFlight(gen)
	if len(u.crashes) >= 5 {
		u.st = upDead
		u.w.log.sym(symDenied, "upstream stopped 5 times in a minute; Deedglass stopped restarting it", "calls are refused")
		buf := u.buf
		u.buf = nil
		go u.w.failQueued(buf, errDead)
		return
	}
	u.st = upRestarting
	delay := u.w.cfg.RestartBase << (len(u.crashes) - 1)
	if delay > 8*time.Second {
		delay = 8 * time.Second
	}
	go func() {
		time.Sleep(delay)
		u.mu.Lock()
		defer u.mu.Unlock()
		if u.st != upRestarting {
			return
		}
		u.launchLocked()
	}()
}

// close ends the child: close its stdin, wait, SIGTERM the group, wait, SIGKILL.
func (u *upstream) close(ctx context.Context) {
	u.mu.Lock()
	u.st = upClosed
	out, in, cmd, exited := u.out, u.in, u.cmd, u.exited
	u.mu.Unlock()
	if out != nil {
		_ = out.Close()
	}
	if in != nil {
		_ = in.Close()
	}
	if cmd == nil || exited == nil {
		return
	}
	select {
	case <-exited:
		return
	case <-time.After(u.w.cfg.ShutdownWait):
	}
	signalGroup(cmd, false)
	select {
	case <-exited:
		return
	case <-time.After(u.w.cfg.KillWait):
	}
	signalGroup(cmd, true)
	select {
	case <-exited:
	case <-time.After(2 * time.Second):
	}
}

func itoa(n int) string { return strconv.Itoa(n) }
