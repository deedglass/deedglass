package main

import (
	"context"
	"fmt"
	"github.com/deedglass/deedglass/internal/witness"
	"io"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"time"

	"github.com/deedglass/deedglass/internal/cli/ui"
	"github.com/deedglass/deedglass/internal/ledger"
)

// deedglass actions list --follow (S15): one line per action as it is decided or sealed, for the
// demo's terminal. This file is the polling follower: once a second, in direct database mode. With
// an API key, S24's follow_sse.go follows the server's event stream instead and falls back to this
// when the server has none.

// Seams for tests.
var (
	followEvery = time.Second
	followNow   = time.Now
	followStop  = func(ctx context.Context) (context.Context, context.CancelFunc) {
		return signal.NotifyContext(ctx, os.Interrupt)
	}
)

// runActionsFollow prints the actions created since it started, each time one reaches a state a
// person cares about: waiting for approval, or sealed (allowed, denied, failed, expired,
// cancelled). Ctrl-C exits 0.
func runActionsFollow(ctx context.Context, e *Env, s *ledgerSession) error {
	ctx, stop := followStop(ctx)
	defer stop()
	start := followNow().Add(-2 * time.Second)
	st := e.Style
	what := "Following actions " + ui.Sep + " org " + s.org.Slug
	if c := actionsListValues.connector; c != "" {
		what += " " + ui.Sep + " connector " + c
	}
	fmt.Fprintln(e.Stdout, st.Dim(what+" "+ui.Sep+" Ctrl-C to stop"))
	shown := map[string]string{}
	tick := time.NewTicker(followEvery)
	defer tick.Stop()
	for {
		v := actionsListValues.values(followNow())
		v.Del("created_at[gte]")
		v.Set("created_at[gte]", start.UTC().Format(time.RFC3339Nano))
		v.Set("limit", "100")
		v.Del("cursor")
		f, p, err := ledger.ParseActionQuery(v)
		if err != nil {
			return ledgerErr(e, err)
		}
		list, err := s.led.ListActions(ctx, s.org.ID, f, p)
		switch {
		case ctx.Err() != nil:
			return nil
		case err != nil:
			fmt.Fprintln(e.Stderr, st.Amber(ui.Unknown+" The Ledger didn't answer; trying again."))
		default:
			for i := len(list.Data) - 1; i >= 0; i-- {
				a := list.Data[i]
				key, ok := followKey(a)
				if !ok || shown[a.ID] == key {
					continue
				}
				shown[a.ID] = key
				writeFollowLine(e.Stdout, st, a)
			}
		}
		select {
		case <-ctx.Done():
			return nil
		case <-tick.C:
		}
	}
}

// followKey is what changed about an action that is worth a line, and whether it is one.
func followKey(a ledger.Action) (string, bool) {
	switch {
	case a.Status == ledger.StatusPendingApproval:
		return "pending", true
	case a.State == "sealed":
		k := a.Status
		if a.Witness != nil {
			k += "/" + a.Witness.Verdict
		}
		return k, true
	}
	return "", false
}

// shortRef is an id cut to its prefix and 8 characters: act_01j9zw3k…
func shortRef(id string) string {
	if i := strings.IndexByte(id, '_'); i > 0 && len(id) > i+9 {
		return id[:i+9] + "…"
	}
	return id
}

// writeFollowLine is one line (brand §13.3 symbols; colour only repeats what the words say):
//
//	10:14:03  ✓ orders.execute_sql   allowed   41 ms    L0 · VERIFIED        act_01j9zw3k…
//	10:14:21  ✕ orders.execute_sql   denied    block-destructive-sql         deed_01j9zw4m…
//	10:14:52  ◐ orders.execute_sql   waiting   orders-writes-need-approval   apr_01j9zw6q…
func writeFollowLine(w io.Writer, st ui.Style, a ledger.Action) {
	at := time.Time(a.CreatedAt).In(time.Local).Format("15:04:05")
	rule := ""
	if a.Policy.RuleID != nil {
		rule = *a.Policy.RuleID
	}
	deed := a.ID
	if a.Deed != nil {
		deed = *a.Deed
	}
	var sym, word, middle, ref string
	color := st.Dim
	switch {
	case a.Status == ledger.StatusPendingApproval:
		sym, word, middle, color = ui.Pending, "waiting", rule, st.Violet
		ref = a.ID
		if a.Approval != nil {
			ref = a.Approval.ID
		}
	case a.Status == ledger.StatusDenied:
		sym, word, color, ref = ui.Fail, "denied", st.Red, deed
		middle = rule
		if middle == "" && a.Decision != nil && a.Decision.Code != nil {
			middle = *a.Decision.Code
		}
	case a.Status == ledger.StatusFailed:
		sym, word, color, ref = ui.Fail, "failed", st.Red, a.ID
		if a.Execution != nil && a.Execution.ErrorCode != nil {
			middle = *a.Execution.ErrorCode
		}
	case a.Status == ledger.StatusExpired, a.Status == ledger.StatusCancelled:
		sym, word, color, ref, middle = ui.Sep, a.Status, st.Dim, deed, rule
	default:
		sym, word, color, ref = ui.OK, "allowed", st.Teal, a.ID
		lat := ""
		if a.Execution != nil && a.Execution.LatencyMS != nil {
			lat = strconv.FormatInt(*a.Execution.LatencyMS, 10) + " ms"
		}
		verdict := ""
		if a.Witness != nil {
			verdict = witness.PlainLevel(a.Witness.Level) + " " + ui.Sep + " " + witness.PlainVerdict(a.Witness.Verdict)
			switch a.Witness.Verdict {
			case "MISMATCH":
				sym, color = ui.Fail, st.Red
			case "UNVERIFIABLE":
				sym, color = ui.Unknown, st.Amber
			}
		}
		middle = ui.Pad(lat, 9) + verdict
	}
	line := at + "  " + color(sym) + " " + ui.Pad(clip(a.Tool, 20), 21) + ui.Pad(word, 10) + ui.Pad(clip(middle, 31), 32) + st.Dim(shortRef(ref))
	fmt.Fprintln(w, strings.TrimRight(line, " "))
}
