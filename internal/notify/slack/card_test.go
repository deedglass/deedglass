package slack

import (
	"bufio"
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/deedglass/deedglass/internal/connector"
)

var updateCards = flag.Bool("update-cards", false, "rewrite testdata/cards/*.json from Card and DenyModal")

// The fixed clock for every golden: the request at 04:14 UTC, expiry at 04:44.
var (
	cardT0      = time.Date(2026, 10, 10, 4, 14, 0, 0, time.UTC)
	cardExpires = cardT0.Add(30 * time.Minute)
)

const (
	cardTestApproval = "apr_01j9zx4k7m2q8r5t3v6w9y0b1c"
	cardTestAction   = "act_01j9zx4k3d5f7h9j1k3m5n7p9q"
	cardTestDeed     = "deed_01j9zx6a2b4c6d8e0f2g4h6j8k"
	cardTestContext  = "sha256:7c1e5d0b9a8f6e4d2c0b1a3f5e7d9c1b3a5f7e9d1c3b5a7f9e1d3c5b7a9f1e3d"
	cardTestRequest  = "sha256:3f9a8b7c6d5e4f3a2b1c0d9e8f7a6b5c4d3e2f1a0b9c8d7e6f5a4b3c2d1ec21e"
	cardTestConsole  = "https://app.deedglass.com/app/approvals/apr_01j9zx4k7m2q8r5t3v6w9y0b1c"
)

// cardBase is the pending card from S29 §3.4: support-bot opens a pull request into main.
func cardBase() ApprovalView {
	return ApprovalView{
		ApprovalID:    cardTestApproval,
		ActionID:      cardTestAction,
		ContextDigest: cardTestContext,
		Tool:          "github.create_pr",
		Agent:         "support-bot",
		Summary: connector.Summary{
			{Text: "support-bot", Style: connector.SummaryStrong},
			{Text: " wants to open a pull request in "},
			{Text: "arjun/deedglass-sandbox", Style: connector.SummaryStrong},
			{Text: " from "},
			{Text: "agent/fix-typo", Style: connector.SummaryCode},
			{Text: " into "},
			{Text: "main", Style: connector.SummaryCode},
			{Text: "."},
		},
		RuleID:        "pull-requests-to-main-need-approval",
		PolicyName:    "acme-demo",
		PolicyVersion: 7,
		Reason:        "Pull requests into main need an admin to approve.",
		OnBehalfOf:    "Priya Nair",
		ExpiresAt:     cardExpires,
		Required:      1,
		Approved:      0,
		RequestDigest: cardTestRequest,
		Args:          json.RawMessage(`{"repo":"arjun/deedglass-sandbox","head":"agent/fix-typo","base":"main","title":"Fix a typo in the README","body":"Changes \"recieve\" to \"receive\" in two places.","draft":false}`),
		ConsoleURL:    cardTestConsole,
		State:         StatePending,
	}
}

