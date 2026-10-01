package wrap

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// logger writes the wrapper's plain-text lines (brand §13.3 symbols, no colour: MCP clients show
// stderr in their own log viewers) to stderr and the log file. Lines carry tool names, ids,
// decisions and timings only: never arguments, results or the key.
type logger struct {
	mu   sync.Mutex
	errw io.Writer
	file *os.File
	now  func() time.Time
}

// Symbols (brand §13.3).
const (
	symOK      = "✓"
	symDenied  = "✕"
	symPending = "◐"
	symUnknown = "○"
	sep        = " · "
)

func newLogger(stderr io.Writer, path string) (*logger, error) {
	l := &logger{errw: stderr, now: time.Now}
	if path == "" {
		return l, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	_ = os.Chmod(filepath.Dir(path), 0o700)
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	_ = f.Chmod(0o600)
	l.file = f
	return l, nil
}

// line writes one line; parts are joined with " · ".
func (l *logger) line(parts ...string) {
	s := strings.Join(parts, sep)
	s = strings.ReplaceAll(s, "\n", " ")
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.errw != nil {
		fmt.Fprintln(l.errw, s)
	}
	if l.file != nil {
		fmt.Fprintln(l.file, l.now().UTC().Format("2006-01-02T15:04:05.000Z")+" "+s)
	}
}

// sym writes "<sym> <first> · rest".
func (l *logger) sym(sym, first string, rest ...string) {
	l.line(append([]string{sym + " " + first}, rest...)...)
}

func (l *logger) close() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file != nil {
		_ = l.file.Close()
		l.file = nil
	}
}

func ms(d time.Duration) string { return fmt.Sprintf("%d ms", d.Milliseconds()) }
