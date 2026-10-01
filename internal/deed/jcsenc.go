package deed

import (
	"slices"
	"strconv"
	"unicode/utf8"
)

// A direct RFC 8785 (JCS) encoder for a deed body and for the strict-parsed tree. It writes the
// bytes StrictCanonical and gowebpki/jcs write (FuzzCanonical compares them) without json.Marshal,
// a second parse or reflection. The strict profile still holds: strings must be valid UTF-8
// (refused, never replaced), integers must be in 0..2^53-1, and a tree may hold only what
// intent.ParseStrictNonNegative returns.

// canonicalBody returns JCS(b). Members are written in RFC 8785 order, which for these ASCII keys
// is byte order; the differential tests against StrictCanonical keep that order right.
func canonicalBody(b *Body) ([]byte, error) {
	w := jcsWriter{b: make([]byte, 0, 1536)}
	w.body(b)
	if w.err != nil {
		return nil, w.err
	}
	return w.b, nil
}

// canonicalTree returns JCS(v) for a tree from intent.ParseStrictNonNegative.
func canonicalTree(v any) ([]byte, *VerifyError) {
	w := jcsWriter{b: make([]byte, 0, 1536)}
	w.tree(v)
	if w.err != nil {
		return nil, w.err
	}
	return w.b, nil
}

// jcsWriter appends canonical JSON to b and keeps the first error.
type jcsWriter struct {
	b    []byte
	err  *VerifyError
	segs []seg // the tree path, for error messages
}

// key writes the separator (unless first) and "k":.
func (w *jcsWriter) key(k string, first bool) {
	if !first {
		w.b = append(w.b, ',')
	}
	w.b = append(w.b, '"')
	w.b = append(w.b, k...)
	w.b = append(w.b, '"', ':')
}

func (w *jcsWriter) str(s, path string) {
	if !utf8.ValidString(s) {
		w.fail("input is not valid UTF-8", path)
		return
	}
	w.b = appendJCSString(w.b, s)
}

func (w *jcsWriter) nstr(s *string, path string) {
	if s == nil {
		w.b = append(w.b, "null"...)
		return
	}
	w.str(*s, path)
}

func (w *jcsWriter) int(n int64, path string) {
	switch {
	case n < 0:
		w.fail("negative numbers and -0 are not allowed", path)
	case n > MaxSafeInteger:
		w.fail("integer magnitude exceeds 2^53-1", path)
	default:
		w.b = strconv.AppendInt(w.b, n, 10)
	}
}

func (w *jcsWriter) boolean(v bool) {
	if v {
		w.b = append(w.b, "true"...)
	} else {
		w.b = append(w.b, "false"...)
	}
}

// fail keeps the first error, worded as the strict parser words it.
func (w *jcsWriter) fail(msg, path string) {
	if w.err != nil {
		return
	}
	if path != "" {
		msg += " at " + path
	}
	w.err = errMalformed(msg)
}

