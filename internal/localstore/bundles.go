package localstore

import (
	"fmt"
	"runtime"
)

// PGVersion is the PostgreSQL build the one-time download installs (the version Deedglass's own
// image and CI run, ADR-024).
const PGVersion = "18.6.0"

// MinSystemMajor is the oldest PostgreSQL found on the computer that the store will use.
const MinSystemMajor = 16

// Bundle is one platform's zonky.io PostgreSQL build on Maven Central: an Apache-2.0 packaging of
// the PostgreSQL-licensed programs, a .jar (a zip) holding one .txz.
type Bundle struct {
	Platform string // GOOS/GOARCH
	Artifact string // the Maven artifact suffix: darwin-arm64v8, linux-amd64, …
	Size     int64  // bytes of the .jar
	SHA256   string // of the .jar, pinned when this table was written (2026-10-28)
}

// bundles are pinned by size and SHA-256 (each matched Maven Central's own .sha1 when pinned). The
// macOS build is one universal file for both architectures; Unpack keeps only this machine's half.
var bundles = map[string]Bundle{
	"darwin/arm64":  {Platform: "darwin/arm64", Artifact: "darwin-arm64v8", Size: 71185174, SHA256: "8f6ed6f38195b50a40712ada8b2a12060b6193c1a6977a6eb5cff053bb5a1b89"},
	"darwin/amd64":  {Platform: "darwin/amd64", Artifact: "darwin-amd64", Size: 71185174, SHA256: "690941008aac9ee360820fde9bb7870a4f3dcac5e9e2f9e45e42333a436ad0f9"},
	"linux/amd64":   {Platform: "linux/amd64", Artifact: "linux-amd64", Size: 15119738, SHA256: "008ee189eaa7ca2b58bd01dd950291e638b998392cf982297acd4779ec898ac1"},
	"linux/arm64":   {Platform: "linux/arm64", Artifact: "linux-arm64v8", Size: 14142322, SHA256: "5f54e016723afa6213925a8acb939bac52944b1d348f65c7a20f0cd2928c31be"},
	"windows/amd64": {Platform: "windows/amd64", Artifact: "windows-amd64", Size: 25366735, SHA256: "b7ef2d03588e0439f0d0a2250f43905f7464f1e96ace47703ceaf79094198150"},
}

// mavenBase is where the bundles are downloaded from (tests point it at a local server).
var mavenBase = "https://repo1.maven.org/maven2/io/zonky/test/postgres"

// BundleFor returns this platform's bundle (ok false when none is published for it).
func BundleFor(goos, goarch string) (Bundle, bool) {
	b, ok := bundles[goos+"/"+goarch]
	return b, ok
}

// CurrentBundle is BundleFor(runtime.GOOS, runtime.GOARCH).
func CurrentBundle() (Bundle, bool) { return BundleFor(runtime.GOOS, runtime.GOARCH) }

// FileName is the .jar's file name.
func (b Bundle) FileName() string {
	return fmt.Sprintf("embedded-postgres-binaries-%s-%s.jar", b.Artifact, PGVersion)
}

// URL is where the .jar is downloaded from.
func (b Bundle) URL() string {
	return fmt.Sprintf("%s/embedded-postgres-binaries-%s/%s/%s", mavenBase, b.Artifact, PGVersion, b.FileName())
}

// MB is the download size in whole megabytes, for messages.
func (b Bundle) MB() int64 { return (b.Size + 500_000) / 1_000_000 }
