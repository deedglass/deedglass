package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/deedglass/deedglass/cmd/deedglass/internal/ui"
	"github.com/deedglass/deedglass/internal/connectorapi"
	"github.com/deedglass/deedglass/internal/decoder"
	"github.com/deedglass/deedglass/internal/httpx"
	"github.com/deedglass/deedglass/pkg/deedglass"
)

// deedglass decoder status|drop (S20 §3.14, §5).

var decoderStatusValues struct {
	connector string
	recent    int
}

func decoderStatusFlags(fs *flag.FlagSet) {
	decoderStatusValues.connector, decoderStatusValues.recent = "", 0
	fs.StringVar(&decoderStatusValues.connector, "connector", "", "one connector's decoder, in detail")
	fs.IntVar(&decoderStatusValues.recent, "recent", 0, "show the newest N frames (0 to 50; with --connector)")
}

var decoderDropValues struct{ connector, confirm string }

func decoderDropFlags(fs *flag.FlagSet) {
	decoderDropValues.connector, decoderDropValues.confirm = "", ""
	fs.StringVar(&decoderDropValues.connector, "connector", "", "the connector whose slot to drop")
	fs.StringVar(&decoderDropValues.confirm, "confirm", "", "the connector's name again, instead of typing it (scripts)")
}

func getDecoder(ctx context.Context, c *deedglass.Client, ref string, recent int) (decoder.View, error) {
	var v decoder.View
	path := "/v1/connectors/" + url.PathEscape(ref) + "/decoder"
	if recent > 0 {
		path += fmt.Sprintf("?recent=%d", recent)
	} else {
		path += "?recent=0"
	}
	_, err := c.Do(ctx, "GET", path, nil, &v)
	return v, err
}

func runDecoderStatus(ctx context.Context, e *Env, args []string) error {
	if len(args) > 0 {
		return usageErr(fmt.Errorf("deedglass decoder status takes flags only (got %q)", args[0]), "deedglass decoder status [--connector postgres] [--recent 4]")
	}
	v := decoderStatusValues
	if v.recent < 0 || v.recent > decoder.MaxRecent {
		return usageErr(errors.New("--recent must be from 0 to 50"), "deedglass decoder status --connector postgres --recent 4")
	}
	c, err := apiClient(e)
	if err != nil {
		return err
	}
	now := time.Now()
	if v.connector != "" {
		d, err := getDecoder(ctx, c, v.connector, v.recent)
		if err != nil {
			return apiErr(e, c, err)
		}
		if e.Globals.JSON {
			if err := writeJSON(e, d); err != nil {
				return err
			}
		} else {
			printDecoderDetail(e, d, now)
		}
		return decoderExit(d)
	}
	var list httpx.List[connectorapi.Connector]
	if _, err := c.Do(ctx, "GET", "/v1/connectors?limit=100", nil, &list); err != nil {
		return apiErr(e, c, err)
	}
	var views []decoder.View
	for _, x := range list.Data {
		if x.Type != "postgres" {
			continue
		}
		d, err := getDecoder(ctx, c, x.ID, 0)
		var de *deedglass.Error
		if errors.As(err, &de) && de.Code == "witness_not_configured" {
			continue
		}
		if err != nil {
			return apiErr(e, c, err)
		}
		views = append(views, d)
	}
	halted := false
	for _, d := range views {
		halted = halted || d.Status == decoder.StatusHalted
	}
	if e.Globals.JSON {
		if err := writeJSON(e, httpx.NewList(views, false, nil)); err != nil {
			return err
		}
		if halted {
			return &ExitError{Code: ExitFailure, ErrCode: "decoder_halted", Err: errors.New("a decoder is halted"), printed: true}
		}
		return nil
	}
	if len(views) == 0 {
		fmt.Fprintln(e.Stdout, "No connectors with checks on yet. Turn on witness.enabled in a postgres connector's config to see what Postgres actually committed.")
		fmt.Fprintln(e.Stdout, "  Next: deedglass connector postgres check --connector <name> walks you through it.")
		return nil
	}
	width := 0
	for _, d := range views {
		width = max(width, len(d.Name))
	}
	for _, d := range views {
		printDecoderSummary(e, d, width, now)
	}
	if halted {
		return &ExitError{Code: ExitFailure, ErrCode: "decoder_halted", Err: errors.New("a decoder is halted"), printed: true}
	}
	return nil
}

