package ui

import (
	"fmt"
	"io"
	"sync"
	"time"
)

// Spinner is the progress line for a step that may take longer than a second (docs/COPY_CATALOG.md
// › CLI style). On a terminal it draws nothing for the first SpinnerDelay, then one line,
// "◐ Postgres  downloading postgres 18.6 · 4 s", redrawn in place until Stop clears it, so the
// step's own ✓ or ✕ line replaces it. Off a terminal (CI, a pipe, --json) it never draws: a log
// gets only the finished lines, plus any note Start was given, printed once.
//
// The quarter-moon frames are the pending symbol's family (◐ is "working, not decided"), so the
// line never reads as a verdict.
type Spinner struct {
	w      io.Writer
	style  Style
	live   bool // redraw in place (a terminal, animation allowed)
	mu     sync.Mutex
	stop   chan struct{}
	done   chan struct{}
	drawn  bool
	label  string
	note   string
	noted  string // the last note printed off a terminal
	start  time.Time
	frames []string
	width  int // terminal columns; the line is cut to fit so it redraws in place (0: 80)
}

// SpinnerDelay is how long a step runs before its line appears (tests shorten it).
var SpinnerDelay = 400 * time.Millisecond

// spinnerTick is the redraw interval.
var spinnerTick = 120 * time.Millisecond

// NewSpinner returns a spinner writing to w. live is true on a terminal where animation is
// allowed; otherwise only notes are printed, once each.
func NewSpinner(w io.Writer, style Style, live bool, width int) *Spinner {
	if width <= 0 {
		width = 80
	}
	return &Spinner{w: w, style: style, live: live, width: width, frames: []string{"◐", "◓", "◑", "◒"}}
}

// clip cuts t to n columns, ending with … when it cut.
func clip(t string, n int) string {
	if Width(t) <= n {
		return t
	}
	out, w := "", 0
	for _, r := range t {
		rw := Width(string(r))
		if w+rw > n-1 {
			break
		}
		out += string(r)
		w += rw
	}
	return out + "…"
}

// Start begins a step called label; note says what it's doing ("" for nothing). Calling Start
// again with the same label only updates the note (the clock keeps running).
func (s *Spinner) Start(label, note string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	same := s.stop != nil && s.label == label
	if same {
		s.note = note
		s.mu.Unlock()
		s.offTerminalNote(label, note)
		return
	}
	s.mu.Unlock()
	s.Stop()
	s.offTerminalNote(label, note)
	if !s.live {
		return
	}
	s.mu.Lock()
	s.label, s.note, s.start = label, note, time.Now()
	s.stop, s.done = make(chan struct{}), make(chan struct{})
	stop, done := s.stop, s.done
	s.mu.Unlock()
	go s.loop(stop, done)
}

func (s *Spinner) offTerminalNote(label, note string) {
	if s.live || note == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.noted == label+"\x00"+note {
		return
	}
	s.noted = label + "\x00" + note
	fmt.Fprintf(s.w, "%s %s %s\n", s.style.Violet("◐"), label, s.style.Dim(Sep+" "+note))
}

func (s *Spinner) loop(stop, done chan struct{}) {
	defer close(done)
	select {
	case <-stop:
		return
	case <-time.After(SpinnerDelay):
	}
	t := time.NewTicker(spinnerTick)
	defer t.Stop()
	for i := 0; ; i++ {
		s.draw(i)
		select {
		case <-stop:
			return
		case <-t.C:
		}
	}
}

func (s *Spinner) draw(i int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	secs := int(time.Since(s.start).Seconds())
	// The seconds go first so a long note is what gets cut, never the clock.
	tail := ""
	if secs >= 1 {
		tail = fmt.Sprintf("%d s", secs)
	}
	if s.note != "" {
		if tail != "" {
			tail += " " + Sep + " "
		}
		tail += s.note
	}
	label := clip(s.label, s.width-3)
	line := s.style.Violet(s.frames[i%len(s.frames)]) + " " + label
	if room := s.width - 1 - 2 - Width(label) - 2; tail != "" && room > 4 {
		line += "  " + s.style.Dim(clip(tail, room))
	}
	fmt.Fprint(s.w, "\r\x1b[K"+line)
	s.drawn = true
}

// Stop ends the current step and clears its line. It is safe to call when nothing runs.
func (s *Spinner) Stop() {
	if s == nil {
		return
	}
	s.mu.Lock()
	stop, done := s.stop, s.done
	s.stop, s.done = nil, nil
	s.mu.Unlock()
	if stop == nil {
		return
	}
	close(stop)
	<-done
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.drawn {
		fmt.Fprint(s.w, "\r\x1b[K")
		s.drawn = false
	}
}
