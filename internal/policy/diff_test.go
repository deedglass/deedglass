package policy

import (
	"fmt"
	"math/rand/v2"
	"strings"
	"testing"
)

func lines(xs ...string) []byte { return []byte(strings.Join(xs, "\n") + "\n") }

func TestDiffTable(t *testing.T) {
	cases := []struct {
		name    string
		a, b    []byte
		context int
		want    string
	}{
		{"same", lines("a", "b"), lines("a", "b"), 3, ""},
		{"empty both", nil, nil, 3, ""},
		{"insert middle", lines("a", "b", "c"), lines("a", "b", "x", "c"), 1, "@@ -2,2 +2,3 @@\n b\n+x\n c\n"},
		{"insert top", lines("a", "b"), lines("x", "a", "b"), 1, "@@ -1 +1,2 @@\n+x\n a\n"},
		{"append", lines("a", "b"), lines("a", "b", "x"), 1, "@@ -2 +2,2 @@\n b\n+x\n"},
		{"remove", lines("a", "b", "c"), lines("a", "c"), 0, "@@ -2 +1,0 @@\n-b\n"},
		{"replace", lines("a", "b", "c"), lines("a", "B", "c"), 0, "@@ -2 +2 @@\n-b\n+B\n"},
		{"from empty", nil, lines("x"), 3, "@@ -0,0 +1 @@\n+x\n"},
		{"two hunks", lines("1", "2", "3", "4", "5", "6", "7", "8", "9"), lines("1", "X", "3", "4", "5", "6", "7", "Y", "9"), 1,
			"@@ -1,3 +1,3 @@\n 1\n-2\n+X\n 3\n@@ -7,3 +7,3 @@\n 7\n-8\n+Y\n 9\n"},
		{"merged hunks", lines("1", "2", "3", "4", "5"), lines("1", "X", "3", "Y", "5"), 1, "@@ -1,5 +1,5 @@\n 1\n-2\n+X\n 3\n-4\n+Y\n 5\n"},
		{"crlf equal", []byte("a\r\nb\r\n"), lines("a", "b"), 3, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			u := Diff(c.a, c.b, c.context)
			if u.Text != c.want {
				t.Fatalf("got\n%q\nwant\n%q", u.Text, c.want)
			}
			if u.Empty() != (c.want == "") {
				t.Fatal("Empty")
			}
			p, err := ParseUnified(u.Text)
			if err != nil {
				t.Fatal(err)
			}
			if len(p.Hunks) != len(u.Hunks) || p.Added != u.Added || p.Removed != u.Removed {
				t.Fatalf("ParseUnified round trip: %+v vs %+v", p, u)
			}
			for i := range p.Hunks {
				if p.Hunks[i].OldStart != u.Hunks[i].OldStart || p.Hunks[i].NewLines != u.Hunks[i].NewLines || len(p.Hunks[i].Lines) != len(u.Hunks[i].Lines) {
					t.Fatalf("hunk %d: %+v vs %+v", i, p.Hunks[i], u.Hunks[i])
				}
			}
		})
	}
	if _, err := ParseUnified("not a diff"); err == nil {
		t.Fatal("ParseUnified accepted garbage")
	}
	if _, err := ParseUnified("@@ -1 +1 @@\n?x\n"); err == nil {
		t.Fatal("ParseUnified accepted a bad op")
	}
	if u := Diff(lines("a"), lines("b"), -1); len(u.Hunks) != 1 {
		t.Fatal("negative context")
	}
}

// TestDiffProperty: applying the edit script to a gives b, and the script is minimal enough that
// equal inputs give no hunks (random line edits, 2,000 cases).
func TestDiffProperty(t *testing.T) {
	r := rand.New(rand.NewPCG(7, 28))
	for i := 0; i < 2000; i++ {
		a := make([]string, r.IntN(30))
		for j := range a {
			a[j] = fmt.Sprint("l", r.IntN(8))
		}
		b := append([]string(nil), a...)
		for k := r.IntN(6); k > 0; k-- {
			switch r.IntN(3) {
			case 0:
				p := r.IntN(len(b) + 1)
				b = append(b[:p], append([]string{fmt.Sprint("n", r.IntN(8))}, b[p:]...)...)
			case 1:
				if len(b) > 0 {
					p := r.IntN(len(b))
					b = append(b[:p], b[p+1:]...)
				}
			default:
				if len(b) > 0 {
					b[r.IntN(len(b))] = fmt.Sprint("m", r.IntN(8))
				}
			}
		}
		es := myers(a, b)
		var gotA, gotB []string
		for _, e := range es {
			switch e.op {
			case ' ':
				gotA, gotB = append(gotA, a[e.a]), append(gotB, b[e.b])
			case '-':
				gotA = append(gotA, a[e.a])
			case '+':
				gotB = append(gotB, b[e.b])
			}
		}
		if strings.Join(gotA, ",") != strings.Join(a, ",") || strings.Join(gotB, ",") != strings.Join(b, ",") {
			t.Fatalf("case %d: the script doesn't rebuild the inputs:\na=%v\nb=%v", i, a, b)
		}
		u := Diff(lines(a...), lines(b...), 3)
		if strings.Join(a, ",") == strings.Join(b, ",") && !u.Empty() {
			t.Fatalf("case %d: equal inputs gave hunks", i)
		}
	}
}
