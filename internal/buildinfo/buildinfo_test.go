package buildinfo

import (
	"runtime/debug"
	"sort"
	"testing"
)

func TestResolve(t *testing.T) {
	saved := [4]string{Version, Commit, Date, Modified}
	t.Cleanup(func() { Version, Commit, Date, Modified = saved[0], saved[1], saved[2], saved[3] })

	fromBuild := func() (*debug.BuildInfo, bool) {
		return &debug.BuildInfo{Main: debug.Module{Version: "(devel)"}, Settings: []debug.BuildSetting{
			{Key: "vcs.revision", Value: "5495f7e0123456789abcdef"},
			{Key: "vcs.modified", Value: "true"},
			{Key: "vcs.time", Value: "2026-10-01T00:00:00Z"},
		}}, true
	}
	cases := []struct {
		name                      string
		version, commit, modified string
		read                      func() (*debug.BuildInfo, bool)
		wantVersion, wantCommit   string
		wantModified              bool
	}{
		{"build info fallback", "0.1.0-dev", "", "", fromBuild, "0.1.0-dev", "5495f7e", true},
		{"ldflags win", "0.2.0", "abcdef1234", "false", fromBuild, "0.2.0", "abcdef1", false},
		{"no build info", "0.1.0-dev", "", "", func() (*debug.BuildInfo, bool) { return nil, false }, "0.1.0-dev", "unknown", false},
		{"go install version", "0.1.0-dev", "", "", func() (*debug.BuildInfo, bool) {
			return &debug.BuildInfo{Main: debug.Module{Version: "v0.3.1"}}, true
		}, "0.3.1", "unknown", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			Version, Commit, Modified, Date = tc.version, tc.commit, tc.modified, ""
			in := resolve(tc.read)
			if in.Version != tc.wantVersion || in.Commit != tc.wantCommit || in.Modified != tc.wantModified {
				t.Fatalf("got %+v", in)
			}
			if in.Object != "version" || in.APIVersions[0] != "2026-10-01" || in.Go == "" || in.OS == "" || in.Arch == "" {
				t.Fatalf("fields: %+v", in)
			}
		})
	}
	if Get().Object != "version" {
		t.Fatal("Get")
	}
}

func TestAPIVersions(t *testing.T) {
	if !sort.StringsAreSorted(APIVersions) || LatestAPIVersion != APIVersions[len(APIVersions)-1] {
		t.Fatalf("APIVersions %v latest %s", APIVersions, LatestAPIVersion)
	}
	if !SupportsAPIVersion("2026-10-01") || SupportsAPIVersion("2026-09-30") {
		t.Fatal("SupportsAPIVersion")
	}
}
