// Package buildinfo reports what this binary is: its version, the commit it was built from, and the
// API versions it serves (contract §8, dated versions).
//
// Invariants:
//   - Version, Commit and Date come from -ldflags -X at build time (see the Makefile). Commit and
//     the modified flag fall back to debug.ReadBuildInfo for plain `go build` and `go install`;
//     Date is the build time, so it has no fallback.
//   - LatestAPIVersion is the last entry of APIVersions; the list is sorted and never loses entries
//     while a version is still supported.
//   - Stdlib only (internal/archtest enforces it).
//
// Filled by: S01
package buildinfo

import (
	"runtime"
	"runtime/debug"
	"strings"
)

// Set with -ldflags "-X github.com/deedglass/deedglass/internal/buildinfo.Version=…" and friends.
var (
	Version  = "0.1.0-dev"
	Commit   = "" // full or short hash; "" means read it from the build info
	Date     = "" // RFC 3339 build time
	Modified = "" // "true" when built from a modified tree
)

// APIVersions lists the Deedglass-Version values this build serves, oldest first.
var APIVersions = []string{"2026-10-01"}

// LatestAPIVersion is the newest version in APIVersions.
var LatestAPIVersion = APIVersions[len(APIVersions)-1]

// Info is the resolved build description.
type Info struct {
	Object      string   `json:"object"`
	Version     string   `json:"version"`
	Commit      string   `json:"commit"`
	Modified    bool     `json:"modified"`
	Date        string   `json:"date,omitempty"`
	APIVersions []string `json:"api_versions"`
	Go          string   `json:"go"`
	OS          string   `json:"os"`
	Arch        string   `json:"arch"`
}

// Get resolves the build description.
func Get() Info { return resolve(readBuildInfo) }

var readBuildInfo = debug.ReadBuildInfo

func resolve(read func() (*debug.BuildInfo, bool)) Info {
	in := Info{
		Object:      "version",
		Version:     Version,
		Commit:      Commit,
		Modified:    Modified == "true",
		Date:        Date,
		APIVersions: append([]string(nil), APIVersions...),
		Go:          runtime.Version(),
		OS:          runtime.GOOS,
		Arch:        runtime.GOARCH,
	}
	if bi, ok := read(); ok {
		for _, s := range bi.Settings {
			switch s.Key {
			case "vcs.revision":
				if in.Commit == "" {
					in.Commit = s.Value
				}
			case "vcs.modified":
				if Modified == "" {
					in.Modified = s.Value == "true"
				}
			}
		}
		if in.Version == "0.1.0-dev" && bi.Main.Version != "" && bi.Main.Version != "(devel)" && !strings.Contains(bi.Main.Version, "+dirty") {
			in.Version = strings.TrimPrefix(bi.Main.Version, "v")
		}
	}
	if in.Commit == "" {
		in.Commit = "unknown"
	}
	if len(in.Commit) > 7 && in.Commit != "unknown" {
		in.Commit = in.Commit[:7]
	}
	return in
}

// SupportsAPIVersion reports whether v is one of APIVersions.
func SupportsAPIVersion(v string) bool {
	for _, a := range APIVersions {
		if a == v {
			return true
		}
	}
	return false
}
