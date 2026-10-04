package github

import (
	"encoding/json"
	"strings"

	"github.com/deedglass/deedglass/internal/connector"
)

// The L1 sentences the catalog shows (S29 §3.2).
const (
	L1CreateIssue  = "GitHub shows the issue with the same title, body and marker, opened by this connector's identity."
	L1Comment      = "The comment exists on issue n with the same body and marker."
	L1CreateBranch = "The branch points at the requested commit."
	L1File         = "The file's blob SHA on that branch equals the git blob SHA of the content Deedglass sent."
	L1CreatePR     = "The pull request is open from head into base with the same title and body."
	L1MergePR      = "GitHub reports it merged, and the merge commit is contained in base."
	L1DeleteBranch = "The branch no longer resolves (404)."
	L1CloseIssue   = "The issue is closed with that reason."
)

// Argument schema fragments (the connector/schema subset).
const (
	sRepo        = `"repo":{"type":"string","pattern":"^[A-Za-z0-9-]{1,39}/[A-Za-z0-9._-]{1,100}$","description":"owner/name; it must be in the connector's repos."}`
	sNumber      = `"number":{"type":"integer","minimum":1,"maximum":2147483647}`
	sBranchPat   = `{"type":"string","pattern":"^[A-Za-z0-9._/-]{1,255}$"}`
	sSHAPat      = `{"type":"string","pattern":"^([0-9a-f]{40}|[0-9a-f]{64})$"}`
	sPath        = `"path":{"type":"string","maxLength":1024,"description":"A path inside the repository, like docs/x.md."}`
	sStateFilter = `"state":{"type":"string","enum":["open","closed","all"]}`
	sPerPage     = `"per_page":{"type":"integer","minimum":1,"maximum":50}`
	sPage        = `"page":{"type":"integer","minimum":1,"maximum":1000}`
	sTitle       = `"title":{"type":"string","maxLength":256}`
	sBody        = `"body":{"type":"string","maxLength":65000}`
)

func obj(required []string, props ...string) json.RawMessage {
	req := ""
	if len(required) > 0 {
		req = `"required":["` + strings.Join(required, `","`) + `"],`
	}
	return json.RawMessage(`{"type":"object",` + req + `"additionalProperties":false,"properties":{` + strings.Join(props, ",") + `}}`)
}

// Output schema fragments: projections, never raw GitHub payloads.
const (
	oStr  = `{"type":"string"}`
	oInt  = `{"type":"integer"}`
	oBool = `{"type":"boolean"}`
	oStrs = `{"type":"array","items":{"type":"string"}}`
)

func out(required []string, props map[string]string) json.RawMessage {
	parts := make([]string, 0, len(props))
	for _, k := range sortedKeys(props) {
		parts = append(parts, `"`+k+`":`+props[k])
	}
	req := ""
	if len(required) > 0 {
		req = `"required":["` + strings.Join(required, `","`) + `"],`
	}
	return json.RawMessage(`{"type":"object",` + req + `"additionalProperties":false,"properties":{` + strings.Join(parts, ",") + `}}`)
}

func outArray(item json.RawMessage) string {
	return `{"type":"array","items":` + string(item) + `}`
}

var (
	issueSummary = map[string]string{"number": oInt, "title": oStr, "state": oStr, "labels": oStrs, "user": oStr,
		"is_pull_request": oBool, "html_url": oStr, "created_at": oStr, "comments": oInt}
	prSummary = map[string]string{"number": oInt, "title": oStr, "state": oStr, "head": oStr, "base": oStr, "draft": oBool,
		"user": oStr, "html_url": oStr, "created_at": oStr}
)

