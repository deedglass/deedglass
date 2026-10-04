package saas

import (
	"errors"
	"strings"
	"unicode/utf8"
)

// MaxSegment is the longest path segment Segments accepts, in bytes.
const MaxSegment = 1024

// ErrSegment is a refused path segment.
var ErrSegment = errors.New("saas: a path segment is empty, '.', '..', longer than 1,024 bytes, or holds '/', a NUL or invalid UTF-8")

// Segments joins path segments into "/a/b/c". Each segment is one path element: it may not be
// empty, "." or "..", hold a '/', a NUL or invalid UTF-8, or be longer than MaxSegment bytes.
// The client escapes each segment on the wire, so a value can never add a path element.
func Segments(parts ...string) (string, error) {
	var b strings.Builder
	for _, p := range parts {
		if p == "" || p == "." || p == ".." || len(p) > MaxSegment || strings.ContainsAny(p, "/\x00") || !utf8.ValidString(p) {
			return "", ErrSegment
		}
		b.WriteByte('/')
		b.WriteString(p)
	}
	return b.String(), nil
}

// MustSegments is Segments for constant parts; it panics on a refused segment.
func MustSegments(parts ...string) string {
	s, err := Segments(parts...)
	if err != nil {
		panic(err)
	}
	return s
}
