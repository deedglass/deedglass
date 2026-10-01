package mark

import (
	"bytes"
	"flag"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/deedglass/deedglass/internal/cli/mark/internal/orbgen"
)

var update = flag.Bool("update", false, "rewrite testdata/*.golden")

func base(d Depth, t Theme, w int) Options {
	return Options{Depth: d, Theme: t, Width: w, Version: "0.1.0", API: "2026-10-01", Next: NextInit}
}

func checkGolden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", name+".golden")
	if *update {
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run go test ./internal/cli/mark -update)", err)
	}
	if got != string(want) {
		t.Errorf("%s differs from the golden (run go test ./internal/cli/mark -update and look at the diff)\n%s", name, got)
	}
}

func TestGoldens(t *testing.T) {
	cases := map[string]string{
		"banner.truecolor.dark":  Banner(base(DepthTrue, ThemeDark, 100)),
		"banner.truecolor.light": Banner(base(DepthTrue, ThemeLight, 100)),
		"banner.256.dark":        Banner(base(Depth256, ThemeDark, 100)),
		"banner.16.dark":         Banner(base(Depth16, ThemeDark, 100)),
		"banner.mono":            Banner(base(DepthNone, ThemeDark, 100)),
		"banner.ascii":           Banner(base(DepthASCII, ThemeDark, 100)),
		"banner.narrow50":        Banner(base(DepthTrue, ThemeDark, 50)),
		"banner.narrow30":        Banner(base(DepthTrue, ThemeDark, 30)),
		"banner.stacked70":       Banner(base(DepthTrue, ThemeDark, 70)),
		"banner.version":         Banner(Options{Depth: DepthTrue, Width: 100, Version: "0.1.0", API: "2026-10-01", Next: "commit 93c2bd7 · built 2026-10-02\ngo1.27.1 · darwin/arm64"}),
		"inline.truecolor":       Inline(Options{Depth: DepthTrue, Command: "doctor"}) + "\n",
		"inline.truecolor.light": Inline(Options{Depth: DepthTrue, Theme: ThemeLight, Command: "doctor"}) + "\n",
		"inline.16":              Inline(Options{Depth: Depth16, Command: "init"}) + "\n",
		"inline.mono":            Inline(Options{Depth: DepthNone, Command: "help"}) + "\n",
		"inline.ascii":           Inline(Options{Depth: DepthASCII, Command: "doctor"}) + "\n",
	}
	for name, got := range cases {
		t.Run(name, func(t *testing.T) { checkGolden(t, name, got) })
	}
}

var sgrRE = regexp.MustCompile(`\x1b\[([0-9;?]*)([A-Za-z])`)

func TestDepthCodes(t *testing.T) {
	for _, c := range []struct {
		d     Depth
		allow *regexp.Regexp
	}{
		{DepthTrue, regexp.MustCompile(`^(0|1|39|49|7|27|(1;)?[34]8;2;\d+;\d+;\d+|[34]8;2;\d+;\d+;\d+;[34]8;2;\d+;\d+;\d+|39;[34]8;2;\d+;\d+;\d+|[34]8;2;\d+;\d+;\d+;49|39;49)$`)},
		{Depth256, regexp.MustCompile(`^(0|1|39|49|7|27|(1;)?[34]8;5;\d+|[34]8;5;\d+;[34]8;5;\d+|39;48;5;\d+|38;5;\d+;49|39;49)$`)},
		{Depth16, regexp.MustCompile(`^(0|1|2|39|49|7|27|\d+|\d+;\d+)$`)},
	} {
		for _, th := range []Theme{ThemeDark, ThemeLight} {
			out := Banner(base(c.d, th, 100)) + Inline(Options{Depth: c.d, Theme: th, Command: "x"})
			for _, m := range sgrRE.FindAllStringSubmatch(out, -1) {
				if m[2] != "m" || !c.allow.MatchString(m[1]) {
					t.Errorf("depth %d theme %d: unexpected escape %q", c.d, th, m[0])
				}
			}
			if c.d == Depth256 && strings.Contains(out, ";2;") {
				t.Errorf("256: 24-bit code in output")
			}
			if c.d == Depth16 && (strings.Contains(out, "8;5;") || strings.Contains(out, "8;2;")) {
				t.Errorf("16: extended colour code in output")
			}
		}
	}
	for _, d := range []Depth{DepthNone, DepthASCII} {
		for _, w := range []int{0, 30, 50, 70, 100} {
			out := Banner(base(d, ThemeDark, w)) + Inline(Options{Depth: d, Command: "x"})
			if strings.Contains(out, "\x1b") {
				t.Errorf("depth %d width %d: escape codes without colour", d, w)
			}
		}
	}
	if out := Banner(base(DepthASCII, ThemeDark, 100)); !isASCII(out) {
		t.Errorf("ASCII depth printed non-ASCII:\n%s", out)
	}
}

