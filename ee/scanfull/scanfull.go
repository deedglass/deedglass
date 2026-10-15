// Package scanfull renders the full scan report on the buyer's machine (S48, Pro; DL-039).
// It is a Go port of S37's scripts/report/render.mjs over the same template: the scan_report v1
// JSON is the only input, every sentence and number comes from its fields, and a field the scan
// didn't fill shows "not checked". Nothing here touches the network.
//
// Licensed under ee/LICENSE (FSL-1.1-ALv2).
package scanfull

import (
	"bytes"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/deedglass/deedglass/internal/embedgz"
)

// report.html is embedded gzip-compressed (report.tar.gz, make embed-gz; the image budget).
//
//go:embed report.tar.gz
var reportGz []byte

var template = embedgz.MustString(embedgz.NewFiles(reportGz), "report.html")

// FileName is the report's file name.
const FileName = "deedglass-full-report.html"

// Options change the page around the scan's facts, never the facts.
type Options struct {
	Sample bool
	// S37Footer keeps the S37 renderer's footer byte for byte (the cross-check with render.mjs).
	S37Footer bool
}

type obj = map[string]any

func get(o any, k string) any {
	if m, ok := o.(obj); ok {
		return m[k]
	}
	return nil
}
func str(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(x)
	}
	return ""
}
func num(v any) float64 { f, _ := v.(float64); return f }
func arr(v any) []any   { a, _ := v.([]any); return a }
func strs(v any) []string {
	var out []string
	for _, x := range arr(v) {
		out = append(out, str(x))
	}
	return out
}

func esc(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;").Replace(s)
}

