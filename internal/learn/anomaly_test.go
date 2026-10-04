package learn

import (
	"fmt"
	"math"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

var anmIST = anmMustLoc("Asia/Kolkata")

func anmMustLoc(name string) *time.Location {
	loc, err := time.LoadLocation(name)
	if err != nil {
		panic(err)
	}
	return loc
}

// anmWindow is the 60-minute window starting at the given org-local hour on 2026-10-08 IST.
func anmWindow(hour int) DetectWindow {
	start := time.Date(2026, 10, 8, hour, 0, 0, 0, anmIST).UTC()
	return DetectWindow{Start: start, End: start.Add(time.Hour)}
}

// anmHistory builds an agent created age before the window's start, with 336 zero-filled
// hourly buckets before the window, each filled by fill(i, inBaseline).
func anmHistory(w DetectWindow, age time.Duration, total int, fill func(i int, b *HourBucket)) AgentHistory {
	h := AgentHistory{
		Agent: "agt_01jbqtest", Name: "growth-agent",
		CreatedAt: w.Start.Add(-age), Total: total, ActiveDays: 14,
		ToolsSeen30d: map[string]bool{"slack.post_message": true},
	}
	first := w.Start.Truncate(time.Hour).Add(-336 * time.Hour)
	for i := range 336 {
		b := HourBucket{Start: first.Add(time.Duration(i) * time.Hour)}
		if fill != nil {
			fill(i, &b)
		}
		h.Hours = append(h.Hours, b)
	}
	for hr := 8; hr < 18; hr++ {
		h.HourOfDayShare[hr] = 0.1
	}
	return h
}

func anmIDs(prefix string, n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = fmt.Sprintf("act_%s%02d", prefix, i)
	}
	return out
}

// anmDetect runs Detect, checks every finding's evidence against the allow-list and that
// a second run gives the same answer.
func anmDetect(t *testing.T, h AgentHistory, w DetectWindow) []Finding {
	t.Helper()
	got := Detect(h, w, anmIST)
	for _, f := range got {
		anmCheckEvidence(t, f)
	}
	if again := Detect(h, w, anmIST); !reflect.DeepEqual(got, again) {
		t.Fatalf("Detect is not deterministic:\n%#v\n%#v", got, again)
	}
	return got
}

func anmCheckEvidence(t *testing.T, f Finding) {
	t.Helper()
	for _, p := range anmEvidenceProblems(f) {
		t.Error(p)
	}
}

// anmEvidenceProblems lists every way a finding breaks the evidence and copy rules.
func anmEvidenceProblems(f Finding) []string {
	var out []string
	bad := func(format string, args ...any) { out = append(out, f.Kind+": "+fmt.Sprintf(format, args...)) }
	for k, v := range f.Evidence {
		if !slices.Contains(EvidenceKeys, k) {
			bad("evidence key %q is outside the allow-list", k)
		}
		switch v := v.(type) {
		case int, float64:
		case string:
			if len(v) > 64 {
				bad("evidence %s is a long string (%d bytes)", k, len(v))
			}
		case []string:
			if k != "action_ids" || len(v) > MaxEvidenceActionIDs {
				bad("evidence %s holds %d strings", k, len(v))
			}
		default:
			bad("evidence %s has type %T", k, v)
		}
	}
	for _, k := range []string{"window_start", "window_end", "hour", "timezone", "history_hours", "action_ids"} {
		if _, ok := f.Evidence[k]; !ok {
			bad("evidence misses %s", k)
		}
	}
	if !slices.Contains([]string{SeverityInfo, SeverityWarn, SeverityHigh}, f.Severity) {
		bad("severity %q", f.Severity)
	}
	if f.Score != math.Round(f.Score*100)/100 {
		bad("score %v is not rounded to 2 decimals", f.Score)
	}
	if strings.ContainsAny(f.Title, "!\u2013\u2014") {
		bad("title %q has an exclamation mark or a dash", f.Title)
	}
	return out
}

func anmOfKind(fs []Finding, kind string) []Finding {
	var out []Finding
	for _, f := range fs {
		if f.Kind == kind {
			out = append(out, f)
		}
	}
	return out
}

func anmOne(t *testing.T, fs []Finding, kind string) Finding {
	t.Helper()
	got := anmOfKind(fs, kind)
	if len(got) != 1 {
		t.Fatalf("want one %s finding, got %d: %#v", kind, len(got), fs)
	}
	return got[0]
}