func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			return false
		}
	}
	return true
}

func TestTruecolorShape(t *testing.T) {
	out := Banner(base(DepthTrue, ThemeDark, 100))
	lines := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
	if len(lines) != orbRows {
		t.Fatalf("%d lines, want %d", len(lines), orbRows)
	}
	for i, l := range lines {
		if !strings.Contains(l, "38;2;") {
			t.Errorf("row %d has no 24-bit colour", i)
		}
		if !strings.HasSuffix(l, "\x1b[0m") {
			t.Errorf("row %d doesn't end with a reset: %q", i, l[max(0, len(l)-12):])
		}
		if strings.Contains(l, "deedglass") {
			if col := visibleCol(l, "deedglass"); col < textCol {
				t.Errorf("text starts at column %d, want ≥ %d", col, textCol)
			}
		}
	}
}

// visibleCol is the display column where needle starts in an escaped line.
func visibleCol(line, needle string) int {
	plain := sgrRE.ReplaceAllString(line, "")
	i := strings.Index(plain, needle)
	return utf8.RuneCountInString(plain[:i])
}

// cell is what a tiny terminal emulator saw at one position.
type cell struct {
	r     rune
	bg    string // "" is the default background
	fg    string
	rev   bool
	valid bool
}

// emulate parses SGR and returns the grid of cells.
func emulate(out string) [][]cell {
	var rows [][]cell
	for _, line := range strings.Split(strings.TrimSuffix(out, "\n"), "\n") {
		var row []cell
		fg, bg, rev := "", "", false
		for len(line) > 0 {
			if m := sgrRE.FindStringSubmatchIndex(line); m != nil && m[0] == 0 {
				params := line[m[2]:m[3]]
				line = line[m[1]:]
				ps := strings.Split(params, ";")
				for i := 0; i < len(ps); i++ {
					switch p := ps[i]; {
					case p == "0" || p == "":
						fg, bg, rev = "", "", false
					case p == "39":
						fg = ""
					case p == "49":
						bg = ""
					case p == "7":
						rev = true
					case p == "27":
						rev = false
					case (p == "38" || p == "48") && i+1 < len(ps) && ps[i+1] == "2":
						v := strings.Join(ps[i+2:i+5], ";")
						if p == "38" {
							fg = v
						} else {
							bg = v
						}
						i += 4
					case (p == "38" || p == "48") && i+1 < len(ps) && ps[i+1] == "5":
						if p == "38" {
							fg = "5;" + ps[i+2]
						} else {
							bg = "5;" + ps[i+2]
						}
						i += 2
					default:
						if n, err := strconv.Atoi(p); err == nil && (n >= 40 && n <= 47 || n >= 100 && n <= 107) {
							bg = p
						} else if err == nil && (n >= 30 && n <= 37 || n >= 90 && n <= 97) {
							fg = p
						}
					}
				}
				continue
			}
			r, n := utf8.DecodeRuneInString(line)
			line = line[n:]
			row = append(row, cell{r, bg, fg, rev, true})
		}
		rows = append(rows, row)
	}
	return rows
}

