package ui

import (
	"bytes"
	"strings"
	"sync"
	"testing"
	"time"
)

type lockedBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuf) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

func TestSpinnerOffTerminalPrintsNotesOnce(t *testing.T) {
	var b lockedBuf
	s := NewSpinner(&b, Style{}, false, 80)
	s.Start("Docker", "")
	s.Start("Postgres", "")
	s.Start("Postgres", "downloading the image")
	s.Start("Postgres", "downloading the image")
	s.Stop()
	if got, want := b.String(), "◐ Postgres · downloading the image\n"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestSpinnerDrawsAfterDelayAndClears(t *testing.T) {
	saved, savedTick := SpinnerDelay, spinnerTick
	SpinnerDelay, spinnerTick = 5*time.Millisecond, 5*time.Millisecond
	defer func() { SpinnerDelay, spinnerTick = saved, savedTick }()
	var b lockedBuf
	s := NewSpinner(&b, Style{}, true, 80)
	s.Start("Postgres", "starting")
	time.Sleep(40 * time.Millisecond)
	s.Stop()
	out := b.String()
	if !strings.Contains(out, "\r\x1b[K◐ Postgres  starting") {
		t.Fatalf("no progress line in %q", out)
	}
	if !strings.HasSuffix(out, "\r\x1b[K") {
		t.Fatalf("the line wasn't cleared: %q", out)
	}
	s.Stop() // a second Stop is a no-op
}

func TestSpinnerQuickStepDrawsNothing(t *testing.T) {
	var b lockedBuf
	s := NewSpinner(&b, Style{}, true, 80)
	s.Start("Keys", "")
	s.Stop()
	if b.String() != "" {
		t.Fatalf("a step under the delay drew %q", b.String())
	}
}

func TestSpinnerLineFitsTheTerminal(t *testing.T) {
	saved, savedTick := SpinnerDelay, spinnerTick
	SpinnerDelay, spinnerTick = 5*time.Millisecond, 5*time.Millisecond
	defer func() { SpinnerDelay, spinnerTick = saved, savedTick }()
	var b lockedBuf
	s := NewSpinner(&b, Style{}, true, 40)
	s.Start("Postgres", "downloading the Postgres 18.6 image, first run only")
	time.Sleep(30 * time.Millisecond)
	s.Stop()
	for _, l := range strings.Split(b.String(), "\r\x1b[K") {
		if Width(l) >= 40 {
			t.Fatalf("a %d-column line on a 40-column terminal: %q", Width(l), l)
		}
	}
}
