package slack

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// escZWSP is the zero-width space the card uses to break Slack syntax it must not trigger: runs of
// backticks inside a code block, emoji shortcodes and scheme separators in agent text.
const escZWSP = "\u200b"

// escReplacer replaces the three characters Slack's mrkdwn treats as control characters. A
// Replacer makes one pass, so an "&" it writes is never escaped again.
var escReplacer = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")

// Escape makes s safe to place in Slack text: & → &amp;, < → &lt;, > → &gt;. With these three gone,
// nothing in s can become a mention (<!channel>, <@U…>), a date token or a link (<https://x|Approve>).
// Every string the card interpolates goes through it.
func Escape(s string) string {
	if strings.IndexAny(s, "&<>") < 0 {
		return s
	}
	return escReplacer.Replace(s)
}

// Cap shortens s to at most max runes, ending with "…" when it cut something. s is text Escape
// already produced: Cap never cuts inside an entity (&amp; &lt; &gt;), so its answer is still
// escaped. max below 1 gives "".
func Cap(s string, max int) string {
	if max < 1 {
		return ""
	}
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	// Keep max-1 runes and add the ellipsis.
	cut, n := 0, 0
	for i := range s {
		if n == max-1 {
			cut = i
			break
		}
		n++
	}
	out := s[:cut]
	// An entity is at most 5 bytes ("&amp;"): drop a trailing "&" with no ";" after it.
	if amp := strings.LastIndexByte(out, '&'); amp >= 0 && amp >= len(out)-5 && !strings.Contains(out[amp:], ";") {
		out = out[:amp]
	}
	return out + "…"
}

// cardText prepares one agent- or org-supplied string for mrkdwn outside a code block: control
// characters and line breaks become spaces (no forged second line), Slack syntax is escaped,
// emoji shortcodes and "scheme://" are broken with a zero-width space (no emoji, no autolinked
// URL), and the result is capped at max runes.
func cardText(s string, max int) string {
	return Cap(escDefuse(Escape(escOneLine(s))), max)
}

// escOneLine turns every control character (line breaks included) into a space and trims the ends.
func escOneLine(s string) string {
	clean := true
	for _, r := range s {
		if unicode.IsControl(r) || r == ' ' || r == ' ' || r == utf8.RuneError {
			clean = false
			break
		}
	}
	if clean {
		return strings.TrimSpace(s)
	}
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if unicode.IsControl(r) || r == ' ' || r == ' ' || r == utf8.RuneError {
			b.WriteByte(' ')
			continue
		}
		b.WriteRune(r)
	}
	return strings.TrimSpace(b.String())
}

// escDefuse breaks ":" before a word character or a "/" with a zero-width space, so ":skull:" can't
// render as an emoji and "https://evil.example" isn't turned into a link. It leaves "Time: 10" and
// "10:14 " alone.
func escDefuse(s string) string {
	if strings.IndexByte(s, ':') < 0 {
		return s
	}
	var b strings.Builder
	b.Grow(len(s) + 8)
	for i := 0; i < len(s); i++ {
		c := s[i]
		b.WriteByte(c)
		if c != ':' || i+1 >= len(s) {
			continue
		}
		n := s[i+1]
		if n == '/' || n == '_' || n == '+' || n == '-' || (n >= 'a' && n <= 'z') || (n >= 'A' && n <= 'Z') || (n >= '0' && n <= '9' && !escDigitBefore(s, i)) {
			b.WriteString(escZWSP)
		}
	}
	return b.String()
}

// escDigitBefore reports whether s[i-1] is a digit, so a clock time like "10:14" stays readable.
func escDigitBefore(s string, i int) bool {
	return i > 0 && s[i-1] >= '0' && s[i-1] <= '9'
}

// escCodeSpan is one inline code span: escaped, one line, and without backticks (which would end the
// span early), capped at max runes.
func escCodeSpan(s string, max int) string {
	s = strings.ReplaceAll(escOneLine(s), "`", "'")
	return "`" + Cap(Escape(s), max) + "`"
}