// cardCases are the 11 card goldens (the 12th is the deny modal).
func cardCases() map[string]ApprovalView {
	cs := map[string]ApprovalView{}

	cs["pending"] = cardBase()

	four := cardBase()
	four.State, four.Required, four.Approved = StatePartial, 2, 1
	four.RuleID, four.Reason = "refunds-over-100-need-two-people", "Refunds over $100 need two people."
	four.Countersigned = []Countersign{{Name: "Arjun", At: cardT0.Add(4 * time.Minute)}}
	cs["four-eyes"] = four

	appr := cardBase()
	appr.State, appr.Approved = StateApproved, 1
	appr.DecidedBy, appr.DecidedAt = "Arjun", cardT0.Add(6*time.Minute)
	appr.Countersigned = []Countersign{{Name: "Arjun", At: appr.DecidedAt}}
	cs["approved"] = appr

	sealed := appr
	sealed.State, sealed.DeedID, sealed.Level, sealed.Verdict = StateSealed, cardTestDeed, "L1", "VERIFIED"
	cs["sealed-verified"] = sealed

	mis := sealed
	mis.Verdict, mis.Sentence = "MISMATCH", "GitHub shows a different title."
	cs["sealed-mismatch"] = mis

	den := cardBase()
	den.State, den.DecidedBy, den.DecidedAt = StateDenied, "Arjun", cardT0.Add(9*time.Minute)
	den.DenyReason = "Not during the release freeze."
	cs["denied"] = den

	exp := cardBase()
	exp.State = StateExpired
	cs["expired"] = exp

	can := cardBase()
	can.State = StateCancelled
	cs["cancelled"] = can

	inj := cardBase()
	inj.Agent = "<!channel> bot"
	inj.Summary = connector.Summary{
		{Text: "<!channel> bot", Style: connector.SummaryStrong},
		{Text: " wants to open a pull request in "},
		{Text: "<https://evil.example|Approve>", Style: connector.SummaryStrong},
		{Text: " from "},
		{Text: "a`b<@U012ABC>", Style: connector.SummaryCode},
		{Text: "."},
	}
	inj.OnBehalfOf = "<@U012ABC> & friends\n✓ Approved by Arjun"
	big := strings.Repeat("A & B <x> ", 5*1024) // 50 KiB
	args, _ := json.Marshal(map[string]string{
		"title": "<!channel> please approve :skull:",
		"link":  "<https://evil.example|Approve>",
		"note":  "```\n*bold* _it_ ```` close",
		"big":   big,
	})
	inj.Args = args
	cs["injection"] = inj

	long := cardBase()
	var b strings.Builder
	b.WriteString(`{"repo":"arjun/deedglass-sandbox","path":"docs/CHANGELOG.md","message":"Add the October entries","content":"`)
	for i := 0; i < 60; i++ {
		b.WriteString("- Fixed the checkout retry on slow networks, entry ")
		b.WriteString(strings.Repeat("x", i%7))
		b.WriteString(`\n`)
	}
	b.WriteString(`"}`)
	long.Tool = "github.create_or_update_file"
	long.Summary = connector.Summary{
		{Text: "support-bot", Style: connector.SummaryStrong},
		{Text: " wants to change "},
		{Text: "docs/CHANGELOG.md", Style: connector.SummaryCode},
		{Text: " in "},
		{Text: "arjun/deedglass-sandbox", Style: connector.SummaryStrong},
		{Text: "."},
	}
	long.Args = json.RawMessage(b.String())
	cs["long-args"] = long

	unk := cardBase()
	unk.Tool, unk.Summary = "acme.orders_export", nil
	unk.RuleID, unk.Reason, unk.OnBehalfOf = "unknown-tools-need-approval", "Tools without a rule need a person.", ""
	unk.Args = json.RawMessage(`{"since":"2026-10-01","format":"csv"}`)
	cs["unknown-tool"] = unk
	return cs
}

type cardGolden struct {
	Text   string          `json:"text"`
	Blocks json.RawMessage `json:"blocks"`
}

