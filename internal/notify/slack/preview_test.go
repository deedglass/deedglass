package slack

import (
	"encoding/json"
	"flag"
	"fmt"
	"html"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

// go test ./internal/notify/slack -run TestCardPreview -preview=DIR writes a Slack-like HTML mock
// of every card state and the deny modal (light and dark) to DIR, for screenshots in the design
// loop. The mock is for the session's eyes only: Slack's own renderer is the truth.
var previewDir = flag.String("preview", "", "write HTML previews of the card goldens to this directory")

var previewStates = []string{"pending", "four-eyes", "approved", "sealed-verified", "sealed-mismatch", "denied", "expired", "cancelled", "injection", "long-args", "unknown-tool"}

func TestCardPreview(t *testing.T) {
	if *previewDir == "" {
		t.Skip("set -preview=DIR to write the HTML mocks")
	}
	if err := os.MkdirAll(*previewDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cases := cardCases()
	var index strings.Builder
	for _, theme := range []string{"light", "dark"} {
		for _, name := range previewStates {
			raw, _, err := Card(cases[name])
			if err != nil {
				t.Fatal(err)
			}
			var blocks []map[string]any
			if err := json.Unmarshal(raw, &blocks); err != nil {
				t.Fatal(err)
			}
			body := `<main class="channel" aria-label="#deedglass-approvals"><header class="topbar"><h1># deedglass-approvals</h1></header>` +
				`<article class="msg"><div class="avatar" aria-hidden="true">d</div><div class="msgbody"><div class="meta"><span class="who">Deedglass</span><span class="app">App</span><time class="ts">4:14 AM</time></div>` +
				renderBlocks(blocks) + `</div></article></main>`
			writePreview(t, name, theme, body)
			fmt.Fprintf(&index, `<li><a href="card-%s-%s.html">%s · %s</a></li>`, name, theme, name, theme)
		}
		writePreview(t, "deny-modal", theme, renderModal(DenyModal(cardTestApproval, cardTestContext)))
		fmt.Fprintf(&index, `<li><a href="card-deny-modal-%s.html">deny-modal · %s</a></li>`, theme, theme)
	}
	writeFile(t, "index.html", page("light", "Card previews", `<main class="channel"><ul>`+index.String()+`</ul></main>`))
}

func writePreview(t *testing.T, name, theme, body string) {
	writeFile(t, "card-"+name+"-"+theme+".html", page(theme, "Card "+name, body))
}

func writeFile(t *testing.T, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(*previewDir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func page(theme, title, body string) string {
	return `<!doctype html><html lang="en" data-theme="` + theme + `"><head><meta charset="utf-8">` +
		`<meta name="viewport" content="width=device-width, initial-scale=1"><meta name="color-scheme" content="` + theme + `">` +
		`<title>` + html.EscapeString(title) + `</title><style>` + previewCSS + `</style></head><body>` + body + `</body></html>`
}

const previewCSS = `
:root{--bg:#fff;--text:#1d1c1d;--muted:#616061;--hair:#dddddd;--code-bg:#f8f8f8;--code-border:#e2e2e2;--inline-code:#e01e5a;
--btn-bg:#fff;--btn-text:#1d1c1d;--btn-border:#bbbabb;--primary:#007a5a;--danger:#e01e5a;--link:#1264a3;--overlay:rgba(29,28,29,.5);--modal:#fff;--focus:#1264a3}
[data-theme=dark]{--bg:#1a1d21;--text:#d1d2d3;--muted:#ababad;--hair:#35373b;--code-bg:#222529;--code-border:#3a3d41;--inline-code:#e8912d;
--btn-bg:#1a1d21;--btn-text:#d1d2d3;--btn-border:#565856;--primary:#007a5a;--danger:#e01e5a;--link:#1d9bd1;--overlay:rgba(0,0,0,.6);--modal:#1a1d21;--focus:#1d9bd1}
*{box-sizing:border-box}
html,body{margin:0;background:var(--bg);color:var(--text)}
body{font:15px/1.46668 Lato,-apple-system,BlinkMacSystemFont,"Segoe UI",Helvetica,Arial,sans-serif;-webkit-font-smoothing:antialiased}
.channel{max-width:none;padding:0 0 24px}
.topbar{border-bottom:1px solid var(--hair);padding:10px 20px}
.topbar h1{font-size:18px;font-weight:900;margin:0}
.msg{display:flex;gap:8px;padding:8px 20px}
.avatar{flex:0 0 36px;height:36px;border-radius:6px;background:#0b0b0f;color:#fff;display:grid;place-items:center;font-weight:900;font-size:18px}
.msgbody{min-width:0;flex:1}
.meta{display:flex;align-items:baseline;gap:6px}
.who{font-weight:900}
.app{font-size:10px;font-weight:700;padding:0 3px;border-radius:2px;background:var(--hair);color:var(--muted);text-transform:uppercase}
.ts{font-size:12px;color:var(--muted)}
.bk{margin-top:4px;max-width:600px;overflow-wrap:anywhere}
.bk-header{font-size:18px;font-weight:900;line-height:1.33;margin-top:4px;text-wrap:balance}
.bk-fields{display:grid;grid-template-columns:1fr 1fr;gap:8px 16px;margin-top:8px}
.bk-context{font-size:12px;color:var(--muted);margin-top:6px}
.bk-actions{display:flex;flex-wrap:wrap;gap:8px;margin-top:12px}
pre{font:12px/1.5 Monaco,Menlo,Consolas,"Courier New",monospace;background:var(--code-bg);border:1px solid var(--code-border);border-radius:4px;padding:8px;margin:4px 0 0;white-space:pre-wrap;overflow-wrap:anywhere}
code{font:12px Monaco,Menlo,Consolas,"Courier New",monospace;color:var(--inline-code);background:var(--code-bg);border:1px solid var(--code-border);border-radius:3px;padding:1px 3px}
.btn{font:700 15px/1 Lato,-apple-system,BlinkMacSystemFont,"Segoe UI",Helvetica,Arial,sans-serif;height:36px;padding:0 12px;border-radius:4px;border:1px solid var(--btn-border);background:var(--btn-bg);color:var(--btn-text);cursor:pointer;display:inline-flex;align-items:center;text-decoration:none;touch-action:manipulation}
.btn:focus-visible{outline:2px solid var(--focus);outline-offset:2px}
.btn.primary{background:var(--primary);border-color:var(--primary);color:#fff}
.btn.danger{background:var(--danger);border-color:var(--danger);color:#fff}
.btn .ext{font-size:12px;margin-left:4px}
.overlay{position:fixed;inset:0;background:var(--overlay);display:grid;place-items:center;padding:16px}
.modal{background:var(--modal);border-radius:8px;width:min(520px,100%);box-shadow:0 18px 48px rgba(0,0,0,.35);overscroll-behavior:contain}
.modal header{display:flex;justify-content:space-between;align-items:center;padding:20px 24px 12px}
.modal h2{font-size:22px;font-weight:900;margin:0}
.modal .x{background:none;border:0;color:var(--muted);font-size:20px;cursor:pointer;width:36px;height:36px;border-radius:4px}
.modal .x:focus-visible{outline:2px solid var(--focus)}
.modal .content{padding:4px 24px 20px}
.modal label{display:block;font-weight:700;margin-bottom:8px}
.modal textarea{width:100%;min-height:88px;font:15px/1.46668 Lato,-apple-system,BlinkMacSystemFont,"Segoe UI",Helvetica,Arial,sans-serif;color:var(--text);background:var(--bg);border:1px solid var(--btn-border);border-radius:4px;padding:8px;resize:vertical}
.modal textarea:focus-visible{outline:2px solid var(--focus);outline-offset:0}
.modal footer{display:flex;justify-content:flex-end;gap:12px;padding:16px 24px;border-top:1px solid var(--hair)}
ul{padding:20px 40px}a{color:var(--link)}
`

func renderBlocks(blocks []map[string]any) string {
	var b strings.Builder
	for _, bl := range blocks {
		switch bl["type"] {
		case "header":
			b.WriteString(`<h2 class="bk bk-header">` + plainHTML(bl["text"]) + `</h2>`)
		case "section":
			b.WriteString(`<div class="bk bk-section">`)
			if t, ok := bl["text"].(map[string]any); ok {
				b.WriteString(`<div>` + mrkdwnHTML(t["text"].(string)) + `</div>`)
			}
			if fs, ok := bl["fields"].([]any); ok {
				b.WriteString(`<div class="bk-fields">`)
				for _, f := range fs {
					b.WriteString(`<div>` + mrkdwnHTML(f.(map[string]any)["text"].(string)) + `</div>`)
				}
				b.WriteString(`</div>`)
			}
			b.WriteString(`</div>`)
		case "context":
			b.WriteString(`<div class="bk bk-context">`)
			for _, e := range bl["elements"].([]any) {
				b.WriteString(mrkdwnHTML(e.(map[string]any)["text"].(string)))
			}
			b.WriteString(`</div>`)
		case "actions":
			b.WriteString(`<div class="bk bk-actions">`)
			for _, e := range bl["elements"].([]any) {
				el := e.(map[string]any)
				cls := "btn"
				if s, ok := el["style"].(string); ok {
					cls += " " + s
				}
				if u, ok := el["url"].(string); ok {
					b.WriteString(`<a class="` + cls + `" href="` + html.EscapeString(u) + `" target="_blank" rel="noopener">` + plainHTML(el["text"]) + `<span class="ext" aria-hidden="true">↗</span></a>`)
				} else {
					b.WriteString(`<button type="button" class="` + cls + `">` + plainHTML(el["text"]) + `</button>`)
				}
			}
			b.WriteString(`</div>`)
		}
	}
	return b.String()
}

func renderModal(raw json.RawMessage) string {
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	in := m["blocks"].([]any)[0].(map[string]any)
	el := in["element"].(map[string]any)
	return `<main class="overlay"><div class="modal" role="dialog" aria-modal="true" aria-labelledby="mt">` +
		`<header><h2 id="mt">` + plainHTML(m["title"]) + `</h2><button type="button" class="x" aria-label="Close">×</button></header>` +
		`<div class="content"><label for="reason">` + plainHTML(in["label"]) + `</label>` +
		`<textarea id="reason" name="reason" maxlength="` + strconv.Itoa(int(el["max_length"].(float64))) + `" required autocomplete="off"></textarea></div>` +
		`<footer><button type="button" class="btn">` + plainHTML(m["close"]) + `</button><button type="submit" class="btn primary">` + plainHTML(m["submit"]) + `</button></footer></div></main>`
}

// slackDecode undoes Slack's three entities, as Slack does when it displays text.
var slackDecode = strings.NewReplacer("&lt;", "<", "&gt;", ">", "&amp;", "&")

func plainHTML(t any) string {
	return html.EscapeString(slackDecode.Replace(t.(map[string]any)["text"].(string)))
}

var (
	fenceRe  = regexp.MustCompile("(?s)```\n?(.*?)\n?```")
	dateRe   = regexp.MustCompile(`<!date\^(\d+)\^[^|]*\|[^>]*>`)
	boldRe   = regexp.MustCompile(`\*([^*\n]+)\*`)
	italRe   = regexp.MustCompile(`(^|\s)_([^_\n]+)_`)
	inlineRe = regexp.MustCompile("`([^`\n]+)`")
)

// mrkdwnHTML is a small Slack mrkdwn renderer: fences, inline code, bold, italic, date tokens and
// line breaks. Anything else stays text.
func mrkdwnHTML(s string) string {
	var b strings.Builder
	last := 0
	for _, m := range fenceRe.FindAllStringSubmatchIndex(s, -1) {
		b.WriteString(inlineHTML(s[last:m[0]]))
		b.WriteString(`<pre>` + html.EscapeString(slackDecode.Replace(s[m[2]:m[3]])) + `</pre>`)
		last = m[1]
	}
	b.WriteString(inlineHTML(s[last:]))
	return b.String()
}

func inlineHTML(s string) string {
	var dates []string
	s = dateRe.ReplaceAllStringFunc(s, func(tok string) string {
		unix, _ := strconv.ParseInt(dateRe.FindStringSubmatch(tok)[1], 10, 64)
		t := time.Unix(unix, 0).UTC()
		dates = append(dates, `<time datetime="`+t.Format(time.RFC3339)+`">`+t.Format("Jan 2")+` at `+t.Format("3:04 PM")+`</time>`)
		return "\x00" + strconv.Itoa(len(dates)-1) + "\x00"
	})
	var codes []string
	s = inlineRe.ReplaceAllStringFunc(s, func(tok string) string {
		codes = append(codes, `<code>`+html.EscapeString(slackDecode.Replace(tok[1:len(tok)-1]))+`</code>`)
		return "\x01" + strconv.Itoa(len(codes)-1) + "\x01"
	})
	s = html.EscapeString(slackDecode.Replace(s))
	s = boldRe.ReplaceAllString(s, `<strong>$1</strong>`)
	s = italRe.ReplaceAllString(s, `$1<em>$2</em>`)
	s = strings.ReplaceAll(s, "\n", "<br>")
	for i, d := range dates {
		s = strings.Replace(s, "\x00"+strconv.Itoa(i)+"\x00", d, 1)
	}
	for i, c := range codes {
		s = strings.Replace(s, "\x01"+strconv.Itoa(i)+"\x01", c, 1)
	}
	return s
}