func anmNone(t *testing.T, fs []Finding, kind string) {
	t.Helper()
	if got := anmOfKind(fs, kind); len(got) != 0 {
		t.Fatalf("want no %s finding, got %#v", kind, got)
	}
}

const anmDay = 24 * time.Hour

func TestDetectNewTool(t *testing.T) {
	w := anmWindow(15)
	w.Current = HourBucket{
		Actions: 9,
		Tools: map[string]int{
			"github.merge_pr": 2, "slack.post_message": 5, "github.list_prs": 1, "github.zero": 0,
		},
		ToolSide: map[string]string{"github.merge_pr": SideWrite, "github.list_prs": SideRead},
		Samples:  Samples{ByTool: map[string][]string{"github.merge_pr": anmIDs("m", 2)}},
	}
	h := anmHistory(w, 10*anmDay, 500, func(i int, b *HourBucket) {
		if i == 5 {
			b.Tools = map[string]int{"github.list_prs": 1}
		}
	})
	fs := anmDetect(t, h, w)
	f := anmOne(t, fs, AnomalyNewTool)
	if f.Tool != "github.merge_pr" || f.Severity != SeverityWarn || f.Score != 2 {
		t.Fatalf("finding %+v", f)
	}
	if want := "first github.merge_pr (write), 15:00 to 16:00 IST"; f.Title != want {
		t.Fatalf("title %q, want %q", f.Title, want)
	}
	if f.Evidence["tool"] != "github.merge_pr" || f.Evidence["side_effect"] != SideWrite ||
		f.Evidence["count"] != 2 || f.Evidence["history_hours"] != 241 ||
		!reflect.DeepEqual(f.Evidence["action_ids"], anmIDs("m", 2)) {
		t.Fatalf("evidence %#v", f.Evidence)
	}

	t.Run("a 3-hour-old agent's first tools", func(t *testing.T) {
		young := anmHistory(w, 3*time.Hour, 40, nil)
		young.ToolsSeen30d = nil
		if fs := anmDetect(t, young, w); len(fs) != 0 {
			t.Fatalf("want nothing, got %#v", fs)
		}
	})
	t.Run("fewer than 20 actions", func(t *testing.T) {
		if fs := anmDetect(t, anmHistory(w, 10*anmDay, 19, nil), w); len(anmOfKind(fs, AnomalyNewTool)) != 0 {
			t.Fatalf("got %#v", fs)
		}
	})
	t.Run("exactly 24 hours old", func(t *testing.T) {
		h := anmHistory(w, 23*time.Hour, 40, nil) // w.End is one hour later: 24 h exactly
		anmNone(t, anmDetect(t, h, w), AnomalyNewTool)
	})
}

func TestDetectNewToolSeverities(t *testing.T) {
	w := anmWindow(15)
	w.Current = HourBucket{
		Actions: 4,
		Tools:   map[string]int{"z.read": 1, "a.drop": 1, "m.mystery": 1, "b.blank": 1},
		ToolSide: map[string]string{
			"z.read": SideRead, "a.drop": SideDestructive, "m.mystery": SideUnknown, "b.blank": "",
		},
	}
	fs := anmDetect(t, anmHistory(w, 10*anmDay, 500, nil), w)
	want := []struct{ tool, sev, side string }{
		{"a.drop", SeverityHigh, SideDestructive},
		{"b.blank", SeverityWarn, SideUnknown},
		{"m.mystery", SeverityWarn, SideUnknown},
		{"z.read", SeverityInfo, SideRead},
	}
	if len(fs) != len(want) {
		t.Fatalf("got %#v", fs)
	}
	for i, wt := range want {
		f := fs[i]
		if f.Tool != wt.tool || f.Severity != wt.sev || f.Evidence["side_effect"] != wt.side {
			t.Errorf("finding %d: %s %s %v, want %+v", i, f.Tool, f.Severity, f.Evidence["side_effect"], wt)
		}
		if want := "first " + wt.tool + " (" + wt.side + "), 15:00 to 16:00 IST"; f.Title != want {
			t.Errorf("title %q, want %q", f.Title, want)
		}
		if ids := f.Evidence["action_ids"].([]string); ids == nil || len(ids) != 0 {
			t.Errorf("action_ids %#v, want an empty list", ids)
		}
	}
}

