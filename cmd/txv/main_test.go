package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestRun(t *testing.T) {
	cases := []struct {
		args       []string
		code       int
		out, errIn string
	}{
		{nil, 2, "", "usage: txv"},
		{[]string{"help"}, 0, "usage: txv", ""},
		{[]string{"version"}, 0, "txv " + version, ""},
		{[]string{"keygen", "--principal"}, 3, "", "txv keygen: not implemented"},
		{[]string{"executor", "serve"}, 3, "", "txv executor serve: not implemented"},
		{[]string{"verifier", "run"}, 3, "", "txv verifier run: not implemented"},
		{[]string{"statement", "verify", "s.cose"}, 3, "", "txv statement verify: not implemented"},
		{[]string{"evidence", "report"}, 3, "", "txv evidence report: not implemented"},
		{[]string{"executor"}, 2, "", `unknown command "executor"`},
		{[]string{"frobnicate"}, 2, "", `unknown command "frobnicate"`},
	}
	for _, tc := range cases {
		var out, errb bytes.Buffer
		code := run(tc.args, &out, &errb)
		if code != tc.code {
			t.Errorf("run(%v) = %d, want %d (stderr %q)", tc.args, code, tc.code, errb.String())
		}
		if tc.out != "" && !strings.Contains(out.String(), tc.out) {
			t.Errorf("run(%v) stdout %q, want it to contain %q", tc.args, out.String(), tc.out)
		}
		if tc.errIn != "" && !strings.Contains(errb.String(), tc.errIn) {
			t.Errorf("run(%v) stderr %q, want it to contain %q", tc.args, errb.String(), tc.errIn)
		}
	}
}

func TestEveryCommandDispatches(t *testing.T) {
	for _, c := range commands {
		var out, errb bytes.Buffer
		code := run(strings.Fields(c.name), &out, &errb)
		want := 3
		if c.name == "version" {
			want = 0
		}
		if code != want {
			t.Errorf("%s: exit %d, want %d", c.name, code, want)
		}
	}
}
