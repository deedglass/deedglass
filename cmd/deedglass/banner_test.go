package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/deedglass/deedglass/internal/config"
)

// The terminal mark in the CLI (S05X §6): where it shows, and everywhere it must not.
func TestBannerGoldens(t *testing.T) {
	truecolor := map[string]string{"COLORTERM": "truecolor", "TERM": "xterm-256color"}
	for _, c := range []struct {
		name string
		env  map[string]string
		args []string
	}{
		{"banner_bare", truecolor, nil},
		{"banner_bare_light", map[string]string{"COLORTERM": "truecolor", "DEEDGLASS_THEME": "light"}, nil},
		{"banner_bare_256", nil, nil},
		{"banner_bare_no_color", map[string]string{"NO_COLOR": "1"}, nil},
		{"banner_version", truecolor, []string{"version"}},
		{"banner_dash_version", truecolor, []string{"--version"}},
		{"help_inline_truecolor", truecolor, []string{"help"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := cli(t, c.env, true, c.args...)
			if r.code != 0 || r.stderr != "" {
				t.Fatalf("exit %d, stderr %q", r.code, r.stderr)
			}
			golden(t, c.name, r)
		})
	}
}

func TestBannerNeverForMachines(t *testing.T) {
	help := cli(t, nil, false).stdout // the plain help: bare deedglass off a TTY
	if help != cli(t, nil, false, "help").stdout {
		t.Fatal("bare deedglass off a TTY must be the plain help")
	}
	plainVersion := cli(t, nil, false, "version").stdout
	jsonVersion := cli(t, nil, false, "version", "--json").stdout
	truecolor := func(extra map[string]string) map[string]string {
		m := map[string]string{"COLORTERM": "truecolor", "TERM": "xterm-256color"}
		for k, v := range extra {
			m[k] = v
		}
		return m
	}
	for _, c := range []struct {
		name string
		env  map[string]string
		tty  bool
		args []string
		want string
	}{
		{"piped", nil, false, nil, help},
		{"help piped", truecolor(nil), false, []string{"help"}, help},
		{"piped with truecolor env", truecolor(nil), false, nil, help},
		{"CI", truecolor(map[string]string{"CI": "true"}), true, nil, ""},
		{"NO_BANNER", truecolor(map[string]string{"DEEDGLASS_NO_BANNER": "1"}), true, nil, ""},
		{"-q", truecolor(nil), true, []string{"-q"}, ""},
		{"--quiet", truecolor(nil), true, []string{"--quiet"}, ""},
		{"--json version", truecolor(nil), true, []string{"--json", "version"}, jsonVersion},
		{"version --json", truecolor(nil), true, []string{"version", "--json"}, jsonVersion},
		{"version piped", truecolor(nil), false, []string{"version"}, plainVersion},
		{"version -q", truecolor(nil), true, []string{"version", "-q"}, ""},
		{"--version -q", truecolor(nil), true, []string{"--version", "-q"}, ""},
		{"version CI", truecolor(map[string]string{"CI": "1"}), true, []string{"version"}, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := cli(t, c.env, c.tty, c.args...)
			if r.code != 0 {
				t.Fatalf("exit %d: %s", r.code, r.stderr)
			}
			if strings.Contains(r.stdout, "▀") || strings.Contains(r.stdout, "◖") || strings.Contains(r.stdout, "38;2;") {
				t.Errorf("decoration in output:\n%s", r.stdout)
			}
			if c.want != "" && r.stdout != c.want {
				t.Errorf("stdout differs from the plain output:\n%s", r.stdout)
			}
			if !c.tty && strings.Contains(r.stdout+r.stderr, "\x1b") {
				t.Error("escape codes off a TTY")
			}
		})
	}
	// -q on a TTY still prints the help or the plain version line.
	if r := cli(t, truecolor(nil), true, "-q"); !strings.Contains(r.stdout, "Usage") {
		t.Errorf("-q: want the help, got %q", r.stdout)
	}
	if r := cli(t, truecolor(nil), true, "version", "-q"); !strings.HasPrefix(r.stdout, "deedglass 0.1.0-dev · commit") {
		t.Errorf("version -q: got %q", r.stdout)
	}
}

func TestBannerNoColor(t *testing.T) {
	for _, c := range []struct {
		env  map[string]string
		args []string
	}{
		{map[string]string{"NO_COLOR": "1", "COLORTERM": "truecolor"}, nil},
		{map[string]string{"COLORTERM": "truecolor"}, []string{"--no-color"}},
		{map[string]string{"TERM": "dumb"}, nil},
		{map[string]string{"NO_COLOR": "1"}, []string{"version"}},
		{map[string]string{"NO_COLOR": "1"}, []string{"help"}},
	} {
		r := cli(t, c.env, true, c.args...)
		if strings.Contains(r.stdout, "\x1b") {
			t.Errorf("%v %v: escape codes with colour off", c.env, c.args)
		}
		if r.code != 0 {
			t.Errorf("%v: exit %d", c.args, r.code)
		}
	}
}

func TestBannerNarrow(t *testing.T) {
	r := cliWidth(t, 50, "")
	if strings.Contains(r, "▀") || !strings.Contains(r, "◖") || !strings.Contains(r, "See what your agents actually did.") {
		t.Errorf("width 50:\n%s", r)
	}
	r = cliWidth(t, 30, "")
	if strings.Count(r, "\n") != 2 || strings.Contains(r, "▀") {
		t.Errorf("width 30:\n%s", r)
	}
}

// cliWidth runs bare deedglass on a truecolor TTY of the given width.
func cliWidth(t *testing.T, width int, _ string) string {
	t.Helper()
	var out, errb bytes.Buffer
	env := map[string]string{"COLORTERM": "truecolor", "TERM": "xterm-256color"}
	e := &Env{Stdout: &out, Stderr: &errb, IsTTY: true, Width: width,
		Getenv:      func(k string) string { return env[k] },
		LoadOptions: func() config.LoadOptions { return config.LoadOptions{Env: env, FS: fstest.MapFS{}, Dir: t.TempDir()} },
	}
	if code := run(context.Background(), e, nil); code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	return out.String()
}

func TestTermWidthFallback(t *testing.T) {
	env := func(v string) func(string) string { return func(string) string { return v } }
	if n := termWidth(nil, env("120")); n != 120 {
		t.Errorf("COLUMNS=120 gave %d", n)
	}
	if n := termWidth(nil, env("x")); n != 0 {
		t.Errorf("junk COLUMNS gave %d", n)
	}
}

func TestBannerOnlyOnce(t *testing.T) {
	r := cli(t, map[string]string{"COLORTERM": "truecolor"}, true, "--version")
	if strings.Count(r.stdout, "deedglass") > 2 { // the wordmark, and nothing repeats it
		t.Errorf("banner printed more than once:\n%s", r.stdout)
	}
}