// anmVolumeHistory: 240 baseline hours cycling 3, 3, 3, 50, 54 (μ 22.6, σ ≈ 24.04) after a
// creation 10 days before the window; the 96 hours before creation are busy and must not count.
func anmVolumeHistory(w DetectWindow) AgentHistory {
	cycle := []int{3, 3, 3, 50, 54}
	return anmHistory(w, 10*anmDay, 5424, func(i int, b *HourBucket) {
		if i < 96 {
			b.Actions = 1000
			return
		}
		b.Actions = cycle[(i-96)%5]
	})
}

func TestDetectVolumeSpike(t *testing.T) {
	w := anmWindow(14)
	w.Current = HourBucket{Actions: 212, Writes: 3, Samples: Samples{All: anmIDs("v", 15)},
		Tools: map[string]int{"slack.post_message": 212}}
	fs := anmDetect(t, anmVolumeHistory(w), w)
	f := anmOne(t, fs, AnomalyVolumeSpike)
	sd := math.Sqrt((3*19.6*19.6 + 27.4*27.4 + 31.4*31.4) / 5)
	if want := math.Round((212-22.6)/sd*100) / 100; f.Score != want || f.Score < 3 || f.Score >= 10 {
		t.Fatalf("score %v, want %v (warn range)", f.Score, want)
	}
	if f.Severity != SeverityWarn {
		t.Fatalf("severity %s", f.Severity)
	}
	if want := "212 calls, 14:00 to 15:00 IST · 9.4× its usual 22.6"; f.Title != want {
		t.Fatalf("title %q, want %q", f.Title, want)
	}
	if f.Evidence["factor"] != 9.4 || f.Evidence["baseline_mean"] != 22.6 || f.Evidence["count"] != 212 ||
		f.Evidence["writes"] != 3 || len(f.Evidence["action_ids"].([]string)) != 10 {
		t.Fatalf("evidence %#v", f.Evidence)
	}
	if len(fs) != 1 {
		t.Fatalf("only the volume spike should fire, got %#v", fs)
	}

	t.Run("high when the score is 10 or more", func(t *testing.T) {
		h := anmHistory(w, 14*anmDay, 6720, func(_ int, b *HourBucket) { b.Actions = 20 })
		f := anmOne(t, anmDetect(t, h, w), AnomalyVolumeSpike)
		if f.Severity != SeverityHigh || f.Score < 10 {
			t.Fatalf("%+v", f)
		}
	})
	t.Run("a steady 2x ramp over 3 days", func(t *testing.T) {
		h := anmHistory(w, 20*anmDay, 4000, func(i int, b *HourBucket) {
			b.Actions = 10
			if i >= 264 {
				b.Actions = 10 + (i-264)*10/72
			}
		})
		w := w
		w.Current = HourBucket{Actions: 20}
		anmNone(t, anmDetect(t, h, w), AnomalyVolumeSpike)
	})
	t.Run("1 to 4 calls stays under the floor", func(t *testing.T) {
		h := anmHistory(w, 14*anmDay, 336, func(_ int, b *HourBucket) { b.Actions = 1 })
		w := w
		w.Current = HourBucket{Actions: 4}
		anmNone(t, anmDetect(t, h, w), AnomalyVolumeSpike)
	})
	t.Run("a quiet agent wakes up", func(t *testing.T) {
		h := anmHistory(w, 4*anmDay, 0, nil)
		w := w
		w.Current = HourBucket{Actions: 25}
		f := anmOne(t, anmDetect(t, h, w), AnomalyVolumeSpike)
		if _, ok := f.Evidence["factor"]; ok {
			t.Fatalf("factor with a zero mean: %#v", f.Evidence)
		}
		if want := "25 calls, 14:00 to 15:00 IST · usually 0 an hour"; f.Title != want {
			t.Fatalf("title %q", f.Title)
		}
		if f.Score != 25 || f.Evidence["baseline_stddev"] != 1.0 {
			t.Fatalf("%+v", f)
		}
	})
}

