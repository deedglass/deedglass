package observe

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/deedglass/deedglass/internal/caught"
	"github.com/deedglass/deedglass/internal/store"
	"github.com/deedglass/deedglass/internal/witness/l2"
)

var t0 = time.Date(2026, 10, 17, 14, 2, 11, 0, time.UTC)

func tp(t time.Time) *time.Time { return &t }

func TestAttachable(t *testing.T) {
	watched := []string{"public.orders", "public.order_events"}
	closed := t0.Add(time.Minute)
	for _, tc := range []struct {
		name string
		at   time.Time
		tabs []string
		w    Window
		want bool
	}{
		{"at opened_at", t0, []string{"public.orders"}, Window{Opened: t0, Closed: &closed}, true},
		{"at closed_at", closed, []string{"public.orders"}, Window{Opened: t0, Closed: &closed}, true},
		{"before", t0.Add(-time.Nanosecond), []string{"public.orders"}, Window{Opened: t0, Closed: &closed}, false},
		{"after", closed.Add(time.Nanosecond), []string{"public.orders"}, Window{Opened: t0, Closed: &closed}, false},
		{"unwatched only", t0.Add(time.Second), []string{"public.products"}, Window{Opened: t0, Closed: &closed}, false},
		{"one watched among others", t0.Add(time.Second), []string{"public.products", "public.order_events"}, Window{Opened: t0, Closed: &closed}, true},
		{"open: up to now", t0.Add(time.Second), []string{"public.orders"}, Window{Opened: t0, Now: t0.Add(2 * time.Second)}, true},
		{"open: not past now", t0.Add(3 * time.Second), []string{"public.orders"}, Window{Opened: t0, Now: t0.Add(2 * time.Second)}, false},
	} {
		if got := Attachable(Candidate{ID: "dtx_1", CommitTime: tc.at, Tables: tc.tabs}, tc.w, watched); got != tc.want {
			t.Errorf("%s: got %v", tc.name, got)
		}
	}
}

func TestSummarizeAndSeal(t *testing.T) {
	frames := []FrameFacts{
		{ID: "dtx_b", CommitTime: tp(t0.Add(2 * time.Second)), Digest: "sha256:bb", Counts: map[string]Counts{"public.orders": {Update: 301}, "deedglass.ledger": {Insert: 9}}},
		{ID: "dtx_a", CommitTime: tp(t0), Digest: "sha256:aa", Counts: map[string]Counts{"public.order_events": {Insert: 301}}},
		{ID: "dtx_c", CommitTime: tp(t0.Add(time.Second)), Excluded: true, Digest: "sha256:cc", Counts: map[string]Counts{"public.orders": {Delete: 2}}},
		{ID: "dtx_x", Expired: true},
	}
	s := Summarize("obs_1", frames, 100, true)
	if s.Transactions != 4 || s.Excluded != 1 || s.Expired != 1 || s.Rows != 604 || s.Tables["public.orders"].Update != 301 ||
		s.Tables["public.orders"].Delete != 2 || s.FirstCommit == nil || !s.FirstCommit.Equal(t0) || !s.LastCommit.Equal(t0.Add(2*time.Second)) {
		t.Fatalf("summary %+v", s)
	}
	if _, ok := s.Tables["deedglass.ledger"]; ok {
		t.Fatal("the ledger never counts")
	}
	if len(s.Caught) != 2 {
		t.Fatalf("caught %v", s.Caught)
	}
	d := Seal("obs_1", "con_1", t0, t0.Add(time.Minute), frames, []string{FlagOverlap, FlagCaught})
	if d.FrameCount != 4 || len(d.Frames) != 4 || d.Frames[0] != "dtx_a" || len(d.Excluded) != 1 || d.Excluded[0] != "dtx_c" ||
		d.Flags[0] != FlagCaught || !strings.HasPrefix(d.FramesDigest, "sha256:") || !strings.HasPrefix(d.ChangesDigest, "sha256:") ||
		d.OpenedAt != "2026-10-17T14:02:11.000Z" || d.Tables["public.orders"].Update != 301 {
		t.Fatalf("seal %+v", d)
	}
	b, _ := json.Marshal(d)
	if strings.Contains(string(b), "deedglass.ledger") {
		t.Fatal("the seal names the ledger table")
	}
	// Past MaxSealFrames, only the digest pins the frames.
	var many []FrameFacts
	for i := 0; i < MaxSealFrames+1; i++ {
		many = append(many, FrameFacts{ID: "dtx_" + strings.Repeat("a", i%5) + string(rune('a'+i%26)), Counts: map[string]Counts{}})
	}
	if d := Seal("obs_1", "con_1", t0, t0, many, nil); d.Frames != nil || d.FrameCount != MaxSealFrames+1 || d.Flags == nil {
		t.Fatalf("many frames: %d listed", len(d.Frames))
	}
}

