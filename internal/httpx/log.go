package httpx

import (
	"context"
	"io"
	"log/slog"
	"regexp"
	"strings"
)

// Logging: slog JSON in prod and text in dev, and every handler wrapped by NewRedactingHandler.

// redactedKeys are attribute names dropped from every record, compared case-insensitively.
var redactedKeys = map[string]bool{
	"authorization": true, "cookie": true, "set-cookie": true, "password": true, "secret": true,
	"token": true, "api_key": true, "dsn": true, "database_url": true,
}

var (
	apiKeyRE   = regexp.MustCompile(`dg_(live|test)_[0-9A-Za-z_]+`)
	userinfoRE = regexp.MustCompile(`://[^/@\s]+@`)
)

// Scrub removes API keys and URL passwords from a string.
func Scrub(s string) string {
	if strings.Contains(s, "dg_") {
		s = apiKeyRE.ReplaceAllString(s, "dg_${1}_[redacted]")
	}
	if strings.Contains(s, "://") {
		s = userinfoRE.ReplaceAllString(s, "://[redacted]@")
	}
	return s
}

// RedactingHandler drops secret-named attributes and scrubs secrets out of string values before
// they reach the wrapped handler.
type RedactingHandler struct{ next slog.Handler }

// NewRedactingHandler wraps h.
func NewRedactingHandler(h slog.Handler) *RedactingHandler { return &RedactingHandler{next: h} }

// NewLogger returns the server's logger: JSON in prod, text in dev, redacting either way.
func NewLogger(w io.Writer, prod bool, level slog.Leveler) *slog.Logger {
	o := &slog.HandlerOptions{Level: level}
	var h slog.Handler
	if prod {
		h = slog.NewJSONHandler(w, o)
	} else {
		h = slog.NewTextHandler(w, o)
	}
	return slog.New(NewRedactingHandler(h))
}

// Enabled implements slog.Handler.
func (h *RedactingHandler) Enabled(ctx context.Context, l slog.Level) bool {
	return h.next.Enabled(ctx, l)
}

// Handle implements slog.Handler.
func (h *RedactingHandler) Handle(ctx context.Context, r slog.Record) error {
	if clean(r) {
		return h.next.Handle(ctx, r) // the common case: nothing to redact, no copy
	}
	nr := slog.NewRecord(r.Time, r.Level, Scrub(r.Message), r.PC)
	r.Attrs(func(a slog.Attr) bool {
		if a, keep := redactAttr(a); keep {
			nr.AddAttrs(a)
		}
		return true
	})
	return h.next.Handle(ctx, nr)
}

// WithAttrs implements slog.Handler.
func (h *RedactingHandler) WithAttrs(as []slog.Attr) slog.Handler {
	out := make([]slog.Attr, 0, len(as))
	for _, a := range as {
		if a, keep := redactAttr(a); keep {
			out = append(out, a)
		}
	}
	return &RedactingHandler{next: h.next.WithAttrs(out)}
}

// WithGroup implements slog.Handler.
func (h *RedactingHandler) WithGroup(name string) slog.Handler {
	return &RedactingHandler{next: h.next.WithGroup(name)}
}

// clean reports whether r has nothing to drop or scrub.
func clean(r slog.Record) bool {
	if Scrub(r.Message) != r.Message {
		return false
	}
	ok := true
	r.Attrs(func(a slog.Attr) bool {
		if redactedKeys[a.Key] || needsRedaction(a) {
			ok = false
		}
		return ok
	})
	return ok
}

func needsRedaction(a slog.Attr) bool {
	if lk := strings.ToLower(a.Key); lk != a.Key && redactedKeys[lk] {
		return true
	}
	switch a.Value.Kind() {
	case slog.KindString:
		s := a.Value.String()
		return strings.Contains(s, "dg_") && apiKeyRE.MatchString(s) || strings.Contains(s, "://") && userinfoRE.MatchString(s)
	case slog.KindInt64, slog.KindUint64, slog.KindFloat64, slog.KindBool, slog.KindDuration, slog.KindTime:
		return false
	}
	return true // groups, LogValuers and Any values take the careful path
}

func redactAttr(a slog.Attr) (slog.Attr, bool) {
	if redactedKeys[strings.ToLower(a.Key)] {
		return a, false
	}
	v := a.Value.Resolve()
	switch v.Kind() {
	case slog.KindString:
		s := v.String()
		if t := Scrub(s); t != s {
			return slog.String(a.Key, t), true
		}
	case slog.KindGroup:
		var kept []slog.Attr
		for _, g := range v.Group() {
			if g, keep := redactAttr(g); keep {
				kept = append(kept, g)
			}
		}
		return slog.Attr{Key: a.Key, Value: slog.GroupValue(kept...)}, true
	case slog.KindAny:
		if err, ok := v.Any().(error); ok {
			return slog.String(a.Key, Scrub(err.Error())), true
		}
		if s, ok := v.Any().(interface{ String() string }); ok {
			return slog.String(a.Key, Scrub(s.String())), true
		}
	}
	return slog.Attr{Key: a.Key, Value: v}, true
}