func TestDetectOffHours(t *testing.T) {
	w := DetectWindow{Start: time.Date(2026, 10, 7, 20, 30, 0, 0, time.UTC)} // 02:00 IST
	w.End = w.Start.Add(time.Hour)
	w.Current = HourBucket{Actions: 12, Samples: Samples{All: anmIDs("o", 12)}}
	h := anmHistory(w, 14*anmDay, 2000, func(_ int, b *HourBucket) { b.Actions = 6 })
	f := anmOne(t, anmDetect(t, h, w), AnomalyOffHours)
	if f.Severity != SeverityInfo || f.Score != 12 {
		t.Fatalf("%+v", f)
	}
	if want := "12 calls, 02:00 to 03:00 IST · usually none at this hour"; f.Title != want {
		t.Fatalf("title %q, want %q", f.Title, want)
	}
	if f.Evidence["hour"] != "02:00" || f.Evidence["timezone"] != "IST" || f.Evidence["share"] != 0.0 ||
		f.Evidence["window_start"] != "2026-10-07T20:30:00Z" || f.Evidence["window_end"] != "2026-10-07T21:30:00Z" ||
		len(f.Evidence["action_ids"].([]string)) != 10 {
		t.Fatalf("evidence %#v", f.Evidence)
	}

	t.Run("warn with a write and a small share", func(t *testing.T) {
		h := h
		h.HourOfDayShare[2] = 0.003
		w := w
		w.Current.Writes = 1
		f := anmOne(t, anmDetect(t, h, w), AnomalyOffHours)
		if f.Severity != SeverityWarn || f.Evidence["share"] != 0.003 || f.Evidence["writes"] != 1 {
			t.Fatalf("%+v", f)
		}
		if want := "12 calls, 02:00 to 03:00 IST · usually 0.3% of its calls at this hour"; f.Title != want {
			t.Fatalf("title %q", f.Title)
		}
	})
	t.Run("nightly-etl at 02:00, its hours", func(t *testing.T) {
		h := h
		h.Name = "nightly-etl"
		h.HourOfDayShare = [24]float64{}
		h.HourOfDayShare[2] = 0.6
		h.HourOfDayShare[3] = 0.4
		anmNone(t, anmDetect(t, h, w), AnomalyOffHours)
	})
	for name, mod := range map[string]func(*AgentHistory, *DetectWindow){
		"share at 1%":      func(h *AgentHistory, _ *DetectWindow) { h.HourOfDayShare[2] = 0.01 },
		"4 calls":          func(_ *AgentHistory, w *DetectWindow) { w.Current.Actions = 4 },
		"199 in 14 days":   func(h *AgentHistory, _ *DetectWindow) { h.Total = 199 },
		"4 active days":    func(h *AgentHistory, _ *DetectWindow) { h.ActiveDays = 4 },
		"cold, 2 days old": func(h *AgentHistory, _ *DetectWindow) { h.CreatedAt = w.Start.Add(-48 * time.Hour) },
	} {
		t.Run(name, func(t *testing.T) {
			h, w := h, w
			mod(&h, &w)
			anmNone(t, anmDetect(t, h, w), AnomalyOffHours)
		})
	}
}

func TestDetectDenySpike(t *testing.T) {
	w := anmWindow(10)
	w.Current = HourBucket{Actions: 24, Denied: 9, Samples: Samples{Denied: anmIDs("d", 9)}}
	// D = 99, N = 4998: p = 100 / 5000 = 0.02.
	h := anmHistory(w, 14*anmDay, 4998, func(i int, b *HourBucket) {
		if i == 300 {
			b.Actions, b.Denied = 4998, 99
		}
	})
	f := anmOne(t, anmDetect(t, h, w), AnomalyDenySpike)
	se := math.Sqrt(0.02 * 0.98 / 24)
	if want := math.Round((9.0/24-0.02)/se*100) / 100; f.Score != want || f.Severity != SeverityWarn {
		t.Fatalf("score %v severity %s, want %v warn", f.Score, f.Severity, want)
	}
	if want := "9 of 24 calls denied, 10:00 to 11:00 IST · usually 2%"; f.Title != want {
		t.Fatalf("title %q, want %q", f.Title, want)
	}
	if f.Evidence["deny_rate"] != 0.375 || f.Evidence["baseline_rate"] != 0.02 || f.Evidence["count"] != 24 ||
		!reflect.DeepEqual(f.Evidence["action_ids"], anmIDs("d", 9)) {
		t.Fatalf("evidence %#v", f.Evidence)
	}

	for name, cur := range map[string]HourBucket{
		"1 of 1 denied":   {Actions: 1, Denied: 1},
		"4 of 24 denied":  {Actions: 24, Denied: 4},
		"19 of 19 denied": {Actions: 19, Denied: 19},
	} {
		t.Run(name, func(t *testing.T) {
			w := w
			w.Current = cur
			anmNone(t, anmDetect(t, h, w), AnomalyDenySpike)
		})
	}
}

