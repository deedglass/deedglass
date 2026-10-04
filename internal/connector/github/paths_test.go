package github

import (
	"path"
	"strings"
	"testing"

	"github.com/deedglass/deedglass/internal/connector/saas"
)

func TestBlobSHAVector(t *testing.T) {
	if got := BlobSHA([]byte("hello\n")); got != "ce013625030ba8dba906f756967f9e9ca394464a" {
		t.Fatalf("BlobSHA(hello) = %s", got)
	}
	if got := BlobSHA(nil); got != "e69de29bb2d1d6434b8b29ae775ad8c2e48c5391" { // git's empty blob
		t.Fatalf("BlobSHA(empty) = %s", got)
	}
}

func TestPathRules(t *testing.T) {
	ok := []string{"README.md", "docs/x.md", ".github/workflows/ci.yml", "a/b/c.txt", "café/menü.md", "日本/文書.md", "한국어.txt", ".gitignore", "a/.github", "x..y"}
	for _, p := range ok {
		if why := pathProblem(p); why != "" {
			t.Errorf("%q refused: %s", p, why)
		}
	}
	bad := []string{"", "../x", ".git/config", "/etc/passwd", `a\b`, "a/./b", "a/../b", "a//b", "a/", ".GIT/x", "a/.Git",
		"a\x00b", "a\nb", "a\x7fb", "a\u0085b", "é.md", "Å.md", "゙", "\xff", strings.Repeat("a", 1025), "x/҃"}
	for _, p := range bad {
		if pathProblem(p) == "" {
			t.Errorf("%q accepted", p)
		}
	}
	if pathProblem(strings.Repeat("a", 1024)) != "" {
		t.Fatal("1,024 bytes refused")
	}
}

func TestBranchRules(t *testing.T) {
	for _, b := range []string{"main", "agent/fix-typo", "release/1.2", "v1.0_x"} {
		if !validBranch(b) {
			t.Errorf("%q refused", b)
		}
	}
	for _, b := range []string{"", "HEAD", "a..b", "a//b", "/a", "a/", "a.", "-x", "a/.b", "x.lock", "a b", "a~1", "a:b", strings.Repeat("a", 256)} {
		if validBranch(b) {
			t.Errorf("%q accepted", b)
		}
	}
}

func TestMarker(t *testing.T) {
	m := Marker(testAction)
	if m != "<!-- deedglass:"+testAction+" -->" {
		t.Fatal(m)
	}
	cases := []struct {
		body, rest, id string
		found          bool
	}{
		{withMarker("Fails.", testAction), "Fails.", testAction, true},
		{withMarker("", testAction), "", testAction, true},
		{"Fails.\r\n\r\n" + m + "\r\n", "Fails.", testAction, true},
		{"Fails.", "Fails.", "", false},
		{"x <!-- deedglass: -->", "x <!-- deedglass: -->", "", false},
		{"x -->", "x -->", "", false},
		{"<!-- deedglass:a b -->", "<!-- deedglass:a b -->", "", false},
	}
	for _, tc := range cases {
		rest, id, found := stripMarker(tc.body)
		if rest != tc.rest || id != tc.id || found != tc.found {
			t.Errorf("stripMarker(%q) = %q %q %v", tc.body, rest, id, found)
		}
	}
}

func FuzzGitHubPath(f *testing.F) {
	for _, s := range []string{"docs/x.md", "../x", ".git/config", "/etc/passwd", `a\b`, "a/./b", ".github/workflows/ci.yml",
		"café.md", "é", "a/.GIT/b", "a//b", "x/..", "..", ".", "a/\x00", "日本/x"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, p string) {
		if pathProblem(p) != "" {
			return
		}
		if strings.HasPrefix(p, "/") || strings.Contains(p, `\`) || len(p) > MaxPathBytes {
			t.Fatalf("accepted %q", p)
		}
		if path.Clean(p) != p {
			t.Fatalf("accepted %q, which cleans to %q", p, path.Clean(p))
		}
		for _, seg := range strings.Split(p, "/") {
			if seg == ".." || seg == "." || strings.EqualFold(seg, ".git") {
				t.Fatalf("accepted %q with segment %q", p, seg)
			}
		}
		if _, err := saas.Segments(append([]string{"repos", "o", "r", "contents"}, segmentsOf(p)...)...); err != nil {
			t.Fatalf("accepted %q, which Segments refuses: %v", p, err)
		}
		for _, r := range p {
			if r >= 0x0300 && r <= 0x036f {
				t.Fatalf("accepted %q with a combining mark", p)
			}
		}
	})
}
