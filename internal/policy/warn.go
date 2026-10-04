package policy

import (
	_ "embed"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"
)

// bannedWords is a byte-for-byte copy of scripts/banned_words.txt (TestBannedWordsCopy).
//
//go:embed bannedwords.txt
var bannedWords string

var bannedRE = sync1(func() *regexp.Regexp {
	var alts []string
	for _, l := range strings.Split(bannedWords, "\n") {
		l = strings.TrimSpace(l)
		if l == "" || strings.HasPrefix(l, "#") {
			continue
		}
		l = strings.TrimPrefix(l, "@until-public ")
		alts = append(alts, strings.ReplaceAll(regexp.QuoteMeta(l), `\ `, `\s+`))
	}
	return regexp.MustCompile(`(?i)(?:^|[^A-Za-z0-9_])(` + strings.Join(alts, "|") + `)(?:$|[^A-Za-z0-9_])`)
})

func sync1[T any](f func() T) func() T {
	var v T
	var done bool
	return func() T {
		if !done {
			v, done = f(), true
		}
		return v
	}
}

var secretRE = regexp.MustCompile(`(dg_live_|dg_test_|sk_live_|rk_live_|ghp_|github_pat_|xoxb-)[A-Za-z0-9_\-]{8,}|AKIA[0-9A-Z]{16}`)

// warn adds the warnings: default allow, ignored fields, secret-looking strings, unreachable rules
// and, with Voice, reason style.
func warn(src *source, d *diags, p *draft, opts Options) {
	if p.defaults.Decision == Allow && p.defaultsDecision != nil {
		d.warnf("default_allow", spanOf(src, p.defaultsDecision), "defaults.decision", "",
			"Deedglass recommends deny. Enforcing this policy will need an explicit acknowledgement.",
			"`defaults.decision: allow` lets every unmatched action run.")
	}
	for _, r := range p.rules {
		for _, kv := range r.ignored {
			field := kv.Key.Value
			hint := "Remove it."
			switch field {
			case "approval":
				hint = "Remove it, or change the decision to require_approval."
			case "verify":
				hint = "Remove it; a denied action never runs, so there is nothing to witness."
			case "notify":
				hint = "Remove it; notifications go out for denials and approval requests."
			}
			d.warnf("ignored_field", keySpan(kv.Key), r.path+"."+field, r.r.ID, hint,
				"`%s` is ignored because the decision is %s.", field, r.r.Decision)
		}
	}
	for i, line := range src.lines {
		if m := secretRE.FindStringSubmatchIndex(line); m != nil {
			prefix := "AKIA" // show only the known prefix, never the value
			if m[2] >= 0 {
				prefix = line[m[2]:m[3]]
			}
			col := utf8.RuneCountInString(line[:m[0]]) + 1
			d.warnf("secret_in_policy", span{Start: Pos{i + 1, col}, End: Pos{i + 1, col + runeLen(line[m[0]:m[1]])}}, "", "",
				"Policies are stored and shown to admins. Keep secrets in the connector vault.",
				"Line %d looks like a secret (`%s…`).", i+1, prefix)
		}
	}
	unreachable(src, d, p)
	if opts.Voice {
		voice(src, d, p)
	}
}

// unreachable warns when an earlier rule with no `when` covers everything a later rule matches.
// It is conservative: no warning unless coverage is certain.
func unreachable(src *source, d *diags, p *draft) {
	for j, b := range p.rules {
		if b.r.ID == "" {
			continue
		}
		for _, a := range p.rules[:j] {
			if a.when != nil || a.r.ID == "" || a.r.Decision == "" {
				continue
			}
			if !matcherCovers(&a.r.Match, &b.r.Match) {
				continue
			}
			whenNote := "has no `when`"
			end := spanOf(src, b.idNode).End
			if b.idNode.Line != b.r.Pos.Line {
				end = Pos{b.r.Pos.Line, b.r.Pos.Col + 2}
			}
			d.warnf("rule_unreachable", span{Start: b.r.Pos, End: end}, b.path, b.r.ID,
				fmt.Sprintf("Move rule %q above rule %q, or narrow rule %q.", b.r.ID, a.r.ID, a.r.ID),
				"Rule %q can never match: rule %q (line %d) matches everything it does and %s.", b.r.ID, a.r.ID, a.idNode.Line, whenNote)
			break
		}
	}
}