// decoderExit: a halted decoder exits 1 (scripts and monitors can tell).
func decoderExit(d decoder.View) error {
	if d.Status == decoder.StatusHalted {
		return &ExitError{Code: ExitFailure, ErrCode: "decoder_halted", Err: errors.New("the decoder is halted"), printed: true}
	}
	return nil
}

// decoderText is a decoder's status in words: the symbol, the headline after the name, and up to
// two more lines (what it means, and the next step).
type decoderText struct {
	kind     string // ok, fail, unknown, pending, off
	headline string
	explain  string
	next     string
}

func detailString(d decoder.View, k string) string {
	if v, ok := d.Detail[k]; ok {
		return fmt.Sprint(v)
	}
	return ""
}

func decoderWords(d decoder.View, now time.Time) decoderText {
	reason := ""
	if d.Reason != nil {
		reason = *d.Reason
	}
	name := d.Name
	switch d.Status {
	case decoder.StatusRunning:
		h := "healthy"
		if d.Lag.Bytes != nil {
			h += " " + ui.Sep + " lag " + walSize(*d.Lag.Bytes)
		}
		if d.LastCommit != nil && d.LastCommit.At != nil {
			h += " " + ui.Sep + " last commit " + agoWords(now.Sub(*d.LastCommit.At))
		}
		return decoderText{kind: "ok", headline: h}
	case decoder.StatusDegraded:
		t := decoderText{kind: "unknown", headline: "degraded " + ui.Sep + " " + reason}
		switch reason {
		case decoder.ReasonLagHigh:
			alert := detailString(d, "alert_bytes")
			t.headline = "degraded " + ui.Sep + " lag " + walSize(deref64(d.Lag.Bytes))
			if n, err := parseInt64(alert); err == nil {
				t.headline += ", above the " + walSize(n) + " alert"
			}
			t.explain = "The slot is holding WAL on the watched database. Deedglass is still reading; it is behind."
			t.next = "Next: check deedglass serve is running and healthy, or raise DEEDGLASS_SLOT_LAG_ALERT_MB if this is expected."
		case decoder.ReasonRestartRetained:
			t.headline = "degraded " + ui.Sep + " the slot keeps " + walSize(deref64Ptr(d)) + " of WAL"
			t.explain = "A long transaction elsewhere in the database holds the slot's restart point, so every read starts from it."
			t.next = "Next: find it in pg_stat_activity (oldest xact_start), and end it if it isn't needed."
		case decoder.ReasonStalled:
			t.headline = "degraded " + ui.Sep + " no heartbeat read back for " + detailString(d, "last_heartbeat_s") + "s"
			t.explain = "Deedglass writes a heartbeat to the database every 10 seconds and hasn't read one back."
			t.next = "Next: check deedglass serve's log for decoder lines. In dev, DEEDGLASS_DECODER_PAUSE pauses the decoder."
		case decoder.ReasonPublicationChanged:
			t.explain = "deedglass_pub changed while Deedglass was reading. Postgres 18 skips row changes it can't publish, so actions from that moment are marked with an evidence gap."
			t.next = "Next: deedglass connector postgres check --connector " + name
		case decoder.ReasonTimelineChanged:
			t.explain = "The database moved to a new timeline (a failover or a restore). Frames from now on are keyed by it; failover is not tested."
			t.next = "Next: deedglass connector postgres check --connector " + name
		case decoder.ReasonPeekSlow:
			t.explain = "One read of the slot takes more than 2 seconds, usually because a long transaction holds the restart point."
			t.next = "Next: look for long transactions in pg_stat_activity."
		}
		return t
	case decoder.StatusHalted:
		t := decoderText{kind: "fail", headline: "halted " + ui.Sep + " " + reason}
		since := d.Since.UTC().Format("2006-01-02 15:04 UTC")
		lost := "Actions waiting on a Proven check for " + name + " won't get that evidence (" + reason + ")."
		switch reason {
		case decoder.ReasonSlotInvalidated:
			ws := detailString(d, "wal_status")
			if ws == "" {
				ws = detailString(d, "invalidation_reason")
			}
			t.headline = fmt.Sprintf("halted %s slot invalidated at %s (wal_status %s)", ui.Sep, since, ws)
			t.explain = lost
			t.next = "Next: deedglass decoder drop --connector " + name + ", then check max_slot_wal_keep_size (docs.deedglass.com/decoder#sizing)"
		case decoder.ReasonSlotMissing:
			t.headline = "halted " + ui.Sep + " slot " + d.Slot + " is gone from the database"
			t.explain = lost
			t.next = "Next: deedglass decoder drop --connector " + name + " to start a new slot."
		case decoder.ReasonPublicationMissing:
			t.explain = "Postgres can't read past a point where deedglass_pub didn't exist. Re-create it (deedglass connector postgres init " + name +
				"), then drop and re-create the slot: deedglass decoder drop --connector " + name + "."
		case decoder.ReasonPublicationInvalid:
			t.explain = "deedglass_pub must publish insert, update, delete and truncate for deedglass.ledger and every watched table, with no row filter or column list."
			t.next = "Next: deedglass connector postgres init --connector " + name
		case decoder.ReasonMalformedEvidence:
			where := "Deedglass stopped reading"
			if l := detailString(d, "lsn"); l != "" {
				where += " at " + l
			}
			what := ""
			if ty := detailString(d, "type"); ty != "" {
				what = " (type " + ty
				if b := detailString(d, "byte"); b != "" {
					what += ", byte " + b
				}
				what += ")"
			}
			t.explain = where + ": a message didn't match the pgoutput format" + what + ". Nothing after it was used. This is rare; please send deedglass decoder status --json to support."
		case decoder.ReasonSessionSettingsChanged:
			setting := detailString(d, "setting")
			t.explain = "The decoder's session settings changed (" + setting + "). Deedglass stopped so every value stays exact. Restart deedglass serve; check ALTER ROLE deedglass_witness."
		case decoder.ReasonLedgerFunctionChanged:
			t.explain = "deedglass.begin_action isn't the function Deedglass installed, so its ledger rows can't be trusted."
			t.next = "Next: deedglass connector postgres check --connector " + name + ", then init to restore it."
		case decoder.ReasonSystemChanged:
			t.explain = "The connector now points at a different Postgres cluster (a restore or a new server). The old slot's evidence doesn't apply."
			t.next = "Next: deedglass decoder drop --connector " + name + " to start a new baseline."
		case decoder.ReasonWALLevel:
			t.explain = "Proven checks need wal_level = logical"
			if w := detailString(d, "wal_level"); w != "" {
				t.explain += " (it is " + w + ")"
			}
			t.explain += ". Set it and restart Postgres."
			t.next = "Next: docs.deedglass.com/postgres-deployment"
		case decoder.ReasonNotReplication:
			t.explain = "The witness role has no REPLICATION attribute, so it can't read the slot."
			t.next = "Next: deedglass connector postgres init --connector " + name
		case decoder.ReasonSlotConflict:
			t.explain = "A slot named " + d.Slot + " exists but isn't a pgoutput slot in this database."
			t.next = "Next: drop it by hand, then the decoder makes its own."
		default:
			t.explain = lost
		}
		return t
	case decoder.StatusConnecting:
		t := decoderText{kind: "pending", headline: "connecting"}
		if a := detailString(d, "attempt"); a != "" {
			t.headline += " " + ui.Sep + " attempt " + a
			if ms, err := parseInt64(detailString(d, "next_try_ms")); err == nil {
				t.headline += " " + ui.Sep + " next try in " + durWords(time.Duration(ms)*time.Millisecond)
			}
		}
		switch reason {
		case decoder.ReasonCredentials:
			t.explain = "The connector has no witness_dsn credential."
			t.next = "Next: deedglass connector credentials set " + name + " --field witness_dsn=env:WITNESS_DSN --field dsn=env:DSN --kind dsn"
		case decoder.ReasonUnreachable:
			if m := detailString(d, "message"); m != "" {
				t.explain = "Last error: " + m
			}
		}
		return t
	case decoder.StatusWaiting:
		t := decoderText{kind: "pending", headline: "waiting " + ui.Sep + " another Deedglass process holds this slot"}
		if h := detailString(d, "holder"); h != "" {
			t.headline += " (" + h + ")"
		}
		return t
	case decoder.StatusPending:
		return decoderText{kind: "pending", headline: "pending " + ui.Sep + " a runner starts within a few seconds"}
	case decoder.StatusDropped:
		return decoderText{kind: "off", headline: "dropped " + ui.Sep + " a new slot starts on the next sync"}
	case decoder.StatusDisabled:
		t := decoderText{kind: "off", headline: "disabled " + ui.Sep + " " + reason}
		if reason == decoder.ReasonSlotOrphaned {
			t.kind = "unknown"
			t.explain = "Deedglass couldn't drop slot " + detailString(d, "slot") + " when the connector stopped being checked. It still holds WAL."
			t.next = "Next: as a superuser on that database: " + detailString(d, "fix")
		}
		return t
	}
	return decoderText{kind: "unknown", headline: d.Status}
}