func TestDetectDenyBaselineFormula(t *testing.T) {
	if p := denyBaseline(AgentHistory{}); p != 0.5 {
		t.Fatalf("p with no history = %v, want 0.5", p)
	}
	w := anmWindow(10)
	h := anmHistory(w, 14*anmDay, 0, nil) // D = N = 0, so p = 1/2
	// threshold: 0.5 + 3·√(0.25/20) = 0.8354; 17/20 = 0.85 fires, 16/20 = 0.80 does not.
	w.Current = HourBucket{Actions: 20, Denied: 17}
	f := anmOne(t, anmDetect(t, h, w), AnomalyDenySpike)
	if want := math.Round((0.85-0.5)/math.Sqrt(0.25/20)*100) / 100; f.Score != want || f.Evidence["baseline_rate"] != 0.5 {
		t.Fatalf("%+v", f)
	}
	if want := "17 of 20 calls denied, 10:00 to 11:00 IST · usually 50%"; f.Title != want {
		t.Fatalf("title %q", f.Title)
	}
	w.Current.Denied = 16
	anmNone(t, anmDetect(t, h, w), AnomalyDenySpike)
}

func TestDetectMismatchSpike(t *testing.T) {
	w := anmWindow(11)
	w.Current = HourBucket{Actions: 40, Mismatch: 3, Samples: Samples{Mismatch: anmIDs("x", 3)}}
	h := anmHistory(w, 14*anmDay, 3000, func(_ int, b *HourBucket) { b.Actions = 9 })
	f := anmOne(t, anmDetect(t, h, w), AnomalyMismatchSpike)
	if f.Severity != SeverityHigh || f.Score != 3 {
		t.Fatalf("%+v", f)
	}
	if want := "3 MISMATCH verdicts, 11:00 to 12:00 IST · usually 0"; f.Title != want {
		t.Fatalf("title %q, want %q", f.Title, want)
	}
	if f.Evidence["mismatches"] != 3 || f.Evidence["baseline_mean"] != 0.0 || f.Evidence["count"] != 40 {
		t.Fatalf("evidence %#v", f.Evidence)
	}

	t.Run("1 mismatch", func(t *testing.T) {
		w := w
		w.Current.Mismatch = 1
		anmNone(t, anmDetect(t, h, w), AnomalyMismatchSpike)
	})
	// A baseline of 0, 1, 0, 1, ...: μ 0.5, σ 0.5, σ' 1.
	noisy := anmHistory(w, 14*anmDay, 3000, func(i int, b *HourBucket) { b.Actions, b.Mismatch = 9, i%2 })
	t.Run("the z rule over a noisy baseline", func(t *testing.T) {
		w := w
		w.Current.Mismatch = 4
		f := anmOne(t, anmDetect(t, noisy, w), AnomalyMismatchSpike)
		if f.Score != 3.5 || f.Severity != SeverityHigh || f.Evidence["baseline_stddev"] != 1.0 {
			t.Fatalf("%+v", f)
		}
		if want := "4 MISMATCH verdicts, 11:00 to 12:00 IST · usually 0.5"; f.Title != want {
			t.Fatalf("title %q", f.Title)
		}
	})
	t.Run("3 over a noisy baseline stays under", func(t *testing.T) {
		w := w
		w.Current.Mismatch = 3
		anmNone(t, anmDetect(t, noisy, w), AnomalyMismatchSpike)
	})
}

