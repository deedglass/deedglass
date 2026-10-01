package harness

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"net/url"
	"sync"
)

// LogCapture keeps everything the server process wrote (its slog records on stderr and its
// summary lines on stdout), for the sentinel sweep and the line count.
type LogCapture struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

// Write implements io.Writer.
func (l *LogCapture) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.Write(p)
}

// Bytes returns a copy of everything captured.
func (l *LogCapture) Bytes() []byte {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]byte(nil), l.buf.Bytes()...)
}

// Lines is the number of lines captured.
func (l *LogCapture) Lines() int {
	return bytes.Count(l.Bytes(), []byte("\n"))
}

// Contains reports whether the capture has s.
func (l *LogCapture) Contains(s string) bool { return bytes.Contains(l.Bytes(), []byte(s)) }

// NewSentinel returns a fresh "dgSENTINEL…" value: planted in every fixture credential, and
// searched for everywhere afterwards.
func NewSentinel() string {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return "dgSENTINEL" + hex.EncodeToString(b[:])
}

// SentinelForms are the encodings a leak could take: as is, URL-escaped and base64.
func SentinelForms(s string) [][]byte {
	forms := []string{s, url.QueryEscape(s), base64.StdEncoding.EncodeToString([]byte(s)),
		base64.RawURLEncoding.EncodeToString([]byte(s)), base64.StdEncoding.EncodeToString([]byte("Bearer " + s))}
	out := make([][]byte, 0, len(forms))
	seen := map[string]bool{}
	for _, f := range forms {
		if !seen[f] {
			seen[f] = true
			out = append(out, []byte(f))
		}
	}
	return out
}

// CountSentinel counts the occurrences of every form of s in b.
func CountSentinel(b []byte, s string) int {
	n := 0
	for _, f := range SentinelForms(s) {
		n += bytes.Count(b, f)
	}
	return n
}

func newIdemKey() string {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return "attack_" + hex.EncodeToString(b[:])
}
