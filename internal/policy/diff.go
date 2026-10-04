package policy

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Diff is the one line diff in the codebase (S28 §3.3): Myers' O(ND) algorithm over lines, shown as
// unified hunks with `context` lines around each change. Suggestions, `deedglass policy diff` and
// the Console's policy change view all read it. Lines are compared without their line endings.

// Unified is a diff as hunks and as unified text (without file headers).
type Unified struct {
	Hunks   []Hunk `json:"hunks"`
	Text    string `json:"text"`
	Added   int    `json:"added"`
	Removed int    `json:"removed"`
}

// Hunk is one block of changes with its context.
type Hunk struct {
	OldStart int        `json:"old_start"`
	OldLines int        `json:"old_lines"`
	NewStart int        `json:"new_start"`
	NewLines int        `json:"new_lines"`
	Lines    []DiffLine `json:"lines"`
}

// DiffLine is one line of a hunk: op " " (context), "+" (added) or "-" (removed).
type DiffLine struct {
	Op   string `json:"op"`
	Text string `json:"text"`
}

// Empty reports whether nothing changed.
func (u Unified) Empty() bool { return len(u.Hunks) == 0 }

func splitLines(b []byte) []string {
	s := strings.ReplaceAll(string(b), "\r\n", "\n")
	s = strings.TrimPrefix(s, "\ufeff")
	if s == "" {
		return nil
	}
	return strings.Split(strings.TrimSuffix(s, "\n"), "\n")
}

type edit struct {
	op   byte // ' ', '+', '-'
	a, b int  // 0-based line indexes in a (for ' ' and '-') and b (for ' ' and '+')
}

// myers returns the shortest edit script from a to b.
func myers(a, b []string) []edit {
	n, m := len(a), len(b)
	max := n + m
	if max == 0 {
		return nil
	}
	off := max
	v := make([]int, 2*max+2)
	var trace [][]int
	found := false
	for d := 0; d <= max && !found; d++ {
		trace = append(trace, append([]int(nil), v...))
		for k := -d; k <= d; k += 2 {
			var x int
			if k == -d || (k != d && v[off+k-1] < v[off+k+1]) {
				x = v[off+k+1]
			} else {
				x = v[off+k-1] + 1
			}
			y := x - k
			for x < n && y < m && a[x] == b[y] {
				x++
				y++
			}
			v[off+k] = x
			if x >= n && y >= m {
				found = true
				break
			}
		}
	}
	// Walk back through the saved frontiers.
	var es []edit
	x, y := n, m
	for d := len(trace) - 1; d >= 0 && (x > 0 || y > 0); d-- {
		vd := trace[d]
		k := x - y
		var pk int
		if k == -d || (k != d && vd[off+k-1] < vd[off+k+1]) {
			pk = k + 1
		} else {
			pk = k - 1
		}
		px := vd[off+pk]
		py := px - pk
		for x > px && y > py {
			x--
			y--
			es = append(es, edit{op: ' ', a: x, b: y})
		}
		if d > 0 {
			if x == px {
				y--
				es = append(es, edit{op: '+', a: x, b: y})
			} else {
				x--
				es = append(es, edit{op: '-', a: x, b: y})
			}
		}
	}
	for i, j := 0, len(es)-1; i < j; i, j = i+1, j-1 {
		es[i], es[j] = es[j], es[i]
	}
	return es
}

// Diff compares a and b line by line.
func Diff(a, b []byte, context int) Unified {
	if context < 0 {
		context = 0
	}
	al, bl := splitLines(a), splitLines(b)
	es := myers(al, bl)
	u := Unified{Hunks: []Hunk{}}
	// Group the changes into hunks with `context` lines around them.
	i := 0
	for i < len(es) {
		if es[i].op == ' ' {
			i++
			continue
		}
		start := max(0, i-context)
		end := i
		for end < len(es) {
			if es[end].op != ' ' {
				end++
				continue
			}
			// a run of context: does another change follow within 2*context?
			run := end
			for run < len(es) && es[run].op == ' ' {
				run++
			}
			if run < len(es) && run-end <= 2*context {
				end = run
				continue
			}
			end = min(len(es), end+context)
			break
		}
		h := Hunk{}
		for j := start; j < end; j++ {
			e := es[j]
			switch e.op {
			case ' ':
				h.Lines = append(h.Lines, DiffLine{Op: " ", Text: al[e.a]})
				h.OldLines++
				h.NewLines++
			case '-':
				h.Lines = append(h.Lines, DiffLine{Op: "-", Text: al[e.a]})
				h.OldLines++
				u.Removed++
			case '+':
				h.Lines = append(h.Lines, DiffLine{Op: "+", Text: bl[e.b]})
				h.NewLines++
				u.Added++
			}
		}
		first := es[start]
		h.OldStart, h.NewStart = first.a+1, first.b+1
		if h.OldLines == 0 {
			h.OldStart-- // unified convention: an empty old side names the line before
		}
		if h.NewLines == 0 {
			h.NewStart--
		}
		u.Hunks = append(u.Hunks, h)
		i = end
	}
	var sb strings.Builder
	for _, h := range u.Hunks {
		fmt.Fprintf(&sb, "@@ -%s +%s @@\n", rangeOf(h.OldStart, h.OldLines), rangeOf(h.NewStart, h.NewLines))
		for _, l := range h.Lines {
			sb.WriteString(l.Op)
			sb.WriteString(l.Text)
			sb.WriteByte('\n')
		}
	}
	u.Text = sb.String()
	return u
}

func rangeOf(start, n int) string {
	if n == 1 {
		return strconv.Itoa(start)
	}
	return strconv.Itoa(start) + "," + strconv.Itoa(n)
}

var hunkHeader = regexp.MustCompile(`^@@ -(\d+)(?:,(\d+))? \+(\d+)(?:,(\d+))? @@`)

// ParseUnified reads Diff's text back into hunks (suggestions store only the text).
func ParseUnified(text string) (Unified, error) {
	u := Unified{Hunks: []Hunk{}, Text: text}
	if text == "" {
		return u, nil
	}
	var h *Hunk
	for _, l := range strings.Split(strings.TrimSuffix(text, "\n"), "\n") {
		if m := hunkHeader.FindStringSubmatch(l); m != nil {
			u.Hunks = append(u.Hunks, Hunk{})
			h = &u.Hunks[len(u.Hunks)-1]
			h.OldStart, _ = strconv.Atoi(m[1])
			h.OldLines = 1
			if m[2] != "" {
				h.OldLines, _ = strconv.Atoi(m[2])
			}
			h.NewStart, _ = strconv.Atoi(m[3])
			h.NewLines = 1
			if m[4] != "" {
				h.NewLines, _ = strconv.Atoi(m[4])
			}
			continue
		}
		if h == nil || l == "" {
			return Unified{}, fmt.Errorf("policy: not a unified diff")
		}
		op := l[:1]
		switch op {
		case "+":
			u.Added++
		case "-":
			u.Removed++
		case " ":
		default:
			return Unified{}, fmt.Errorf("policy: not a unified diff")
		}
		h.Lines = append(h.Lines, DiffLine{Op: op, Text: l[1:]})
	}
	return u, nil
}