func TestCloseDecision(t *testing.T) {
	closed := t0
	hb := time.Second
	for _, tc := range []struct {
		name    string
		now     time.Time
		d       DecoderFacts
		flagged bool
		want    CloseStep
	}{
		{"a commit after the close", t0.Add(time.Second), DecoderFacts{LastCommitAt: tp(t0.Add(time.Millisecond)), Heartbeat: hb}, false, StepSeal},
		{"a commit at the close", t0.Add(time.Second), DecoderFacts{LastCommitAt: tp(t0), Heartbeat: hb}, false, StepSeal},
		{"a heartbeat two periods later", t0.Add(3 * time.Second), DecoderFacts{LastHeartbeatAt: tp(t0.Add(2 * time.Second)), Heartbeat: hb}, false, StepSeal},
		{"a heartbeat too soon", t0.Add(3 * time.Second), DecoderFacts{LastHeartbeatAt: tp(t0.Add(time.Second)), Heartbeat: hb}, false, StepWait},
		{"default heartbeat period", t0.Add(25 * time.Second), DecoderFacts{LastHeartbeatAt: tp(t0.Add(19 * time.Second))}, false, StepWait},
		{"behind after 30 s", t0.Add(31 * time.Second), DecoderFacts{}, false, StepFlagBehind},
		{"behind, already flagged", t0.Add(31 * time.Second), DecoderFacts{}, true, StepWait},
		{"late window over", t0.Add(LateAttach), DecoderFacts{}, true, StepSeal},
	} {
		if got := NextCloseStep(closed, tc.now, tc.d, tc.flagged); got != tc.want {
			t.Errorf("%s: got %d want %d", tc.name, got, tc.want)
		}
	}
}

func TestValidLabelAndCursor(t *testing.T) {
	for s, want := range map[string]bool{"nightly agent": true, "": false, "   ": false, "two\nlines": false, "tab\there": false,
		strings.Repeat("a", 80): true, strings.Repeat("a", 81): false, "ünïcode ✓": true, "line sep": false, "c1\u0085": false} {
		if ValidLabel(s) != want {
			t.Errorf("%q: %v", s, !want)
		}
	}
	at, id, ok := parseCursor("1760709731000000000.obs_01m3x71p6wesj7xewec6bwbwxm")
	if !ok || id != "obs_01m3x71p6wesj7xewec6bwbwxm" || at.UnixNano() != 1760709731000000000 {
		t.Fatal("cursor")
	}
	for _, bad := range []string{"", "x", "1.act_01m3x71p6wesj7xewec6bwbwxm", "-1.obs_01m3x71p6wesj7xewec6bwbwxm", "01.obs_01m3x71p6wesj7xewec6bwbwxm", "a.obs_01m3x71p6wesj7xewec6bwbwxm"} {
		if _, _, ok := parseCursor(bad); ok {
			t.Errorf("cursor %q accepted", bad)
		}
	}
}

func TestBuildDiff(t *testing.T) {
	s := func(x string) *string { return &x }
	f := l2.Frame{Total: 25, Changes: []l2.Change{
		{Seq: 2, Op: "U", Table: "public.orders", Key: map[string]*string{"id": s("42")},
			Old: map[string]l2.Value{"id": {T: s("42")}, "status": {T: s("paid")}, "email": {D: "sha256:x"}},
			New: map[string]l2.Value{"id": {T: s("42")}, "status": {T: s("shipped")}, "email": {D: "sha256:y"}}, Changed: []string{"status", "email"}},
		{Seq: 1, Op: "I", Table: "public.order_events", Key: map[string]*string{"id": nil}, New: map[string]l2.Value{"id": {T: s("7")}, "note": {N: true}}},
		{Seq: 3, Op: "D", Table: "public.orders", Key: map[string]*string{"id": s("9")}, Old: map[string]l2.Value{"id": {T: s("9")}, "blob": {X: "ff"}}},
		{Seq: 4, Op: "U", Table: "public.orders", Key: map[string]*string{"id": s("10")}, New: map[string]l2.Value{"big": {U: true}}},
		{Seq: 5, Op: "I", Table: "deedglass.ledger"},
	}}
	d := BuildDiff(f, 3)
	if len(d.Rows) != 3 || !d.Truncated || d.Total != 25 || d.Rows[0].Op != "insert" || d.Rows[0].Key["id"] != "NULL" || d.Rows[0].Cells[1].After != nil || *d.Rows[0].Cells[0].After != "7" {
		t.Fatalf("diff %+v", d)
	}
	up := d.Rows[1]
	if up.Op != "update" || len(up.Cells) != 2 || *up.Cells[0].Before != "paid" || *up.Cells[0].After != "shipped" || *up.Cells[1].After != Hidden {
		t.Fatalf("update %+v", up)
	}
	if del := d.Rows[2]; del.Op != "delete" || *del.Cells[0].Before != `\xff` {
		t.Fatalf("delete %+v", del)
	}
	d = BuildDiff(f, 20)
	if len(d.Rows) != 4 || *d.Rows[3].Cells[0].After != "(unchanged)" || d.Rows[3].Cells[0].Before != nil {
		t.Fatalf("all rows %+v", d.Rows)
	}
	if shown(l2.Value{}) != nil {
		t.Fatal("an empty value shows as NULL")
	}
}

