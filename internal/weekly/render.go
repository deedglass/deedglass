package weekly

import (
	"bytes"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"html/template"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/deedglass/deedglass/internal/caught"
	"github.com/deedglass/deedglass/internal/embedgz"
)

// Brand is an agency's name on a client report (S52 §3.2, Pro): parsed and checked by ee/agency.
// The free single-org report has none.
type Brand struct {
	Name        string // "Northwind Automation"
	ContactLine string // "ops@northwind.example · +1 555 0100"
	LogoMIME    string // image/png or image/svg+xml; "" for no logo
	Logo        []byte // already checked (size, sanitised SVG)
}

// Options shape a rendering.
type Options struct {
	// Brand puts "Prepared by <agency> with Deedglass" on the report. nil: no brand lines.
	Brand *Brand
	// ProUndoLine adds "Undo the whole session in one step (Pro)" beside a caught session's free
	// path. Set only when the server has no undo_session (S50 §3.7); never another upsell.
	ProUndoLine bool
	// ConsoleURL is the user's own server's Console ("https://deedglass.acme.internal/app"); "" omits it.
	ConsoleURL string
}

// --- shared wording (one place: the CLI, Markdown, HTML and the email read these) ---

// N writes n with thousands separators: 5612 → "5,612".
func N(n int64) string {
	if n < 0 {
		return "-" + N(-n)
	}
	s := strconv.FormatInt(n, 10)
	var b strings.Builder
	for i, c := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(c)
	}
	return b.String()
}

// CountOf is "1 action" or "412 actions".
func CountOf(n int64, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return N(n) + " " + many
}

// When is "this week", "in October 2026" for a month.
func (p Period) When() string {
	if p.Kind == KindMonth {
		return "in " + p.Label
	}
	return "this week"
}

// Headline is "What your agents did this week · acme · 13 to 19 Oct".
func (s Summary) Headline() string {
	org := orgWord(s.Org)
	if s.Period.Kind == KindMonth {
		return "What your agents did in " + s.Period.Label + " · " + org
	}
	return "What your agents did this week · " + org + " · " + s.Period.ShortLabel()
}

func orgWord(o Org) string {
	for _, v := range []string{o.Slug, o.Name, o.ID} {
		if v = strings.TrimSpace(oneLine(v)); v != "" {
			return v
		}
	}
	return "your org"
}

// NumbersLine is "412 actions · 371 allowed · 22 waited for a person · 19 blocked by rules".
func (s Summary) NumbersLine() string {
	a := s.Actions
	if a.Total == 0 {
		return "No agent actions " + s.Period.When() + "."
	}
	return CountOf(a.Total, "action", "actions") + " · " + N(a.Allowed) + " allowed · " + N(a.Waited) + " waited for a person · " +
		N(a.Blocked) + " blocked by rules"
}

// CheckLine is "Check: 388 match · 0 don't match · 24 can't check" (and "· 12 not checked" when any).
func (s Summary) CheckLine() string {
	c := s.Check
	l := "Check: " + N(c.Matches) + " match · " + N(c.DoesntMatch) + " don't match · " + N(c.CantCheck) + " can't check"
	if c.NotChecked > 0 {
		l += " · " + N(c.NotChecked) + " not checked"
	}
	return l
}

// CaughtHead is "Deedglass caught 1 thing" or "Deedglass caught 3 things"; "" when nothing.
func (s Summary) CaughtHead() string {
	if s.CaughtTotal == 0 {
		return ""
	}
	return "Deedglass caught " + CountOf(int64(s.CaughtTotal), "thing", "things")
}

// ItemLine is a caught item's sentence without the prefix: "the L2 check of … doesn't match …".
func ItemLine(it caught.Item) string {
	return strings.TrimPrefix(oneLine(it.Line), caught.Prefix)
}

// ItemSentence is ItemLine starting with a capital, for a list of items.
func ItemSentence(it caught.Item) string {
	l := ItemLine(it)
	if l == "" {
		return l
	}
	return strings.ToUpper(l[:1]) + l[1:]
}