func TestTransparency(t *testing.T) {
	for _, d := range []Depth{DepthTrue, Depth256, Depth16} {
		for _, th := range []Theme{ThemeDark, ThemeLight} {
			g := grid(th)
			rows := emulate(Banner(base(d, th, 100)))
			for y, row := range rows {
				for x := 0; x < orbSize && x < len(row); x++ {
					c := row[x]
					empty := g[2*y][x].cov == 0 && g[2*y+1][x].cov == 0
					if empty && (c.bg != "" || c.rev || c.r != ' ') {
						t.Errorf("depth %d theme %d: cell (%d,%d) outside the orb paints %q bg=%q rev=%v", d, th, x, y, c.r, c.bg, c.rev)
					}
					// Geometric check: a cell whose 4 corners are all well outside the circle.
					if outsideCircle(x, y) && c.bg != "" {
						t.Errorf("cell (%d,%d) is outside the circle but sets a background", x, y)
					}
				}
			}
		}
	}
}

func outsideCircle(x, y int) bool {
	for _, p := range [][2]float64{{0, 0}, {1, 0}, {0, 2}, {1, 2}, {0.5, 1}} {
		dx, dy := float64(x)+p[0]-orbgen.CX, (float64(2*y)+p[1]-orbgen.CY)*orbgen.Aspect
		if dx*dx+dy*dy <= orbgen.Radius*orbgen.Radius {
			return false
		}
	}
	return true
}

func TestLightNeverPaintsDarkBackground(t *testing.T) {
	out := Banner(base(DepthTrue, ThemeLight, 100))
	if strings.Contains(out, "7;8;12") {
		t.Error("light theme paints #07080C")
	}
	// The right half uses the light glass: at least one right-half pixel is near #9AA3B8.
	var found bool
	for y := 0; y < orbSize; y++ {
		for x := orbgen.RightMin; x < orbSize; x++ {
			if p := orbLight[y][x]; p.cov == 255 && p.r > 0x80 {
				found = true
			}
			if p := orbLight[y][x]; p.cov > 0 && p.r == 0x07 && p.g == 0x08 && p.b == 0x0c {
				t.Errorf("light pixel (%d,%d) is #07080C", x, y)
			}
		}
	}
	if !found {
		t.Error("the light right half isn't light glass")
	}
}

func TestSymmetry(t *testing.T) {
	for _, g := range []*[orbSize][orbSize]px{&orbDark, &orbLight} {
		var left, right, pane = map[int]bool{}, map[int]bool{}, map[int]bool{}
		for y := 0; y < orbSize; y++ {
			l, r := 0, 0
			for x := 0; x < orbSize; x++ {
				p := g[y][x]
				if p.cov == 0 {
					continue
				}
				switch p.region {
				case regionLeft:
					left[x] = true
					l++
				case regionRight:
					right[x] = true
					r++
				case regionPane:
					pane[x] = true
				}
			}
			if l != r {
				t.Errorf("row %d: %d left pixels, %d right", y, l, r)
			}
		}
		if len(left) != len(right) {
			t.Errorf("left half spans %d columns, right %d", len(left), len(right))
		}
		if len(pane) != 1 {
			t.Errorf("the pane spans %d columns, want 1", len(pane))
		}
		for _, gap := range []int{orbgen.LeftMax + 1, orbgen.RightMin - 1} {
			for y := 0; y < orbSize; y++ {
				if g[y][gap].cov != 0 {
					t.Errorf("gap column %d row %d is painted", gap, y)
				}
			}
		}
	}
}

// Hard edges: every pixel is fully on or off, so the rim never greys or speckles.
func TestHardEdges(t *testing.T) {
	for _, g := range []*[orbSize][orbSize]px{&orbDark, &orbLight} {
		rows := 0
		for y := 0; y < orbSize; y++ {
			on := false
			for x := 0; x < orbSize; x++ {
				if c := g[y][x].cov; c != 0 && c != 255 {
					t.Errorf("pixel (%d,%d) is partly covered (%d)", x, y, c)
				}
				on = on || g[y][x].cov > 0
			}
			if on {
				rows++
			}
		}
		if rows < 18 || rows > 20 {
			t.Errorf("the orb is %d pixels tall, want 18-20", rows)
		}
	}
}