func symbolFor(e *Env, kind string) string {
	s := e.Style
	switch kind {
	case "ok":
		return s.Teal(ui.OK)
	case "fail":
		return s.Red(ui.Fail)
	case "pending":
		return s.Violet("◐")
	case "off":
		return s.Dim("·")
	}
	return s.Amber(ui.Unknown)
}

func printDecoderSummary(e *Env, d decoder.View, width int, now time.Time) {
	t := decoderWords(d, now)
	fmt.Fprintf(e.Stdout, "%s %s %s %s\n", symbolFor(e, t.kind), e.Style.Bold(ui.Pad(d.Name, width)), ui.Sep, t.headline)
	if t.explain != "" {
		fmt.Fprintln(e.Stdout, "  "+t.explain)
	}
	if t.next != "" {
		fmt.Fprintln(e.Stdout, "  "+t.next)
	}
}

func printDecoderDetail(e *Env, d decoder.View, now time.Time) {
	t := decoderWords(d, now)
	s := e.Style
	head := d.Name + " " + ui.Sep + " " + d.Health + " " + ui.Sep + " slot " + d.Slot
	if d.Server.Version != nil {
		head += " " + ui.Sep + " PG " + *d.Server.Version
	}
	fmt.Fprintf(e.Stdout, "%s %s\n", symbolFor(e, t.kind), head)
	if d.Status != decoder.StatusRunning {
		fmt.Fprintln(e.Stdout, "  "+strings.TrimPrefix(t.headline, d.Status+" "+ui.Sep+" "))
		if t.explain != "" {
			fmt.Fprintln(e.Stdout, "  "+t.explain)
		}
		if t.next != "" {
			fmt.Fprintln(e.Stdout, "  "+t.next)
		}
	}
	row := func(k, v string) { fmt.Fprintf(e.Stdout, "  %s %s\n", s.Dim(ui.Pad(k, 11)), v) }
	lag := "—"
	if d.Lag.Bytes != nil {
		lag = walSize(*d.Lag.Bytes)
		if d.Lag.Seconds != nil {
			lag += fmt.Sprintf(" %s %.1f s behind", ui.Sep, *d.Lag.Seconds)
		}
	}
	row("lag", ui.Pad(lag, 30)+s.Dim("checkpoint")+"  "+orDash(derefStr(d.Positions.CheckpointLSN)))
	watching := strings.Join(d.Watching, " "+ui.Sep+" ")
	if watching == "" {
		watching = "—"
	}
	row("watching", watching+"  "+s.Dim("(+ deedglass.ledger)"))
	frames := []string{}
	for _, c := range decoder.Classes {
		if n := d.Frames[string(c)]; n > 0 {
			frames = append(frames, fmt.Sprintf("%s %d", c, n))
		}
	}
	if len(frames) == 0 {
		frames = append(frames, "none yet")
	}
	row("frames", strings.Join(frames, " "+ui.Sep+" "))
	for i, f := range d.Recent {
		k := ""
		if i == 0 {
			k = "recent"
		}
		action := "—"
		if f.Action != nil {
			action = clipID(*f.Action)
		}
		row(k, fmt.Sprintf("%s  %s  %s  %s", ui.Pad(orDash(derefStr(f.CommitLSN)), 10), ui.Pad(classWords(f.Class), 13),
			ui.Pad(action, 16), countsWords(f)))
	}
}

