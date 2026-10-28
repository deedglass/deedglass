package localstore

//deedglass:allow-exec the local store runs the PostgreSQL programs (postgres --version, initdb, pg_ctl) it manages for deedglass app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Where a set of PostgreSQL programs came from.
const (
	SourceRecorded   = "recorded"   // the programs store.json names
	SourceEnv        = "env"        // DEEDGLASS_PG_BIN
	SourceDownloaded = "downloaded" // RuntimeDir, from the one-time download or DEEDGLASS_PG_BUNDLE
	SourceSystem     = "system"     // a PostgreSQL already installed on this computer
)

// Binaries is a directory holding initdb, pg_ctl and postgres, and their version.
type Binaries struct {
	Dir     string `json:"dir"`
	Major   int    `json:"major"`
	Version string `json:"version"` // "18.6"
	Source  string `json:"source"`
}

func (b Binaries) path(name string) string {
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	return filepath.Join(b.Dir, name)
}

var versionRE = regexp.MustCompile(`\(PostgreSQL\) (\d+)(?:\.(\d+))?`)

// probe checks that dir holds the three programs and reads postgres --version.
func probe(ctx context.Context, dir, source string) (Binaries, error) {
	b := Binaries{Dir: dir, Source: source}
	for _, n := range []string{"initdb", "pg_ctl", "postgres"} {
		fi, err := os.Stat(b.path(n))
		if err != nil || fi.IsDir() {
			return b, fmt.Errorf("%s has no %s", dir, n)
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, b.path("postgres"), "--version")
	cmd.Env = childEnv(nil)
	out, err := cmd.Output()
	if err != nil {
		return b, fmt.Errorf("%s doesn't run: %v", b.path("postgres"), err)
	}
	m := versionRE.FindStringSubmatch(string(out))
	if m == nil {
		return b, fmt.Errorf("%s printed no version", b.path("postgres"))
	}
	b.Major, _ = strconv.Atoi(m[1])
	b.Version = m[1]
	if m[2] != "" {
		b.Version += "." + m[2]
	}
	return b, nil
}

// systemCandidates are directories where an installed PostgreSQL usually lives, newest first.
func systemCandidates(getenv func(string) string) []string {
	var out []string
	majors := []int{18, 17, 16}
	switch runtime.GOOS {
	case "darwin":
		for _, m := range majors {
			out = append(out, fmt.Sprintf("/opt/homebrew/opt/postgresql@%d/bin", m), fmt.Sprintf("/usr/local/opt/postgresql@%d/bin", m),
				fmt.Sprintf("/Applications/Postgres.app/Contents/Versions/%d/bin", m), fmt.Sprintf("/Library/PostgreSQL/%d/bin", m))
		}
	case "linux":
		for _, m := range majors {
			out = append(out, fmt.Sprintf("/usr/lib/postgresql/%d/bin", m), fmt.Sprintf("/usr/pgsql-%d/bin", m))
		}
	case "windows":
		pf := getenv("ProgramFiles")
		if pf == "" {
			pf = `C:\Program Files`
		}
		for _, m := range majors {
			out = append(out, filepath.Join(pf, "PostgreSQL", strconv.Itoa(m), "bin"))
		}
	}
	// PATH last: pg_ctl's directory, when it also has initdb and postgres.
	if p, err := exec.LookPath("pg_ctl"); err == nil {
		out = append(out, filepath.Dir(p))
	}
	return out
}

// FindSystem returns the newest PostgreSQL (16 or newer) installed on this computer.
func FindSystem(ctx context.Context, getenv func(string) string) (Binaries, error) {
	var found []Binaries
	seen := map[string]bool{}
	for _, d := range systemCandidates(getenv) {
		if seen[d] {
			continue
		}
		seen[d] = true
		b, err := probe(ctx, d, SourceSystem)
		if err == nil && b.Major >= MinSystemMajor {
			found = append(found, b)
		}
	}
	if len(found) == 0 {
		return Binaries{}, errors.New("no PostgreSQL 16 or newer is installed")
	}
	sort.SliceStable(found, func(i, j int) bool { return found[i].Major > found[j].Major })
	return found[0], nil
}

// childEnv is the environment for PostgreSQL's programs: no PG* variables from the person's shell
// (PGDATA or PGPORT would point them elsewhere), and the C locale, which the cluster uses and which
// keeps macOS's postmaster from refusing to start under a UTF-8-only locale setting.
func childEnv(extra []string) []string {
	var env []string
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		up := strings.ToUpper(k)
		if strings.HasPrefix(up, "PG") || strings.HasPrefix(up, "LC_") || up == "LANG" || strings.HasPrefix(up, "DEEDGLASS_") {
			continue
		}
		env = append(env, kv)
	}
	env = append(env, "LC_ALL=C", "LANG=C")
	return append(env, extra...)
}