// FreeLine is the item's free way back: "Undo it yourself, free: deedglass undo preview act_…".
func FreeLine(it caught.Item) string {
	lead, cmd := freeParts(it)
	if cmd == "" {
		return ""
	}
	return lead + " " + cmd
}

func freeParts(it caught.Item) (lead, cmd string) {
	if it.UndoFreePath == "" {
		return "", ""
	}
	if !it.UndoPossible {
		return "Look at it:", oneLine(it.UndoFreePath)
	}
	return "Undo it yourself, free:", oneLine(it.UndoFreePath)
}

// ProLine is the one Pro line, on a caught session only, when the server can't undo sessions.
const ProLine = "Undo the whole session in one step (Pro)"

// ShowsPro says whether an item carries the Pro line under o.
func ShowsPro(it caught.Item, o Options) bool {
	return o.ProUndoLine && it.Session != "" && it.UndoPossible
}

// ChainLine is the record chain's sentence.
func (s Summary) ChainLine() string {
	switch s.Chain.Status {
	case ChainIntact:
		return "Records chain intact."
	case ChainBroken:
		if s.Chain.FirstBroken != "" {
			return "Records chain broken at " + oneLine(s.Chain.FirstBroken) + ". Run deedglass ledger check."
		}
		return "Records chain broken. Run deedglass ledger check."
	case ChainNoRecords:
		return "No records yet, so nothing to check."
	}
	return "Records chain not checked. Run deedglass ledger check."
}

// Subject is the Monday email's subject: "What your agents did this week · 412 actions · 1 mistake
// caught" (or "· nothing caught").
func (s Summary) Subject() string {
	tail := "nothing caught"
	if s.CaughtTotal > 0 {
		tail = CountOf(int64(s.CaughtTotal), "mistake", "mistakes") + " caught"
	}
	return "What your agents did this week · " + CountOf(s.Actions.Total, "action", "actions") + " · " + tail
}

// ApprovalsLine is "6 asked · 5 approved · 1 denied · 0 expired · decided in 4 minutes (median)".
func (s Summary) ApprovalsLine() string {
	a := s.Approvals
	if a.Requested == 0 {
		return "No approvals asked " + s.Period.When() + "."
	}
	l := N(a.Requested) + " asked · " + N(a.Approved) + " approved · " + N(a.Denied) + " denied · " + N(a.Expired) + " expired"
	if a.Pending > 0 {
		l += " · " + N(a.Pending) + " still waiting"
	}
	if a.MedianSeconds != nil {
		l += " · decided in " + humanSeconds(*a.MedianSeconds) + " (median)"
	}
	return l
}

func humanSeconds(sec int64) string {
	switch {
	case sec < 60:
		return CountOf(sec, "second", "seconds")
	case sec < 3600:
		return CountOf((sec+30)/60, "minute", "minutes")
	case sec < 48*3600:
		return CountOf((sec+1800)/3600, "hour", "hours")
	}
	return CountOf((sec+43200)/86400, "day", "days")
}

// UndoStopLine is "2 undos (1 action, 1 watched session) · Stop used 0 times".
func (s Summary) UndoStopLine() string {
	u := s.Undos.Actions + s.Undos.Sessions
	l := CountOf(u, "undo", "undos")
	if u > 0 {
		l += " (" + CountOf(s.Undos.Actions, "action", "actions") + ", " + CountOf(s.Undos.Sessions, "watched session", "watched sessions") + ")"
	}
	stops := s.Stops.Agent + s.Stops.All
	return l + " · Stop used " + CountOf(stops, "time", "times")
}

// SessionsLine is "3 watched sessions · 1 with something caught".
func (s Summary) SessionsLine() string {
	if s.Sessions.Watched == 0 {
		return "No watched sessions " + s.Period.When() + "."
	}
	return CountOf(s.Sessions.Watched, "watched session", "watched sessions") + " · " + N(s.Sessions.Caught) + " with something caught"
}

