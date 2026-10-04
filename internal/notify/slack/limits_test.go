package slack

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestValidateLimits(t *testing.T) {
	long := func(n int) string { return strings.Repeat("a", n) }
	txt := func(s string) string { b, _ := json.Marshal(s); return string(b) }
	sec := func(s string) string { return `{"type":"section","text":{"type":"mrkdwn","text":` + txt(s) + `}}` }
	btn := func(text, id, extra string) string {
		return `[{"type":"actions","elements":[{"type":"button","text":{"type":"plain_text","text":` + txt(text) + `},"action_id":` + txt(id) + extra + `}]}]`
	}
	many := func(n int, b string) string { return "[" + strings.TrimSuffix(strings.Repeat(b+",", n), ",") + "]" }
	field := func(n int, s string) string {
		return `[{"type":"section","fields":` + many(n, `{"type":"mrkdwn","text":`+txt(s)+`}`) + `}]`
	}
	confirm := func(title, text, ok, no string) string {
		return `,"confirm":{"title":{"type":"plain_text","text":` + txt(title) + `},"text":{"type":"plain_text","text":` + txt(text) + `},"confirm":{"type":"plain_text","text":` + txt(ok) + `},"deny":{"type":"plain_text","text":` + txt(no) + `}}`
	}
	for _, c := range []struct {
		name, blocks, err string
	}{
		{"ok", "[" + sec("hi") + "]", ""},
		{"ok divider", `[{"type":"divider"}]`, ""},
		{"not json", `{`, "not a JSON array"},
		{"empty", `[]`, "no blocks"},
		{"51 blocks", many(51, `{"type":"divider"}`), "51 blocks"},
		{"too big", "[" + sec(long(2900)) + strings.Repeat(","+sec(long(2900)), 5) + "]", "bytes of JSON"},
		{"header 151", `[{"type":"header","text":{"type":"plain_text","text":` + txt(long(151)) + `}}]`, "header text is 151"},
		{"header 150 runes", `[{"type":"header","text":{"type":"plain_text","text":` + txt(strings.Repeat("é", 150)) + `}}]`, ""},
		{"header mrkdwn", `[{"type":"header","text":{"type":"mrkdwn","text":"x"}}]`, "needs plain_text"},
		{"section 3001", "[" + sec(long(3001)) + "]", "section text is 3001"},
		{"section empty", `[{"type":"section"}]`, "needs text or fields"},
		{"11 fields", field(11, "x"), "11 fields"},
		{"field 2001", field(1, long(2001)), "field is 2001"},
		{"block_id 256", `[{"type":"divider","block_id":` + txt(long(256)) + `}]`, "block_id is 256"},
		{"context empty", `[{"type":"context","elements":[]}]`, "context elements"},
		{"context 11", `[{"type":"context","elements":` + many(11, `{"type":"mrkdwn","text":"x"}`) + `}]`, "11 context"},
		{"context bad", `[{"type":"context","elements":[1]}]`, "cannot unmarshal"},
		{"context 3001", `[{"type":"context","elements":[{"type":"mrkdwn","text":` + txt(long(3001)) + `}]}]`, "context text"},
		{"actions empty", `[{"type":"actions","elements":[]}]`, "actions"},
		{"actions 26", `[{"type":"actions","elements":` + many(26, `{"type":"button","text":{"type":"plain_text","text":"x"},"action_id":"a"}`) + `}]`, "26 actions"},
		{"element bad", `[{"type":"actions","elements":[1]}]`, "cannot unmarshal"},
		{"not a button", `[{"type":"actions","elements":[{"type":"static_select"}]}]`, "unexpected element"},
		{"no text", `[{"type":"actions","elements":[{"type":"button","action_id":"a"}]}]`, "needs text"},
		{"no action id", btn("Go", "", ""), "needs an action_id"},
		{"button 76", btn(long(76), "a", ""), "button text is 76"},
		{"action_id 256", btn("Go", long(256), ""), "action_id is 256"},
		{"value 2001", btn("Go", "a", `,"value":`+txt(long(2001))), "button value"},
		{"url 3001", btn("Go", "a", `,"url":`+txt(long(3001))), "button url"},
		{"confirm ok", btn("Go", "a", confirm("t", "x", "ok", "no")), ""},
		{"confirm title", btn("Go", "a", confirm(long(101), "x", "ok", "no")), "confirm title"},
		{"confirm text", btn("Go", "a", confirm("t", long(301), "ok", "no")), "confirm text"},
		{"confirm button", btn("Go", "a", confirm("t", "x", long(31), "no")), "confirm button"},
		{"deny button", btn("Go", "a", confirm("t", "x", "ok", long(31))), "deny button"},
		{"unknown block", `[{"type":"image"}]`, "unexpected block type"},
	} {
		err := ValidateLimits(json.RawMessage(c.blocks))
		switch {
		case c.err == "" && err != nil:
			t.Errorf("%s: %v", c.name, err)
		case c.err != "" && (err == nil || !strings.Contains(err.Error(), c.err)):
			t.Errorf("%s: got %v, want %q", c.name, err, c.err)
		}
	}
}
