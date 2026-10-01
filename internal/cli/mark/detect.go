package mark

import (
	"runtime"
	"strconv"
	"strings"
	"time"
)

// Detect works out the Options for this terminal from the environment, whether stdout is a TTY
// and its width. It is pure: tests inject env. Callers then apply --json and -q (Quiet) and
// --no-color (DepthNone).
func Detect(env func(string) string, isTTY bool, width int) Options {
	return detect(env, isTTY, width, runtime.GOOS)
}

func detect(env func(string) string, isTTY bool, width int, goos string) Options {
	o := Options{Width: width, Depth: DepthNone}
	o.Quiet = !isTTY || truthy(env("CI")) || truthy(env("DEEDGLASS_NO_BANNER"))
	o.Theme, _ = ThemeFromEnv(env)
	if !isTTY {
		return o
	}
	term := env("TERM")
	switch ct := strings.ToLower(env("COLORTERM")); {
	case env("NO_COLOR") != "":
		o.Depth = DepthNone
	case term == "dumb":
		o.Depth = DepthASCII
	case goos == "windows" && env("WT_SESSION") == "" && term == "":
		// The Windows console may not have virtual-terminal support turned on, and we can't
		// ask it without x/sys; plain ASCII is always safe. Windows Terminal sets WT_SESSION.
		o.Depth = DepthASCII
	case ct == "truecolor" || ct == "24bit":
		o.Depth = DepthTrue
	case strings.Contains(term, "256color"):
		o.Depth = Depth256
	default:
		o.Depth = Depth16
	}
	o.Anim = !o.Quiet && o.Depth == DepthTrue && !truthy(env("DEEDGLASS_NO_ANIM"))
	return o
}

// ThemeFromEnv reads DEEDGLASS_THEME (light|dark), then COLORFGBG (a background of 7 or 15 is
// light). ok is false when neither says; the theme is then dark.
func ThemeFromEnv(env func(string) string) (t Theme, ok bool) {
	switch strings.ToLower(strings.TrimSpace(env("DEEDGLASS_THEME"))) {
	case "light":
		return ThemeLight, true
	case "dark":
		return ThemeDark, true
	}
	if v := env("COLORFGBG"); v != "" {
		parts := strings.Split(v, ";")
		if n, err := strconv.Atoi(parts[len(parts)-1]); err == nil {
			if n == 7 || n == 15 {
				return ThemeLight, true
			}
			return ThemeDark, true
		}
	}
	return ThemeDark, false
}

// Prober asks the terminal for its background colour (OSC 11). Implementations must give up
// after timeout and restore the terminal state on every path, signals included.
type Prober interface {
	Background(timeout time.Duration) (r, g, b uint8, ok bool)
}

// ProbeTimeout is the hard limit on the OSC 11 reply.
const ProbeTimeout = 50 * time.Millisecond

// WithProbe sets the theme from the terminal's answer when the environment didn't say. A nil
// prober, a quiet banner or no answer leave o unchanged (dark by default).
//
// This build has no prober: golang.org/x/term isn't a dependency, and S05X §3.3 says to skip the
// query rather than add one. The seam is here for when it is.
func (o Options) WithProbe(env func(string) string, p Prober) Options {
	if _, ok := ThemeFromEnv(env); ok || p == nil || o.Quiet {
		return o
	}
	r, g, b, ok := p.Background(ProbeTimeout)
	if !ok {
		return o
	}
	// Relative luminance (sRGB, rough): a light background is above the middle.
	if 0.2126*float64(r)+0.7152*float64(g)+0.0722*float64(b) > 127.5 {
		o.Theme = ThemeLight
	}
	return o
}

// truthy is set and not 0, false, no or off.
func truthy(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "", "0", "false", "no", "off":
		return false
	}
	return true
}