func (w *jcsWriter) body(b *Body) {
	w.b = append(w.b, '{')
	w.key("action", true)
	if a := b.Action; a == nil {
		w.b = append(w.b, "null"...)
	} else {
		w.b = append(w.b, '{')
		w.key("connector", true)
		w.nstr(a.Connector, "action.connector")
		w.key("id", false)
		w.str(a.ID, "action.id")
		w.key("intent_hash", false)
		w.nstr(a.IntentHash, "action.intent_hash")
		w.key("intent_signed", false)
		w.boolean(a.IntentSigned)
		w.key("mode", false)
		w.str(a.Mode, "action.mode")
		w.key("request_digest", false)
		w.str(a.RequestDigest, "action.request_digest")
		w.key("tool", false)
		w.str(a.Tool, "action.tool")
		w.b = append(w.b, '}')
	}
	w.key("admin", false)
	if a := b.Admin; a == nil {
		w.b = append(w.b, "null"...)
	} else {
		w.b = append(w.b, '{')
		w.key("action", true)
		w.str(a.Action, "admin.action")
		w.key("actor", false)
		w.ref(&a.Actor, "admin.actor")
		w.key("data_digest", false)
		w.nstr(a.DataDigest, "admin.data_digest")
		w.key("event", false)
		w.str(a.Event, "admin.event")
		w.key("reason", false)
		w.nstr(a.Reason, "admin.reason")
		w.key("target", false)
		if a.Target == nil {
			w.b = append(w.b, "null"...)
		} else {
			w.ref(a.Target, "admin.target")
		}
		w.b = append(w.b, '}')
	}
	w.key("agent", false)
	if a := b.Agent; a == nil {
		w.b = append(w.b, "null"...)
	} else {
		w.b = append(w.b, '{')
		w.key("id", true)
		w.str(a.ID, "agent.id")
		w.key("name", false)
		w.str(a.Name, "agent.name")
		w.b = append(w.b, '}')
	}
	w.key("amends", false)
	w.nstr(b.Amends, "amends")
	w.key("approval", false)
	if a := b.Approval; a == nil {
		w.b = append(w.b, "null"...)
	} else {
		w.b = append(w.b, '{')
		w.key("decided_at", true)
		w.nstr(a.DecidedAt, "approval.decided_at")
		w.key("decided_by", false)
		if a.DecidedBy == nil {
			w.b = append(w.b, "null"...)
		} else {
			w.b = append(w.b, '[')
			for i, u := range a.DecidedBy {
				if i > 0 {
					w.b = append(w.b, ',')
				}
				if !utf8.ValidString(u) {
					w.fail("input is not valid UTF-8", "approval.decided_by["+strconv.Itoa(i)+"]")
				}
				w.b = appendJCSString(w.b, u)
			}
			w.b = append(w.b, ']')
		}
		w.key("deed", false)
		w.nstr(a.Deed, "approval.deed")
		w.key("id", false)
		w.str(a.ID, "approval.id")
		w.key("outcome", false)
		w.str(a.Outcome, "approval.outcome")
		w.b = append(w.b, '}')
	}
	w.key("cost", false)
	if c := b.Cost; c == nil {
		w.b = append(w.b, "null"...)
	} else {
		w.b = append(w.b, '{')
		w.key("amount", true)
		w.int(c.Amount, "cost.amount")
		w.key("currency", false)
		w.str(c.Currency, "cost.currency")
		w.b = append(w.b, '}')
	}
	w.key("deed_id", false)
	w.str(b.DeedID, "deed_id")
	w.key("execution", false)
	if e := b.Execution; e == nil {
		w.b = append(w.b, "null"...)
	} else {
		w.b = append(w.b, '{')
		w.key("error_code", true)
		w.nstr(e.ErrorCode, "execution.error_code")
		w.key("finished_at", false)
		w.str(e.FinishedAt, "execution.finished_at")
		w.key("latency_ms", false)
		w.int(e.LatencyMS, "execution.latency_ms")
		w.key("result_digest", false)
		w.nstr(e.ResultDigest, "execution.result_digest")
		w.key("started_at", false)
		w.str(e.StartedAt, "execution.started_at")
		w.key("status", false)
		w.str(e.Status, "execution.status")
		w.b = append(w.b, '}')
	}
	w.key("issued_at", false)
	w.str(b.IssuedAt, "issued_at")
	w.key("kind", false)
	w.str(string(b.Kind), "kind")
	w.key("on_behalf_of", false)
	w.nstr(b.OnBehalfOf, "on_behalf_of")
	w.key("org", false)
	w.str(b.Org, "org")
	w.key("policy", false)
	if p := b.Policy; p == nil {
		w.b = append(w.b, "null"...)
	} else {
		w.b = append(w.b, '{')
		w.key("decision", true)
		w.str(p.Decision, "policy.decision")
		w.key("id", false)
		w.nstr(p.ID, "policy.id")
		w.key("mode", false)
		w.str(p.Mode, "policy.mode")
		w.key("rule_id", false)
		w.nstr(p.RuleID, "policy.rule_id")
		w.key("version_hash", false)
		w.nstr(p.VersionHash, "policy.version_hash")
		w.key("version_id", false)
		w.nstr(p.VersionID, "policy.version_id")
		w.b = append(w.b, '}')
	}
	w.key("prev_hash", false)
	w.str(b.PrevHash, "prev_hash")
	w.key("seal_key_id", false)
	w.str(b.SealKeyID, "seal_key_id")
	w.key("seq", false)
	w.int(b.Seq, "seq")
	w.key("session", false)
	w.nstr(b.Session, "session")
	w.key("v", false)
	w.int(b.V, "v")
	w.key("witness", false)
	if x := b.Witness; x == nil {
		w.b = append(w.b, "null"...)
	} else {
		w.b = append(w.b, '{')
		w.key("detail_digest", true)
		w.nstr(x.DetailDigest, "witness.detail_digest")
		w.key("level", false)
		w.str(x.Level, "witness.level")
		w.key("method", false)
		w.str(x.Method, "witness.method")
		w.key("verdict", false)
		w.str(x.Verdict, "witness.verdict")
		w.b = append(w.b, '}')
	}
	w.b = append(w.b, '}')
}