// classWords is a frame class as the CLI shows it.
func classWords(c string) string {
	switch c {
	case string(decoder.ClassForgedMarker), string(decoder.ClassForgedMarkerNonTx):
		return "forged marker"
	case string(decoder.ClassMarkerCount):
		return "marker count"
	case string(decoder.ClassMarkerMismatch):
		return "mismatched"
	case string(decoder.ClassForeignLedger):
		return "foreign ledger"
	case string(decoder.ClassPublicationGap):
		return "pub. gap"
	}
	return c
}

// countsWords is "public.orders U 1" for a frame's counts (or why there are none).
func countsWords(f decoder.ViewFrame) string {
	switch f.Class {
	case string(decoder.ClassForgedMarker), string(decoder.ClassForgedMarkerNonTx):
		if len(f.Counts) == 0 {
			return "(no ledger row) " + ui.Sep + " alert sent"
		}
	case string(decoder.ClassMissing):
		return "(no frame for this ledger row)"
	}
	var parts []string
	for _, t := range sortedKeys(f.Counts) {
		c := f.Counts[t]
		for _, op := range []struct{ k, l string }{{"insert", "I"}, {"update", "U"}, {"delete", "D"}, {"truncate", "T"}} {
			if n := c[op.k]; n > 0 {
				parts = append(parts, fmt.Sprintf("%s %s %d", t, op.l, n))
			}
		}
	}
	if len(parts) == 0 {
		return "—"
	}
	return strings.Join(parts, " "+ui.Sep+" ")
}

