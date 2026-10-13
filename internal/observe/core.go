// Package observe is S50's observe mode (ADR-094): watched sessions over a Postgres connector's
// change log. Open a session, let an agent work, close it: every transaction Postgres committed on
// the connector's watched tables in that window, and that no Deedglass action claimed (the
// decoder's unattributed frames), belongs to the session. The closed session is sealed as an admin
// deed (ids, counts and digests; never a row value).
//
// What it can't know, and says so everywhere a person reads it: which database role or program
// made each change (Postgres's change log doesn't carry it), and anything on unwatched tables.
// Another writer during the window appears too; a person can leave a transaction out before an
// undo, and the record lists what was left out. A watched session has no claim to compare, so it
// never gets a level or a Matches/Doesn't match verdict.
//
// core.go is pure (attach rule, summaries, seal data, the close decision); service.go is the store
// adapter and the serve job.
package observe

import (
	"crypto/sha256"
	"encoding/hex"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/deedglass/deedglass/internal/caught"
)

// States of a watched session (the database trigger enforces the edges).
const (
	StateOpen    = "open"
	StateClosing = "closing"
	StateSealed  = "sealed"
	StateUndoing = "undoing"
	StateUndone  = "undone"
)

// Flags a session can carry.
const (
	FlagOverlap       = "overlap"        // a frame also belongs to another session on the connector
	FlagMaxSession    = "max_session"    // the session closed itself after witness.observe.max_session
	FlagDecoderBehind = "decoder_behind" // the decoder hadn't passed the close time 30 s after it
	FlagCaught        = "caught"         // a caught item (TRUNCATE, or a transaction over caught_rows)
	FlagFramesExpired = "frames_expired" // decoder retention removed some of its frames
)

// Close timing (S50 §3.2).
const (
	CatchUpWait  = 30 * time.Second // wait this long for the decoder before flagging decoder_behind
	LateAttach   = 10 * time.Minute // keep attaching late frames this long, then seal regardless
	AttachPeriod = 2 * time.Second  // the serve job's period
)

// Candidate is an unattributed frame that might belong to a session.
type Candidate struct {
	ID         string
	CommitTime time.Time
	Tables     []string
}

// Window is a session's time window: [Opened, Closed], or [Opened, Now] while open.
type Window struct {
	Opened time.Time
	Closed *time.Time
	Now    time.Time
}

// End is the window's end.
func (w Window) End() time.Time {
	if w.Closed != nil {
		return *w.Closed
	}
	return w.Now
}

// Attachable reports whether a frame belongs to a session: committed inside the window
// (boundaries included) and touching at least one watched table. Bound frames never reach here
// (they have a record already); the caller passes unattributed frames only.
func Attachable(c Candidate, w Window, watched []string) bool {
	if c.CommitTime.Before(w.Opened) || c.CommitTime.After(w.End()) {
		return false
	}
	for _, t := range c.Tables {
		if slices.Contains(watched, t) {
			return true
		}
	}
	return false
}

// Counts are one table's row changes.
type Counts = caught.Counts

// FrameFacts is one attached transaction as stored (no row values).
type FrameFacts struct {
	ID         string            `json:"id"`
	CommitTime *time.Time        `json:"commit_time"`
	CommitLSN  string            `json:"commit_lsn,omitempty"`
	Xid        int64             `json:"xid,omitempty"`
	Counts     map[string]Counts `json:"tables"`
	Total      int               `json:"changes"`
	Digest     string            `json:"changes_digest,omitempty"`
	Flags      []string          `json:"flags,omitempty"`
	Excluded   bool              `json:"excluded"`
	Expired    bool              `json:"expired"`
}

// Summary is a session's counts: what the list, the header line and the seal read.
type Summary struct {
	Transactions int               `json:"transactions"` // attached transactions, excluded ones included
	Excluded     int               `json:"excluded"`
	Expired      int               `json:"expired"`
	Rows         int               `json:"rows"`
	Tables       map[string]Counts `json:"tables"`
	FirstCommit  *time.Time        `json:"first_commit"`
	LastCommit   *time.Time        `json:"last_commit"`
	Caught       []caught.Item     `json:"caught"`
}

// Summarize totals the frames. Expired frames count as transactions but add no rows (their counts
// are gone with them); excluded frames count everywhere: leaving one out of an undo doesn't change
// what Postgres committed.
func Summarize(session string, frames []FrameFacts, caughtRows int, undoable bool) Summary {
	s := Summary{Tables: map[string]Counts{}, Caught: []caught.Item{}}
	var cf []caught.Frame
	for _, f := range frames {
		s.Transactions++
		if f.Excluded {
			s.Excluded++
		}
		if f.Expired {
			s.Expired++
			continue
		}
		for t, c := range f.Counts {
			if t == "deedglass.ledger" {
				continue
			}
			x := s.Tables[t]
			x.Insert, x.Update, x.Delete, x.Truncate = x.Insert+c.Insert, x.Update+c.Update, x.Delete+c.Delete, x.Truncate+c.Truncate
			s.Tables[t] = x
			s.Rows += c.Rows()
		}
		if f.CommitTime != nil {
			if s.FirstCommit == nil || f.CommitTime.Before(*s.FirstCommit) {
				t := *f.CommitTime
				s.FirstCommit = &t
			}
			if s.LastCommit == nil || f.CommitTime.After(*s.LastCommit) {
				t := *f.CommitTime
				s.LastCommit = &t
			}
		}
		cf = append(cf, caught.Frame{ID: f.ID, Counts: f.Counts, Excluded: f.Excluded})
	}
	if items := caught.Session(session, cf, caughtRows, undoable); len(items) > 0 {
		s.Caught = items
	}
	return s
}

