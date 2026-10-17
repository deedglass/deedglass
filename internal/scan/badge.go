package scan

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Badge is the shields.io endpoint body (schemaVersion 1) written to .deedglass/score.json.
type Badge struct {
	SchemaVersion int    `json:"schemaVersion"`
	Label         string `json:"label"`
	Message       string `json:"message"`
	Color         string `json:"color"`
	CacheSeconds  int    `json:"cacheSeconds"`
}

// badgeColors never use red: red means deny and doesn't match (brand §6.3, S47 §3.3). A and B are
// the matches teal, C neutral, D and F the can't-check amber.
var badgeColors = map[string]string{"A": "0F766E", "B": "0F766E", "C": "3F4453", "D": "9A5B00", "F": "9A5B00"}

// NewBadge is the badge for a grade. shields.io caches an endpoint for at least 300 s anyway.
func NewBadge(grade string) Badge {
	return Badge{SchemaVersion: 1, Label: "agent blast radius", Message: grade, Color: badgeColors[grade], CacheSeconds: 3600}
}

// GitignoreHidesBadge reports whether dir's .gitignore ignores the whole .deedglass directory with a
// directory pattern, which git can't re-include a file from (read as text; no git process).
func GitignoreHidesBadge(dir string) bool {
	b, err := os.ReadFile(filepath.Join(dir, ".gitignore"))
	if err != nil {
		return false
	}
	for _, l := range strings.Split(string(b), "\n") {
		switch strings.TrimSpace(l) {
		case ".deedglass/", ".deedglass", "/.deedglass/", "/.deedglass":
			return true
		}
	}
	return false
}

// Remote is the GitHub owner, repo and branch of the checkout at dir, read from .git/config and
// .git/HEAD (no git process, no network). OK is false without a github.com origin.
type Remote struct {
	Owner, Repo, Branch string
	OK                  bool
}

var reGitHub = regexp.MustCompile(`github\.com[:/]([A-Za-z0-9_.-]+)/([A-Za-z0-9_.-]+?)(?:\.git)?/?$`)

// ReadRemote finds the origin remote and current branch of the repository at dir.
func ReadRemote(dir string) Remote {
	cfg, err := os.ReadFile(filepath.Join(dir, ".git", "config"))
	if err != nil {
		return Remote{}
	}
	var url string
	in := false
	for _, l := range strings.Split(string(cfg), "\n") {
		t := strings.TrimSpace(l)
		if strings.HasPrefix(t, "[") {
			in = t == `[remote "origin"]`
			continue
		}
		if in && strings.HasPrefix(t, "url") {
			if _, v, ok := strings.Cut(t, "="); ok {
				url = strings.TrimSpace(v)
			}
		}
	}
	m := reGitHub.FindStringSubmatch(url)
	if m == nil {
		return Remote{}
	}
	head, _ := os.ReadFile(filepath.Join(dir, ".git", "HEAD"))
	branch := strings.TrimPrefix(strings.TrimSpace(string(head)), "ref: refs/heads/")
	if branch == "" || strings.Contains(branch, " ") || len(branch) == 40 {
		branch = "main"
	}
	return Remote{Owner: m[1], Repo: m[2], Branch: branch, OK: true}
}

// BadgeTarget is where the README badge links by default: the page that explains the grade, never
// one about this repository (PRD-09).
const BadgeTarget = "https://deedglass.com/score"

// BadgeMarkdown is the one line the user pastes into their README. link is where the badge goes:
// "" is BadgeTarget; scan --badge-share passes the owner's /s link (BadgeShare).
func BadgeMarkdown(r Remote, link string) string {
	owner, repo, branch := "<owner>", "<repo>", "<branch>"
	if r.OK {
		owner, repo, branch = r.Owner, r.Repo, r.Branch
	}
	if link == "" {
		link = BadgeTarget
	}
	return "[![agent blast radius](https://img.shields.io/endpoint?url=https%3A%2F%2Fraw.githubusercontent.com%2F" + owner + "%2F" + repo + "%2F" + branch + "%2F.deedglass%2Fscore.json)](" + link + ")"
}

// BadgeShare is the share payload a README badge may link to (scan --badge-share): the grade, the
// points and the finding counts only. A README is public, so no finding or deduction id goes in:
// those come from the configs and the database, and would tell anyone where to look (PRD-09).
func BadgeShare(r *Report) SharePayload {
	p := NewShare(r)
	p.F, p.D = []string{}, []ShareDed{}
	return p
}

// WriteBadge writes .deedglass/score.json under dir (mode 0644; the directory 0700 if new).
func WriteBadge(dir, grade string) (string, error) {
	d := filepath.Join(dir, ".deedglass")
	if fi, err := os.Lstat(d); err == nil && fi.Mode()&os.ModeSymlink != 0 {
		return "", os.ErrPermission
	}
	if err := os.MkdirAll(d, 0o700); err != nil {
		return "", err
	}
	b, _ := json.Marshal(NewBadge(grade))
	p := filepath.Join(d, "score.json")
	if err := os.WriteFile(p, append(b, '\n'), 0o644); err != nil {
		return "", err
	}
	return p, os.Chmod(p, 0o644)
}