func TestDetectUserRefBurst(t *testing.T) {
	w := anmWindow(16)
	w.Current = HourBucket{Actions: 70, NewUserRefs: 61, Samples: Samples{NewUserRefs: anmIDs("u", 61)}}
	h := anmHistory(w, 14*anmDay, 5000, func(_ int, b *HourBucket) { b.Actions, b.NewUserRefs = 14, 2 })
	h.Name = "support-bot"
	f := anmOne(t, anmDetect(t, h, w), AnomalyUserRefBurst)
	if want := math.Round(59/math.Sqrt(2)*100) / 100; f.Score != want || f.Severity != SeverityWarn {
		t.Fatalf("%+v", f)
	}
	if want := "61 new on-behalf-of users, 16:00 to 17:00 IST · usually 2 an hour"; f.Title != want {
		t.Fatalf("title %q, want %q", f.Title, want)
	}
	if f.Evidence["new_refs"] != 61 || f.Evidence["factor"] != 30.5 || len(f.Evidence["action_ids"].([]string)) != 10 {
		t.Fatalf("evidence %#v", f.Evidence)
	}

	t.Run("a steady 3 new refs every hour", func(t *testing.T) {
		h := anmHistory(w, 14*anmDay, 5000, func(_ int, b *HourBucket) { b.Actions, b.NewUserRefs = 14, 3 })
		w := w
		w.Current = HourBucket{Actions: 14, NewUserRefs: 3}
		anmNone(t, anmDetect(t, h, w), AnomalyUserRefBurst)
	})
	t.Run("12 against a usual 5 is under 3x", func(t *testing.T) {
		h := anmHistory(w, 14*anmDay, 5000, func(_ int, b *HourBucket) { b.Actions, b.NewUserRefs = 14, 5 })
		w := w
		w.Current = HourBucket{Actions: 14, NewUserRefs: 12}
		anmNone(t, anmDetect(t, h, w), AnomalyUserRefBurst)
	})
}

// anmEverything fires all six kinds at once (two new tools) for an agent 14 days old.
func anmEverything() (AgentHistory, DetectWindow) {
	w := DetectWindow{Start: time.Date(2026, 10, 7, 18, 30, 0, 0, time.UTC)} // 00:00 IST
	w.End = w.Start.Add(time.Hour)
	w.Current = HourBucket{
		Actions: 300, Denied: 50, Mismatch: 3, NewUserRefs: 30, Writes: 120,
		Tools:    map[string]int{"z.tool": 1, "a.tool": 299},
		ToolSide: map[string]string{"z.tool": SideRead, "a.tool": SideWrite},
		Samples: Samples{
			All: anmIDs("a", 12), Denied: anmIDs("d", 11), Mismatch: anmIDs("x", 3), NewUserRefs: anmIDs("u", 30),
			ByTool: map[string][]string{"z.tool": anmIDs("z", 1), "a.tool": anmIDs("t", 20)},
		},
	}
	h := anmHistory(w, 14*anmDay, 672, func(_ int, b *HourBucket) {
		b.Actions = 2
		b.Tools = map[string]int{"slack.post_message": 2}
	})
	return h, w
}

func TestDetectDeterministicOrder(t *testing.T) {
	h, w := anmEverything()
	fs := anmDetect(t, h, w)
	var got []string
	for _, f := range fs {
		got = append(got, f.Kind+"/"+f.Tool)
	}
	want := []string{
		"new_tool/a.tool", "new_tool/z.tool", "volume_spike/", "off_hours/",
		"deny_spike/", "mismatch_spike/", "user_ref_burst/",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("order %v, want %v", got, want)
	}
	for range 50 {
		if again := Detect(h, w, anmIST); !reflect.DeepEqual(fs, again) {
			t.Fatal("a later run differs")
		}
	}
	if !slices.Equal(AnomalyKinds, []string{"new_tool", "volume_spike", "off_hours", "deny_spike", "mismatch_spike", "user_ref_burst"}) {
		t.Fatalf("AnomalyKinds %v", AnomalyKinds)
	}
	if DetectorVersion != "1" || MinHistory != 72*time.Hour {
		t.Fatal("thresholds changed without a DetectorVersion bump")
	}
}

func TestEvidenceAllowList(t *testing.T) {
	h, w := anmEverything()
	// Refs and args never reach the detectors; make sure nothing but ids leaves via Samples.
	fs := Detect(h, w, anmIST)
	if len(fs) != 7 {
		t.Fatalf("want 7 findings, got %d", len(fs))
	}
	seen := map[string]bool{}
	for _, f := range fs {
		anmCheckEvidence(t, f)
		for k := range f.Evidence {
			seen[k] = true
		}
		for _, id := range f.Evidence["action_ids"].([]string) {
			if !strings.HasPrefix(id, "act_") {
				t.Errorf("%s: action id %q", f.Kind, id)
			}
		}
	}
	for _, k := range EvidenceKeys {
		if !seen[k] {
			t.Errorf("allow-listed key %q is never produced; drop it or test it", k)
		}
	}
	// The check itself fails on a key outside the list.
	bad := Finding{Kind: "x", Severity: SeverityInfo, Evidence: map[string]any{
		"window_start": "", "window_end": "", "hour": "", "timezone": "", "history_hours": 0,
		"action_ids": []string{}, "args": "{}",
	}}
	if got := anmEvidenceProblems(bad); len(got) != 1 || !strings.Contains(got[0], `"args"`) {
		t.Fatalf("problems %q, want one about args", got)
	}
}