// PackLine names the period's security review pack, or "".
func (s Summary) PackLine() string {
	if s.SecurityPack == nil {
		return ""
	}
	l := "Security review pack built on " + s.SecurityPack.BuiltAt.UTC().Format("2 Jan 2006")
	if s.SecurityPack.Folder != "" {
		l += ": " + oneLine(s.SecurityPack.Folder)
	}
	return l + ". Check it with deedglass security-pack verify <folder>."
}

// FooterLine says where the numbers come from.
func (s Summary) FooterLine() string {
	return "Built by Deedglass from " + orgWord(s.Org) + "'s own records on " + s.GeneratedAt.UTC().Format("2 Jan 2006 15:04") +
		" UTC. Numbers, ids and names only: no arguments, results or row values."
}

// --- JSON ---

// JSON is the summary as indented JSON with a final newline.
func JSON(s Summary) ([]byte, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(s); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

// --- Markdown ---

// Markdown is the report as Markdown.
func Markdown(s Summary, o Options) string {
	var b strings.Builder
	w := func(lines ...string) {
		for _, l := range lines {
			b.WriteString(l)
			b.WriteByte('\n')
		}
	}
	title := "What your agents did · " + mdText(orgName(s.Org)) + " · " + s.Period.Label
	w("# "+title, "")
	if o.Brand != nil {
		w("Prepared by " + mdText(o.Brand.Name) + " with Deedglass.  ")
		if o.Brand.ContactLine != "" {
			w(mdText(o.Brand.ContactLine) + "  ")
		}
		w("")
	}
	w(mdText(s.NumbersLine())+"  ", mdText(s.CheckLine()), "")
	if h := s.CaughtHead(); h != "" {
		w("## "+h, "")
		for _, it := range s.Caught {
			w("- " + mdText(ItemSentence(it)))
			if lead, cmd := freeParts(it); cmd != "" {
				w("  " + lead + " `" + strings.ReplaceAll(cmd, "`", "") + "`  ")
			}
			if ShowsPro(it, o) {
				w("  " + ProLine)
			}
		}
		if more := s.CaughtTotal - len(s.Caught); more > 0 {
			w("- And " + CountOf(int64(more), "more", "more") + ": deedglass actions list --verdict MISMATCH")
		}
		w("")
	} else {
		w("Nothing caught "+s.Period.When()+".", "")
	}
	if len(s.Actions.ByAgent) > 0 {
		w("## Actions by agent", "", "| Agent | Actions |", "|---|---:|")
		for _, c := range s.Actions.ByAgent {
			w("| " + mdCell(c.Name) + " | " + N(c.Count) + " |")
		}
		w("")
	}
	if len(s.Actions.ByTool) > 0 {
		w("## Actions by tool", "", "| Tool | Actions |", "|---|---:|")
		for _, c := range s.Actions.ByTool {
			w("| " + mdCell(c.Name) + " | " + N(c.Count) + " |")
		}
		w("")
	}
	if len(s.Denials) > 0 {
		w("## Blocked most by", "", "| Rule | Reason | Times |", "|---|---|---:|")
		for _, d := range s.Denials {
			w("| " + mdCell(d.Rule) + " | " + mdCell(d.Reason) + " | " + N(d.Count) + " |")
		}
		w("")
	}
	w("## Approvals", "", mdText(s.ApprovalsLine()), "")
	w("## Undo and Stop", "", mdText(s.UndoStopLine()), "")
	w("## Watched sessions", "", mdText(s.SessionsLine()), "")
	w("## Records", "", mdText(s.ChainLine()))
	if p := s.PackLine(); p != "" {
		w("", mdText(p))
	}
	w("", "---", "", mdText(s.FooterLine()))
	return b.String()
}

func orgName(o Org) string {
	if n := strings.TrimSpace(oneLine(o.Name)); n != "" {
		return n
	}
	return orgWord(o)
}

// mdText escapes the characters Markdown would read as markup in a name.
func mdText(s string) string {
	r := strings.NewReplacer(`\`, `\\`, "*", `\*`, "_", `\_`, "`", "\\`", "[", `\[`, "]", `\]`, "<", "&lt;", ">", "&gt;", "|", `\|`, "#", `\#`)
	return r.Replace(oneLine(s))
}

func mdCell(s string) string { return mdText(s) }

// --- HTML ---

// report.html is embedded gzip-compressed (report.tar.gz, make embed-gz; the image budget).
//
//go:embed report.tar.gz
var reportGz []byte

var reportTmpl = sync.OnceValue(func() *template.Template {
	return template.Must(template.New("report").Option("missingkey=error").Funcs(template.FuncMap{"n": N}).Parse(embedgz.MustString(embedgz.NewFiles(reportGz), "report.html")()))
})

type htmlItem struct {
	Line, Free string
	Pro        bool
}

type htmlView struct {
	Title, Org, OrgID, Label, PeriodKey, Numbers, Check, CaughtHead, More string
	Items                                                                 []htmlItem
	Agents, Tools                                                         []Count
	Denials                                                               []Denial
	Approvals, UndoStop, Sessions, Chain, Pack, Footer, Generated         string
	ProLine                                                               string
	Brand                                                                 *Brand
	Logo                                                                  template.URL
	Console                                                               string
	When                                                                  string
}

// HTML is the report as one self-contained, print-ready page: no script, no request for a font
// or an image (a brand logo is inlined as a data URI).
func HTML(s Summary, o Options) (string, error) {
	v := htmlView{Title: "What your agents did · " + orgName(s.Org) + " · " + s.Period.Label, Org: orgName(s.Org), OrgID: s.Org.ID,
		Label: s.Period.Label, PeriodKey: s.Period.Key, Numbers: s.NumbersLine(), Check: s.CheckLine(), CaughtHead: s.CaughtHead(),
		Agents: s.Actions.ByAgent, Tools: s.Actions.ByTool, Denials: s.Denials, Approvals: s.ApprovalsLine(), UndoStop: s.UndoStopLine(),
		Sessions: s.SessionsLine(), Chain: s.ChainLine(), Pack: s.PackLine(), Footer: s.FooterLine(), ProLine: ProLine,
		Generated: s.GeneratedAt.UTC().Format(time.RFC3339), Brand: o.Brand, When: s.Period.When()}
	for _, it := range s.Caught {
		v.Items = append(v.Items, htmlItem{Line: ItemSentence(it), Free: FreeLine(it), Pro: ShowsPro(it, o)})
	}
	if more := s.CaughtTotal - len(s.Caught); more > 0 {
		v.More = "And " + CountOf(int64(more), "more", "more") + ": deedglass actions list --verdict MISMATCH"
	}
	if o.Brand != nil && len(o.Brand.Logo) > 0 && (o.Brand.LogoMIME == "image/png" || o.Brand.LogoMIME == "image/svg+xml") {
		v.Logo = template.URL("data:" + o.Brand.LogoMIME + ";base64," + base64.StdEncoding.EncodeToString(o.Brand.Logo)) // #nosec: checked by ee/agency
	}
	if strings.HasPrefix(o.ConsoleURL, "https://") || strings.HasPrefix(o.ConsoleURL, "http://") {
		v.Console = o.ConsoleURL
	}
	var b bytes.Buffer
	if err := reportTmpl().Execute(&b, v); err != nil {
		return "", err
	}
	return b.String(), nil
}

// --- plain text (the CLI and the email's text part share these lines) ---

// TextLines are the summary's plain lines, in the Monday email's order.
func TextLines(s Summary, o Options) []string {
	out := []string{s.Headline(), s.NumbersLine(), s.CheckLine()}
	if h := s.CaughtHead(); h != "" {
		single := len(s.Caught) == 1
		if !single {
			out = append(out, h+":")
		}
		for _, it := range s.Caught {
			free, indent := FreeLine(it), "    "
			if single {
				out, indent = append(out, h+": "+ItemLine(it)), "  "
			} else {
				out = append(out, "  "+ItemSentence(it))
			}
			if ShowsPro(it, o) {
				free += "   ·   " + ProLine
			}
			if free != "" {
				out = append(out, indent+free)
			}
		}
	} else {
		out = append(out, "Nothing caught "+s.Period.When()+".")
	}
	return append(out, s.ChainLine())
}
