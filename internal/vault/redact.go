package vault

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"log/slog"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"sync"
)

// Redactor replaces exact secret values in every form they travel in: raw, query- and
// path-escaped, base64 (standard and URL, padded or not), base64("user:pass"), JSON-escaped, and
// a DSN password raw or percent-encoded. Values shorter than MinFieldLength bytes are skipped
// (they would match ordinary text; prod refuses to store them).
type Redactor struct {
	mu      sync.RWMutex
	needles []string // longest first
	rep     *strings.Replacer
}

// NewRedactor covers values.
func NewRedactor(values ...string) *Redactor {
	r := &Redactor{}
	r.add(values...)
	return r
}

// Add covers more values.
func (r *Redactor) Add(values ...string) {
	if r != nil {
		r.add(values...)
	}
}

// Merge covers everything o covers.
func (r *Redactor) Merge(o *Redactor) {
	if r == nil || o == nil || r == o {
		return
	}
	o.mu.RLock()
	ns := append([]string(nil), o.needles...)
	o.mu.RUnlock()
	r.addForms(ns)
}

// addForms adds needles as they are (already expanded into their forms).
func (r *Redactor) addForms(ns []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	set := map[string]bool{}
	for _, n := range r.needles {
		set[n] = true
	}
	for _, n := range ns {
		set[n] = true
	}
	r.rebuild(set)
}

// AddBasic covers base64("user:pass"), the Authorization: Basic form of a username and password.
func (r *Redactor) AddBasic(user, pass string) {
	if r == nil || pass == "" {
		return
	}
	up := user + ":" + pass
	r.add(base64.StdEncoding.EncodeToString([]byte(up)), base64.RawStdEncoding.EncodeToString([]byte(up)))
}

func (r *Redactor) add(values ...string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	set := map[string]bool{}
	for _, n := range r.needles {
		set[n] = true
	}
	for _, v := range values {
		if len(v) < MinFieldLength {
			continue
		}
		for _, f := range forms(v) {
			if len(f) >= MinFieldLength {
				set[f] = true
			}
		}
	}
	r.rebuild(set)
}

func (r *Redactor) rebuild(set map[string]bool) {
	r.needles = r.needles[:0]
	for n := range set {
		r.needles = append(r.needles, n)
	}
	sort.Slice(r.needles, func(i, j int) bool {
		if len(r.needles[i]) != len(r.needles[j]) {
			return len(r.needles[i]) > len(r.needles[j])
		}
		return r.needles[i] < r.needles[j]
	})
	pairs := make([]string, 0, 2*len(r.needles))
	for _, n := range r.needles {
		pairs = append(pairs, n, RedactedText)
	}
	r.rep = strings.NewReplacer(pairs...)
}

// forms are the encodings a value may appear in.
func forms(v string) []string {
	out := []string{v, url.QueryEscape(v), url.PathEscape(v),
		base64.StdEncoding.EncodeToString([]byte(v)), base64.RawStdEncoding.EncodeToString([]byte(v)),
		base64.URLEncoding.EncodeToString([]byte(v)), base64.RawURLEncoding.EncodeToString([]byte(v)),
		strings.TrimPrefix(url.UserPassword("x", v).String(), "x:")}
	if j, err := json.Marshal(v); err == nil {
		out = append(out, string(j[1:len(j)-1]))
	}
	// Percent-encoding with lowercase hex, which some clients emit.
	out = append(out, lowerHex(url.QueryEscape(v)), lowerHex(url.PathEscape(v)))
	return out
}

var rePct = regexp.MustCompile(`%[0-9A-F]{2}`)

func lowerHex(s string) string { return rePct.ReplaceAllStringFunc(s, strings.ToLower) }

// maxPasses bounds the fixpoint loop; a string that still holds a secret after it is replaced
// whole (fail closed).
const maxPasses = 4

