package weekly

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/deedglass/deedglass/internal/ids"
	"github.com/deedglass/deedglass/internal/mailer"
)

// The Monday job (S52 §3.1): every Monday from 08:00 in the org's time zone, the week that just
// ended goes to the org's owners whose weekly_email is on, once per org and ISO week across every
// serve instance (learn_runs kind "weekly", insert-on-conflict-skip). Without a mailer nothing is
// sent and the Console's This week card shows the same summary.

// Schedule.
const (
	SendHour  = 8
	TickEvery = 5 * time.Minute
	RunKind   = "weekly"
)

// Recipient is one owner who gets the email.
type Recipient struct {
	UserID ids.UserID
	Email  string
}

// OrgInfo is what the job needs to know about an org.
type OrgInfo struct {
	ID       ids.OrgID
	Timezone string
}

// Store is what the job reads and writes.
type Store interface {
	ActiveOrgs(ctx context.Context) ([]OrgInfo, error)
	ClaimRun(ctx context.Context, org ids.OrgID, kind, period string, at time.Time) (bool, error)
	FinishRun(ctx context.Context, org ids.OrgID, kind, period string, ok bool, stats map[string]any, errText string, at time.Time) error
	// Recipients lists the org's active owners whose weekly_email is on.
	Recipients(ctx context.Context, org ids.OrgID) ([]Recipient, error)
	Summarize(ctx context.Context, org ids.OrgID, p Period, now time.Time) (Summary, error)
}

// MailQueue queues an email (mailer.Queue); false when it couldn't.
type MailQueue interface {
	Enqueue(m mailer.Message) bool
}

// Job is the Monday loop.
type Job struct {
	Store   Store
	Mail    MailQueue // nil: no mailer configured, nothing is sent
	BaseURL string    // DEEDGLASS_BASE_URL: the Console and the off link live here
	OffKey  []byte    // signs the one-click off links (OffKeyFrom)
	// ProUndoLine is whether to add the Pro line to caught sessions (the server has no undo_session).
	ProUndoLine func() bool
	Now         func() time.Time
	Log         *slog.Logger
}

func (j *Job) now() time.Time {
	if j.Now != nil {
		return j.Now().UTC()
	}
	return time.Now().UTC()
}

func (j *Job) log() *slog.Logger {
	if j.Log != nil {
		return j.Log
	}
	return slog.Default()
}

// Run ticks until ctx ends.
func (j *Job) Run(ctx context.Context) error {
	t := time.NewTicker(TickEvery)
	defer t.Stop()
	for {
		j.Tick(ctx)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
		}
	}
}

// Tick sends what is due now.
func (j *Job) Tick(ctx context.Context) {
	if j.Mail == nil {
		return // no mailer: the Console card is the summary
	}
	orgs, err := j.Store.ActiveOrgs(ctx)
	if err != nil {
		if ctx.Err() == nil {
			j.log().Warn("weekly: listing orgs failed", "err", err)
		}
		return
	}
	now := j.now()
	for _, o := range orgs {
		if ctx.Err() != nil {
			return
		}
		if _, err := j.SendOrg(ctx, o, now); err != nil && ctx.Err() == nil {
			j.log().Warn("weekly: the Monday email failed", "org_id", o.ID.String(), "err", err)
		}
	}
}

// Due reports whether org's Monday email is due at now, and for which week.
func Due(o OrgInfo, now time.Time) (Period, bool) {
	loc := LoadLocation(o.Timezone)
	local := now.In(loc)
	if local.Weekday() != time.Monday || local.Hour() < SendHour {
		return Period{}, false
	}
	return LastWeek(now, loc), true
}

// SendOrg sends org's week when due: once, to each owner whose weekly_email is on. It returns how
// many emails were queued.
func (j *Job) SendOrg(ctx context.Context, o OrgInfo, now time.Time) (int, error) {
	p, ok := Due(o, now)
	if !ok || j.Mail == nil {
		return 0, nil
	}
	claimed, err := j.Store.ClaimRun(ctx, o.ID, RunKind, p.Key, now)
	if err != nil || !claimed {
		return 0, err
	}
	sent, total, err := j.send(ctx, o, p, now)
	stats := map[string]any{"recipients": total, "queued": sent}
	if err != nil {
		_ = j.Store.FinishRun(context.WithoutCancel(ctx), o.ID, RunKind, p.Key, false, stats, err.Error(), j.now())
		return sent, err
	}
	return sent, j.Store.FinishRun(ctx, o.ID, RunKind, p.Key, true, stats, "", j.now())
}

