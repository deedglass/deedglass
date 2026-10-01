package policy

import (
	"fmt"
	"regexp"
	"unicode/utf8"
)

var slugRE = regexp.MustCompile(`^[a-z][a-z0-9-]{1,62}$`)

// isBidiOrInvisible reports bidirectional controls and invisible formatting characters
// (docs/POLICY_LANGUAGE.md §4.6, "Trojan Source").
func isBidiOrInvisible(r rune) bool {
	switch {
	case r == 0x061C, r >= 0x200B && r <= 0x200F, r >= 0x202A && r <= 0x202E,
		r >= 0x2060 && r <= 0x2069, r == 0xFEFF:
		return true
	}
	return false
}

// badChar returns the first refused character in s and a description, or ok. Tabs and line
// breaks are allowed only when multiline is set (descriptions and expressions).
func badChar(s string, multiline bool) (rune, string, bool) {
	if !utf8.ValidString(s) {
		return utf8.RuneError, "an invalid UTF-8 sequence", true
	}
	for _, r := range s {
		switch {
		case (r == '\t' || r == '\n' || r == '\r') && multiline:
		case r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0):
			return r, fmt.Sprintf("a control character (U+%04X)", r), true
		case isBidiOrInvisible(r):
			if r >= 0x202A && r <= 0x202E || r >= 0x2066 && r <= 0x2069 || r == 0x061C || r == 0x200E || r == 0x200F {
				return r, fmt.Sprintf("a bidirectional control character (U+%04X)", r), true
			}
			return r, fmt.Sprintf("an invisible character (U+%04X)", r), true
		}
	}
	return 0, "", false
}

func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			return false
		}
	}
	return true
}