// locale formats a number like JavaScript's toLocaleString('en-US') for the integers the scan holds.
func locale(f float64) string {
	neg := f < 0
	f = math.Abs(f)
	whole := strconv.FormatFloat(math.Trunc(f), 'f', 0, 64)
	var b strings.Builder
	for i, c := range whole {
		if i > 0 && (len(whole)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(c)
	}
	s := b.String()
	if frac := math.Round((f-math.Trunc(f))*1000) / 1000; frac > 0 {
		s += strings.TrimPrefix(strconv.FormatFloat(frac, 'f', -1, 64), "0")
	}
	if neg {
		s = "-" + s
	}
	return s
}

func plural(n float64, one, many string) string {
	if n == 1 {
		return locale(n) + " " + one
	}
	return locale(n) + " " + many
}

var reDigest = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

func short(d string) string {
	if reDigest.MatchString(d) {
		return d[:11] + "…" + d[len(d)-4:]
	}
	return d
}

var icon = map[string]string{
	"undoNo":     `<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M3 7v6h6"/><path d="M21 17a9 9 0 0 0-15-6.7L3 13"/><path d="M4 4l16 16"/></svg>`,
	"undoYes":    `<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M3 7v6h6"/><path d="M21 17a9 9 0 0 0-15-6.7L3 13"/></svg>`,
	"noApproval": `<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><circle cx="9" cy="7" r="4"/><path d="M3 21v-2a4 4 0 0 1 4-4h4a4 4 0 0 1 4 4v2"/><path d="M17 8l5 5M22 8l-5 5"/></svg>`,
	"routed":     `<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M20 6L9 17l-5-5"/></svg>`,
}

type chip struct{ cls, icon, word string }

func (c chip) html() string {
	return `<span class="chip ` + c.cls + `">` + icon[c.icon] + esc(c.word) + `</span>`
}

type row struct {
	name, can, worst string
	undo, today      *chip
	add              []string
}

type policy struct {
	n         int
	why, yaml string
}

type model struct {
	grade, points  string
	hasGrade       bool
	deductions     []any
	s              []string
	rows           []row
	pols           []policy
	looked, notLkd []string
	dir, date      string
	checked        string
}

var (
	reRulesAt = regexp.MustCompile(`(?m)^(rules|budgets):`)
	reCore    = regexp.MustCompile(`^\s*- id:|^\s+(match|when|decision|window|max_calls|per):`)
)

func build(scan obj) (model, error) {
	if str(scan["object"]) != "scan_report" || num(scan["version"]) != 1 {
		return model{}, errors.New("this isn't a scan_report v1; run deedglass scan --json and pass its output")
	}
	pg := scan["postgres"]
	tables := arr(get(pg, "tables"))
	score := scan["score"]
	unrouted := map[string]bool{}
	var unroutedOrder []string
	for _, d := range arr(get(score, "deductions")) {
		if str(get(d, "id")) == "unchecked_write_path" {
			s := str(get(d, "subject"))
			if !unrouted[s] {
				unroutedOrder = append(unroutedOrder, s)
			}
			unrouted[s] = true
		}
	}
	sum := scan["summary"]
	tname := func(t any) string { return str(get(t, "schema")) + "." + str(get(t, "table")) }
	can := func(t any) []string { return strs(get(t, "can")) }
	tools := arr(scan["tools"])
	writeTools := num(get(sum, "write_capable_tools"))
	var m model
	if pg != nil {
		line := "Your agent can change " + plural(float64(len(tables)), "table", "tables") + " in " + str(get(pg, "database"))
		if writeTools > 0 {
			line += " and use " + plural(writeTools, "write tool", "write tools")
		}
		m.s = append(m.s, line+".")
	} else if writeTools > 0 {
		m.s = append(m.s, "Your agent can use "+plural(writeTools, "write tool", "write tools")+".")
	} else {
		m.s = append(m.s, "The scan found nothing your agent can change.")
	}
	lose := map[string]bool{}
	for _, t := range tables {
		if slices.Contains(can(t), "TRUNCATE") || get(t, "undo_ready") != true {
			lose[tname(t)] = true
		}
	}
	if len(lose) > 0 {
		m.s = append(m.s, plural(float64(len(lose)), "table", "tables")+" can lose rows in a way undo can't put back.")
	}
	if len(unrouted) > 0 {
		m.s = append(m.s, plural(float64(len(unrouted)), "write path", "write paths")+" reach the database with no policy and no person in between.")
	}
	pols := arr(scan["policies"])
	if len(pols) > 3 {
		pols = pols[:3]
	}
	policyFor := func(name string) []string {
		var out []string
		for i, p := range pols {
			if strings.Contains(str(get(p, "why")), name) || str(get(p, "rule")) == "block-destructive-sql" {
				out = append(out, strconv.Itoa(i+1))
			}
		}
		return out
	}
	for _, t := range tables {
		name, c := tname(t), can(t)
		var trig []string
		for _, x := range arr(get(t, "triggers")) {
			if get(x, "enabled") == true {
				trig = append(trig, str(get(x, "name")))
			}
		}
		all := true
		for _, op := range []string{"INSERT", "UPDATE", "DELETE", "TRUNCATE"} {
			all = all && slices.Contains(c, op)
		}
		cans := "every row"
		if !all {
			var low []string
			for _, x := range c {
				low = append(low, strings.ToLower(x))
			}
			cans = strings.Join(low, ", ")
			if cans == "" {
				cans = "nothing"
			}
		}
		worst := "INSERT"
		switch {
		case slices.Contains(c, "TRUNCATE"):
			worst = "TRUNCATE, about " + locale(num(get(t, "est_rows"))) + " rows"
		case slices.Contains(c, "DELETE"):
			worst = "DELETE without a limit"
		case slices.Contains(c, "UPDATE"):
			worst = "UPDATE without a limit"
		}
		if len(trig) > 0 {
			worst += "; UPDATE fires " + strings.Join(trig, ", ")
		}
		undo := &chip{"undo-yes", "undoYes", "Undo ready"}
		if get(t, "undo_ready") != true || slices.Contains(c, "TRUNCATE") {
			undo = &chip{"undo-no", "undoNo", "Can't be undone"}
		}
		today := &chip{"routed", "routed", "Routed"}
		if unrouted[name] {
			today = &chip{"no-approval", "noApproval", "No approval"}
		}
		m.rows = append(m.rows, row{name: name, can: cans, worst: worst, undo: undo, today: today, add: policyFor(name)})
	}
	var toolRows []row
	var seen, servers []string
	for _, t := range tools {
		if str(get(t, "side_effect")) != "write" {
			continue
		}
		n := str(get(t, "server")) + "." + str(get(t, "tool"))
		if get(t, "routed") == true {
			seen = append(seen, n)
			if s := str(get(t, "server")); !slices.Contains(servers, s) {
				servers = append(servers, s)
			}
			continue
		}
		toolRows = append(toolRows, row{name: n, today: &chip{"no-approval", "noApproval", "Not routed"}, add: []string{"wrap"}})
	}
	if len(seen) > 0 {
		toolRows = append(toolRows, row{name: plural(float64(len(seen)), "tool", "tools") + " on " + strings.Join(servers, ", "),
			today: &chip{"routed", "routed", "Through Deedglass"}, add: []string{"–"}})
	}
	m.rows = append(toolRows, m.rows...)
	for i, p := range pols {
		y := str(get(p, "yaml"))
		if loc := reRulesAt.FindStringIndex(y); loc != nil {
			y = y[loc[0]:]
		}
		var core []string
		for _, l := range strings.Split(y, "\n") {
			if reCore.MatchString(l) {
				core = append(core, strings.TrimPrefix(l, "  "))
			}
		}
		m.pols = append(m.pols, policy{n: i + 1, why: str(get(p, "why")), yaml: strings.Join(core, "\n")})
	}
	if w := scan["walk"]; w != nil {
		s := plural(num(get(w, "files")), "file", "files") + " in " + str(scan["dir"])
		if get(w, "stopped") == true {
			s += " (stopped early)"
		}
		m.looked = append(m.looked, s)
	}
	if cf, ok := scan["configs"].([]any); ok {
		m.looked = append(m.looked, plural(float64(len(cf)), "MCP client config", "MCP client configs")+", "+plural(num(get(sum, "mcp_servers")), "server", "servers")+" in them")
	}
	if pg != nil {
		ver := strings.SplitN(str(get(pg, "server_version")), " ", 2)[0]
		m.looked = append(m.looked, "Postgres "+ver+" as "+str(get(pg, "role"))+": grants, triggers, row security and definer functions, "+
			plural(float64(len(tables))+num(get(pg, "tables_read_only")), "table", "tables"))
	}
	if pg == nil {
		m.notLkd = append(m.notLkd, "Your database: the scan had no connection string (--postgres-dsn-env).")
	}
	if len(tools) == 0 {
		m.notLkd = append(m.notLkd, "MCP tools: the configs it read list no servers, so no tool was graded.")
	} else {
		m.notLkd = append(m.notLkd, "Each MCP server's own code: tools are graded from a catalog.")
	}
	if scan["offline"] == true {
		m.notLkd = append(m.notLkd, "Anything online: it ran with --offline.")
	}
	m.notLkd = append(m.notLkd, "The rows in your tables: it never reads data.", "What your agent will do: it grades what the setup allows.")
	if g := get(score, "grade"); g != nil {
		m.grade, m.hasGrade = str(g), true
		m.points = str(get(score, "points"))
	}
	m.deductions = arr(get(score, "deductions"))
	m.dir = str(scan["dir"])
	if d := str(scan["scanned_at"]); len(d) >= 10 {
		m.date = d[:10]
	} else {
		m.date = d
	}
	m.checked = "config checked"
	if get(score, "database_checked") == true {
		m.checked = "config and database checked"
	}
	return m, nil
}

const nc = `<span class="nc">not checked</span>`

// Render fills the template for one scan_report v1 (the raw `deedglass scan --json` bytes).
func Render(raw []byte, o Options) (string, error) {
	var scan obj
	if err := json.Unmarshal(raw, &scan); err != nil {
		return "", fmt.Errorf("the scan JSON can't be read: %w", err)
	}
	m, err := build(scan)
	if err != nil {
		return "", err
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, raw); err != nil {
		return "", err
	}
	sum := sha256.Sum256(compact.Bytes())
	digest := "sha256:" + hex.EncodeToString(sum[:])
	title, label := "Full scan report", ""
	if o.Sample {
		title, label = "Full scan report · Sample, from the Deedglass demo", `<p class="label">Sample, from the Deedglass demo</p>`
	}
	orNC := func(s string) string {
		if s == "" {
			return "not checked"
		}
		return s
	}
	meta := []string{esc(orNC(m.dir)), esc(orNC(m.date)), "scan " + esc(short(digest)), esc(m.checked)}
	if m.hasGrade {
		meta = append(meta, "<b>Blast radius: "+esc(m.grade)+"</b>")
	} else {
		meta = append(meta, "Blast radius: not checked")
	}
	var b strings.Builder
	for _, x := range m.s {
		b.WriteString("<span>" + esc(x) + "</span>")
	}
	summary := b.String()
	grade := `<p class="g"><span class="letter">–</span><span class="pts">` + nc + `</span></p>`
	if m.hasGrade {
		b.Reset()
		b.WriteString(`<p class="g"><span class="letter">` + esc(m.grade) + `</span><span class="pts">` + esc(m.points) + ` of 100 · deductions</span></p><ul>`)
		for _, d := range m.deductions {
			b.WriteString(`<li><span>` + esc(str(get(d, "subject"))) + `</span><span>−` + esc(str(get(d, "points"))) + `</span></li>`)
		}
		b.WriteString(`</ul><p class="small">A means a small blast radius, not a safe agent.</p>`)
		grade = b.String()
	}
	or := func(s string) string {
		if s == "" {
			return nc
		}
		return esc(s)
	}
	chipOr := func(c *chip) string {
		if c == nil {
			return nc
		}
		return c.html()
	}
	b.Reset()
	for _, r := range m.rows {
		add := nc
		if len(r.add) > 0 {
			var e []string
			for _, a := range r.add {
				e = append(e, esc(a))
			}
			add = strings.Join(e, ", ")
		}
		b.WriteString(`<tr><td class="mono" translate="no">` + esc(r.name) + `</td><td>` + or(r.can) + `</td><td>` + or(r.worst) + `</td>` +
			`<td>` + chipOr(r.undo) + `</td><td>` + chipOr(r.today) + `</td>` + `<td class="mono" translate="no">` + add + `</td></tr>`)
	}
	rows := b.String()
	if len(m.rows) == 0 {
		rows = `<tr><td colspan="6">` + nc + `</td></tr>`
	}
	b.Reset()
	for _, p := range m.pols {
		b.WriteString(`<div class="policy"><p><b>` + strconv.Itoa(p.n) + `</b> ` + esc(p.why) + `</p><pre translate="no">` + esc(p.yaml) + `</pre></div>`)
	}
	pols := b.String()
	if len(m.pols) == 0 {
		pols = `<div class="policy">` + nc + `</div>`
	}
	li := func(xs []string) string {
		var s strings.Builder
		for _, x := range xs {
			s.WriteString("<li>" + esc(x) + "</li>")
		}
		return s.String()
	}
	looked := li(m.looked)
	if looked == "" {
		looked = "<li>" + nc + "</li>"
	}
	footer := `<span>Scan ` + esc(short(digest)) + ` · verify this file: deedglass verify --file ` + FileName + `</span><span>deedglass scan --json reproduces this page</span>`
	if o.S37Footer {
		footer = `<span>Scan ` + esc(short(digest)) + ` · sealing as a record: next release</span><span>deedglass scan --json reproduces this page</span>`
	}
	slots := map[string]string{"title": esc(title), "label": label, "meta": strings.Join(meta, " · "), "summary": summary, "grade": grade,
		"rows": rows, "policies": pols, "looked": looked, "notlooked": li(m.notLkd), "footer": footer}
	var bad error
	out := reSlot.ReplaceAllStringFunc(template(), func(s string) string {
		k := s[2 : len(s)-2]
		v, ok := slots[k]
		if !ok {
			bad = fmt.Errorf("report.html has an unknown slot {{%s}}", k)
		}
		return v
	})
	return out, bad
}

var reSlot = regexp.MustCompile(`\{\{(\w+)\}\}`)