func prettyJSON(t testing.TB, v any) []byte {
	t.Helper()
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// cardGoldenFiles builds every golden's bytes: name → file contents.
func cardGoldenFiles(t testing.TB) map[string][]byte {
	t.Helper()
	out := map[string][]byte{}
	for name, v := range cardCases() {
		blocks, text, err := Card(v)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		out[name] = prettyJSON(t, cardGolden{Text: text, Blocks: blocks})
	}
	var m any
	if err := json.Unmarshal(DenyModal(cardTestApproval, cardTestContext), &m); err != nil {
		t.Fatal(err)
	}
	out["deny-modal"] = prettyJSON(t, m)
	return out
}

func TestCardGoldens(t *testing.T) {
	files := cardGoldenFiles(t)
	if len(files) != 12 {
		t.Fatalf("%d goldens, want 12", len(files))
	}
	dir := filepath.Join("testdata", "cards")
	if *updateCards {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for name, got := range files {
		path := filepath.Join(dir, name+".json")
		if *updateCards {
			if err := os.WriteFile(path, got, 0o644); err != nil {
				t.Fatal(err)
			}
			continue
		}
		want, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("%s: %v (run go test ./internal/notify/slack -run TestCardGoldens -update-cards)", name, err)
		}
		if !bytes.Equal(got, want) {
			t.Errorf("%s differs from %s (run with -update-cards and review the diff)\n%s", name, path, got)
		}
	}
	// No stray goldens.
	ents, _ := os.ReadDir(dir)
	for _, e := range ents {
		if _, ok := files[strings.TrimSuffix(e.Name(), ".json")]; !ok {
			t.Errorf("stray golden %s", e.Name())
		}
	}
}

// readGoldens reads the committed goldens, so the rules below hold for what is on disk.
func readGoldens(t *testing.T) map[string]cardGolden {
	t.Helper()
	out := map[string]cardGolden{}
	paths, _ := filepath.Glob(filepath.Join("testdata", "cards", "*.json"))
	if len(paths) != 12 {
		t.Fatalf("%d goldens on disk, want 12", len(paths))
	}
	for _, p := range paths {
		name := strings.TrimSuffix(filepath.Base(p), ".json")
		if name == "deny-modal" {
			continue
		}
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		var g cardGolden
		if err := json.Unmarshal(b, &g); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		out[name] = g
	}
	return out
}

func TestCardGoldensPassLimits(t *testing.T) {
	for name, g := range readGoldens(t) {
		var compact bytes.Buffer
		if err := json.Compact(&compact, g.Blocks); err != nil {
			t.Fatal(err)
		}
		if err := ValidateLimits(compact.Bytes()); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// textStrings collects every "text" string in a decoded JSON value (block text, fields, buttons,
// confirm dialogs, context) and every url.
func textStrings(v any, texts, urls *[]string) {
	switch x := v.(type) {
	case map[string]any:
		for k, val := range x {
			if s, ok := val.(string); ok {
				switch k {
				case "text":
					*texts = append(*texts, s)
				case "url":
					*urls = append(*urls, s)
				}
				continue
			}
			textStrings(val, texts, urls)
		}
	case []any:
		for _, val := range x {
			textStrings(val, texts, urls)
		}
	}
}

var dateToken = regexp.MustCompile(`<!date\^\d+\^\{date_short_pretty\} at \{time\}\|\d{4}-\d\d-\d\d \d\d:\d\d UTC>`)

func cardTexts(t *testing.T, g cardGolden) (texts, urls []string) {
	t.Helper()
	var v any
	if err := json.Unmarshal(g.Blocks, &v); err != nil {
		t.Fatal(err)
	}
	textStrings(v, &texts, &urls)
	return append(texts, g.Text), urls
}

func TestCardNoRawMarkup(t *testing.T) {
	entity := regexp.MustCompile(`&(amp|lt|gt);`)
	for name, g := range readGoldens(t) {
		texts, urls := cardTexts(t, g)
		for _, s := range texts {
			rest := dateToken.ReplaceAllString(s, "")
			if strings.ContainsAny(rest, "<>") {
				t.Errorf("%s: raw < or > outside a date token: %q", name, s)
			}
			if strings.Count(rest, "&") != len(entity.FindAllString(rest, -1)) {
				t.Errorf("%s: raw & in %q", name, s)
			}
			if strings.Contains(s, "://") {
				t.Errorf("%s: a URL in text: %q", name, s)
			}
		}
		for _, u := range urls {
			if u != cardTestConsole {
				t.Errorf("%s: url %q is not the Console URL", name, u)
			}
		}
	}
}

func bannedWords(t *testing.T) []*regexp.Regexp {
	t.Helper()
	f, err := os.Open(filepath.Join("..", "..", "..", "scripts", "banned_words.txt"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var out []*regexp.Regexp
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		line = strings.TrimSpace(strings.TrimPrefix(line, "@until-public "))
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		out = append(out, regexp.MustCompile(`(?i)(^|[^\pL\pN_])`+regexp.QuoteMeta(line)+`($|[^\pL\pN_])`))
	}
	if len(out) < 20 {
		t.Fatalf("only %d banned words read", len(out))
	}
	return out
}

func isEmoji(r rune) bool {
	switch r {
	case '✓', '✕': // the state symbols (brand §6.3), Dingbats but not emoji presentation
		return false
	}
	return (r >= 0x1F000 && r <= 0x1FAFF) || (r >= 0x2600 && r <= 0x27BF) || r == 0xFE0F || (r >= 0x2B00 && r <= 0x2BFF)
}

func TestCardCopyRules(t *testing.T) {
	banned := bannedWords(t)
	shortcode := regexp.MustCompile(`:[a-z0-9_+-]+:`)
	files := map[string]string{}
	for name, g := range readGoldens(t) {
		texts, _ := cardTexts(t, g)
		files[name] = strings.Join(texts, "\n")
	}
	modal, err := os.ReadFile(filepath.Join("testdata", "cards", "deny-modal.json"))
	if err != nil {
		t.Fatal(err)
	}
	files["deny-modal"] = string(modal)
	for name, s := range files {
		if strings.ContainsAny(s, "\u2014\u2013") {
			t.Errorf("%s: an em or en dash", name)
		}
		for _, r := range s {
			if isEmoji(r) {
				t.Errorf("%s: emoji %U", name, r)
			}
		}
		for _, re := range banned {
			if re.MatchString(s) {
				t.Errorf("%s: banned word %s", name, re)
			}
		}
		// Outside code blocks, no emoji shortcode survives.
		outside := regexp.MustCompile("(?s)```.*?```").ReplaceAllString(s, "")
		if m := shortcode.FindString(outside); m != "" {
			t.Errorf("%s: emoji shortcode %q outside a code block", name, m)
		}
		if name != "injection" && strings.Contains(outside, "!") && !strings.Contains(outside, "<!date") {
			t.Errorf("%s: an exclamation mark", name)
		}
	}
}

// decode is a test view of a card: its blocks as generic maps.
func decodeCard(t *testing.T, v ApprovalView) (blocks []map[string]any, text string) {
	t.Helper()
	raw, text, err := Card(v)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &blocks); err != nil {
		t.Fatal(err)
	}
	return blocks, text
}

func blockTypes(bs []map[string]any) string {
	var ts []string
	for _, b := range bs {
		ts = append(ts, b["type"].(string))
	}
	return strings.Join(ts, ",")
}

func TestCardStructure(t *testing.T) {
	bs, text := decodeCard(t, cardBase())
	if got, want := blockTypes(bs), "header,section,section,context,section,context,actions"; got != want {
		t.Fatalf("blocks %s, want %s", got, want)
	}
	if text != "Approval needed · github.create_pr · support-bot" {
		t.Errorf("text %q", text)
	}
	if h := bs[0]["text"].(map[string]any); h["text"] != "Approval needed\u00a0· github.create_pr" || h["emoji"] != false {
		t.Errorf("header %v", h)
	}
	fields := bs[2]["fields"].([]any)
	var labels []string
	for _, f := range fields {
		labels = append(labels, strings.SplitN(f.(map[string]any)["text"].(string), "\n", 2)[0])
	}
	if got := strings.Join(labels, ","); got != "*Rule*,*Reason*,*On behalf of*,*Expires*,*Approvals*,*Request*" {
		t.Errorf("fields %s", got)
	}
	if !strings.Contains(fields[5].(map[string]any)["text"].(string), "`sha256:3f9a…c21e`") {
		t.Errorf("request digest %v", fields[5])
	}
	els := bs[6]["elements"].([]any)
	if len(els) != 3 {
		t.Fatalf("%d buttons", len(els))
	}
	approve, deny, open := els[0].(map[string]any), els[1].(map[string]any), els[2].(map[string]any)
	value := cardTestApproval + "|" + cardTestContext
	if approve["action_id"] != CardActionApprove || approve["style"] != "primary" || approve["value"] != value {
		t.Errorf("approve %v", approve)
	}
	cf := approve["confirm"].(map[string]any)
	if cf["title"].(map[string]any)["text"] != "Approve this action?" ||
		cf["text"].(map[string]any)["text"] != "Deedglass will run github.create_pr as support-bot." ||
		cf["confirm"].(map[string]any)["text"] != "Approve" || cf["deny"].(map[string]any)["text"] != "Cancel" {
		t.Errorf("confirm %v", cf)
	}
	if deny["action_id"] != CardActionDeny || deny["style"] != "danger" || deny["value"] != value || deny["confirm"] != nil {
		t.Errorf("deny %v", deny)
	}
	if open["action_id"] != CardActionOpen || open["url"] != cardTestConsole || open["value"] != nil {
		t.Errorf("open %v", open)
	}
}

func TestCardHeaderAndContext(t *testing.T) {
	cs := cardCases()
	for name, want := range map[string]string{
		"pending":         "Approval needed\u00a0· github.create_pr",
		"four-eyes":       "Approval needed\u00a0· github.create_pr",
		"approved":        "Approval request\u00a0· github.create_pr",
		"sealed-verified": "Approval request\u00a0· github.create_pr",
		"denied":          "Approval request\u00a0· github.create_pr",
	} {
		bs, _ := decodeCard(t, cs[name])
		if got := bs[0]["text"].(map[string]any)["text"]; got != want {
			t.Errorf("%s: header %q", name, got)
		}
	}
	ctx := func(v ApprovalView) string {
		bs, _ := decodeCard(t, v)
		return bs[len(bs)-2]["elements"].([]any)[0].(map[string]any)["text"].(string)
	}
	if got := ctx(cs["pending"]); got != "`act_01j9…7p9q` · `apr_01j9…0b1c` · policy acme-demo v7" {
		t.Errorf("pending context %q", got)
	}
	if got := ctx(cs["sealed-mismatch"]); got != "`deed_01j9…6j8k` · `act_01j9…7p9q` · `apr_01j9…0b1c` · policy acme-demo v7" {
		t.Errorf("sealed context %q", got)
	}
	for in, want := range map[string]string{"apr_01j9": "apr_01j9", "noprefix": "noprefix", "x_01j9-zz-zzzzz": "x_01j9-zz-zzzzz"} {
		if got := cardShortID(in); got != want {
			t.Errorf("cardShortID(%q) = %q", in, got)
		}
	}
}

func TestCardFinalStatesShrink(t *testing.T) {
	for name, v := range cardCases() {
		if v.State == StatePending || v.State == StatePartial {
			continue
		}
		bs, _ := decodeCard(t, v)
		if got := blockTypes(bs); got != "header,section,section,context,actions" {
			t.Errorf("%s: blocks %s", name, got)
		}
		els := bs[len(bs)-1]["elements"].([]any)
		if len(els) != 1 || els[0].(map[string]any)["action_id"] != CardActionOpen {
			t.Errorf("%s: final actions %v", name, els)
		}
	}
}

func cardLine(t *testing.T, v ApprovalView) string {
	t.Helper()
	bs, _ := decodeCard(t, v)
	for _, b := range bs[2:] {
		if b["type"] == "section" && b["text"] != nil {
			s := b["text"].(map[string]any)["text"].(string)
			if !strings.HasPrefix(s, "```") {
				return dateToken.ReplaceAllString(s, "<date>")
			}
		}
	}
	t.Fatalf("no state line")
	return ""
}

func TestCardStateLines(t *testing.T) {
	cs := cardCases()
	twoOf3 := cs["four-eyes"]
	twoOf3.Required, twoOf3.Approved = 5, 4
	twoOf3.Countersigned = []Countersign{{"Arjun", cardT0}, {"Priya", cardT0}, {"Meera", time.Time{}}, {"Kabir", cardT0}}
	noSign := cs["four-eyes"]
	noSign.Countersigned, noSign.Approved, noSign.Required = nil, 0, 3

	twoApproved := cs["approved"]
	twoApproved.Countersigned = []Countersign{{"Priya", cardT0}}
	manyApproved := cs["approved"]
	manyApproved.Countersigned = []Countersign{{"A", cardT0}, {"B", cardT0}, {"C", cardT0}}
	manyApproved.DecidedBy = "D"
	nobody := cs["approved"]
	nobody.Countersigned, nobody.DecidedBy, nobody.DecidedAt = nil, "", time.Time{}

	failed := cs["sealed-verified"]
	failed.Failed, failed.Verdict = true, ""
	unver := cs["sealed-verified"]
	unver.Verdict, unver.Sentence, unver.Level = "UNVERIFIABLE", "GitHub didn't answer the read-back.", "L1"
	bare := cs["sealed-verified"]
	bare.Verdict, bare.DeedID, bare.Level = "", "", ""
	denNoReason := cs["denied"]
	denNoReason.DenyReason = " \n"
	expNoTime := cs["expired"]
	expNoTime.ExpiresAt = time.Time{}
	odd := cardBase()
	odd.State = State("paused")

	for _, c := range []struct {
		v    ApprovalView
		want string
	}{
		{cs["four-eyes"], "◐ 1 of 2 approvals · Arjun approved <date>. Waiting for one more person."},
		{twoOf3, "◐ 4 of 5 approvals · Arjun approved <date>, Priya approved <date>, Meera approved, and 1 more. Waiting for one more person."},
		{noSign, "◐ 0 of 3 approvals Waiting for 3 more people."},
		{cs["approved"], "✓ Approved by Arjun · <date>\nRunning now."},
		{twoApproved, "✓ Approved by Priya and Arjun · <date>\nRunning now."},
		{manyApproved, "✓ Approved by A, B and C and 1 more · <date>\nRunning now."},
		{nobody, "✓ Approved by a person\nRunning now."},
		{cs["sealed-verified"], "✓ Approved by Arjun · <date>\n✓ Deed sealed · Checked · Matches"},
		{cs["sealed-mismatch"], "✓ Approved by Arjun · <date>\n✕ Checked · Doesn't match · GitHub shows a different title."},
		{unver, "✓ Approved by Arjun · <date>\n○ Checked · Can't check · GitHub didn't answer the read-back."},
		{failed, "✓ Approved by Arjun · <date>\n✕ The tool failed. Nothing was applied."},
		{bare, "✓ Approved by Arjun · <date>\nDeed sealed."},
		{cs["denied"], "✕ Denied by Arjun · <date> · \"Not during the release freeze.\""},
		{denNoReason, "✕ Denied by Arjun · <date>"},
		{cs["expired"], "○ Expired <date>. Nothing ran."},
		{expNoTime, "○ Expired. Nothing ran."},
		{cs["cancelled"], "○ Cancelled by the agent. Nothing ran."},
		{odd, "○ paused"},
	} {
		if got := cardLine(t, c.v); got != c.want {
			t.Errorf("state %s:\n got %q\nwant %q", c.v.State, got, c.want)
		}
	}
}

func TestCardFallbackText(t *testing.T) {
	cs := cardCases()
	failed := cs["sealed-verified"]
	failed.Failed = true
	empty := ApprovalView{State: StatePending}
	for _, c := range []struct {
		v    ApprovalView
		want string
	}{
		{cs["pending"], "Approval needed · github.create_pr · support-bot"},
		{cs["four-eyes"], "Approval needed (1 of 2) · github.create_pr · support-bot"},
		{cs["approved"], "Approved · github.create_pr · support-bot"},
		{cs["sealed-verified"], "Deed sealed · github.create_pr · support-bot"},
		{cs["sealed-mismatch"], "Deed sealed, doesn't match · github.create_pr · support-bot"},
		{failed, "The tool failed · github.create_pr · support-bot"},
		{cs["denied"], "Denied · github.create_pr · support-bot"},
		{cs["expired"], "Expired · github.create_pr · support-bot"},
		{cs["cancelled"], "Cancelled · github.create_pr · support-bot"},
		{cs["injection"], "Approval needed · github.create_pr · &lt;!channel&gt; bot"},
		{empty, "Approval needed"},
	} {
		_, text, err := Card(c.v)
		if err != nil {
			t.Fatal(err)
		}
		if text != c.want {
			t.Errorf("got %q, want %q", text, c.want)
		}
	}
}

func TestCardEdges(t *testing.T) {
	// No args, no fields, no ids, no Console URL: still a valid card.
	bare := ApprovalView{State: StatePending, Tool: "x.y", Agent: "a"}
	bs, _ := decodeCard(t, bare)
	if got := blockTypes(bs); got != "header,section,section,context,actions" {
		t.Errorf("bare: %s", got)
	}
	if f := bs[2]["fields"].([]any)[0].(map[string]any)["text"]; f != "*Rule*\nNone recorded" {
		t.Errorf("bare fields %v", f)
	}
	if c := bs[3]["elements"].([]any)[0].(map[string]any)["text"]; c != "Deedglass" {
		t.Errorf("bare context %v", c)
	}
	if n := len(bs[4]["elements"].([]any)); n != 2 {
		t.Errorf("bare: %d buttons, want 2 (no Console link)", n)
	}
	// A Console URL that isn't http(s) with a host is dropped, never emitted.
	for _, u := range []string{"javascript:alert(1)", "ftp://x/y", "https://", "%zz", "/app/approvals/x", "https://x/" + strings.Repeat("a", 3000)} {
		v := cardBase()
		v.ConsoleURL = u
		raw, _, err := Card(v)
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(raw, []byte(CardActionOpen)) {
			t.Errorf("url %q kept", u)
		}
	}
	// Arguments that aren't JSON are shown as text, still escaped.
	v := cardBase()
	v.Args = json.RawMessage("not json <b>")
	bs, _ = decodeCard(t, v)
	if s := bs[4]["text"].(map[string]any)["text"]; s != "```\nnot json &lt;b&gt;\n```" {
		t.Errorf("non-JSON args %q", s)
	}
	// Policy without a version; a malformed digest is shown as given.
	v = cardBase()
	v.PolicyVersion, v.RequestDigest = 0, "md5:zz"
	raw, _, _ := Card(v)
	if !bytes.Contains(raw, []byte("policy acme-demo\"")) || !bytes.Contains(raw, []byte("`md5:zz`")) {
		t.Errorf("policy or digest: %s", raw)
	}
	// Approvals with Required 0 is left out.
	v = cardBase()
	v.Required = 0
	raw, _, _ = Card(v)
	if bytes.Contains(raw, []byte("*Approvals*")) {
		t.Error("Approvals shown with Required 0")
	}
}

func TestCardSummary(t *testing.T) {
	v := cardBase()
	v.Summary = connector.Summary{
		{Text: "*sup*port-bot", Style: connector.SummaryStrong},
		{Text: " wants  "},
		{Text: "  ", Style: connector.SummaryCode},
		{Text: "", Style: connector.SummaryStrong},
		{Text: " "},
		{Text: "a:b https://x.example :fire: 10:14", Style: connector.SummaryPlain},
	}
	bs, _ := decodeCard(t, v)
	got := bs[1]["text"].(map[string]any)["text"].(string)
	want := "*support-bot* wants  a:\u200bb https:\u200b//x.example :\u200bfire: 10:14"
	if got != want {
		t.Errorf("summary\n got %q\nwant %q", got, want)
	}
	// The fallback when a connector has no summary.
	v.Summary = nil
	bs, _ = decodeCard(t, v)
	if got := bs[1]["text"].(map[string]any)["text"]; got != "*support-bot* wants to run `github.create_pr`." {
		t.Errorf("fallback summary %q", got)
	}
}

func TestCardArgsTruncation(t *testing.T) {
	cs := cardCases()
	for _, name := range []string{"long-args", "injection"} {
		bs, _ := decodeCard(t, cs[name])
		var body, note string
		for i, b := range bs {
			if b["type"] == "section" && b["text"] != nil && strings.HasPrefix(b["text"].(map[string]any)["text"].(string), "```") {
				body = b["text"].(map[string]any)["text"].(string)
				note = bs[i+1]["elements"].([]any)[0].(map[string]any)["text"].(string)
			}
		}
		inner := strings.TrimSuffix(strings.TrimPrefix(body, "```\n"), "\n```")
		if n := utf8.RuneCountInString(inner); n > cardArgsMaxRunes+2 {
			t.Errorf("%s: %d characters in the fence", name, n)
		}
		if strings.HasSuffix(inner, "…") || !regexp.MustCompile(`^… [\d,]+ more characters\. The full request is in the Console\.$`).MatchString(note) {
			t.Errorf("%s: note %q, tail %q", name, note, inner[len(inner)-10:])
		}
		if strings.Contains(inner, "``") {
			t.Errorf("%s: adjacent backticks inside the fence", name)
		}
	}
	// Exactly at the cap: no note.
	s := strings.Repeat("a", cardArgsMaxRunes)
	if body, more := cardFenceBody(s); more != 0 || body != s {
		t.Errorf("at the cap: more=%d", more)
	}
	if _, more := cardFenceBody(s + "bc"); more != 2 {
		t.Errorf("over the cap by 2: more=%d", more)
	}
	// The byte budget binds first for wide runes.
	wide := strings.Repeat("\u00e9\"\x01", 1000)
	body, more := cardFenceBody(wide)
	if more == 0 || len(body) > cardArgsMaxBytes+8 {
		t.Errorf("wide: more=%d len=%d", more, len(body))
	}
	if cardJSONWidth('\u2028') != 6 || cardJSONWidth('\t') != 2 || cardJSONWidth('€') != 3 {
		t.Error("cardJSONWidth")
	}
}

// TestCardHostile throws the worst strings at every field and checks the card is still valid,
// escaped and inside the limits.
func TestCardHostile(t *testing.T) {
	huge := strings.Repeat("<&>\"\u2028😀", 4000)
	v := cardBase()
	v.State = StatePartial
	v.Required, v.Approved = 9, 3
	for _, p := range []*string{&v.Tool, &v.Agent, &v.RuleID, &v.Reason, &v.OnBehalfOf, &v.PolicyName, &v.DenyReason, &v.Sentence, &v.DecidedBy} {
		*p = huge
	}
	v.Summary = connector.Summary{{Text: huge, Style: connector.SummaryStrong}, {Text: huge}, {Text: huge, Style: connector.SummaryCode}}
	for i := 0; i < 9; i++ {
		v.Countersigned = append(v.Countersigned, Countersign{Name: huge, At: cardT0})
	}
	args, _ := json.Marshal(map[string]string{"a": huge})
	v.Args = args
	for _, st := range []State{StatePending, StatePartial, StateApproved, StateSealed, StateDenied, StateExpired, StateCancelled} {
		v.State = st
		v.Verdict = "MISMATCH"
		raw, text, err := Card(v)
		if err != nil {
			t.Fatalf("%s: %v", st, err)
		}
		if len(raw) >= limitCardBytes {
			t.Errorf("%s: %d bytes", st, len(raw))
		}
		texts, _ := cardTexts(t, cardGolden{Text: text, Blocks: raw})
		for _, s := range texts {
			if strings.ContainsAny(dateToken.ReplaceAllString(s, ""), "<>") {
				t.Fatalf("%s: raw markup in %q", st, s[:80])
			}
		}
	}
}

func TestDenyModal(t *testing.T) {
	var m struct {
		Type, CallbackID, PrivateMetadata string
		Title, Submit, Close              bkText
		Blocks                            []struct {
			Type    string `json:"type"`
			BlockID string `json:"block_id"`
			Label   bkText `json:"label"`
			Element struct {
				Type      string `json:"type"`
				ActionID  string `json:"action_id"`
				Multiline bool   `json:"multiline"`
				MinLength int    `json:"min_length"`
				MaxLength int    `json:"max_length"`
			} `json:"element"`
		}
	}
	raw := DenyModal(cardTestApproval, cardTestContext)
	if err := json.Unmarshal(bytes.ReplaceAll(bytes.ReplaceAll(raw, []byte(`"callback_id"`), []byte(`"CallbackID"`)), []byte(`"private_metadata"`), []byte(`"PrivateMetadata"`)), &m); err != nil {
		t.Fatal(err)
	}
	if m.Type != "modal" || m.CallbackID != DenyCallbackID || m.PrivateMetadata != cardTestApproval+"|"+cardTestContext {
		t.Errorf("modal %+v", m)
	}
	if m.Title.Text != "Deny action" || m.Submit.Text != "Deny" || m.Close.Text != "Cancel" {
		t.Errorf("modal text %+v", m)
	}
	for _, s := range []string{m.Title.Text, m.Submit.Text, m.Close.Text} {
		if utf8.RuneCountInString(s) > 24 {
			t.Errorf("%q is over Slack's 24-character view limit", s)
		}
	}
	if len(m.Blocks) != 1 {
		t.Fatalf("%d blocks", len(m.Blocks))
	}
	b := m.Blocks[0]
	if b.Type != "input" || b.BlockID != DenyReasonBlockID || b.Label.Text != "Reason · recorded with the decision" ||
		b.Element.Type != "plain_text_input" || b.Element.ActionID != DenyReasonActionID || !b.Element.Multiline ||
		b.Element.MinLength != 1 || b.Element.MaxLength != 280 {
		t.Errorf("input %+v", b)
	}
}

func TestCardHelpers(t *testing.T) {
	for in, want := range map[int]string{0: "0", 999: "999", 1000: "1,000", 51234: "51,234", 1234567: "1,234,567"} {
		if got := cardGroupDigits(in); got != want {
			t.Errorf("cardGroupDigits(%d) = %q", in, got)
		}
	}
	for in, want := range map[string]string{
		cardTestRequest:     "sha256:3f9a…c21e",
		"sha256:3f9a":       "sha256:3f9a",
		"sha256:3f9azzzzzz": "sha256:3f9azzzzzz",
		"nodigest":          "nodigest",
	} {
		if got := cardShortDigest(in); got != want {
			t.Errorf("cardShortDigest(%q) = %q", in, got)
		}
	}
	if got := cardJoinNames([]string{"A", "B", "C"}); got != "A, B and C" {
		t.Error(got)
	}
	if got := cardDate(cardExpires); got != "<!date^1791607440^{date_short_pretty} at {time}|2026-10-10 04:44 UTC>" {
		t.Error(got)
	}
	// Card answers the limit error when something is still too big (a defensive path).
	keys := make([]string, 0)
	for k := range cardCases() {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if len(keys) != 11 {
		t.Errorf("%d card cases", len(keys))
	}
}

func TestCardUnescapeHTML(t *testing.T) {
	for in, want := range map[string]string{
		`{"a":"x"}`:            `{"a":"x"}`,
		`{"a":"<!channel> &"}`: `{"a":"<!channel> &"}`,
		`{"a":"\u003C"}`:       `{"a":"<"}`,
		`{"a":"\\u003c"}`:      `{"a":"\\u003c"}`,
		`{"a":"\\<"}`:          `{"a":"\\<"}`,
		`{"a":"\"é"}`:          `{"a":"\"é"}`,
		`"\u00`:                `"\u00`,
	} {
		if got := cardUnescapeHTML(in); got != want {
			t.Errorf("cardUnescapeHTML(%s) = %s, want %s", in, got, want)
		}
	}
	// The card shows the characters, escaped for Slack once.
	v := cardBase()
	v.Args = json.RawMessage(`{"t":"<!channel> &"}`)
	bs, _ := decodeCard(t, v)
	if s := bs[4]["text"].(map[string]any)["text"]; s != "```\n{\n  \"t\": \"&lt;!channel&gt; &amp;\"\n}\n```" {
		t.Errorf("args %q", s)
	}
}
