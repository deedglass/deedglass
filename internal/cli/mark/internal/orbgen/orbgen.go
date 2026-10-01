// Package orbgen computes the Split Orb's pixel grid for the terminal mark (brand §5, §13.3;
// S05X §3.1) and writes it as Go source. It is pure maths: no image libraries, no randomness,
// so the output is byte-identical on every run (mark's TestGeneratedFresh checks the checked-in
// orb_gen.go against Generate).
package orbgen

import (
	"bytes"
	"fmt"
	"go/format"
	"math"
)

// Geometry. Pixel (x, y) covers [x, x+1) × [y, y+1).
const (
	Size = 20 // the grid is Size × Size pixels, rendered as 20 columns × 10 rows (founder, 2026-10-01: a crisp icon, not a poster)

	// The circle. cx puts the pane on column 9 with 8 columns on each side. cy = 10.5 puts the
	// orb on pixel rows 1..19, so the top row of cells holds only lower pixels (▄) and the
	// bottom row only full cells: block glyphs sit low in the cell in Terminal.app (the line
	// leading is above them), and an upper-only cell (▀) on the bottom edge would float.
	// Radius 9.5: the brief's 13.4 scaled to the smaller grid, then nudged so the top and bottom
	// rows are 2 pixels per half, not 1. Aspect is pixel height / pixel width in the terminal fonts we
	// checked (half-block pixels are close to square), so the vertical radius is Radius / Aspect.
	CX     = 9.5
	CY     = 10.5
	Radius = 9.5
	Aspect = 1.0

	PaneCol  = 9  // the pane column
	LeftMax  = 7  // left half: columns 0..LeftMax (column 8 is the gap)
	RightMin = 11 // right half: columns RightMin.. (column 10 is the gap)

	// Edges are hard: a pixel at least half inside the orb is drawn at full colour, anything
	// less is dropped. Blending partial pixels against an assumed background greyed the rim and
	// speckled the right half (founder, 2026-10-01).
	minCoverage = 0.5
	samples     = 4 // 4×4 supersampling
)

// Regions.
const (
	None  = 0
	Left  = 1
	Pane  = 2
	Right = 3
)

// Pixel is one generated pixel: its colour, its coverage (0 transparent, 255 drawn; edges are
// hard, never partial) and its region.
type Pixel struct {
	R, G, B  uint8
	Coverage uint8 // 0..255
	Region   uint8
	X256     uint8 // the nearest xterm-256 colour (16..255), by ΔE in OKLab
}

type rgb struct{ r, g, b float64 } // 0..1 sRGB

func hex(v uint32) rgb {
	return rgb{float64(v>>16&0xff) / 255, float64(v>>8&0xff) / 255, float64(v&0xff) / 255}
}

var (
	violet   = hex(0x7C6CFF)
	blue     = hex(0x4F8CFF)
	teal     = hex(0x2FE6D1)
	white    = hex(0xFFFFFF)
	paneDim  = hex(0xC9CCD6)
	glass    = hex(0x1C2233)
	glassRim = hex(0x3A4560)
	frost    = hex(0xC9CCD6)
	refract  = hex(0x3B6BFF)
	lightGls = hex(0x9AA3B8)
	lightRim = hex(0x6B7080)
)

// inside reports whether the point is inside the orb.
func inside(x, y float64) bool {
	dx, dy := x-CX, (y-CY)*Aspect
	return dx*dx+dy*dy <= Radius*Radius
}

// coverage is the share of the pixel's 4×4 samples inside the orb.
func coverage(px, py int) float64 {
	n := 0
	for i := 0; i < samples; i++ {
		for j := 0; j < samples; j++ {
			if inside(float64(px)+(float64(i)+0.5)/samples, float64(py)+(float64(j)+0.5)/samples) {
				n++
			}
		}
	}
	return float64(n) / (samples * samples)
}

func region(x int) uint8 {
	switch {
	case x <= LeftMax:
		return Left
	case x == PaneCol:
		return Pane
	case x >= RightMin:
		return Right
	}
	return None
}