var toolSpecs = []connector.ToolSpec{
	{Name: "get_repo", Description: "Read a repository's settings: visibility, default branch, description.",
		InputSchema: obj([]string{"repo"}, sRepo), SideEffect: connector.SideEffectRead, Idempotent: true,
		OutputSchema: out([]string{"full_name", "default_branch"}, map[string]string{"full_name": oStr, "private": oBool, "default_branch": oStr,
			"archived": oBool, "description": oStr, "html_url": oStr, "open_issues_count": oInt, "can_push": oBool})},
	{Name: "list_issues", Description: "List issues (and pull requests, flagged) in a repository, newest first, up to 50 a page.",
		InputSchema: obj([]string{"repo"}, sRepo, sStateFilter, `"labels":{"type":"string","maxLength":200,"description":"Comma-separated label names."}`, sPerPage, sPage),
		SideEffect:  connector.SideEffectRead, Idempotent: true,
		OutputSchema: out([]string{"issues"}, map[string]string{"issues": outArray(out(nil, issueSummary)), "page": oInt})},
	{Name: "get_issue", Description: "Read one issue: title, body, state and labels.",
		InputSchema: obj([]string{"repo", "number"}, sRepo, sNumber), SideEffect: connector.SideEffectRead, Idempotent: true,
		OutputSchema: out([]string{"number", "title", "state"}, map[string]string{"number": oInt, "title": oStr, "state": oStr, "state_reason": oStr,
			"body": oStr, "labels": oStrs, "user": oStr, "comments": oInt, "is_pull_request": oBool, "html_url": oStr, "created_at": oStr, "closed_at": oStr})},
	{Name: "get_file", Description: "Read a file at a branch, tag or commit. Text up to 256 KiB comes back as is; anything larger or binary as a digest.",
		InputSchema: obj([]string{"repo", "path"}, sRepo, sPath, `"ref":{"type":"string","maxLength":255,"description":"A branch, tag or commit; default the default branch."}`),
		SideEffect:  connector.SideEffectRead, Idempotent: true,
		OutputSchema: out([]string{"path", "type"}, map[string]string{"path": oStr, "type": oStr, "sha": oStr, "size": oInt, "encoding": oStr,
			"content": oStr, "digest": oStr, "html_url": oStr,
			"entries": outArray(out(nil, map[string]string{"name": oStr, "path": oStr, "type": oStr}))})},
	{Name: "list_prs", Description: "List pull requests in a repository, newest first, up to 50 a page.",
		InputSchema: obj([]string{"repo"}, sRepo, sStateFilter, `"base":`+sBranchPat, sPerPage, sPage),
		SideEffect:  connector.SideEffectRead, Idempotent: true,
		OutputSchema: out([]string{"pulls"}, map[string]string{"pulls": outArray(out(nil, prSummary)), "page": oInt})},
	{Name: "get_pr", Description: "Read one pull request: title, body, head, base and merge state.",
		InputSchema: obj([]string{"repo", "number"}, sRepo, sNumber), SideEffect: connector.SideEffectRead, Idempotent: true,
		OutputSchema: out([]string{"number", "state"}, map[string]string{"number": oInt, "title": oStr, "state": oStr, "body": oStr, "head": oStr,
			"head_sha": oStr, "base": oStr, "draft": oBool, "merged": oBool, "mergeable": oStr, "user": oStr, "html_url": oStr, "created_at": oStr})},

	{Name: "create_issue", Description: "Open an issue. Deedglass appends a hidden marker with the action id to the body.",
		InputSchema: obj([]string{"repo", "title"}, sRepo, sTitle, sBody, `"labels":{"type":"array","items":{"type":"string","maxLength":50}}`),
		SideEffect:  connector.SideEffectWrite, L1: L1CreateIssue,
		OutputSchema: out([]string{"number", "state"}, map[string]string{"number": oInt, "html_url": oStr, "state": oStr, "title": oStr})},
	{Name: "comment", Description: "Comment on an issue or a pull request. Deedglass appends a hidden marker with the action id.",
		InputSchema: obj([]string{"repo", "number", "body"}, sRepo, sNumber, sBody), SideEffect: connector.SideEffectWrite, L1: L1Comment,
		OutputSchema: out([]string{"id", "issue_number"}, map[string]string{"id": oInt, "issue_number": oInt, "html_url": oStr})},
	{Name: "create_branch", Description: "Create a branch from another branch or a commit (default: the default branch).",
		InputSchema: obj([]string{"repo", "branch"}, sRepo, `"branch":`+sBranchPat,
			`"from":{"type":"string","pattern":"^[A-Za-z0-9._/-]{1,255}$","description":"A branch name or a full commit SHA."}`),
		SideEffect: connector.SideEffectWrite, L1: L1CreateBranch,
		OutputSchema: out([]string{"branch", "sha"}, map[string]string{"ref": oStr, "branch": oStr, "sha": oStr, "existed": oBool})},
	{Name: "create_or_update_file", Description: "Create or replace one text file on a branch in one commit. Send expected_sha to replace an existing file.",
		InputSchema: obj([]string{"repo", "path", "message", "content"}, sRepo, sPath, `"branch":`+sBranchPat,
			`"message":{"type":"string","maxLength":1000}`, `"content":{"type":"string","maxLength":1048576}`, `"expected_sha":`+sSHAPat),
		SideEffect: connector.SideEffectWrite, L1: L1File,
		OutputSchema: out([]string{"path", "blob_sha"}, map[string]string{"path": oStr, "branch": oStr, "blob_sha": oStr, "commit_sha": oStr, "html_url": oStr})},
	{Name: "create_pr", Description: "Open a pull request from head into base. Deedglass appends a hidden marker with the action id to the body.",
		InputSchema: obj([]string{"repo", "head", "base", "title"}, sRepo, `"head":`+sBranchPat, `"base":`+sBranchPat, sTitle, sBody, `"draft":{"type":"boolean"}`),
		SideEffect:  connector.SideEffectWrite, L1: L1CreatePR,
		OutputSchema: out([]string{"number", "state"}, map[string]string{"number": oInt, "html_url": oStr, "state": oStr, "title": oStr, "head": oStr, "base": oStr})},
	{Name: "merge_pr", Description: "Merge a pull request. expected_head_sha is required: GitHub refuses the merge if the head moved.",
		InputSchema: obj([]string{"repo", "number"}, sRepo, sNumber, `"expected_head_sha":`+sSHAPat,
			`"merge_method":{"type":"string","enum":["merge","squash","rebase"]}`, `"commit_title":{"type":"string","maxLength":256}`),
		SideEffect: connector.SideEffectDestructive, L1: L1MergePR,
		OutputSchema: out([]string{"number", "merged"}, map[string]string{"number": oInt, "merged": oBool, "sha": oStr})},
	{Name: "delete_branch", Description: "Delete a branch. The repository's default branch is never deleted.",
		InputSchema: obj([]string{"repo", "branch"}, sRepo, `"branch":`+sBranchPat), SideEffect: connector.SideEffectDestructive, L1: L1DeleteBranch,
		OutputSchema: out([]string{"branch", "deleted"}, map[string]string{"branch": oStr, "deleted": oBool})},
	{Name: "close_issue", Description: "Close an issue as completed or not planned.",
		InputSchema: obj([]string{"repo", "number"}, sRepo, sNumber, `"state_reason":{"type":"string","enum":["completed","not_planned"]}`),
		SideEffect:  connector.SideEffectDestructive, L1: L1CloseIssue,
		OutputSchema: out([]string{"number", "state"}, map[string]string{"number": oInt, "state": oStr, "state_reason": oStr, "html_url": oStr})},
}
