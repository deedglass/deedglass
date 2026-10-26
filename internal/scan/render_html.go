//go:build !(js && wasm)

package scan

import (
	"bytes"
	"fmt"
	"html/template"
	"strings"
)

// The report page's styles: DESIGN.md tokens, inline (the page loads nothing). Light by default,
// dark when the reader's system asks for it. No script, no font files, no external URL.
const reportCSS = `
:root{--bg:#F7F7F4;--surface:#FFFFFF;--surface-alt:#EDEEF2;--text:#0D0F14;--text-2:#4A4F5E;--text-3:#6B7080;
--hair:#E4E5EA;--accent:#2F5BEA;--code-bg:#0E1017;--code-hair:#1C1F2A;--code-text:#F5F6FA;
--high-t:#C8322B;--high-b:#FDECEA;--med-t:#9A5B00;--med-b:#FFF4E0;--low-t:#3F4453;--low-b:#EDEEF2;--ok-t:#0F766E;--ok-b:#E6F6F3;
--sans:Inter,-apple-system,BlinkMacSystemFont,"Segoe UI",Roboto,Helvetica,Arial,sans-serif;
--mono:"Geist Mono",ui-monospace,SFMono-Regular,Menlo,Consolas,monospace;color-scheme:light dark}
@media (prefers-color-scheme:dark){:root{--bg:#07080C;--surface:#0E1017;--surface-alt:#141722;--text:#F5F6FA;--text-2:#8B90A0;
--text-3:#7D8293;--hair:#1C1F2A;--accent:#6F8DFF;--high-t:#FF7A70;--high-b:#3A1512;--med-t:#F7B955;--med-b:#352407;
--low-t:#C9CCD6;--low-b:#1C1F2A;--ok-t:#34E3C9;--ok-b:#0F2A28}}
*{box-sizing:border-box}
html{-webkit-text-size-adjust:100%}
body{margin:0;background:var(--bg);color:var(--text);font:400 15px/1.55 var(--sans);font-feature-settings:"tnum" 0}
main{max-width:880px;margin:0 auto;padding:40px 16px 64px}
header p{margin:4px 0 0;color:var(--text-2);font-size:13px}
.mark{font:600 15px/1 var(--sans);letter-spacing:-0.04em}
h1{font:600 22px/28px var(--sans);letter-spacing:-0.015em;margin:28px 0 8px}
h1 .seg{display:inline-block}
.nowrap{white-space:nowrap}
.what{overflow-wrap:anywhere;min-width:12ch}
h1,h2,h3{text-wrap:balance}
h2{font:600 17px/24px var(--sans);margin:40px 0 12px}
.card{background:var(--surface);border:1px solid var(--hair);border-radius:14px;padding:20px}
.score{display:grid;grid-template-columns:auto 1fr;gap:4px 20px;align-items:center}
.grade{grid-row:span 2;width:72px;height:72px;border-radius:14px;border:1px solid var(--hair);background:var(--surface-alt);
display:flex;align-items:center;justify-content:center;font:650 40px/1 var(--sans);letter-spacing:-0.03em}
.line{font-weight:600}
.small{color:var(--text-2);font-size:13px;margin:0}
table{border-collapse:collapse;width:100%;font-size:13px}
th{font-weight:600;color:var(--text-2);text-align:left;font-size:12px;letter-spacing:.02em}
th,td{padding:8px 12px;border-bottom:1px solid var(--hair);vertical-align:top}
td.num,th.num{text-align:right;font-variant-numeric:tabular-nums;white-space:nowrap}
.frame{overflow-x:auto;border:1px solid var(--hair);border-radius:10px;background:var(--surface)}
.frame:focus-visible{outline:2px solid var(--accent);outline-offset:2px}
.frame table.wide{min-width:720px}
.nowrap code{overflow-wrap:normal}
.frame table td:first-child,.frame table th:first-child{position:sticky;left:0;background:var(--surface)}
code,.id{font-family:var(--mono);font-size:12.5px;overflow-wrap:anywhere}
ul{padding-left:20px;margin:8px 0}
li{margin:4px 0}
.finding{margin:12px 0}
.finding h3{font:600 15px/22px var(--sans);margin:0 0 6px;display:flex;gap:10px;align-items:baseline;flex-wrap:wrap}
.finding p{margin:6px 0;color:var(--text)}
.chip{display:inline-flex;align-items:center;gap:6px;height:22px;padding:0 8px;border-radius:6px;font:600 12px/1 var(--sans);
letter-spacing:.04em;text-transform:uppercase;white-space:nowrap}
.high{color:var(--high-t);background:var(--high-b)}.medium{color:var(--med-t);background:var(--med-b)}
.low,.info{color:var(--low-t);background:var(--low-b)}
.muted{color:var(--text-2)}
.fix{color:var(--text-2)}
pre{background:var(--code-bg);color:var(--code-text);border:1px solid var(--code-hair);border-radius:10px;padding:16px;
overflow-x:auto;font:400 12.5px/1.6 var(--mono);margin:8px 0 16px}
a{color:var(--accent)}
a:hover{text-decoration-thickness:2px}
pre:focus-visible{outline:2px solid var(--accent);outline-offset:2px}
a:focus-visible{outline:2px solid var(--accent);outline-offset:2px;border-radius:2px}
footer{margin-top:48px;padding-top:16px;border-top:1px solid var(--hair);color:var(--text-2);font-size:13px}
.ok{color:var(--ok-t)}
@media (max-width:600px){main{padding-top:24px}td.nowrap:not(:first-child){white-space:normal}.score{grid-template-columns:1fr}.grade{grid-row:auto}}
@media print{body{background:#fff}.card,.frame{break-inside:avoid}}
`

