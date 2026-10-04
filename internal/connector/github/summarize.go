package github

import (
	"encoding/json"
	"strconv"

	"github.com/deedglass/deedglass/internal/connector"
)

// Summarize implements connector.Summarizer: one sentence from identifiers only (the repository,
// branches, a path, numbers), never from free agent text such as a title or a body. An argument
// that fails the connector's own checks is left out; with no usable repository the answer is
// nil, and the card falls back to "<agent> wants to run <tool>.".
func (c *Connector) Summarize(in connector.SummaryInput) connector.Summary {
	var a args
	if json.Unmarshal(in.Args, &a) != nil || !reRepo.MatchString(a.Repo) {
		return nil
	}
	facts := map[string]any{}
	if g, ok := in.Facts["github"].(map[string]any); ok {
		facts = g
	}
	fact := func(k string) string {
		s, _ := facts[k].(string)
		if !validBranch(s) {
			return ""
		}
		return s
	}
	branch := func(v string) string {
		if validBranch(v) {
			return v
		}
		return ""
	}
	tool := in.Tool.Name
	if tool == "" {
		if _, t, ok := connector.Split(in.Qualified); ok {
			tool = t
		}
	}
	agent := in.Agent
	if agent == "" {
		agent = "An agent"
	}
	s := connector.Summary{{Text: agent, Style: connector.SummaryStrong}}
	add := func(parts ...connector.SummaryPart) { s = append(s, parts...) }
	plain := func(t string) connector.SummaryPart { return connector.SummaryPart{Text: t} }
	strong := func(t string) connector.SummaryPart {
		return connector.SummaryPart{Text: t, Style: connector.SummaryStrong}
	}
	code := func(t string) connector.SummaryPart {
		return connector.SummaryPart{Text: t, Style: connector.SummaryCode}
	}
	repo := strong(a.Repo)
	n := ""
	if a.Number > 0 {
		n = "#" + strconv.FormatInt(a.Number, 10)
	}
	path := ""
	if a.Path != "" && pathProblem(a.Path) == "" {
		path = a.Path
	}

	switch tool {
	case "create_pr":
		head, base := branch(a.Head), branch(a.Base)
		if head == "" || base == "" {
			add(plain(" wants to open a pull request in "), repo, plain("."))
			break
		}
		add(plain(" wants to open a pull request in "), repo, plain(" from "), code(head), plain(" into "), code(base), plain("."))
	case "merge_pr":
		if n == "" {
			return nil
		}
		add(plain(" wants to merge pull request "), strong(n), plain(" in "), repo)
		if base := fact("base"); base != "" {
			add(plain(" into "), code(base))
		}
		add(plain("."))
	case "delete_branch":
		b := branch(a.Branch)
		if b == "" {
			return nil
		}
		add(plain(" wants to delete the branch "), code(b), plain(" in "), repo, plain("."))
	case "create_or_update_file":
		if path == "" {
			return nil
		}
		add(plain(" wants to change "), code(path), plain(" on "))
		if b := branch(a.Branch); b != "" {
			add(code(b))
		} else if b := fact("branch"); b != "" {
			add(code(b))
		} else {
			add(plain("the default branch"))
		}
		add(plain(" in "), repo, plain("."))
	case "create_issue":
		add(plain(" wants to open an issue in "), repo, plain("."))
	case "comment":
		if n == "" {
			return nil
		}
		add(plain(" wants to comment on "), strong(n), plain(" in "), repo, plain("."))
	case "close_issue":
		if n == "" {
			return nil
		}
		add(plain(" wants to close issue "), strong(n), plain(" in "), repo, plain("."))
	case "create_branch":
		b := branch(a.Branch)
		if b == "" {
			return nil
		}
		add(plain(" wants to create the branch "), code(b), plain(" in "), repo)
		if f := a.From; f != "" && (validBranch(f) || reSHA.MatchString(f)) {
			add(plain(" from "), code(f))
		}
		add(plain("."))
	case "get_repo":
		add(plain(" wants to read "), repo, plain("."))
	case "list_issues":
		add(plain(" wants to list issues in "), repo, plain("."))
	case "get_issue":
		if n == "" {
			return nil
		}
		add(plain(" wants to read issue "), strong(n), plain(" in "), repo, plain("."))
	case "get_file":
		if path == "" {
			return nil
		}
		add(plain(" wants to read "), code(path), plain(" in "), repo, plain("."))
	case "list_prs":
		add(plain(" wants to list pull requests in "), repo, plain("."))
	case "get_pr":
		if n == "" {
			return nil
		}
		add(plain(" wants to read pull request "), strong(n), plain(" in "), repo, plain("."))
	default:
		return nil
	}
	return s
}