func TestDetectColdStart(t *testing.T) {
	h, w := anmEverything()
	h.CreatedAt = w.End.Add(-40 * time.Hour)
	h.Total = 100
	fs := anmDetect(t, h, w)
	if !BaselineBuilding(h, w.End) || HistoryAge(h, w.End) != 40*time.Hour {
		t.Fatalf("age %v", HistoryAge(h, w.End))
	}
	for _, f := range fs {
		if f.Kind != AnomalyNewTool {
			t.Fatalf("a cold agent got %s", f.Kind)
		}
	}
	if len(fs) != 2 {
		t.Fatalf("want the two new tools, got %#v", fs)
	}
	if fs[0].Evidence["history_hours"] != 40 {
		t.Fatalf("history_hours %v", fs[0].Evidence["history_hours"])
	}
	if got, want := BaselineBuildingSentence("support-bot", 41),
		"support-bot has 41 hours of history. Volume and hours checks start after 72 hours."; got != want {
		t.Fatalf("%q, want %q", got, want)
	}
	h.CreatedAt = w.End.Add(-MinHistory)
	if BaselineBuilding(h, w.End) {
		t.Fatal("72 h of history is enough")
	}
}

func TestBaselineHistoryAge(t *testing.T) {
	now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	for _, c := range []struct {
		created time.Time
		want    time.Duration
	}{
		{time.Time{}, 0},
		{now.Add(time.Hour), 0},
		{now, 0},
		{now.Add(-90 * time.Minute), 90 * time.Minute},
	} {
		if got := HistoryAge(AgentHistory{CreatedAt: c.created}, now); got != c.want {
			t.Errorf("HistoryAge(%v) = %v, want %v", c.created, got, c.want)
		}
	}
	if !BaselineBuilding(AgentHistory{}, now) {
		t.Fatal("an agent with no creation time is building its baseline")
	}
}

func TestDetectZeroHistory(t *testing.T) {
	if fs := Detect(AgentHistory{}, DetectWindow{}, nil); fs == nil || len(fs) != 0 {
		t.Fatalf("got %#v", fs)
	}
	// Old agent, nil maps, no buckets, a busy window: no panic, and only what the data supports.
	w := anmWindow(3)
	w.Current = HourBucket{Actions: 500, Denied: 400, Mismatch: 9, NewUserRefs: 200, Writes: 1}
	h := AgentHistory{CreatedAt: w.Start.Add(-30 * anmDay), Total: 1000, ActiveDays: 10}
	fs := anmDetect(t, h, w)
	var kinds []string
	for _, f := range fs {
		kinds = append(kinds, f.Kind)
	}
	want := []string{AnomalyVolumeSpike, AnomalyOffHours, AnomalyDenySpike, AnomalyMismatchSpike, AnomalyUserRefBurst}
	if !slices.Equal(kinds, want) {
		t.Fatalf("kinds %v, want %v", kinds, want)
	}
	// nil loc means UTC.
	fs = Detect(h, w, nil)
	if fs[0].Evidence["timezone"] != "UTC" || fs[0].Evidence["hour"] != "21:00" {
		t.Fatalf("evidence %#v", fs[0].Evidence)
	}
	if want := "500 calls, 21:30 to 22:30 UTC · usually 0 an hour"; fs[0].Title != want {
		t.Fatalf("title %q", fs[0].Title)
	}
}

