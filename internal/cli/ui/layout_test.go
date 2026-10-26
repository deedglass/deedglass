package ui

import (
	"bytes"
	"strings"
	"testing"
)

func TestShortID(t *testing.T) {
	for in, want := range map[string]string{
		"act_01m4x8kqmn6xmkfcbz0j1e1wvz": "act_01m4x8kq…",
		"sk_01m5vha6yrz8ty04pxpnx2vxe1":  "sk_01m5vha6…",
		"deed_01m4":                      "deed_01m4",
		"plain":                          "plain",
	} {
		if got := ShortID(in); got != want {
			t.Errorf("ShortID(%q) = %q, want %q", in, got, want)
		}
	}
}

// The KV block aligns keys, wraps values under themselves, keeps tags at the right edge and never
// passes the width, with or without colour; colour adds codes and nothing else.
func TestKV80Columns(t *testing.T) {
	rows := []Row{
		{Key: "request", Value: "UPDATE public.orders SET status = 'shipped' WHERE id = 42"},
		{Key: "check", Glyph: Fail, Value: "Doesn't match: Postgres changed 301 rows in public.orders. The agent reported 1.", Tag: "replay"},
		{Key: "", Glyph: OK, Value: "id 42   paid → shipped"},
		{Key: "result", Value: "first\nsecond paragraph"},
	}
	for _, w := range []int{0, 60, 80, 200} {
		plain := Style{}.KV("     ", w, rows...)
		colour := Style{Color: true}.KV("     ", w, rows...)
		if strings.Join(plain, "\n") != StripANSI(strings.Join(colour, "\n")) {
			t.Errorf("width %d: colour changes the layout\n%s\n%s", w, strings.Join(plain, "\n"), StripANSI(strings.Join(colour, "\n")))
		}
		for _, l := range plain {
			if Width(l) > LayoutWidth(w) {
				t.Errorf("width %d: %d columns: %q", w, Width(l), l)
			}
		}
	}
	got := strings.Join(Style{}.KV("     ", 80, rows...), "\n")
	want := `     request  UPDATE public.orders SET status = 'shipped' WHERE id = 42
     check    ✕ Doesn't match: Postgres changed 301 rows in               replay
                public.orders. The agent reported 1.
              ✓ id 42   paid → shipped
     result   first
              second paragraph`
	if got != want {
		t.Errorf("KV:\n%s\nwant:\n%s", got, want)
	}
}

func TestHeaderSectionNext(t *testing.T) {
	off, on := Style{}, Style{Color: true}
	if got := off.Header("deedglass scan", "agentproj", "", "read-only"); got != "deedglass scan  agentproj · read-only" {
		t.Errorf("header %q", got)
	}
	if got := on.Header("t", "m"); got != "\x1b[1mt\x1b[0m  \x1b[2mm\x1b[0m" {
		t.Errorf("styled header %q", got)
	}
	if got := off.Next("deedglass doctor"); got != "  next  deedglass doctor" {
		t.Errorf("next %q", got)
	}
	if got := off.Status(Pending, "waiting"); got != "◐ waiting" {
		t.Errorf("status %q", got)
	}
	if got := on.Status(OK, "ok"); got != "\x1b[36m✓\x1b[0m ok" {
		t.Errorf("styled status %q", got)
	}
}

func TestTable(t *testing.T) {
	st := Style{Color: true}
	lines := st.Table([]string{"ID", "STATUS", "NOTE"}, [][]string{
		{"sk_01m5vha6…", st.Status(OK, "active"), "a very long note that is clipped"},
		{"sk_01m4x8kq…", "retired", "short"},
	}, []int{0, 0, 12})
	got := StripANSI(strings.Join(lines, "\n"))
	want := "ID            STATUS    NOTE\nsk_01m5vha6…  ✓ active  a very long…\nsk_01m4x8kq…  retired   short"
	if got != want {
		t.Errorf("table:\n%s\nwant:\n%s", got, want)
	}
	if !strings.HasPrefix(lines[0], "\x1b[2m") {
		t.Error("the header row is dim")
	}
}

func TestHangWrap(t *testing.T) {
	l := "  check      Proven · Doesn't match · Postgres changed 301 rows in public.orders. The agent reported 1."
	got := HangWrap(l, 80)
	if len(got) != 2 || !strings.HasPrefix(got[1], strings.Repeat(" ", 13)+"T") && !strings.HasPrefix(got[1], strings.Repeat(" ", 13)) {
		t.Fatalf("%q", got)
	}
	for _, g := range got {
		if Width(g) > 80 {
			t.Errorf("%d columns: %q", Width(g), g)
		}
	}
	var b bytes.Buffer
	w := WrapWriter(&b, 80)
	w.Write([]byte(l[:20]))
	w.Write([]byte(l[20:] + "\nshort\n"))
	if b.String() != strings.Join(got, "\n")+"\nshort\n" {
		t.Errorf("writer %q", b.String())
	}
}

// NO_COLOR, a pipe or --no-color: no escape codes anywhere; ASCII stand-ins keep their columns.
func TestNoColorAndASCII(t *testing.T) {
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	if ColorAllowed(true, env(map[string]string{"NO_COLOR": "1"}), false) {
		t.Fatal("NO_COLOR")
	}
	st := Style{Color: ColorAllowed(true, env(map[string]string{"NO_COLOR": "1"}), false)}
	out := strings.Join(append(st.KV("  ", 80, Row{Key: "k", Glyph: OK, Value: "v", Tag: "live"}), st.Header("h", "m"), st.Next("n")), "\n")
	if strings.Contains(out, "\x1b") {
		t.Errorf("escape codes under NO_COLOR: %q", out)
	}
	for _, c := range []struct {
		tty  bool
		env  map[string]string
		want bool
	}{
		{true, nil, false},
		{true, map[string]string{"DEEDGLASS_ASCII": "1"}, true},
		{true, map[string]string{"DEEDGLASS_ASCII": "0"}, false},
		{true, map[string]string{"TERM": "dumb"}, true},
		{false, map[string]string{"DEEDGLASS_ASCII": "1"}, false},
	} {
		if got := ASCIIOnly(c.tty, env(c.env)); got != c.want {
			t.Errorf("ASCIIOnly(%v, %v) = %v", c.tty, c.env, got)
		}
	}
	var b bytes.Buffer
	ASCIIWriter(&b).Write([]byte("✓ ok\n✕ no\n◐ wait\n○ ?\n• note · a → b …\n"))
	if b.String() != "+ ok\nx no\n~ wait\no ?\n- note - a -> b ...\n" {
		t.Errorf("ascii %q", b.String())
	}
}

func TestCommands(t *testing.T) {
	got := strings.Join(Style{}.Commands("  ", 80, [2]string{"npx deedglass scan", "grade it"}, [2]string{"deedglass init --yes", "the stack"}), "\n")
	want := "  npx deedglass scan    grade it\n  deedglass init --yes  the stack"
	if got != want {
		t.Errorf("%q", got)
	}
}