func TestPresent(t *testing.T) {
	label := "nightly agent"
	closed := t0.Add(89 * time.Second)
	x := &Session{Row: store.ObserveSession{ID: "obs_01k", Label: &label, OpenedAt: t0, ClosedAt: &closed, State: StateSealed},
		Connector: ConnectorFacts{Name: "pg-demo"}}
	if got := Headline(x, nil); got != "Session obs_01k · nightly agent · 14:02–14:03 · pg-demo · sealed" {
		t.Fatalf("headline %q", got)
	}
	if got := WindowText(t0, nil, time.UTC); got != "14:02–now" {
		t.Fatal(got)
	}
	nextDay := t0.Add(24 * time.Hour)
	if got := WindowText(t0, &nextDay, nil); got != "17 Oct 14:02–18 Oct 14:02" {
		t.Fatal(got)
	}
	sum := Summary{Transactions: 3, Tables: map[string]Counts{"public.orders": {Update: 301, Truncate: 1}, "public.order_events": {Insert: 1301},
		"public.empty": {}, "public.t": {Truncate: 2, Delete: 1}}}
	if got := CountsLine(sum); got != "3 transactions · public.order_events 1,301 inserted · public.orders 301 updated, truncated once · public.t 1 deleted, truncated 2 times" {
		t.Fatalf("counts %q", got)
	}
	if CountsLine(Summary{Transactions: 1}) != "1 transaction" {
		t.Fatal("singular")
	}
	if got := CaughtLines([]caught.Item{{Line: "a"}, {Line: "b"}}); len(got) != 2 {
		t.Fatal(got)
	}
	if n := FlagNotes([]string{FlagOverlap, FlagDecoderBehind, FlagMaxSession, FlagFramesExpired, FlagCaught}); len(n) != 4 {
		t.Fatalf("notes %v", n)
	}
	for _, s := range []string{NoteObserved, NoteAttribution, NoteCantKnow, NoteExpired, NoteOverlap, NoteBehind, NoteMaxSession, ProSheet} {
		if strings.Contains(s, "—") || !strings.HasSuffix(s, ".") {
			t.Errorf("copy %q", s)
		}
	}
	if (&Session{Row: store.ObserveSession{State: StateOpen}}).Undoable() {
		t.Fatal("an open session isn't undoable")
	}
	u := &Session{Row: store.ObserveSession{State: StateSealed}, Summary: Summary{Transactions: 2, Excluded: 1}, Frames: []FrameFacts{{ID: "a"}, {ID: "b", Excluded: true}}}
	if !u.Undoable() {
		t.Fatal("one included frame is undoable")
	}
	u.Frames[0].Expired = true
	if u.Undoable() {
		t.Fatal("an expired included frame blocks the undo")
	}
	if (&NotEnabledError{Connector: "pg"}).Error() == "" || System.ref().ID != "deedglass" || (Actor{Type: "user", ID: "usr_1"}).idPtr() == nil || System.idPtr() != nil {
		t.Fatal("actors")
	}
}

func TestConnectorFacts(t *testing.T) {
	cf := connectorFacts(store.Connector{ID: "con_1", Name: "pg", Type: "postgres", Status: "active",
		Config: json.RawMessage(`{"witness":{"enabled":true,"watched_tables":["public.orders"],"sensitive_columns":[],"observe":{"enabled":true,"max_session":"2h","caught_rows":50}}}`)})
	if !cf.Enabled || cf.MaxSession != 2*time.Hour || cf.CaughtRows != 50 || len(cf.Watched) != 1 {
		t.Fatalf("%+v", cf)
	}
	if cf := connectorFacts(store.Connector{Type: "rest", Config: json.RawMessage(`{}`)}); cf.Enabled {
		t.Fatal("rest")
	}
	if cf := connectorFacts(store.Connector{Type: "postgres", Config: json.RawMessage(`not json`)}); cf.Enabled || cf.CaughtRows != 100 {
		t.Fatal("bad config")
	}
	if cf := connectorFacts(store.Connector{Type: "postgres", Config: json.RawMessage(`{"witness":{"enabled":false,"observe":{"enabled":true}}}`)}); cf.Enabled {
		t.Fatal("observe needs the witness")
	}
}
