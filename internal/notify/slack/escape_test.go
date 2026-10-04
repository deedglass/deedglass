package slack

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"
)

var unescaper = strings.NewReplacer("&amp;", "&", "&lt;", "<", "&gt;", ">")

func TestEscape(t *testing.T) {
	for in, want := range map[string]string{
		"":                               "",
		"plain":                          "plain",
		"a & b <c>":                      "a &amp; b &lt;c&gt;",
		"<!channel>":                     "&lt;!channel&gt;",
		"<@U012ABC>":                     "&lt;@U012ABC&gt;",
		"<https://evil.example|Approve>": "&lt;https://evil.example|Approve&gt;",
		"&amp;":                          "&amp;amp;",
		"&lt;!here&gt;":                  "&amp;lt;!here&amp;gt;",
	} {
		if got := Escape(in); got != want {
			t.Errorf("Escape(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCap(t *testing.T) {
	for _, c := range []struct {
		in   string
		max  int
		want string
	}{
		{"abc", 3, "abc"},
		{"abcd", 3, "ab…"},
		{"abcd", 1, "…"},
		{"abcd", 0, ""},
		{"héllo wörld", 6, "héllo…"},
		{"a&amp;b", 3, "a…"},   // never "a&…"
		{"ab&amp;c", 5, "ab…"}, // never "ab&am…"
		{"ab&amp;cdef", 8, "ab&amp;…"},
		{"x&lt;", 4, "x…"},
	} {
		if got := Cap(c.in, c.max); got != c.want {
			t.Errorf("Cap(%q, %d) = %q, want %q", c.in, c.max, got, c.want)
		}
	}
}

func TestCardTextHelpers(t *testing.T) {
	if got := cardText("  a\nb\r\tc\u2028d\x00 ", 50); got != "a b  c d" {
		t.Errorf("cardText %q", got)
	}
	if got := escDefuse("10:14 at :fire: and https://x and a:B :_ :+1: :-) x:9 :"); got != "10:14 at :\u200bfire: and https:\u200b//x and a:\u200bB :\u200b_ :\u200b+1: :\u200b-) x:\u200b9 :" {
		t.Errorf("escDefuse %q", got)
	}
	if got := escCodeSpan("a`b<c>\nd", 50); got != "`a'b&lt;c&gt; d`" {
		t.Errorf("escCodeSpan %q", got)
	}
}

// FuzzSlackEscape: Escape leaves no raw <, > or & (every & starts an entity), round-trips, and
// never panics; Cap keeps the text escaped; a card built from the input stays escaped and valid.
func FuzzSlackEscape(f *testing.F) {
	for _, s := range []string{"", "<!channel>", "<@U1>", "<https://evil|Approve>", "a & b", "&amp;", "```", ":skull:", "\u2028\x00", strings.Repeat("<&>", 400)} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		out := Escape(s)
		if strings.ContainsAny(out, "<>") {
			t.Fatalf("raw < or > in %q", out)
		}
		checkAmps(t, out)
		if unescaper.Replace(out) != s {
			t.Fatalf("round trip lost %q", s)
		}
		for _, n := range []int{1, 5, 7, 40} {
			c := Cap(out, n)
			if utf8.RuneCountInString(c) > n {
				t.Fatalf("Cap(%d) gave %d runes", n, utf8.RuneCountInString(c))
			}
			checkAmps(t, c)
		}
		ct := cardText(s, 100)
		if strings.ContainsAny(ct, "<>\n") {
			t.Fatalf("cardText left markup: %q", ct)
		}
		checkAmps(t, ct)

		v := cardBase()
		v.Agent, v.Tool, v.Reason, v.OnBehalfOf, v.DenyReason = s, s, s, s, s
		v.Summary = nil
		args, _ := json.Marshal(map[string]string{"v": s})
		v.Args = args
		raw, text, err := Card(v)
		if err != nil {
			t.Fatalf("Card: %v", err)
		}
		var decoded any
		if err := json.Unmarshal(raw, &decoded); err != nil {
			t.Fatal(err)
		}
		var texts, urls []string
		textStrings(decoded, &texts, &urls)
		for _, x := range append(texts, text) {
			x = dateToken.ReplaceAllString(x, "")
			if strings.ContainsAny(x, "<>") {
				t.Fatalf("raw markup in card text %q", x)
			}
			checkAmps(t, x)
		}
	})
}

func checkAmps(t *testing.T, s string) {
	t.Helper()
	for i := strings.IndexByte(s, '&'); i >= 0; {
		rest := s[i:]
		if !strings.HasPrefix(rest, "&amp;") && !strings.HasPrefix(rest, "&lt;") && !strings.HasPrefix(rest, "&gt;") {
			t.Fatalf("raw & in %q", s)
		}
		j := strings.IndexByte(s[i+1:], '&')
		if j < 0 {
			break
		}
		i += 1 + j
	}
}