var reportTmpl = template.Must(template.New("report").Funcs(template.FuncMap{
	"scoreLine": ScoreLine, "segs": scoreSegments, "dedLabel": DedLabel, "dedText": DedText, "cells": tableCells, "join": strings.Join,
	"minus": func(n int) string {
		if n == 0 {
			return "0"
		}
		return fmt.Sprintf("%s%d", Minus, n)
	},
	"plural": plural, "word": numberWord, "counts": settingsCounts,
	"limit": func(ts []Table) []Table {
		if len(ts) > 25 {
			return ts[:25]
		}
		return ts
	},
}).Parse(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="color-scheme" content="light dark">
<meta name="theme-color" content="#F7F7F4" media="(prefers-color-scheme: light)">
<meta name="theme-color" content="#07080C" media="(prefers-color-scheme: dark)">
<meta name="referrer" content="no-referrer">
<meta http-equiv="Content-Security-Policy" content="default-src 'none'; style-src 'unsafe-inline'; img-src 'none'; base-uri 'none'; form-action 'none'">
<meta name="description" content="What this agent setup can change, graded A to F. Read-only, local, nothing sent.">
<title>Blast radius {{.Score.Grade}} · deedglass scan</title>
<style>{{.CSS}}</style>
</head>
<body>
<main>
<header>
<div class="mark">deedglass scan</div>
<p>{{.R.Dir}} · {{.R.ScannedAt}} · read-only · nothing left this machine</p>
</header>

<h1>{{range $i, $s := segs .Score}}{{if $i}} {{end}}<span class="seg">{{$s}}</span>{{end}}</h1>
<section class="card score" aria-label="Agent Blast-Radius Score">
<div class="grade" role="img" aria-label="Grade {{.Score.Grade}}">{{.Score.Grade}}</div>
<div class="line">{{.Score.Points}} points · formula v{{.Score.FormulaVersion}}</div>
<p class="small">{{.SmallPrint}}</p>
</section>
<div class="frame" style="margin-top:12px" tabindex="0" role="region" aria-label="Deductions">
<table>
<thead><tr><th class="num" scope="col">Points</th><th scope="col">Deduction</th><th scope="col">What</th></tr></thead>
<tbody>
{{range .Score.Deductions}}<tr><td class="num">{{minus .Points}}</td><td class="nowrap">{{dedLabel .ID}}</td><td class="what">{{dedText .}}</td></tr>
{{end}}<tr><td class="num"><strong>{{.Score.Points}}</strong></td><td>points</td><td class="muted">100 minus the deductions</td></tr>
</tbody>
</table>
</div>

<h2>Found</h2>
<ul>
<li>{{.R.Summary.Configs}} MCP client {{plural .R.Summary.Configs "config" "configs"}} · {{.R.Summary.MCPServers}} {{plural .R.Summary.MCPServers "server" "servers"}} · {{.R.Summary.WriteCapableTools}} {{plural .R.Summary.WriteCapableTools "tool" "tools"}} that can write</li>
{{range .R.Configs}}<li><code translate="no">{{.Path}}</code> <span class="muted">({{.Client}})</span>{{range .Servers}} · {{.Name}}{{if .Package}} <code translate="no">{{.Package}}</code>{{end}}{{if .Routed}} <span class="ok">routed through Deedglass</span>{{end}}{{end}}</li>
{{end}}{{range .R.Dependencies}}<li><code translate="no">{{.File}}</code>: {{.Name}} <code translate="no">{{.Spec}}</code></li>
{{end}}{{range .R.EnvNames}}<li><code translate="no">{{.File}}</code>: database URL variables (names only): <code translate="no">{{join .Names ", "}}</code></li>
{{end}}{{range .R.ClaudeSettings}}<li><code translate="no">{{.Path}}</code>: Claude Code settings <span class="muted">({{.Scope}})</span> · {{counts .}}</li>
{{end}}</ul>

{{if .R.Findings}}<h2>Findings</h2>
{{range .R.Findings}}<article class="card finding">
<h3><span class="chip {{.Severity}}">{{.Severity}}</span> {{.Title}}{{if .Paths}} <span class="muted">· {{join .Paths ", "}}</span>{{end}}</h3>
<p>{{.Detail}}</p>
{{if .Checked}}<p class="muted">{{.Checked}}</p>{{end}}
{{range .Fix}}<p class="fix">Fix: {{.}}</p>{{end}}
{{range .Sources}}<p class="small">Source: <code translate="no">{{.}}</code></p>{{end}}
</article>
{{end}}{{end}}

<h2>Postgres</h2>
{{if .R.Postgres}}{{with .R.Postgres}}<p class="muted">{{.Database}} · role {{.Role}} · wal_level {{.WALLevel}} · read-only session · {{.TablesReadOnly}} {{plural .TablesReadOnly "table" "tables"}} read-only</p>
<div class="frame" tabindex="0" role="region" aria-label="Tables the role can change">
<table class="wide">
<thead><tr><th scope="col">Table</th><th scope="col">Can change</th><th class="num" scope="col">Est. rows</th><th scope="col">Cascades to</th><th scope="col">Triggers</th><th scope="col">Undo-ready</th><th scope="col">Proven-ready</th></tr></thead>
<tbody>
{{range limit .Tables}}{{$c := cells .}}<tr><td class="nowrap"><code translate="no">{{index $c 0}}</code></td><td>{{index $c 1}}</td><td class="num">{{index $c 2}}</td><td>{{index $c 3}}</td><td>{{index $c 4}}</td><td>{{index $c 5}}</td><td>{{index $c 6}}</td></tr>
{{end}}</tbody>
</table>
</div>{{end}}
{{else if .R.PostgresError}}<p>Couldn't connect with the URL in {{.R.PostgresError.EnvVar}} ({{.R.PostgresError.SQLState}} {{.R.PostgresError.Class}}). The file scan was written. Run again when the database is reachable.</p>
{{else}}<p class="muted">Postgres not checked. Add <code translate="no">--postgres-dsn-env VAR</code> to rank the tables your agent's role can change.</p>{{end}}

{{if .R.Policies}}<h2>{{word (len .R.Policies)}} policies to add first</h2>
<ol>
{{range .R.Policies}}<li><strong>{{.Rule}}</strong> <span class="muted">from {{.Starter}}</span>: {{.Why}}</li>
{{end}}</ol>
{{range .R.Policies}}<pre tabindex="0" aria-label="{{.Rule}} policy">{{.YAML}}</pre>
{{end}}{{end}}

<h2>What it doesn't do</h2>
<p>It reads your configuration and catalog. It doesn't run your agent, test your tools or connect anywhere you didn't name.</p>

<footer>{{.FooterHead}}<a href="https://deedglass.com/score" rel="noreferrer">deedglass.com/score</a>{{.FooterTail}}</footer>
</main>
</body>
</html>
`))

// RenderHTML is deedglass-scan.html: self-contained, no script, no resource from another origin.
func RenderHTML(r *Report) (string, error) {
	var b bytes.Buffer
	head, tail, _ := strings.Cut(FooterLine, "deedglass.com/score")
	err := reportTmpl.Execute(&b, map[string]any{"R": r, "Score": r.Score, "SmallPrint": SmallPrint, "FooterHead": head, "FooterTail": tail,
		"CSS": template.CSS(reportCSS)})
	return b.String(), err
}

// scoreSegments splits the score line at its separators so the heading wraps between its parts,
// each separator ending the part before it. Joined with spaces, the parts read as ScoreLine.
func scoreSegments(sc Score) []string {
	parts := strings.Split(ScoreLine(sc), " · ")
	for i := range parts[:len(parts)-1] {
		parts[i] += " ·"
	}
	return parts
}
