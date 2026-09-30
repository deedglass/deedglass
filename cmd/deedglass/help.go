package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/deedglass/deedglass/cmd/deedglass/internal/ui"
)

const tagline = "the execution and accountability layer for AI agents"

func runHelp(_ context.Context, e *Env, args []string) error { return printHelp(e, args) }

// printHelp prints the whole tree, or one command or family.
func printHelp(e *Env, path []string) error {
	if len(path) == 0 {
		if e.Globals.JSON {
			return helpJSON(e)
		}
		helpAll(e)
		return nil
	}
	if cmd, n := lookup(path); cmd != nil && n == len(path) {
		helpCommand(e, cmd)
		return nil
	}
	if fam := family(path); len(fam) > 0 {
		helpFamily(e, path, fam)
		return nil
	}
	return notFound(e, path)
}

func lineBuilt(l Line) bool {
	for _, p := range l.Paths {
		if c, _ := lookup(strings.Fields(p)); c == nil || !c.Built() {
			return false
		}
	}
	return true
}

func helpAll(e *Env) {
	s := e.Style
	nameW, sumW := 0, 0
	for _, l := range lines {
		nameW = max(nameW, ui.Width(l.Name))
		sumW = max(sumW, ui.Width(l.Summary))
	}
	w := e.Stdout
	fmt.Fprintf(w, "%s %s %s\n\n", s.Bold("deedglass"), ui.Sep, tagline)
	fmt.Fprintf(w, "%s  deedglass <command> [flags]\n\n", s.Bold("Usage"))
	group := Group(-1)
	for _, l := range lines {
		if l.Group != group {
			group = l.Group
			fmt.Fprintf(w, "%s\n", s.Bold(group.String()))
		}
		row := "  " + ui.Pad(l.Name, nameW) + "  "
		if lineBuilt(l) && len(l.Subs) == 0 {
			fmt.Fprintln(w, row+l.Summary)
			continue
		}
		row += ui.Pad(l.Summary, sumW+3)
		status := "soon"
		if lineBuilt(l) {
			status = "    "
		}
		row += s.Dim(status)
		if len(l.Subs) > 0 {
			row += "   " + s.Dim(strings.Join(l.Subs, " "+ui.Sep+" "))
		}
		fmt.Fprintln(w, row)
	}
	fmt.Fprintf(w, "\n%s  --json %s --server <url> %s --api-key <key> %s --org <id> %s --no-color %s -v\n",
		s.Bold("Global flags"), ui.Sep, ui.Sep, ui.Sep, ui.Sep, ui.Sep)
	fmt.Fprintf(w, "Run deedglass help <command> for details %s %s\n", ui.Sep, docsURL)
}

func helpFamily(e *Env, prefix []string, fam []*Command) {
	s := e.Style
	w := e.Stdout
	name := strings.Join(prefix, " ")
	summary := ""
	for _, l := range lines {
		if l.Name == name {
			summary = l.Summary
		}
	}
	if summary != "" {
		fmt.Fprintf(w, "%s %s %s\n\n", s.Bold("deedglass "+name), ui.Sep, summary)
	} else {
		fmt.Fprintf(w, "%s\n\n", s.Bold("deedglass "+name))
	}
	fmt.Fprintf(w, "%s  deedglass %s <command> [flags]\n\n", s.Bold("Usage"), name)
	nameW, sumW := 0, 0
	for _, c := range fam {
		nameW = max(nameW, ui.Width(strings.Join(c.Path[len(prefix):], " ")))
		sumW = max(sumW, ui.Width(c.Summary))
	}
	for _, c := range fam {
		row := "  " + ui.Pad(strings.Join(c.Path[len(prefix):], " "), nameW) + "  "
		if c.Built() {
			fmt.Fprintln(w, row+c.Summary)
			continue
		}
		fmt.Fprintln(w, row+ui.Pad(c.Summary, sumW+3)+s.Dim("soon "+ui.Sep+" "+c.Owner))
	}
	fmt.Fprintf(w, "\nsoon: listed in the contract, not in this build yet; the session that builds it is shown.\n")
}

func helpCommand(e *Env, c *Command) {
	s := e.Style
	w := e.Stdout
	name := "deedglass " + strings.Join(c.Path, " ")
	fmt.Fprintf(w, "%s %s %s\n\n", s.Bold(name), ui.Sep, c.Summary)
	usage := name
	if c.Usage != "" {
		usage += " " + c.Usage
	}
	fmt.Fprintf(w, "%s  %s\n", s.Bold("Usage"), usage)
	if !c.Built() {
		fmt.Fprintf(w, "\n%s\n", s.Dim("Not in this build yet (planned for "+c.Owner+")."))
	}
	fmt.Fprintf(w, "\n%s  --json %s --server <url> %s --api-key <key> %s --org <id> %s --no-color %s -v\n",
		s.Bold("Global flags"), ui.Sep, ui.Sep, ui.Sep, ui.Sep, ui.Sep)
}

func helpJSON(e *Env) error {
	type item struct {
		Path    string `json:"path"`
		Group   string `json:"group"`
		Summary string `json:"summary"`
		Usage   string `json:"usage"`
		Built   bool   `json:"built"`
		Owner   string `json:"owner,omitempty"`
		Hidden  bool   `json:"hidden"`
	}
	out := struct {
		Object string `json:"object"`
		Data   []item `json:"data"`
	}{Object: "command_list"}
	for _, c := range commands {
		out.Data = append(out.Data, item{strings.Join(c.Path, " "), c.Group.String(), c.Summary, c.Usage, c.Built(), c.Owner, c.Hidden})
	}
	enc := json.NewEncoder(e.Stdout)
	enc.SetEscapeHTML(false)
	return enc.Encode(out)
}