func matcherCovers(a, b *Matcher) bool {
	if !globsCover(a.Tool, b.Tool) || !globsCover(a.ConnectorType, b.ConnectorType) {
		return false
	}
	if len(a.SideEffect) > 0 {
		if len(b.SideEffect) == 0 {
			return false
		}
		for _, s := range b.SideEffect {
			if !contains(a.SideEffect, s) {
				return false
			}
		}
	}
	if a.hasAgent {
		if !b.hasAgent {
			return false
		}
		for _, id := range b.AgentIDs {
			if !contains(a.AgentIDs, id) {
				return false
			}
		}
		if !globsCover(a.AgentNames, b.AgentNames) {
			return false
		}
		if len(b.AgentNames) > 0 && len(a.AgentNames) == 0 {
			return false
		}
	}
	for _, l := range a.Labels {
		found := false
		for _, m := range b.Labels {
			if m == l {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// globsCover: every glob in b is covered by some glob in a. Empty a means "anything"; empty b
// means "anything", which only an all-star glob in a covers.
func globsCover(a, b []Glob) bool {
	if len(a) == 0 {
		return true
	}
	if len(b) == 0 {
		for _, g := range a {
			if g.kind == globAny {
				return true
			}
		}
		return false
	}
	for _, y := range b {
		ok := false
		for _, x := range a {
			if x.covers(y) {
				ok = true
				break
			}
		}
		if !ok {
			return false
		}
	}
	return true
}

// voice is the opt-in brand-voice check on reasons (--voice).
func voice(src *source, d *diags, p *draft) {
	check := func(reason string, path, ruleID string, line, col int) {
		if reason == "" {
			return
		}
		sp := span{Start: Pos{line, col}}
		r, _ := utf8.DecodeRuneInString(reason)
		switch {
		case strings.Contains(reason, "!"):
			d.warnf("reason_style", sp, path, ruleID, "Say it calmly, without an exclamation mark.", "This reason has an exclamation mark.")
		case unicode.IsLower(r):
			d.warnf("reason_style", sp, path, ruleID, "Start with a capital letter.", "This reason starts with a lowercase letter.")
		case !strings.HasSuffix(reason, ".") && !strings.HasSuffix(reason, "?"):
			d.warnf("reason_style", sp, path, ruleID, "End it with a period.", "This reason ends without a period.")
		}
		if m := bannedRE().FindStringSubmatch(reason); m != nil {
			d.warnf("reason_style", sp, path, ruleID, "Use plain, exact words; see the brand vocabulary.",
				"This reason uses %q, which Deedglass copy never uses.", strings.ToLower(m[1]))
		}
	}
	for _, r := range p.rules {
		line, col := r.r.Pos.Line, r.r.Pos.Col
		if r.reasonNode != nil {
			line, col = r.reasonNode.Line, r.reasonNode.Column
		}
		check(r.r.Reason, r.path+".reason", r.r.ID, line, col)
	}
	if n := p.defaultsReasonNode; n != nil {
		check(p.defaults.Reason, "defaults.reason", "", n.Line, n.Column)
	}
}

var bannedPublic struct {
	once sync.Once
	re   *regexp.Regexp
}

// BannedWordIn returns the first banned word or phrase in s (scripts/banned_words.txt, matched as
// CI matches it), or "". Safe for concurrent use; S28's phrasing validator calls it.
func BannedWordIn(s string) string {
	bannedPublic.once.Do(func() {
		var alts []string
		for _, l := range strings.Split(bannedWords, "\n") {
			l = strings.TrimSpace(l)
			if l == "" || strings.HasPrefix(l, "#") {
				continue
			}
			l = strings.TrimPrefix(l, "@until-public ")
			alts = append(alts, strings.ReplaceAll(regexp.QuoteMeta(l), `\ `, `[\s-]+`))
		}
		bannedPublic.re = regexp.MustCompile(`(?i)(?:^|[^A-Za-z0-9_])(` + strings.Join(alts, "|") + `)(?:$|[^A-Za-z0-9_])`)
	})
	if m := bannedPublic.re.FindStringSubmatch(s); m != nil {
		return m[1]
	}
	return ""
}
