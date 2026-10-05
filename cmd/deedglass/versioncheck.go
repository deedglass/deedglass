package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/deedglass/deedglass/internal/connector/egress"
)

// `deedglass version --check` (S35 §3.3): opt-in, never implicit. One GET to GitHub's releases API
// through the egress door's first-party client, 3 s, with only a User-Agent: no identifiers, no
// key, nothing about the machine. Prereleases are ignored unless --pre. A 404 (the repository is
// private or has no release) is a calm "no public releases yet". Exit 0 when the check ran, 2 when
// it couldn't tell.

// githubReleasesAPI is a variable so tests point it at an httptest stand-in; nothing else sets it.
var githubReleasesAPI = "https://api.github.com/repos/deedglass/deedglass/releases"

// versionCheckTimeout bounds the whole check (tests shorten it).
var versionCheckTimeout = versionCheckDefault

const (
	releasesPage        = "https://github.com/deedglass/deedglass/releases"
	versionCheckDefault = 3 * time.Second
)

type ghRelease struct {
	TagName    string `json:"tag_name"`
	HTMLURL    string `json:"html_url"`
	Draft      bool   `json:"draft"`
	Prerelease bool   `json:"prerelease"`
}

type versionCheck struct {
	Object          string  `json:"object"`
	Current         string  `json:"current"`
	Latest          *string `json:"latest"`
	UpdateAvailable bool    `json:"update_available"`
	URL             string  `json:"url"`
}

func checkFailed(why string) error {
	return &ExitError{Code: ExitUsage, ErrCode: "update_check_failed", Plain: true,
		Err:  errors.New(why + " Nothing else was affected."),
		Next: "Try again later, or see " + releasesPage}
}

// failCheck prints S35 §5's calm ○ line and its next step (text mode); JSON keeps the envelope.
func failCheck(e *Env, why string) error {
	err := checkFailed(why)
	if e.Globals.JSON {
		return err
	}
	x := err.(*ExitError)
	fmt.Fprintln(e.Stderr, e.Style.UnknownLine(x.Err.Error()))
	fmt.Fprintln(e.Stderr, "  next  "+x.Next)
	x.printed = true
	return x
}

func runVersionCheck(ctx context.Context, e *Env, current string) error {
	rctx, cancel := context.WithTimeout(ctx, versionCheckTimeout)
	defer cancel()
	url := githubReleasesAPI + "/latest"
	if versionFlagValues.pre {
		url = githubReleasesAPI + "?per_page=30"
	}
	req, _ := http.NewRequestWithContext(rctx, http.MethodGet, url, nil)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	client := egress.FirstParty(egress.FirstPartyOptions{Timeout: versionCheckTimeout, Proxy: true,
		Redirects: egress.RedirectSameHost, UserAgent: "deedglass/" + current})
	res, err := client.Do(req)
	if err != nil {
		return failCheck(e, "Couldn't reach GitHub to check for updates.")
	}
	defer res.Body.Close()
	body, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return failCheck(e, "Couldn't reach GitHub to check for updates.")
	}
	out := versionCheck{Object: "version_check", Current: current, URL: releasesPage}
	switch {
	case res.StatusCode == http.StatusNotFound:
		return printVersionCheck(e, out, "")
	case res.StatusCode != http.StatusOK:
		return failCheck(e, fmt.Sprintf("GitHub answered %d, so the update check didn't finish.", res.StatusCode))
	}
	var rels []ghRelease
	if versionFlagValues.pre {
		err = json.Unmarshal(body, &rels)
	} else {
		var one ghRelease
		err = json.Unmarshal(body, &one)
		rels = []ghRelease{one}
	}
	if err != nil {
		return failCheck(e, "GitHub's answer couldn't be read, so the update check didn't finish.")
	}
	var best *ghRelease
	var bestV semver
	for i := range rels {
		r := &rels[i]
		v, ok := parseSemver(r.TagName)
		if !ok || r.Draft || ((r.Prerelease || v.pre != "") && !versionFlagValues.pre) {
			continue
		}
		if best == nil || v.compare(bestV) > 0 {
			best, bestV = r, v
		}
	}
	if best == nil {
		if len(rels) > 0 && rels[0].TagName == "" {
			return failCheck(e, "GitHub's answer couldn't be read, so the update check didn't finish.")
		}
		return printVersionCheck(e, out, "")
	}
	latest := strings.TrimPrefix(best.TagName, "v")
	out.Latest = &latest
	out.URL = releasesPage + "/tag/" + best.TagName
	cur, ok := parseSemver(current)
	out.UpdateAvailable = !ok || bestV.compare(cur) > 0
	return printVersionCheck(e, out, latest)
}

