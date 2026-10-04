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
}

var badgeColors = map[string]string{"A": "brightgreen", "B": "green", "C": "yellow", "D": "orange", "F": "red"}

// NewBadge is the badge for a grade.
func NewBadge(grade string) Badge {
	return Badge{SchemaVersion: 1, Label: "blast radius", Message: grade, Color: badgeColors[grade]}
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

// BadgeMarkdown is the one line the user pastes into their README.
func BadgeMarkdown(r Remote) string {
	owner, repo, branch := "<owner>", "<repo>", "<branch>"
	if r.OK {
		owner, repo, branch = r.Owner, r.Repo, r.Branch
	}
	return "[![Blast radius](https://img.shields.io/endpoint?url=https://raw.githubusercontent.com/" + owner + "/" + repo + "/" + branch + "/.deedglass/score.json)](https://deedglass.com/score)"
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