func (j *Job) send(ctx context.Context, o OrgInfo, p Period, now time.Time) (int, int, error) {
	people, err := j.Store.Recipients(ctx, o.ID)
	if err != nil || len(people) == 0 {
		return 0, 0, err
	}
	s, err := j.Store.Summarize(ctx, o.ID, p, now)
	if err != nil {
		return 0, len(people), err
	}
	opts := Options{ProUndoLine: j.ProUndoLine != nil && j.ProUndoLine()}
	sent := 0
	for _, r := range people {
		off, err := OffLink(j.BaseURL, j.OffKey, o.ID, r.UserID)
		if err != nil {
			return sent, len(people), err
		}
		m, err := Email(s, opts, r.Email, ConsoleLink(j.BaseURL), off)
		if err != nil {
			return sent, len(people), err
		}
		m.IdempotencyKey = "weekly/" + o.ID.String() + "/" + p.Key + "/" + r.UserID.String()
		m.UserID = r.UserID.String()
		if j.Mail.Enqueue(m) {
			sent++
		}
	}
	return sent, len(people), nil
}

// Email renders the Monday email for one recipient.
func Email(s Summary, o Options, to, console, off string) (mailer.Message, error) {
	d := mailer.WeeklyData{Email: to, Subject: s.Subject(), Headline: s.Headline(), Numbers: s.NumbersLine(), Check: s.CheckLine(),
		CaughtHead: s.CaughtHead(), Nothing: "Nothing caught " + s.Period.When() + ".", ProLine: ProLine, Chain: s.ChainLine(),
		Console: console, Off: off}
	for _, it := range s.Caught {
		line := ItemLine(it)
		if len(s.Caught) > 1 {
			line = ItemSentence(it)
		}
		d.Items = append(d.Items, mailer.WeeklyItem{Line: line, Free: FreeLine(it), Pro: ShowsPro(it, o)})
	}
	return mailer.WeeklyMessage(d)
}

// ConsoleLink is the user's own Console.
func ConsoleLink(base string) string { return strings.TrimRight(base, "/") + "/app" }

// LoadLocation is the org's time zone, UTC when unknown.
func LoadLocation(name string) *time.Location {
	if name == "" {
		return time.UTC
	}
	l, err := time.LoadLocation(name)
	if err != nil {
		return time.UTC
	}
	return l
}

// --- the one-click off link (single purpose, signed by the server) ---

// offPurpose binds a token to this one use.
const offPurpose = "deedglass/weekly-email-off/v1"

// OffKeyFrom derives the off links' key from the server's API key pepper, so no new secret is
// needed and the key never signs anything else.
func OffKeyFrom(pepper []byte) []byte {
	m := hmac.New(sha256.New, pepper)
	m.Write([]byte(offPurpose))
	return m.Sum(nil)
}

// ErrOffToken is a token that isn't one of ours.
var ErrOffToken = errors.New("weekly: the link isn't valid")

// OffToken is base64url("<org>.<user>") + "." + base64url(HMAC(key, purpose ‖ org ‖ user)).
func OffToken(key []byte, org ids.OrgID, user ids.UserID) (string, error) {
	if len(key) < 16 {
		return "", errors.New("weekly: the off-link key is missing")
	}
	body := org.String() + "." + user.String()
	enc := base64.RawURLEncoding
	return enc.EncodeToString([]byte(body)) + "." + enc.EncodeToString(offMAC(key, body)), nil
}

func offMAC(key []byte, body string) []byte {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(offPurpose + "\n" + body))
	return m.Sum(nil)
}

// ParseOffToken checks a token and returns its org and user.
func ParseOffToken(key []byte, tok string) (ids.OrgID, ids.UserID, error) {
	var zo ids.OrgID
	var zu ids.UserID
	if len(key) < 16 || len(tok) > 256 {
		return zo, zu, ErrOffToken
	}
	b64, sig, ok := strings.Cut(tok, ".")
	if !ok {
		return zo, zu, ErrOffToken
	}
	enc := base64.RawURLEncoding.Strict()
	raw, err1 := enc.DecodeString(b64)
	mac, err2 := enc.DecodeString(sig)
	if err1 != nil || err2 != nil || !hmac.Equal(mac, offMAC(key, string(raw))) {
		return zo, zu, ErrOffToken
	}
	os, us, ok := strings.Cut(string(raw), ".")
	if !ok {
		return zo, zu, ErrOffToken
	}
	org, err1 := ids.Parse[ids.OrgKind](os)
	user, err2 := ids.Parse[ids.UserKind](us)
	if err1 != nil || err2 != nil {
		return zo, zu, ErrOffToken
	}
	return org, user, nil
}

// OffLink is the page a recipient opens to stop the email.
func OffLink(base string, key []byte, org ids.OrgID, user ids.UserID) (string, error) {
	t, err := OffToken(key, org, user)
	if err != nil {
		return "", err
	}
	return strings.TrimRight(base, "/") + "/weekly/off?t=" + t, nil
}