func printVersionCheck(e *Env, out versionCheck, latest string) error {
	if e.Globals.JSON {
		enc := json.NewEncoder(e.Stdout)
		enc.SetEscapeHTML(false)
		return enc.Encode(out)
	}
	s, w := e.Style, e.Stdout
	cur, _ := parseSemver(out.Current)
	lat, _ := parseSemver(latest)
	switch {
	case latest == "":
		fmt.Fprintln(w, s.UnknownLine("No public releases yet. You have deedglass "+out.Current+"."))
		fmt.Fprintln(w, "  next  See "+releasesPage)
	case out.UpdateAvailable:
		fmt.Fprintln(w, s.UnknownLine("deedglass "+latest+" is available. You have "+out.Current+"."))
		fmt.Fprintln(w, "  notes  "+out.URL)
	case lat.compare(cur) == 0:
		fmt.Fprintln(w, s.OKLine("deedglass "+out.Current+" is the latest release."))
	default:
		fmt.Fprintln(w, s.OKLine("deedglass "+out.Current+" is newer than the latest release, "+latest+"."))
	}
	return nil
}

// semver is the subset Deedglass tags use: MAJOR.MINOR.PATCH with an optional -prerelease.
type semver struct {
	n   [3]int
	pre string
}

func parseSemver(s string) (semver, bool) {
	s = strings.TrimPrefix(s, "v")
	if i := strings.IndexByte(s, '+'); i >= 0 {
		s = s[:i]
	}
	var v semver
	if i := strings.IndexByte(s, '-'); i >= 0 {
		v.pre, s = s[i+1:], s[:i]
		if v.pre == "" {
			return v, false
		}
	}
	parts := strings.Split(s, ".")
	if len(parts) != 3 {
		return v, false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 || (len(p) > 1 && p[0] == '0') {
			return v, false
		}
		v.n[i] = n
	}
	return v, true
}

// compare orders by SemVer 2.0.0 §11: a prerelease sorts before its release; identifiers compare
// numerically when both are numbers, otherwise as text, and a longer list wins a tie.
func (a semver) compare(b semver) int {
	for i := range a.n {
		if a.n[i] != b.n[i] {
			if a.n[i] < b.n[i] {
				return -1
			}
			return 1
		}
	}
	switch {
	case a.pre == b.pre:
		return 0
	case a.pre == "":
		return 1
	case b.pre == "":
		return -1
	}
	ap, bp := strings.Split(a.pre, "."), strings.Split(b.pre, ".")
	for i := 0; i < len(ap) && i < len(bp); i++ {
		if ap[i] == bp[i] {
			continue
		}
		an, aErr := strconv.Atoi(ap[i])
		bn, bErr := strconv.Atoi(bp[i])
		switch {
		case aErr == nil && bErr == nil:
			if an < bn {
				return -1
			}
			return 1
		case aErr == nil:
			return -1
		case bErr == nil:
			return 1
		case ap[i] < bp[i]:
			return -1
		default:
			return 1
		}
	}
	switch {
	case len(ap) < len(bp):
		return -1
	case len(ap) > len(bp):
		return 1
	}
	return 0
}