// SealedData is the admin deed's data for observe.session_sealed: ids, counts and digests only.
// The explicit frame list is kept up to MaxSealFrames ids; past that, frames_digest alone (sha256
// over the sorted ids, one per line) pins them.
type SealedData struct {
	Session       string            `json:"session"`
	Connector     string            `json:"connector"`
	OpenedAt      string            `json:"opened_at"`
	ClosedAt      string            `json:"closed_at"`
	FrameCount    int               `json:"frame_count"`
	Frames        []string          `json:"frames,omitempty"`
	FramesDigest  string            `json:"frames_digest"`
	Excluded      []string          `json:"excluded"`
	Tables        map[string]Counts `json:"tables"`
	ChangesDigest string            `json:"changes_digest"`
	Flags         []string          `json:"flags"`
}

// MaxSealFrames is the most frame ids a seal lists by name (the admin data cap is 16 KiB).
const MaxSealFrames = 100

// TimeLayout is the seal's time format (the deed format: milliseconds, Z).
const TimeLayout = "2006-01-02T15:04:05.000Z"

// Seal builds the seal's data. changes_digest is sha256 over the frames' own changes digests in
// commit order, one per line, so a verifier with the frames recomputes it without row values.
func Seal(session, connector string, opened, closed time.Time, frames []FrameFacts, flags []string) SealedData {
	d := SealedData{Session: session, Connector: connector, OpenedAt: opened.UTC().Format(TimeLayout), ClosedAt: closed.UTC().Format(TimeLayout),
		FrameCount: len(frames), Excluded: []string{}, Tables: map[string]Counts{}, Flags: append([]string{}, flags...)}
	sort.Strings(d.Flags)
	ids := make([]string, 0, len(frames))
	var digests []string
	for _, f := range frames {
		ids = append(ids, f.ID)
		if f.Excluded {
			d.Excluded = append(d.Excluded, f.ID)
		}
		digests = append(digests, f.ID+" "+f.Digest)
		for t, c := range f.Counts {
			if t == "deedglass.ledger" {
				continue
			}
			x := d.Tables[t]
			x.Insert, x.Update, x.Delete, x.Truncate = x.Insert+c.Insert, x.Update+c.Update, x.Delete+c.Delete, x.Truncate+c.Truncate
			d.Tables[t] = x
		}
	}
	sorted := append([]string{}, ids...)
	sort.Strings(sorted)
	if len(ids) <= MaxSealFrames {
		d.Frames = sorted
	}
	d.FramesDigest = digestLines(sorted)
	d.ChangesDigest = digestLines(digests)
	sort.Strings(d.Excluded)
	return d
}

func digestLines(lines []string) string {
	h := sha256.Sum256([]byte(strings.Join(lines, "\n")))
	return "sha256:" + hex.EncodeToString(h[:])
}

// DecoderFacts is what the connector's decoder slot row says about progress.
type DecoderFacts struct {
	LastCommitAt    *time.Time // the commit time of the newest frame it decoded
	LastHeartbeatAt *time.Time // when it decoded its newest heartbeat
	Heartbeat       time.Duration
}

// CaughtUp reports whether every transaction committed before closedAt has been decoded: the
// decoder decoded a transaction committed after it (WAL is decoded in commit order), or a valid
// heartbeat it decoded at least two heartbeat periods after it (one emitted after the close).
func CaughtUp(closedAt time.Time, d DecoderFacts) bool {
	if d.LastCommitAt != nil && !d.LastCommitAt.Before(closedAt) {
		return true
	}
	hb := d.Heartbeat
	if hb <= 0 {
		hb = 10 * time.Second
	}
	return d.LastHeartbeatAt != nil && !d.LastHeartbeatAt.Before(closedAt.Add(2*hb))
}

// CloseStep is what the serve job does with a closing session this tick.
type CloseStep int

const (
	StepWait       CloseStep = iota // keep attaching; the decoder may still be behind
	StepFlagBehind                  // flag decoder_behind, keep attaching late frames
	StepSeal                        // seal now
)

// NextCloseStep decides a closing session's step: seal when the decoder caught up or the late-attach
// window ended; flag decoder_behind once after CatchUpWait.
func NextCloseStep(closedAt, now time.Time, d DecoderFacts, flagged bool) CloseStep {
	switch {
	case CaughtUp(closedAt, d):
		return StepSeal
	case now.Sub(closedAt) >= LateAttach:
		return StepSeal
	case now.Sub(closedAt) >= CatchUpWait && !flagged:
		return StepFlagBehind
	}
	return StepWait
}

// ValidLabel reports whether a label is acceptable: 1 to 80 characters, no control characters
// (the database checks the same).
func ValidLabel(s string) bool {
	n := 0
	for _, r := range s {
		n++
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0) || r == ' ' || r == ' ' {
			return false
		}
	}
	return n >= 1 && n <= 80 && strings.TrimSpace(s) != ""
}