// clipID keeps an id's prefix and the first 8 characters after it: act_01j9zt3a…
func clipID(id string) string {
	i := strings.LastIndexByte(id, '_')
	if i < 0 || len(id) <= i+1+8 {
		return id
	}
	return id[:i+1+8] + "…"
}

// walSize is a WAL size in Postgres's binary units (1 MB = 1,048,576 bytes, as max_slot_wal_keep_size
// and DEEDGLASS_SLOT_LAG_ALERT_MB count): 3.9 kB, 611 MB.
func walSize(n int64) string {
	switch {
	case n < 1024:
		return fmt.Sprintf("%d B", n)
	case n < 1<<20:
		return fmt.Sprintf("%.1f kB", float64(n)/1024)
	case n < 1<<30:
		return fmt.Sprintf("%.0f MB", float64(n)/(1<<20))
	}
	return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
}

func agoWords(d time.Duration) string {
	switch {
	case d < 0:
		return "now"
	case d < time.Minute:
		return fmt.Sprintf("%ds ago", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	}
	return fmt.Sprintf("%dh ago", int(d.Hours()))
}

func deref64(p *int64) int64 {
	if p == nil {
		return 0
	}
	return *p
}

func deref64Ptr(d decoder.View) int64 {
	if d.Lag.RetainedBytes != nil {
		return *d.Lag.RetainedBytes
	}
	return deref64(d.Lag.Bytes)
}

func derefStr(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func orDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

// parseInt64 reads a whole number from a status detail (JSON numbers arrive as float64).
func parseInt64(s string) (int64, error) {
	f, err := strconv.ParseFloat(s, 64)
	return int64(f), err
}

// durWords is a short duration for people: 8s, 2m.
func durWords(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Round(time.Second).Seconds()))
	}
	return fmt.Sprintf("%dm", int(d.Minutes()))
}

func runDecoderDrop(ctx context.Context, e *Env, args []string) error {
	if len(args) > 0 {
		return usageErr(fmt.Errorf("deedglass decoder drop takes flags only (got %q)", args[0]), "deedglass decoder drop --connector postgres")
	}
	v := decoderDropValues
	if v.connector == "" {
		return usageErr(errors.New("--connector is required"), "deedglass decoder drop --connector postgres")
	}
	if v.confirm == "" && (!e.IsTTY || e.Globals.JSON) {
		return usageErr(errors.New("Dropping a slot needs a typed confirmation, and this isn't an interactive terminal"),
			"Run it in a terminal, or add --confirm "+v.connector+" in scripts.")
	}
	c, err := apiClient(e)
	if err != nil {
		return err
	}
	d, err := getDecoder(ctx, c, v.connector, 0)
	if err != nil {
		return apiErr(e, c, err)
	}
	confirm := v.confirm
	if confirm == "" {
		fmt.Fprintf(e.Stdout, "This drops slot %s on %q.\n", d.Slot, d.Name)
		fmt.Fprintf(e.Stdout, "Actions still waiting for Proven evidence on this connector won't get it (slot_dropped).\n")
		fmt.Fprintf(e.Stdout, "A new slot starts a new baseline. Type the connector name to confirm: ")
		line, _ := bufio.NewReader(e.stdin()).ReadString('\n')
		confirm = strings.TrimSpace(line)
	}
	if confirm != d.Name {
		return &ExitError{Code: ExitFailure, ErrCode: "not_confirmed", Err: errors.New("Nothing was dropped: the confirmation didn't match"),
			Next: "Run it again and type " + d.Name + ", or press Ctrl-C to leave the slot as it is."}
	}
	var res connectorapi.DropResult
	if _, err := c.Do(ctx, "DELETE", "/v1/connectors/"+url.PathEscape(v.connector)+"/decoder", map[string]string{"confirm": confirm}, &res); err != nil {
		return apiErr(e, c, err)
	}
	if e.Globals.JSON {
		return writeJSON(e, res)
	}
	fmt.Fprintln(e.Stdout, e.Style.OKLine(fmt.Sprintf("Slot dropped %s %s %s admin record %s %s a new slot starts on the next sync",
		ui.Sep, clipID(res.Slot), ui.Sep, clipID(res.Deed), ui.Sep)))
	return nil
}