func (w *jcsWriter) ref(r *Ref, path string) {
	w.b = append(w.b, '{')
	w.key("id", true)
	w.str(r.ID, path+".id")
	w.key("type", false)
	w.str(r.Type, path+".type")
	w.b = append(w.b, '}')
}

// tree writes one strict-tree value. Object members are sorted by UTF-16 code units. The path is
// kept as segments and joined only for an error message.
func (w *jcsWriter) tree(v any) {
	if w.err != nil {
		return
	}
	switch x := v.(type) {
	case nil:
		w.b = append(w.b, "null"...)
	case bool:
		w.boolean(x)
	case int64:
		if x < 0 || x > MaxSafeInteger {
			w.int(x, w.path())
			return
		}
		w.b = strconv.AppendInt(w.b, x, 10)
	case string:
		if !utf8.ValidString(x) {
			w.str(x, w.path())
			return
		}
		w.b = appendJCSString(w.b, x)
	case []any:
		w.b = append(w.b, '[')
		for i, e := range x {
			if i > 0 {
				w.b = append(w.b, ',')
			}
			w.segs = append(w.segs, seg{idx: i})
			w.tree(e)
			w.segs = w.segs[:len(w.segs)-1]
		}
		w.b = append(w.b, ']')
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			if !utf8.ValidString(k) {
				w.fail("input is not valid UTF-8", w.path())
				return
			}
			keys = append(keys, k)
		}
		slices.SortFunc(keys, compareUTF16)
		w.b = append(w.b, '{')
		for i, k := range keys {
			if i > 0 {
				w.b = append(w.b, ',')
			}
			w.b = appendJCSString(w.b, k)
			w.b = append(w.b, ':')
			w.segs = append(w.segs, seg{key: k, isKey: true})
			w.tree(x[k])
			w.segs = w.segs[:len(w.segs)-1]
		}
		w.b = append(w.b, '}')
	default:
		w.fail("a value that is not JSON", w.path())
	}
}

// seg is one step of a tree path: an object key or an array index.
type seg struct {
	key   string
	idx   int
	isKey bool
}

// path joins the segments the way the strict parser writes paths: a.b[2].c.
func (w *jcsWriter) path() string {
	var p []byte
	for _, s := range w.segs {
		if !s.isKey {
			p = append(p, '[')
			p = strconv.AppendInt(p, int64(s.idx), 10)
			p = append(p, ']')
			continue
		}
		if len(p) > 0 {
			p = append(p, '.')
		}
		p = append(p, s.key...)
	}
	return string(p)
}

// compareUTF16 orders two valid UTF-8 strings by their UTF-16 code units (RFC 8785 §3.2.3). That is
// byte order except where U+E000..U+FFFF (lead byte 0xEE or 0xEF) meets a supplementary character
// (lead byte 0xF0..0xF4, a surrogate pair from 0xD800): there UTF-16 puts the supplementary first.
// Equal prefixes put both strings on the same rune boundary, so the first differing bytes are
// both lead bytes whenever that case applies.
func compareUTF16(a, b string) int {
	n := min(len(a), len(b))
	for i := 0; i < n; i++ {
		x, y := a[i], b[i]
		if x == y {
			continue
		}
		if x >= 0xF0 && (y == 0xEE || y == 0xEF) {
			return -1
		}
		if y >= 0xF0 && (x == 0xEE || x == 0xEF) {
			return 1
		}
		if x < y {
			return -1
		}
		return 1
	}
	return len(a) - len(b)
}

// appendJCSString appends s (valid UTF-8) as ECMAScript JSON.stringify writes it: \b \t \n \f \r \"
// and \\ as two-character escapes, other C0 controls as \u00xx in lowercase hex, everything else
// (DEL, U+2028, U+2029 and all non-ASCII) as literal UTF-8.
func appendJCSString(dst []byte, s string) []byte {
	dst = append(dst, '"')
	start := 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 0x20 && c != '"' && c != '\\' {
			continue
		}
		dst = append(dst, s[start:i]...)
		switch c {
		case '"', '\\':
			dst = append(dst, '\\', c)
		case '\b':
			dst = append(dst, '\\', 'b')
		case '\t':
			dst = append(dst, '\\', 't')
		case '\n':
			dst = append(dst, '\\', 'n')
		case '\f':
			dst = append(dst, '\\', 'f')
		case '\r':
			dst = append(dst, '\\', 'r')
		default:
			const hex = "0123456789abcdef"
			dst = append(dst, '\\', 'u', '0', '0', hex[c>>4], hex[c&0xf])
		}
		start = i + 1
	}
	dst = append(dst, s[start:]...)
	return append(dst, '"')
}
