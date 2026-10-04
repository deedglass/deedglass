package learn

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/deedglass/deedglass/internal/ids"
	"github.com/deedglass/deedglass/internal/mailer"
)

// The weekly digest (S28 §3.9): on Monday at 09:00 org-local, one email per recipient with the
// previous ISO week's numbers. Exactly once per org and week across instances (learn_runs digest
// rows, insert-on-conflict-skip). Numbers and rule names only: never payloads or customer refs.

// MailSender queues an email (mailer.Queue); it never sends inside a transaction.
type MailSender interface {
	Enqueue(m mailer.Message) bool
}

// DigestRecipient is one person who gets the digest.
type DigestRecipient struct {
	UserID, Email, Role string
}

// DigestNumbers are the week's numbers.
type DigestNumbers struct {
	Actions, Verified, Witnessed, Denied, Mismatches  int64
	TopDenyRules                                      []mailer.DigestRule
	AnomaliesOpen, AnomaliesOpened, AnomaliesResolved int
	AnomalyTitle                                      string
}

// DigestStore is what the digest reads.
type DigestStore interface {
	Org(ctx context.Context, org ids.OrgID) (OrgInfo, error)
	DigestRecipients(ctx context.Context, org ids.OrgID, roles []string) ([]DigestRecipient, error)
	DigestNumbers(ctx context.Context, org ids.OrgID, from, to time.Time) (*DigestNumbers, error)
	ListSuggestions(ctx context.Context, org ids.OrgID, f SuggestionFilter, limit int, cursor string) ([]Suggestion, string, error)
}

// Digester builds and queues digests.
type Digester struct {
	Store    DigestStore
	Insights *Service // the shadow report; nil leaves the shadow line out
	Mail     MailSender
	BaseURL  string
	Log      *slog.Logger
}

// DigestPeriod is the ISO week key ("2026-W41") and the week's org-local Monday.
func DigestPeriod(year, week int) string { return fmt.Sprintf("%d-W%02d", year, week) }

// weekStart is the Monday 00:00 (in loc) of the ISO week containing t.
func weekStart(t time.Time, loc *time.Location) time.Time {
	l := t.In(loc)
	d := (int(l.Weekday()) + 6) % 7 // Monday = 0
	y, m, day := l.AddDate(0, 0, -d).Date()
	return time.Date(y, m, day, 0, 0, 0, 0, loc)
}

// Build renders one org's digest for the ISO week that starts at monday (org-local), one per recipient.
func (g *Digester) Build(ctx context.Context, org ids.OrgID, monday time.Time) ([]mailer.Message, *mailer.DigestData, error) {
	info, err := g.Store.Org(ctx, org)
	if err != nil {
		return nil, nil, err
	}
	rec := info.Settings.Digest.Recipients
	if rec == DigestOff {
		return nil, nil, nil
	}
	roles := []string{"owner"}
	if rec == DigestAdmins {
		roles = []string{"owner", "admin"}
	}
	people, err := g.Store.DigestRecipients(ctx, org, roles)
	if err != nil {
		return nil, nil, err
	}
	loc := loadLoc(info.Timezone)
	from := time.Date(monday.Year(), monday.Month(), monday.Day(), 0, 0, 0, 0, loc)
	to := from.AddDate(0, 0, 7)
	n, err := g.Store.DigestNumbers(ctx, org, from, to)
	if err != nil {
		return nil, nil, err
	}
	sugs, _, err := g.Store.ListSuggestions(ctx, org, SuggestionFilter{Status: SugOpen}, 100, "")
	if err != nil {
		return nil, nil, err
	}
	year, week := from.ISOWeek()
	d := mailer.DigestData{OrgName: info.Name, Year: year, Week: week, Monday: from, Actions: n.Actions, Verified: n.Verified,
		Witnessed: n.Witnessed, Denied: n.Denied, Mismatches: n.Mismatches, SuggestionsOpen: len(sugs), AnomaliesOpen: n.AnomaliesOpen,
		AnomalyTitle: n.AnomalyTitle, AnomaliesOpened: n.AnomaliesOpened, AnomaliesResolved: n.AnomaliesResolved, TopDenyRules: n.TopDenyRules,
		Link: strings.TrimRight(g.BaseURL, "/") + "/app/insights"}
	for i := 0; i < len(sugs) && i < 2; i++ {
		d.SuggestionTitles = append(d.SuggestionTitles, sugs[i].Title)
	}
	if g.Insights != nil {
		if rep, err := g.Insights.Shadow(ctx, org, ShadowQuery{}); err == nil && rep != nil {
			d.ShadowLine = DigestShadowLine(rep, to)
		}
	}
	var out []mailer.Message
	for _, p := range people {
		dd := d
		dd.Email, dd.Role = p.Email, p.Role
		r, err := mailer.RenderDigest(dd)
		if err != nil {
			return nil, nil, err
		}
		out = append(out, mailer.Message{To: p.Email, Subject: r.Subject, HTML: r.HTML, Text: r.Text, Kind: "digest",
			IdempotencyKey: "digest/" + org.String() + "/" + DigestPeriod(year, week) + "/" + p.UserID, UserID: p.UserID})
	}
	return out, &d, nil
}

// DigestShadowLine is "acme-sim v3 has run in shadow for 2 days and agrees on 1,180 of 1,204 actions."
func DigestShadowLine(r *ShadowReport, now time.Time) string {
	label := r.Shadow.Name + " v" + strconv.Itoa(r.Shadow.Version)
	days := 0
	if !r.Shadow.Since.IsZero() {
		days = int(now.Sub(r.Shadow.Since).Hours() / 24)
	}
	run := "has run in shadow for " + strconv.Itoa(days) + " " + plural(days, "day", "days")
	if days == 0 {
		run = "started running in shadow today"
	}
	if r.Summary.Evaluated == 0 {
		return label + " " + run + " and hasn't seen any actions yet."
	}
	agree := r.Summary.Evaluated - r.Summary.Disagree
	return fmt.Sprintf("%s %s and agrees on %s of %s actions.", label, run, fmtCount(agree), fmtCount(r.Summary.Evaluated))
}

// Send builds and queues the digest for org's week, once: the learn_runs row is the guard.
func (g *Digester) Send(ctx context.Context, st SuggestStore, org ids.OrgID, monday time.Time, now time.Time) (int, error) {
	year, week := monday.ISOWeek()
	period := DigestPeriod(year, week)
	ok, err := st.ClaimRun(ctx, org, "digest", period, now)
	if err != nil || !ok {
		return 0, err
	}
	msgs, _, err := g.Build(ctx, org, monday)
	if err != nil {
		_ = st.FinishRun(context.WithoutCancel(ctx), org, "digest", period, false, nil, err.Error(), now)
		return 0, err
	}
	sent := 0
	for _, m := range msgs {
		if g.Mail != nil && g.Mail.Enqueue(m) {
			sent++
		}
	}
	return sent, st.FinishRun(ctx, org, "digest", period, true, map[string]any{"recipients": len(msgs), "queued": sent}, "", now)
}

// ErrNoMailer is a digest preview without a way to render (never: rendering needs no mailer).
var ErrNoMailer = errors.New("learn: no mailer")