// Grid returns both themes' grids, [y][x].
func Grid() (dark, light [Size][Size]Pixel) {
	var cov [Size][Size]float64
	for y := 0; y < Size; y++ {
		for x := 0; x < Size; x++ {
			if region(x) == None {
				continue
			}
			if c := coverage(x, y); c >= minCoverage {
				cov[y][x] = c
			}
		}
	}
	// The pane is as tall as the halves' inner edges.
	for y := 0; y < Size; y++ {
		if cov[y][LeftMax] == 0 || cov[y][RightMin] == 0 {
			cov[y][PaneCol] = 0
		}
	}
	// The pane's first and last pixels are dimmed to suggest the glow.
	paneTop, paneBottom := -1, -1
	for y := 0; y < Size; y++ {
		if cov[y][PaneCol] > 0 {
			if paneTop < 0 {
				paneTop = y
			}
			paneBottom = y
		}
	}
	// rim: the pixel is on the outer arc (a horizontal or vertical neighbour on the arc side is
	// outside the orb). The flat inner edges are not rim.
	rim := func(x, y int) bool {
		out := func(nx, ny int) bool {
			return nx < 0 || ny < 0 || nx >= Size || ny >= Size || cov[ny][nx] == 0
		}
		r := region(x)
		up, down := out(x, y-1), out(x, y+1)
		switch r {
		case Left:
			return out(x-1, y) || up || down
		case Right:
			return out(x+1, y) || up || down
		}
		return false
	}
	for y := 0; y < Size; y++ {
		for x := 0; x < Size; x++ {
			if cov[y][x] == 0 {
				continue
			}
			r := region(x)
			var d, l rgb
			switch r {
			case Left:
				d = aurora(x, y)
				if rim(x, y) {
					d = mix(d, white, 0.25)
				}
				l = d
			case Pane:
				d = white
				if y == paneTop || y == paneBottom {
					d = paneDim
				}
				l = d // light terminals draw the pane in the terminal's foreground; colour unused
			case Right:
				d, l = rightGlass(x, y, rim(x, y))
			}
			dark[y][x] = pixel(d, 1, r)
			light[y][x] = pixel(l, 1, r)
		}
	}
	return dark, light
}

// aurora is the left half: violet → blue → teal top to bottom, with a +0.15·x skew so it reads
// lit from the upper left, interpolated in OKLab.
func aurora(x, y int) rgb {
	fx := (float64(x) + 0.5) / Size
	fy := (float64(y) + 0.5) / Size
	t := clamp((fy+0.15*fx-0.04)/1.04, 0, 1)
	// Blue holds the middle a little longer than an even split, as in the logo.
	const mid = 0.42
	if t < mid {
		return mixLab(violet, blue, t/mid)
	}
	return mixLab(blue, teal, (t-mid)/(1-mid))
}

// rightGlass is the right half: dark glass brightening toward the rim, a frost rim, and a soft
// refraction wedge near the pane at mid-height. Light terminals get a lighter glass and darker rim.
func rightGlass(x, y int, isRim bool) (dark, light rgb) {
	px, py := float64(x)+0.5, float64(y)+0.5
	dx, dy := px-CX, (py-CY)*Aspect
	d := math.Sqrt(dx*dx+dy*dy) / Radius // 0 at the centre, 1 at the edge
	base := mix(glass, glassRim, smooth(clamp((d-0.55)/0.45, 0, 1)))
	// The wedge: strongest against the pane just below mid-height, fading right and away.
	w := clamp(1-(px-RightMin)/(0.2*Size)-math.Abs(py-CY-0.04*Size)/(0.25*Size), 0, 1)
	dark = mix(base, refract, 0.6*smooth(w))
	light = mix(lightGls, refract, 0.35*smooth(w))
	if isRim {
		dark, light = frost, lightRim
	}
	return dark, light
}

func pixel(c rgb, cov float64, r uint8) Pixel {
	R, G, B := to8(c.r), to8(c.g), to8(c.b)
	return Pixel{R: R, G: G, B: B, Coverage: to8(cov), Region: r, X256: Nearest256(R, G, B)}
}

func to8(v float64) uint8 { return uint8(math.Round(clamp(v, 0, 1) * 255)) }

func mix(a, b rgb, t float64) rgb {
	return rgb{a.r + (b.r-a.r)*t, a.g + (b.g-a.g)*t, a.b + (b.b-a.b)*t}
}

func mixLab(a, b rgb, t float64) rgb {
	la, lb := toLab(a), toLab(b)
	return fromLab([3]float64{la[0] + (lb[0]-la[0])*t, la[1] + (lb[1]-la[1])*t, la[2] + (lb[2]-la[2])*t})
}

func smooth(t float64) float64 { return t * t * (3 - 2*t) }

func clamp(v, lo, hi float64) float64 { return math.Max(lo, math.Min(hi, v)) }

// --- OKLab (Björn Ottosson, 2020) ---

func lin(c float64) float64 {
	if c <= 0.04045 {
		return c / 12.92
	}
	return math.Pow((c+0.055)/1.055, 2.4)
}

func gam(c float64) float64 {
	if c <= 0.0031308 {
		return 12.92 * c
	}
	return 1.055*math.Pow(c, 1/2.4) - 0.055
}

func toLab(c rgb) [3]float64 {
	r, g, b := lin(c.r), lin(c.g), lin(c.b)
	l := math.Cbrt(0.4122214708*r + 0.5363325363*g + 0.0514459929*b)
	m := math.Cbrt(0.2119034982*r + 0.6806995451*g + 0.1073969566*b)
	s := math.Cbrt(0.0883024619*r + 0.2817188376*g + 0.6299787005*b)
	return [3]float64{
		0.2104542553*l + 0.7936177850*m - 0.0040720468*s,
		1.9779984951*l - 2.4285922050*m + 0.4505937099*s,
		0.0259040371*l + 0.7827717662*m - 0.8086757660*s,
	}
}