func TestGeneratedFresh(t *testing.T) {
	a, b := orbgen.Generate(), orbgen.Generate()
	if !bytes.Equal(a, b) {
		t.Fatal("the generator isn't deterministic")
	}
	got, err := os.ReadFile("orb_gen.go")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, a) {
		t.Fatal("orb_gen.go is stale: run go generate ./internal/cli/mark")
	}
}

func TestNarrow(t *testing.T) {
	n50 := Banner(base(DepthTrue, ThemeDark, 50))
	if strings.Contains(n50, "▀") || !strings.Contains(n50, "◖") || !strings.Contains(n50, Tagline) {
		t.Errorf("width 50: want the inline mark and the text block, no orb:\n%s", n50)
	}
	n30 := Banner(base(DepthTrue, ThemeDark, 30))
	if lines := strings.Count(n30, "\n"); lines != 2 || strings.Contains(n30, "▀") {
		t.Errorf("width 30: want the inline mark and one line:\n%s", n30)
	}
	if Banner(Options{Quiet: true, Depth: DepthTrue}) != "" || Inline(Options{Quiet: true}) != "" {
		t.Error("quiet options rendered something")
	}
}

func TestDetect(t *testing.T) {
	type want struct {
		depth Depth
		theme Theme
		quiet bool
		anim  bool
	}
	cases := []struct {
		name string
		env  map[string]string
		tty  bool
		goos string
		want want
	}{
		{"truecolor", map[string]string{"COLORTERM": "truecolor", "TERM": "xterm-256color"}, true, "darwin", want{DepthTrue, ThemeDark, false, true}},
		{"24bit", map[string]string{"COLORTERM": "24bit"}, true, "linux", want{DepthTrue, ThemeDark, false, true}},
		{"TRUECOLOR upper", map[string]string{"COLORTERM": "TRUECOLOR"}, true, "linux", want{DepthTrue, ThemeDark, false, true}},
		{"256", map[string]string{"TERM": "xterm-256color"}, true, "darwin", want{Depth256, ThemeDark, false, false}},
		{"screen-256", map[string]string{"TERM": "screen-256color"}, true, "linux", want{Depth256, ThemeDark, false, false}},
		{"16", map[string]string{"TERM": "xterm"}, true, "linux", want{Depth16, ThemeDark, false, false}},
		{"empty TERM", map[string]string{}, true, "linux", want{Depth16, ThemeDark, false, false}},
		{"dumb", map[string]string{"TERM": "dumb"}, true, "linux", want{DepthASCII, ThemeDark, false, false}},
		{"NO_COLOR", map[string]string{"NO_COLOR": "1", "COLORTERM": "truecolor"}, true, "darwin", want{DepthNone, ThemeDark, false, false}},
		{"NO_COLOR empty is unset", map[string]string{"NO_COLOR": "", "COLORTERM": "truecolor"}, true, "darwin", want{DepthTrue, ThemeDark, false, true}},
		{"not a TTY", map[string]string{"COLORTERM": "truecolor"}, false, "darwin", want{DepthNone, ThemeDark, true, false}},
		{"CI=true", map[string]string{"CI": "true", "COLORTERM": "truecolor"}, true, "linux", want{DepthTrue, ThemeDark, true, false}},
		{"CI=1", map[string]string{"CI": "1", "TERM": "xterm"}, true, "linux", want{Depth16, ThemeDark, true, false}},
		{"CI=false", map[string]string{"CI": "false", "TERM": "xterm"}, true, "linux", want{Depth16, ThemeDark, false, false}},
		{"NO_BANNER", map[string]string{"DEEDGLASS_NO_BANNER": "1", "COLORTERM": "truecolor"}, true, "darwin", want{DepthTrue, ThemeDark, true, false}},
		{"NO_BANNER=0", map[string]string{"DEEDGLASS_NO_BANNER": "0", "COLORTERM": "truecolor"}, true, "darwin", want{DepthTrue, ThemeDark, false, true}},
		{"NO_ANIM", map[string]string{"DEEDGLASS_NO_ANIM": "1", "COLORTERM": "truecolor"}, true, "darwin", want{DepthTrue, ThemeDark, false, false}},
		{"theme light", map[string]string{"DEEDGLASS_THEME": "light", "COLORTERM": "truecolor"}, true, "darwin", want{DepthTrue, ThemeLight, false, true}},
		{"theme dark beats COLORFGBG", map[string]string{"DEEDGLASS_THEME": "dark", "COLORFGBG": "0;15"}, true, "darwin", want{Depth16, ThemeDark, false, false}},
		{"COLORFGBG light 15", map[string]string{"COLORFGBG": "0;15", "TERM": "xterm-256color"}, true, "darwin", want{Depth256, ThemeLight, false, false}},
		{"COLORFGBG light 7", map[string]string{"COLORFGBG": "0;default;7"}, true, "linux", want{Depth16, ThemeLight, false, false}},
		{"COLORFGBG dark", map[string]string{"COLORFGBG": "15;0"}, true, "linux", want{Depth16, ThemeDark, false, false}},
		{"theme auto falls through", map[string]string{"DEEDGLASS_THEME": "auto", "COLORFGBG": "0;15"}, true, "linux", want{Depth16, ThemeLight, false, false}},
		{"COLORFGBG junk", map[string]string{"COLORFGBG": "x;y"}, true, "linux", want{Depth16, ThemeDark, false, false}},
		{"windows console", map[string]string{}, true, "windows", want{DepthASCII, ThemeDark, false, false}},
		{"windows terminal", map[string]string{"WT_SESSION": "abc", "COLORTERM": "truecolor"}, true, "windows", want{DepthTrue, ThemeDark, false, true}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			o := detect(func(k string) string { return c.env[k] }, c.tty, 120, c.goos)
			got := want{o.Depth, o.Theme, o.Quiet, o.Anim}
			if got != c.want {
				t.Errorf("got %+v, want %+v", got, c.want)
			}
			if o.Width != 120 {
				t.Errorf("width %d", o.Width)
			}
		})
	}
}

