package github

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strings"
	"unicode/utf8"
)

// GitHub's payloads: only the fields the projections read. Emails, node ids and API URLs are
// never decoded, so they can't be echoed.
type (
	ghUser struct {
		Login string `json:"login"`
	}
	ghLabel struct {
		Name string `json:"name"`
	}
	ghRepo struct {
		FullName        string  `json:"full_name"`
		Private         bool    `json:"private"`
		DefaultBranch   string  `json:"default_branch"`
		Archived        bool    `json:"archived"`
		Description     *string `json:"description"`
		HTMLURL         string  `json:"html_url"`
		OpenIssuesCount int64   `json:"open_issues_count"`
		Permissions     *struct {
			Push bool `json:"push"`
		} `json:"permissions"`
	}
	ghIssue struct {
		Number      int64           `json:"number"`
		Title       string          `json:"title"`
		State       string          `json:"state"`
		StateReason *string         `json:"state_reason"`
		Body        *string         `json:"body"`
		Labels      []ghLabel       `json:"labels"`
		User        ghUser          `json:"user"`
		Comments    int64           `json:"comments"`
		HTMLURL     string          `json:"html_url"`
		CreatedAt   string          `json:"created_at"`
		ClosedAt    *string         `json:"closed_at"`
		PullRequest json.RawMessage `json:"pull_request"`
	}
	ghComment struct {
		ID       int64   `json:"id"`
		Body     *string `json:"body"`
		HTMLURL  string  `json:"html_url"`
		IssueURL string  `json:"issue_url"`
	}
	ghBranchRef struct {
		Ref string `json:"ref"`
		SHA string `json:"sha"`
	}
	ghPR struct {
		Number         int64       `json:"number"`
		Title          string      `json:"title"`
		State          string      `json:"state"`
		Body           *string     `json:"body"`
		Head           ghBranchRef `json:"head"`
		Base           ghBranchRef `json:"base"`
		Draft          bool        `json:"draft"`
		Merged         bool        `json:"merged"`
		Mergeable      *bool       `json:"mergeable"`
		MergeCommitSHA *string     `json:"merge_commit_sha"`
		User           ghUser      `json:"user"`
		HTMLURL        string      `json:"html_url"`
		CreatedAt      string      `json:"created_at"`
	}
	ghContent struct {
		Type     string `json:"type"`
		Name     string `json:"name"`
		Path     string `json:"path"`
		SHA      string `json:"sha"`
		Size     int64  `json:"size"`
		Encoding string `json:"encoding"`
		Content  string `json:"content"`
		HTMLURL  string `json:"html_url"`
	}
	ghRef struct {
		Ref    string `json:"ref"`
		Object struct {
			SHA string `json:"sha"`
		} `json:"object"`
	}
)

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func labelNames(ls []ghLabel) []string {
	out := make([]string, 0, len(ls))
	for _, l := range ls {
		out = append(out, l.Name)
	}
	return out
}

func isPull(raw json.RawMessage) bool { return len(raw) > 0 && string(raw) != "null" }

// Projections: small, stable, closed (each tool's OutputSchema).
type (
	repoOut struct {
		FullName        string `json:"full_name"`
		Private         bool   `json:"private"`
		DefaultBranch   string `json:"default_branch"`
		Archived        bool   `json:"archived"`
		Description     string `json:"description"`
		HTMLURL         string `json:"html_url"`
		OpenIssuesCount int64  `json:"open_issues_count"`
		CanPush         bool   `json:"can_push"`
	}
	issueItem struct {
		Number        int64    `json:"number"`
		Title         string   `json:"title"`
		State         string   `json:"state"`
		Labels        []string `json:"labels"`
		User          string   `json:"user"`
		IsPullRequest bool     `json:"is_pull_request"`
		HTMLURL       string   `json:"html_url"`
		CreatedAt     string   `json:"created_at"`
		Comments      int64    `json:"comments"`
	}
	issueList struct {
		Issues []issueItem `json:"issues"`
		Page   int         `json:"page"`
	}
	issueOut struct {
		Number        int64    `json:"number"`
		Title         string   `json:"title"`
		State         string   `json:"state"`
		StateReason   string   `json:"state_reason"`
		Body          string   `json:"body"`
		Labels        []string `json:"labels"`
		User          string   `json:"user"`
		Comments      int64    `json:"comments"`
		IsPullRequest bool     `json:"is_pull_request"`
		HTMLURL       string   `json:"html_url"`
		CreatedAt     string   `json:"created_at"`
		ClosedAt      string   `json:"closed_at"`
	}
	dirEntry struct {
		Name string `json:"name"`
		Path string `json:"path"`
		Type string `json:"type"`
	}
	fileOut struct {
		Path     string     `json:"path"`
		Type     string     `json:"type"`
		SHA      string     `json:"sha,omitempty"`
		Size     int64      `json:"size,omitempty"`
		Encoding string     `json:"encoding,omitempty"` // "text" or "digest"
		Content  *string    `json:"content,omitempty"`
		Digest   string     `json:"digest,omitempty"`
		HTMLURL  string     `json:"html_url,omitempty"`
		Entries  []dirEntry `json:"entries,omitempty"`
	}
	prItem struct {
		Number    int64  `json:"number"`
		Title     string `json:"title"`
		State     string `json:"state"`
		Head      string `json:"head"`
		Base      string `json:"base"`
		Draft     bool   `json:"draft"`
		User      string `json:"user"`
		HTMLURL   string `json:"html_url"`
		CreatedAt string `json:"created_at"`
	}
	prList struct {
		Pulls []prItem `json:"pulls"`
		Page  int      `json:"page"`
	}
	prOut struct {
		Number    int64  `json:"number"`
		Title     string `json:"title"`
		State     string `json:"state"`
		Body      string `json:"body"`
		Head      string `json:"head"`
		HeadSHA   string `json:"head_sha"`
		Base      string `json:"base"`
		Draft     bool   `json:"draft"`
		Merged    bool   `json:"merged"`
		Mergeable string `json:"mergeable"` // "yes", "no" or "unknown" (GitHub is still computing)
		User      string `json:"user"`
		HTMLURL   string `json:"html_url"`
		CreatedAt string `json:"created_at"`
	}
	createdIssue struct {
		Number  int64  `json:"number"`
		HTMLURL string `json:"html_url"`
		State   string `json:"state"`
		Title   string `json:"title"`
	}
	createdComment struct {
		ID          int64  `json:"id"`
		IssueNumber int64  `json:"issue_number"`
		HTMLURL     string `json:"html_url"`
	}
	createdBranch struct {
		Ref     string `json:"ref"`
		Branch  string `json:"branch"`
		SHA     string `json:"sha"`
		Existed bool   `json:"existed"`
	}
	writtenFile struct {
		Path      string `json:"path"`
		Branch    string `json:"branch"`
		BlobSHA   string `json:"blob_sha"`
		CommitSHA string `json:"commit_sha"`
		HTMLURL   string `json:"html_url"`
	}
	createdPR struct {
		Number  int64  `json:"number"`
		HTMLURL string `json:"html_url"`
		State   string `json:"state"`
		Title   string `json:"title"`
		Head    string `json:"head"`
		Base    string `json:"base"`
	}
	mergedPR struct {
		Number int64  `json:"number"`
		Merged bool   `json:"merged"`
		SHA    string `json:"sha"`
	}
	deletedBranch struct {
		Branch  string `json:"branch"`
		Deleted bool   `json:"deleted"`
	}
	closedIssue struct {
		Number      int64  `json:"number"`
		State       string `json:"state"`
		StateReason string `json:"state_reason"`
		HTMLURL     string `json:"html_url"`
	}
)

