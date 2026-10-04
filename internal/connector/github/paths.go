package github

import (
	"crypto/sha1" //nolint:gosec // git's blob id is SHA-1 by definition; it is compared, never trusted for secrecy
	"encoding/hex"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

// MaxPathBytes is the longest file path the connector sends.
const MaxPathBytes = 1024

// pathProblem says why a repository file path is refused, or "" when it is accepted. A path is
// refused when it is empty or over 1,024 bytes, absolute, not valid UTF-8, holds a backslash or a
// control character, has an empty, "." , ".." or ".git" segment (any case), or might not be NFC.
func pathProblem(p string) string {
	switch {
	case p == "":
		return "is empty"
	case len(p) > MaxPathBytes:
		return "is longer than 1,024 bytes"
	case !utf8.ValidString(p):
		return "isn't valid UTF-8"
	case strings.HasPrefix(p, "/"):
		return "is absolute; send a path inside the repository, like docs/x.md"
	case strings.ContainsRune(p, '\\'):
		return "holds a backslash; use / between folders"
	}
	for _, r := range p {
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f) {
			return "holds a control character"
		}
	}
	for _, seg := range strings.Split(p, "/") {
		switch {
		case seg == "":
			return "has an empty segment"
		case seg == "." || seg == "..":
			return `has a "." or ".." segment`
		case strings.EqualFold(seg, ".git"):
			return "reaches into .git"
		}
	}
	if !nfcSafe(p) {
		return "might not be in Unicode NFC form; Deedglass accepts ASCII paths and a conservative set of precomposed letters"
	}
	return ""
}

// nfcSafe is a conservative NFC check without golang.org/x/text: an ASCII path is always NFC; a
// non-ASCII rune passes only when it belongs to blocks whose characters are all precomposed or
// have no decomposition, and so stay unchanged under NFC (Latin-1 Supplement letters, Latin
// Extended-A and -B, Greek and Cyrillic letters, Hiragana, Katakana, CJK ideographs, Hangul
// syllables). Combining marks (U+0300–U+036F and the other combining blocks), conjoining jamo,
// singletons that NFC replaces (U+2126 OHM, U+212B ANGSTROM, U+0340…) and everything else are
// refused. It can refuse a path that is NFC; it never accepts one that isn't.
func nfcSafe(p string) bool {
	for _, r := range p {
		if r >= utf8.RuneSelf && !nfcStable(r) {
			return false
		}
	}
	return true
}

func nfcStable(r rune) bool {
	switch {
	case r >= 0x00A0 && r <= 0x024F: // Latin-1 Supplement, Latin Extended-A and -B: precomposed or plain
		return true
	case r >= 0x0391 && r <= 0x03A9, r >= 0x03B1 && r <= 0x03C9: // basic Greek letters (no tonos forms)
		return true
	case r >= 0x0400 && r <= 0x0482, r >= 0x048A && r <= 0x04FF: // Cyrillic without its combining marks
		return true
	case r >= 0x3041 && r <= 0x3096, r >= 0x30A1 && r <= 0x30FA: // Hiragana and Katakana (not U+3099/U+309A)
		return true
	case r >= 0x4E00 && r <= 0x9FFF: // CJK Unified Ideographs
		return true
	case r >= 0xAC00 && r <= 0xD7A3: // Hangul syllables (already composed)
		return true
	}
	return false
}

var reBranch = regexp.MustCompile(`^[A-Za-z0-9._/-]{1,255}$`)

// validBranch is git check-ref-format's rules for the names the tools accept: ASCII letters,
// digits, ".", "_", "-" and "/", no "..", no "//", no segment starting with "." or ending with
// ".lock", not starting or ending with "/", and not HEAD.
func validBranch(b string) bool {
	if !reBranch.MatchString(b) || b == "HEAD" || strings.Contains(b, "..") || strings.Contains(b, "//") ||
		strings.HasPrefix(b, "/") || strings.HasSuffix(b, "/") || strings.HasSuffix(b, ".") || strings.HasPrefix(b, "-") {
		return false
	}
	for _, seg := range strings.Split(b, "/") {
		if strings.HasPrefix(seg, ".") || strings.HasSuffix(seg, ".lock") {
			return false
		}
	}
	return true
}

var reSHA = regexp.MustCompile(`^([0-9a-f]{40}|[0-9a-f]{64})$`)

// BlobSHA is git's blob id of content: sha1("blob " + len + "\x00" + content), what
// `git hash-object` prints. "hello\n" is ce013625030ba8dba906f756967f9e9ca394464a.
func BlobSHA(content []byte) string {
	h := sha1.New() //nolint:gosec // see the import
	h.Write([]byte("blob " + strconv.Itoa(len(content)) + "\x00"))
	h.Write(content)
	return hex.EncodeToString(h.Sum(nil))
}

// segmentsOf splits a validated path or branch into path segments for saas.Segments.
func segmentsOf(p string) []string { return strings.Split(p, "/") }