// Redact replaces every covered value in s.
func (r *Redactor) Redact(s string) string {
	if r == nil || s == "" {
		return s
	}
	r.mu.RLock()
	rep, needles := r.rep, r.needles
	r.mu.RUnlock()
	if rep == nil || len(needles) == 0 {
		return s
	}
	// The replacement text next to the remaining text can form a value again (for a value that
	// shares characters with "[redacted]"), so replace until nothing is left.
	for range maxPasses {
		if !containsAny(s, needles) {
			return s
		}
		s = rep.Replace(s)
	}
	if containsAny(s, needles) {
		return RedactedText
	}
	return s
}

// RedactBytes is Redact for a byte slice.
func (r *Redactor) RedactBytes(b []byte) []byte {
	if r == nil || len(b) == 0 {
		return b
	}
	s := r.Redact(string(b))
	if s == string(b) {
		return b
	}
	return []byte(s)
}

// Contains reports whether s holds any covered value.
func (r *Redactor) Contains(s string) bool {
	if r == nil {
		return false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return containsAny(s, r.needles)
}

func containsAny(s string, needles []string) bool {
	for _, n := range needles {
		if strings.Contains(s, n) {
			return true
		}
	}
	return false
}

// Redact is the shorthand for NewRedactor(secrets...).Redact(s).
func Redact(s string, secrets ...string) string { return NewRedactor(secrets...).Redact(s) }

// --- patterns: secrets Deedglass doesn't hold ---

type scrubRule struct {
	re   *regexp.Regexp
	repl string
	hint string // a cheap substring the pattern needs
}

var scrubRules = []scrubRule{
	{regexp.MustCompile(`dg_(live|test)_[0-9A-Za-z_]+`), "dg_${1}_" + RedactedText, "dg_"},
	{regexp.MustCompile(`\b(sk|rk)_(live|test)_[0-9A-Za-z]{8,}`), "${1}_${2}_" + RedactedText, "_"},
	{regexp.MustCompile(`(?i)\bbearer\s+[A-Za-z0-9._~+/=-]{8,}`), "Bearer " + RedactedText, ""},
	{regexp.MustCompile(`://[^/\s:@]+:[^/\s@]+@`), "://" + RedactedText + "@", "://"},
	{regexp.MustCompile(`\b(AKIA|ASIA)[0-9A-Z]{16}\b`), RedactedText, "IA"},
	{regexp.MustCompile(`\bgh[pousr]_[A-Za-z0-9]{36,}`), RedactedText, "gh"},
	{regexp.MustCompile(`\bgithub_pat_[A-Za-z0-9_]{22,}`), RedactedText, "github_pat_"},
	{regexp.MustCompile(`\bxox[abprs]-[A-Za-z0-9-]{10,}`), RedactedText, "xox"},
	// S29: Slack app-level tokens, PEM private keys (a GitHub App's), and compact JWTs (an App's
	// signed assertion). An unterminated PEM block is redacted to the end of the text.
	{regexp.MustCompile(`\bxapp-[A-Za-z0-9-]{10,}`), RedactedText, "xapp-"},
	{regexp.MustCompile(`-----BEGIN (?:[A-Z0-9]+ )*PRIVATE KEY-----(?:[\s\S]*?-----END (?:[A-Z0-9]+ )*PRIVATE KEY-----|[\s\S]*)`), RedactedText, "PRIVATE KEY-----"},
	{regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{8,}\.eyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{16,}`), RedactedText, "eyJ"},
	// S30: Google OAuth access tokens, refresh tokens and client secrets (the Gmail connector).
	{regexp.MustCompile(`\bya29\.[A-Za-z0-9_-]{16,}`), RedactedText, "ya29."},
	{regexp.MustCompile(`\b1//0[A-Za-z0-9_-]{16,}`), RedactedText, "1//"},
	{regexp.MustCompile(`\bGOCSPX-[A-Za-z0-9_-]{16,}`), RedactedText, "GOCSPX-"},
	// S31 (the Notion connector): internal integration tokens and the legacy 43-character secret_ form.
	{regexp.MustCompile(`\bntn_[A-Za-z0-9]{20,}`), RedactedText, "ntn_"},
	{regexp.MustCompile(`\bsecret_[A-Za-z0-9]{43}\b`), RedactedText, "secret_"},
}

// Scrub removes secrets by their shape: Deedglass API keys, Stripe secret and restricted keys,
// bearer tokens, URL passwords, AWS access key ids, GitHub tokens (ghs_ App installation tokens
// included), Slack tokens (xapp- since S29), PEM private keys, compact JWTs, and Google OAuth
// access tokens, refresh tokens and client secrets (S30), and Notion integration tokens (S31).
func Scrub(s string) string {
	for _, r := range scrubRules {
		if r.hint != "" && !strings.Contains(s, r.hint) {
			continue
		}
		s = r.re.ReplaceAllString(s, r.repl)
	}
	return s
}

// --- the per-call redactor in the context ---

type ctxKey struct{}

// WithRedactor attaches r to ctx: every log line written with ctx (through RedactingHandler) and
// every connector output is redacted with it.
func WithRedactor(ctx context.Context, r *Redactor) context.Context {
	if r == nil {
		return ctx
	}
	if prev := RedactorFrom(ctx); prev != nil {
		merged := NewRedactor()
		merged.Merge(prev)
		merged.Merge(r)
		r = merged
	}
	return context.WithValue(ctx, ctxKey{}, r)
}

// RedactorFrom returns the context's redactor, or nil.
func RedactorFrom(ctx context.Context) *Redactor {
	if ctx == nil {
		return nil
	}
	r, _ := ctx.Value(ctxKey{}).(*Redactor)
	return r
}

// --- the slog handler ---

// RedactingHandler applies both layers to every log record: the context's exact values, then the
// pattern scrub, on the message and every string attribute, nested groups included. Errors and
// Stringers are rendered and scrubbed too.
type RedactingHandler struct {
	next slog.Handler
}

// NewRedactingHandler wraps next.
func NewRedactingHandler(next slog.Handler) *RedactingHandler { return &RedactingHandler{next: next} }

// Enabled implements slog.Handler.
func (h *RedactingHandler) Enabled(ctx context.Context, l slog.Level) bool {
	return h.next.Enabled(ctx, l)
}

// Handle implements slog.Handler.
func (h *RedactingHandler) Handle(ctx context.Context, r slog.Record) error {
	red := RedactorFrom(ctx)
	clean := func(s string) string { return Scrub(red.Redact(s)) }
	nr := slog.NewRecord(r.Time, r.Level, clean(r.Message), r.PC)
	r.Attrs(func(a slog.Attr) bool {
		nr.AddAttrs(cleanAttr(a, clean))
		return true
	})
	return h.next.Handle(ctx, nr)
}

func cleanAttr(a slog.Attr, clean func(string) string) slog.Attr {
	v := a.Value.Resolve()
	switch v.Kind() {
	case slog.KindString:
		return slog.String(a.Key, clean(v.String()))
	case slog.KindGroup:
		g := v.Group()
		out := make([]slog.Attr, 0, len(g))
		for _, x := range g {
			out = append(out, cleanAttr(x, clean))
		}
		return slog.Attr{Key: a.Key, Value: slog.GroupValue(out...)}
	case slog.KindAny:
		switch x := v.Any().(type) {
		case *Secret:
			return slog.String(a.Key, RedactedText)
		case error:
			return slog.String(a.Key, clean(x.Error()))
		case interface{ String() string }:
			return slog.String(a.Key, clean(x.String()))
		case []byte:
			return slog.String(a.Key, clean(string(x)))
		}
	}
	return slog.Attr{Key: a.Key, Value: v}
}

// WithAttrs implements slog.Handler: attributes fixed on a logger are scrubbed once, here.
func (h *RedactingHandler) WithAttrs(as []slog.Attr) slog.Handler {
	out := make([]slog.Attr, 0, len(as))
	for _, a := range as {
		out = append(out, cleanAttr(a, Scrub))
	}
	return &RedactingHandler{next: h.next.WithAttrs(out)}
}

// WithGroup implements slog.Handler.
func (h *RedactingHandler) WithGroup(name string) slog.Handler {
	return &RedactingHandler{next: h.next.WithGroup(name)}
}
