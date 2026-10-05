package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestVersionCheck runs every S35 §6 row against an httptest stand-in for GitHub; no network is used.
func TestVersionCheck(t *testing.T) {
	saved, savedT := githubReleasesAPI, versionCheckTimeout
	t.Cleanup(func() { githubReleasesAPI, versionCheckTimeout = saved, savedT })
	versionCheckTimeout = 300 * time.Millisecond
	cases := []struct {
		name, current string
		status        int
		body          string
		args          []string
		code          int
	}{
		{"newer", "0.1.0", 200, `{"tag_name":"v0.1.1","html_url":"x","prerelease":false,"draft":false}`, nil, 0},
		{"same", "0.1.0", 200, `{"tag_name":"v0.1.0","prerelease":false}`, nil, 0},
		{"older", "0.2.0", 200, `{"tag_name":"v0.1.0","prerelease":false}`, nil, 0},
		{"prerelease_ignored", "0.1.0", 200, `{"tag_name":"v0.2.0-rc.1","prerelease":true}`, nil, 0},
		{"prerelease_with_pre", "0.1.0", 200, `[{"tag_name":"v0.1.0"},{"tag_name":"v0.2.0-rc.2","prerelease":true},{"tag_name":"v0.2.0-rc.10","prerelease":true},{"tag_name":"v0.3.0","draft":true}]`, []string{"--pre"}, 0},
		{"no_releases", "0.1.0", 404, `{"message":"Not Found"}`, nil, 0},
		{"timeout", "0.1.0", -1, ``, nil, 2},
		{"malformed", "0.1.0", 200, `{"tag_name":`, nil, 2},
		{"server_error", "0.1.0", 502, `oops`, nil, 2},
		{"json_newer", "0.1.0", 200, `{"tag_name":"v0.1.1"}`, []string{"--json"}, 0},
		{"json_no_releases", "0.1.0", 404, `{}`, []string{"--json"}, 0},
		{"json_timeout", "0.1.0", -1, ``, []string{"--json"}, 2},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var ua, path string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				ua, path = r.Header.Get("User-Agent"), r.URL.RequestURI()
				if c.status < 0 {
					select {
					case <-r.Context().Done():
					case <-time.After(2 * time.Second):
					}
					return
				}
				w.WriteHeader(c.status)
				_, _ = w.Write([]byte(c.body))
			}))
			githubReleasesAPI = srv.URL + "/repos/deedglass/deedglass/releases"
			args := append([]string{"version", "--check"}, c.args...)
			r := cliWithVersion(t, c.current, args...)
			srv.Close() // waits for the handler, so ua and path are settled
			if r.code != c.code {
				t.Fatalf("exit %d, want %d: %+v", r.code, c.code, r)
			}
			if ua != "deedglass/"+c.current {
				t.Errorf("User-Agent %q: only the version may be sent", ua)
			}
			if wantPre := len(c.args) > 0 && c.args[0] == "--pre"; wantPre != strings.Contains(path, "per_page") {
				t.Errorf("path %q", path)
			}
			golden(t, "version_check/"+c.name, r)
		})
	}
	if r := cli(t, nil, false, "version", "--pre"); r.code != 2 {
		t.Fatalf("--pre without --check: %+v", r)
	}
}

func TestSemverCompare(t *testing.T) {
	order := []string{"0.0.9", "0.1.0-dev", "0.1.0-rc.1", "0.1.0-rc.2", "0.1.0-rc.10", "0.1.0", "0.1.1", "0.10.0", "1.0.0"}
	for i := range order {
		for j := range order {
			a, okA := parseSemver(order[i])
			b, okB := parseSemver(order[j])
			if !okA || !okB {
				t.Fatalf("parse %s %s", order[i], order[j])
			}
			want := 0
			if i < j {
				want = -1
			} else if i > j {
				want = 1
			}
			if got := a.compare(b); got != want {
				t.Errorf("%s vs %s = %d, want %d", order[i], order[j], got, want)
			}
		}
	}
	for _, bad := range []string{"", "1", "1.2", "1.2.x", "01.2.3", "1.2.3-", "v"} {
		if _, ok := parseSemver(bad); ok {
			t.Errorf("%q parsed", bad)
		}
	}
}