type fakeProbe struct {
	r, g, b uint8
	ok      bool
	calls   int
}

func (f *fakeProbe) Background(d time.Duration) (uint8, uint8, uint8, bool) {
	f.calls++
	if d != ProbeTimeout {
		panic("timeout")
	}
	return f.r, f.g, f.b, f.ok
}

func TestWithProbe(t *testing.T) {
	none := func(string) string { return "" }
	white := &fakeProbe{r: 250, g: 250, b: 245, ok: true}
	if o := (Options{}).WithProbe(none, white); o.Theme != ThemeLight {
		t.Error("a white background should be light")
	}
	black := &fakeProbe{r: 7, g: 8, b: 12, ok: true}
	if o := (Options{}).WithProbe(none, black); o.Theme != ThemeDark {
		t.Error("a black background should be dark")
	}
	silent := &fakeProbe{}
	if o := (Options{}).WithProbe(none, silent); o.Theme != ThemeDark {
		t.Error("no answer should stay dark")
	}
	envSaid := &fakeProbe{r: 255, g: 255, b: 255, ok: true}
	(Options{}).WithProbe(func(k string) string {
		if k == "DEEDGLASS_THEME" {
			return "dark"
		}
		return ""
	}, envSaid)
	quiet := &fakeProbe{ok: true}
	(Options{Quiet: true}).WithProbe(none, quiet)
	if envSaid.calls != 0 || quiet.calls != 0 {
		t.Error("probed when the environment said, or when quiet")
	}
	if o := (Options{}).WithProbe(none, nil); o.Theme != ThemeDark {
		t.Error("nil prober")
	}
}