func fromLab(c [3]float64) rgb {
	l := c[0] + 0.3963377774*c[1] + 0.2158037573*c[2]
	m := c[0] - 0.1055613458*c[1] - 0.0638541728*c[2]
	s := c[0] - 0.0894841775*c[1] - 1.2914855480*c[2]
	l, m, s = l*l*l, m*m*m, s*s*s
	return rgb{
		gam(+4.0767416621*l - 3.3077115913*m + 0.2309699292*s),
		gam(-1.2684380046*l + 2.6097574011*m - 0.3413193965*s),
		gam(-0.0041960863*l - 0.7034186147*m + 1.7076147010*s),
	}
}

// Xterm256 returns the sRGB value of xterm colour n (16..255): the 6×6×6 cube and the greys.
func Xterm256(n int) (r, g, b uint8) {
	if n >= 232 {
		v := uint8(8 + 10*(n-232))
		return v, v, v
	}
	n -= 16
	level := [6]uint8{0, 95, 135, 175, 215, 255}
	return level[n/36], level[n/6%6], level[n%6]
}

// Nearest256 picks the xterm colour (16..255; 0..15 depend on the terminal theme) with the
// smallest ΔE in OKLab.
func Nearest256(r, g, b uint8) uint8 {
	want := toLab(rgb{float64(r) / 255, float64(g) / 255, float64(b) / 255})
	best, bestD := 16, math.Inf(1)
	for n := 16; n < 256; n++ {
		cr, cg, cb := Xterm256(n)
		c := toLab(rgb{float64(cr) / 255, float64(cg) / 255, float64(cb) / 255})
		d := (c[0]-want[0])*(c[0]-want[0]) + (c[1]-want[1])*(c[1]-want[1]) + (c[2]-want[2])*(c[2]-want[2])
		if d < bestD {
			best, bestD = n, d
		}
	}
	return uint8(best)
}

// Text colours the renderer uses, with their 256 indices precomputed here.
var TextColours = []struct {
	Name string
	Hex  uint32
}{
	{"snow", 0xF5F6FA},
	{"slate400", 0x8B90A0},
	{"slate500", 0x6B7080},
	{"accentDark", 0x6F8DFF},
	{"accentLight", 0x2F5BEA},
	{"auroraBlue", 0x4F8CFF},
	{"frost", 0xC9CCD6},
	{"frostLight", 0x9AA3B8},
	{"paneWhite", 0xFFFFFF},
	{"verified", 0x34E3C9},
	{"verifiedLight", 0x0F766E},
}

// Generate returns the gofmt'd source of orb_gen.go.
func Generate() []byte {
	dark, light := Grid()
	var b bytes.Buffer
	b.WriteString("// Code generated by go run ./gen; DO NOT EDIT.\n\n")
	b.WriteString("package mark\n\n")
	fmt.Fprintf(&b, "// The orb: %d×%d pixels, centre (%.1f, %.1f), radius %.1f, aspect %.2f, pane column %d.\n",
		Size, Size, CX, CY, Radius, Aspect, PaneCol)
	b.WriteString("// Each pixel is {r, g, b, coverage, region, xterm-256}; coverage 0 is transparent.\n\n")
	grid := func(name, note string, g [Size][Size]Pixel) {
		fmt.Fprintf(&b, "// %s %s\nvar %s = [orbSize][orbSize]px{\n", name, note, name)
		for y := 0; y < Size; y++ {
			b.WriteString("\t{")
			for x := 0; x < Size; x++ {
				p := g[y][x]
				if x > 0 {
					b.WriteString(", ")
				}
				if p.Coverage == 0 {
					b.WriteString("{}")
					continue
				}
				fmt.Fprintf(&b, "{0x%02x, 0x%02x, 0x%02x, %d, %d, %d}", p.R, p.G, p.B, p.Coverage, p.Region, p.X256)
			}
			b.WriteString("},\n")
		}
		b.WriteString("}\n\n")
	}
	fmt.Fprintf(&b, "const orbSize = %d\n\n", Size)
	grid("orbDark", "is the palette for dark terminals.", dark)
	grid("orbLight", "is the palette for light terminals.", light)
	b.WriteString("// Text colours: {r, g, b, xterm-256}.\nvar (\n")
	for _, c := range TextColours {
		r, g, bb := uint8(c.Hex>>16), uint8(c.Hex>>8), uint8(c.Hex)
		fmt.Fprintf(&b, "\tc%s = colour{0x%02x, 0x%02x, 0x%02x, %d} // #%06X\n", c.Name, r, g, bb, Nearest256(r, g, bb), c.Hex)
	}
	b.WriteString(")\n")
	out, err := format.Source(b.Bytes())
	if err != nil {
		panic(err) // a generator bug, not an input problem
	}
	return out
}