func TestTitleGoldens(t *testing.T) {
	span := func(h int) string {
		w := anmWindow(h)
		return AnomalySpan(w.Start, w.End, anmIST)
	}
	for _, c := range []struct{ got, want string }{
		{TitleMismatchSpike(3, 0, span(11)), "3 MISMATCH verdicts, 11:00 to 12:00 IST · usually 0"},
		{TitleNewTool("github.merge_pr", "write", span(15)), "first github.merge_pr (write), 15:00 to 16:00 IST"},
		{TitleNewTool("x.y", "", span(15)), "first x.y (unknown), 15:00 to 16:00 IST"},
		{TitleVolumeSpike(212, 22.6, span(14)), "212 calls, 14:00 to 15:00 IST · 9.4× its usual 22.6"},
		{TitleVolumeSpike(1212, 100, span(14)), "1,212 calls, 14:00 to 15:00 IST · 12.1× its usual 100"},
		{TitleOffHours(12, 0, span(2)), "12 calls, 02:00 to 03:00 IST · usually none at this hour"},
		{TitleOffHours(12, 0.003, span(2)), "12 calls, 02:00 to 03:00 IST · usually 0.3% of its calls at this hour"},
		{TitleOffHours(1, 0.0004, span(2)), "1 call, 02:00 to 03:00 IST · usually under 0.1% of its calls at this hour"},
		{TitleDenySpike(9, 24, 0.02, span(10)), "9 of 24 calls denied, 10:00 to 11:00 IST · usually 2%"},
		{TitleDenySpike(1, 1, 0.5, span(10)), "1 of 1 call denied, 10:00 to 11:00 IST · usually 50%"},
		{TitleUserRefBurst(61, 2, span(16)), "61 new on-behalf-of users, 16:00 to 17:00 IST · usually 2 an hour"},
		{TitleUserRefBurst(1, 0, span(16)), "1 new on-behalf-of user, 16:00 to 17:00 IST · usually 0 an hour"},
		{TitleMismatchSpike(1, 0.02, span(11)), "1 MISMATCH verdict, 11:00 to 12:00 IST · usually under 0.1"},
		{BaselineBuildingSentence("support-bot", 41), "support-bot has 41 hours of history. Volume and hours checks start after 72 hours."},
		{BaselineBuildingSentence("new-bot", 1), "new-bot has 1 hour of history. Volume and hours checks start after 72 hours."},
	} {
		if c.got != c.want {
			t.Errorf("got  %q\nwant %q", c.got, c.want)
		}
	}
}

func TestTitleSpanZones(t *testing.T) {
	// 20:30 UTC is 02:00 IST: org-local hours don't line up with UTC hours at +05:30.
	s := time.Date(2026, 10, 7, 20, 30, 0, 0, time.UTC)
	if got := AnomalySpan(s, s.Add(time.Hour), anmIST); got != "02:00 to 03:00 IST" {
		t.Fatalf("%q", got)
	}
	if got := AnomalySpan(s, s.Add(time.Hour), nil); got != "20:30 to 21:30 UTC" {
		t.Fatalf("%q", got)
	}
	// America/New_York leaves DST at 06:00 UTC on 2026-11-01: both ends name their zone.
	ny := anmMustLoc("America/New_York")
	d := time.Date(2026, 11, 1, 5, 30, 0, 0, time.UTC)
	if got := AnomalySpan(d, d.Add(time.Hour), ny); got != "01:30 EDT to 01:30 EST" {
		t.Fatalf("%q", got)
	}
}

func TestTitleNumberFormats(t *testing.T) {
	for _, c := range []struct {
		v    float64
		want string
	}{
		{0, "0"}, {2, "2"}, {22.6, "22.6"}, {22.64, "22.6"}, {9.38, "9.4"},
		{1234.56, "1,234.6"}, {1000000, "1,000,000"}, {0.04, "under 0.1"}, {-1234.5, "-1,234.5"},
	} {
		if got := anmDecimal(c.v); got != c.want {
			t.Errorf("anmDecimal(%v) = %q, want %q", c.v, got, c.want)
		}
	}
	for n, want := range map[int]string{0: "0", 999: "999", 1000: "1,000", 1212: "1,212", 123456: "123,456", -1212: "-1,212"} {
		if got := anmCount(n); got != want {
			t.Errorf("anmCount(%d) = %q, want %q", n, got, want)
		}
	}
	if got := anmPercent(0.02); got != "2%" {
		t.Errorf("anmPercent = %q", got)
	}
}

func TestAnomalyCopyVoice(t *testing.T) {
	h, w := anmEverything()
	titles := []string{BaselineBuildingSentence("support-bot", 41)}
	for _, f := range Detect(h, w, anmIST) {
		titles = append(titles, f.Title)
	}
	// No alarm words: anomalies are signals, not verdicts.
	banned := []string{"suspicious", "alert", "attack", "danger", "warning"}
	for _, s := range titles {
		lower := strings.ToLower(s)
		for _, b := range banned {
			if strings.Contains(lower, b) {
				t.Errorf("%q uses %q", s, b)
			}
		}
		if strings.ContainsAny(s, "!\u2013\u2014") {
			t.Errorf("%q has an exclamation mark or a dash", s)
		}
	}
}