func projectRepo(r ghRepo) repoOut {
	return repoOut{FullName: r.FullName, Private: r.Private, DefaultBranch: r.DefaultBranch, Archived: r.Archived,
		Description: clip(deref(r.Description), 1000), HTMLURL: r.HTMLURL, OpenIssuesCount: r.OpenIssuesCount,
		CanPush: r.Permissions != nil && r.Permissions.Push}
}

func projectIssueItem(i ghIssue) issueItem {
	return issueItem{Number: i.Number, Title: i.Title, State: i.State, Labels: labelNames(i.Labels), User: i.User.Login,
		IsPullRequest: isPull(i.PullRequest), HTMLURL: i.HTMLURL, CreatedAt: i.CreatedAt, Comments: i.Comments}
}

func projectIssue(i ghIssue) issueOut {
	return issueOut{Number: i.Number, Title: i.Title, State: i.State, StateReason: deref(i.StateReason), Body: deref(i.Body),
		Labels: labelNames(i.Labels), User: i.User.Login, Comments: i.Comments, IsPullRequest: isPull(i.PullRequest),
		HTMLURL: i.HTMLURL, CreatedAt: i.CreatedAt, ClosedAt: deref(i.ClosedAt)}
}

func projectPRItem(p ghPR) prItem {
	return prItem{Number: p.Number, Title: p.Title, State: p.State, Head: p.Head.Ref, Base: p.Base.Ref, Draft: p.Draft,
		User: p.User.Login, HTMLURL: p.HTMLURL, CreatedAt: p.CreatedAt}
}

func projectPR(p ghPR) prOut {
	m := "unknown"
	if p.Mergeable != nil {
		m = map[bool]string{true: "yes", false: "no"}[*p.Mergeable]
	}
	return prOut{Number: p.Number, Title: p.Title, State: p.State, Body: deref(p.Body), Head: p.Head.Ref, HeadSHA: p.Head.SHA,
		Base: p.Base.Ref, Draft: p.Draft, Merged: p.Merged, Mergeable: m, User: p.User.Login, HTMLURL: p.HTMLURL, CreatedAt: p.CreatedAt}
}

// projectFile keeps text up to 256 KiB; anything larger, binary or not base64 is a digest.
func projectFile(f ghContent) fileOut {
	out := fileOut{Path: f.Path, Type: nonEmpty(f.Type, "file"), SHA: f.SHA, Size: f.Size, HTMLURL: f.HTMLURL}
	var raw []byte
	ok := false
	if f.Encoding == "base64" {
		b, err := base64.StdEncoding.DecodeString(strings.NewReplacer("\n", "", "\r", "").Replace(f.Content))
		raw, ok = b, err == nil
	}
	if ok && len(raw) <= maxTextFileBytes && utf8.Valid(raw) && !strings.ContainsRune(string(raw), 0) {
		s := string(raw)
		out.Encoding, out.Content = "text", &s
		return out
	}
	out.Encoding = "digest"
	if ok {
		sum := sha256.Sum256(raw)
		out.Digest = "sha256:" + hex.EncodeToString(sum[:])
	}
	return out
}

func sortedKeys(m map[string]string) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}