func TestSealed(t *testing.T) {
	o := base(DepthTrue, ThemeDark, 100)
	o.Anim = true
	var sleeps []time.Duration
	var w bytes.Buffer
	if err := sealed(&w, o, func(d time.Duration) { sleeps = append(sleeps, d) }, nil); err != nil {
		t.Fatal(err)
	}
	out := w.String()
	// 6 frames: each frame clears orbRows rows.
	if n := strings.Count(out, "\x1b[2K"); n != sealedFrames*orbRows {
		t.Errorf("%d cleared rows, want %d (6 frames)", n, sealedFrames*orbRows)
	}
	var total time.Duration
	for _, d := range sleeps {
		total += d
	}
	if len(sleeps) != sealedFrames-1 || total != 300*time.Millisecond {
		t.Errorf("sleeps %v (total %v), want 5 adding to 300ms", sleeps, total)
	}
	if !strings.HasPrefix(out, "\x1b[?25l") || !strings.HasSuffix(out, "\x1b[0m\x1b[?25h") {
		t.Error("cursor not hidden and restored")
	}
	if !strings.Contains(out, "✓\x1b[0m "+SealedLine) {
		t.Error("no Sealed line")
	}
	checkGolden(t, "sealed.final", finalFrameOnly(t, o))

	// DEEDGLASS_NO_ANIM: only the final frame.
	o.Anim = false
	if got := finalFrameOnly(t, o); strings.Count(got, "\x1b[2K") != orbRows || strings.Contains(got, "?25l") {
		t.Error("without Anim, want only the final frame and no cursor games")
	}
	// Quiet: one plain line.
	var q bytes.Buffer
	sealed(&q, Options{Quiet: true, Depth: DepthTrue}, nil, nil)
	if q.String() != "✓ "+SealedLine+"\n" {
		t.Errorf("quiet Sealed = %q", q.String())
	}
}

func finalFrameOnly(t *testing.T, o Options) string {
	t.Helper()
	o.Anim = false
	var w bytes.Buffer
	if err := sealed(&w, o, nil, nil); err != nil {
		t.Fatal(err)
	}
	return w.String()
}

func TestSealedInterrupted(t *testing.T) {
	o := base(DepthTrue, ThemeDark, 100)
	o.Anim = true
	stop := make(chan os.Signal, 1)
	stop <- os.Interrupt
	var w bytes.Buffer
	err := sealed(&w, o, func(time.Duration) {}, stop)
	if err != ErrInterrupted {
		t.Fatalf("err = %v", err)
	}
	if !strings.HasSuffix(w.String(), "\x1b[0m\x1b[?25h") {
		t.Error("cursor not restored after an interrupt")
	}
}

func TestNearest256(t *testing.T) {
	for _, c := range []struct {
		r, g, b uint8
		want    uint8
	}{{255, 255, 255, 231}, {0, 0, 0, 16}, {128, 128, 128, 244}} {
		if got := orbgen.Nearest256(c.r, c.g, c.b); got != c.want {
			t.Errorf("Nearest256(%d,%d,%d) = %d, want %d", c.r, c.g, c.b, got, c.want)
		}
	}
}

func BenchmarkBanner(b *testing.B) {
	o := base(DepthTrue, ThemeDark, 100)
	b.ReportAllocs()
	for b.Loop() {
		Banner(o)
	}
}

func TestBannerBudget(t *testing.T) {
	if testing.Short() || raceEnabled {
		t.Skip("timing")
	}
	o := base(DepthTrue, ThemeDark, 100)
	allocs := testing.AllocsPerRun(50, func() { Banner(o) })
	if allocs >= 50 {
		t.Errorf("Banner allocates %.0f times, budget < 50", allocs)
	}
	r := testing.Benchmark(BenchmarkBanner)
	if per := time.Duration(r.NsPerOp()); per > 200*time.Microsecond {
		t.Errorf("Banner takes %v, budget 200µs", per)
	}
}
