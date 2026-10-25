package main

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// plainError is the last step before an error line reaches a person (docs/COPY_CATALOG.md › CLI
// style): it rewrites the Go and library phrasings that leak through wrapped errors into the
// sentence a person reads. Call sites should say it well first; this is the net under them, and
// TestNoRawGoErrors keeps both honest.
func plainError(s string) string {
	for _, r := range plainRewrites {
		s = r.re.ReplaceAllString(s, r.to)
	}
	return s
}

var plainRewrites = []struct {
	re *regexp.Regexp
	to string
}{
	// os.Open and friends: "open /x/y: no such file or directory".
	{regexp.MustCompile(`\b(?:open|stat|lstat|readdir) (\S+): no such file or directory`), "$1 doesn't exist"},
	{regexp.MustCompile(`\b(?:open|stat|lstat|readdir) (\S+): permission denied`), "$1 can't be read (permission denied)"},
	{regexp.MustCompile(`\b(?:open|stat|lstat) (\S+): is a directory`), "$1 is a folder, not a file"},
	{regexp.MustCompile(`: no such file or directory`), ": no such file"},
	// net: "dial tcp 127.0.0.1:5432: connect: connection refused".
	{regexp.MustCompile(`dial tcp (\S+?): connect: connection refused`), "nothing is listening at $1"},
	{regexp.MustCompile(`dial tcp (\S+?): i/o timeout`), "$1 didn't answer in time"},
	{regexp.MustCompile(`dial tcp: lookup (\S+?)(?: on \S+)?: no such host`), "there's no host called $1"},
	{regexp.MustCompile(`lookup (\S+?) on \S+: no such host`), "there's no host called $1"},
	{regexp.MustCompile(`\bi/o timeout\b`), "the connection timed out"},
	{regexp.MustCompile(`context deadline exceeded`), "it took too long"},
	{regexp.MustCompile(`\bunexpected EOF\b`), "the input ended early"},
	{regexp.MustCompile(`\bx509: [^.;]*`), "the server's TLS setup couldn't be checked"},
	{regexp.MustCompile(`\btls: [^.;]*`), "the TLS handshake failed"},
	// encoding/json.
	{regexp.MustCompile(`invalid character '[^']*' (?:looking for beginning of value|after [a-z ]+|in [a-z ]+)`), "isn't valid JSON"},
	{regexp.MustCompile(`json: cannot unmarshal (\w+) into Go (?:struct field|value of type) \S+`), "has a $1 where it can't"},
	{regexp.MustCompile(`json: unknown field "([^"]+)"`), `has a field it doesn't know, "$1"`},
	// strconv.
	{regexp.MustCompile(`strconv\.\w+: parsing "([^"]*)": invalid syntax`), `"$1" isn't a number`},
	{regexp.MustCompile(`strconv\.\w+: parsing "([^"]*)": value out of range`), `"$1" is out of range`},
	// pgx's DSN parser.
	{regexp.MustCompile("cannot parse `([^`]*)`: failed to parse as (?:keyword/value|URL)[^.]*"), "`$1` isn't a Postgres URL"},
	// flag package leftovers.
	{regexp.MustCompile(`flag provided but not defined: (-\S+)`), "there's no flag $1"},
}

// capitalize starts a sentence with a capital, except when it starts with a command, a flag, a path
// or an id, whose case matters.
func capitalize(s string) string {
	r, n := utf8.DecodeRuneInString(s)
	if n == 0 || !unicode.IsLower(r) {
		return s
	}
	first, _, _ := strings.Cut(s, " ")
	if first == "deedglass" || first == "npx" || strings.ContainsAny(first, "_./:=`") {
		return s
	}
	return string(unicode.ToUpper(r)) + s[n:]
}
