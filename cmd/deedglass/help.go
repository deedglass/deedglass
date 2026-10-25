package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/deedglass/deedglass/cmd/deedglass/internal/ui"
	"github.com/deedglass/deedglass/internal/cli/mark"
	"github.com/deedglass/deedglass/internal/recordexport"
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

// globalFlags prints the global flags line, wrapped to width.
func globalFlags(e *Env, w io.Writer, width int) {
	gl := fmt.Sprintf("              --json %s --server <url> %s --api-key <key> %s --org <id> %s --no-color %s -v", ui.Sep, ui.Sep, ui.Sep, ui.Sep, ui.Sep)
	fmt.Fprintln(w)
	for i, l := range ui.Wrap(gl, width, 0) {
		if i == 0 {
			l = e.Style.Bold("Global flags") + "  " + strings.TrimLeft(l, " ")
		}
		fmt.Fprintln(w, l)
	}
}

// helpWidth is the column budget help wraps to: the terminal's width, at most 100, or 80 when it
// isn't known (a pipe, a file).
func helpWidth(e *Env) int {
	if e.Width > 0 {
		return min(e.Width, 100)
	}
	return 80
}

func helpAll(e *Env) {
	s := e.Style
	width := helpWidth(e)
	nameW, sumW := 0, 0
	for _, l := range lines {
		nameW = max(nameW, ui.Width(l.Name))
		sumW = max(sumW, ui.Width(l.Summary))
	}
	w := e.Stdout
	if head := mark.Inline(e.Mark); head != "" {
		fmt.Fprintf(w, "%s %s %s\n\n", head, ui.Sep, tagline)
	} else {
		fmt.Fprintf(w, "%s %s %s\n\n", s.Bold("deedglass"), ui.Sep, tagline)
	}
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
		subs := strings.Join(l.Subs, " "+ui.Sep+" ")
		status := ""
		if !lineBuilt(l) {
			status = "soon"
		}
		// Wide terminals keep the subcommands in their own column; narrower ones (80 columns, a
		// pipe) put them on the next line under the summary.
		if 2+nameW+2+sumW+3+4+3+ui.Width(subs) <= width {
			if status == "" {
				status = "    "
			}
			fmt.Fprintln(w, row+ui.Pad(l.Summary, sumW+3)+s.Dim(status)+"   "+s.Dim(subs))
			continue
		}
		if status != "" {
			row += l.Summary + "  " + s.Dim(status)
		} else {
			row += l.Summary
		}
		fmt.Fprintln(w, row)
		if subs != "" {
			for _, sl := range ui.Wrap(strings.Repeat(" ", 2+nameW+2)+subs, width, 0) {
				fmt.Fprintln(w, s.Dim(sl))
			}
		}
	}
	globalFlags(e, w, width)
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
	soon := false
	for _, c := range fam {
		row := "  " + ui.Pad(strings.Join(c.Path[len(prefix):], " "), nameW) + "  "
		if c.Built() {
			fmt.Fprintln(w, row+c.Summary)
			continue
		}
		soon = true
		fmt.Fprintln(w, row+ui.Pad(c.Summary, sumW+3)+s.Dim("soon "+ui.Sep+" "+c.Owner))
	}
	if soon {
		fmt.Fprintln(w)
		for _, l := range ui.Wrap("soon: listed in the contract, not in this build yet; the session that builds it is shown.", helpWidth(e), 6) {
			fmt.Fprintln(w, l)
		}
	}
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
	width := helpWidth(e)
	for i, l := range ui.Wrap("       "+usage, width, 0) {
		if i == 0 {
			l = s.Bold("Usage") + "  " + strings.TrimPrefix(l, "       ")
		}
		fmt.Fprintln(w, l)
	}
	if c.Detail != "" {
		for _, l := range strings.Split(strings.TrimRight(c.Detail, "\n"), "\n") {
			// A line that starts with spaces is an example or a list item: kept as written unless it
			// overflows, then wrapped under itself.
			hang := 0
			if strings.HasPrefix(l, " ") {
				hang = 2
			}
			for _, wl := range ui.Wrap(l, width, hang) {
				if wl != "" && !strings.HasPrefix(l, " ") {
					wl = s.Bold(wl)
				}
				fmt.Fprintln(w, wl)
			}
		}
	}
	if !c.Built() {
		fmt.Fprintf(w, "\n%s\n", s.Dim("Not in this build yet (planned for "+c.Owner+")."))
	}
	globalFlags(e, w, width)
}

// flagValues are the closed value lists help --json carries for completions (S35 generates them).
var flagValues = map[string][]string{
	"init --client":     initClients,
	"init --postgres":   {"docker"},
	"scan --format":     scanFormats,
	"scan --fail-under": {"A", "B", "C", "D"},
	"export --format":   recordexport.Formats,
}

// helpJSON is the registry as data (S32 §3.11): S33 renders the CLI reference from it and S35
// generates shell completions from it. A golden locks the schema.
func helpJSON(e *Env) error {
	type flagItem struct {
		Name    string   `json:"name"`
		Type    string   `json:"type"`
		Default string   `json:"default"`
		Usage   string   `json:"usage"`
		Values  []string `json:"values,omitempty"`
	}
	type cmdItem struct {
		Path    []string   `json:"path"`
		Summary string     `json:"summary"`
		Usage   string     `json:"usage"`
		Flags   []flagItem `json:"flags"`
		Built   bool       `json:"built"`
		Owner   string     `json:"owner,omitempty"`
		Hidden  bool       `json:"hidden,omitempty"`
	}
	type group struct {
		Name     string    `json:"name"`
		Commands []cmdItem `json:"commands"`
	}
	out := struct {
		Object      string            `json:"object"`
		Version     string            `json:"version"`
		Groups      []group           `json:"groups"`
		GlobalFlags []flagItem        `json:"global_flags"`
		ExitCodes   map[string]string `json:"exit_codes"`
	}{Object: "command_tree", Version: buildInfo().Version,
		GlobalFlags: []flagItem{
			{Name: "json", Type: "bool", Default: "false", Usage: "print JSON"},
			{Name: "server", Type: "string", Default: "", Usage: "the Deedglass server URL (else DEEDGLASS_SERVER, DEEDGLASS_BASE_URL, http://127.0.0.1:8080)"},
			{Name: "api-key", Type: "string", Default: "", Usage: "an API key (else DEEDGLASS_API_KEY)"},
			{Name: "org", Type: "string", Default: "", Usage: "the org, for operator commands"},
			{Name: "no-color", Type: "bool", Default: "false", Usage: "no colour"},
			{Name: "v", Type: "bool", Default: "false", Usage: "more detail"},
		},
		ExitCodes: map[string]string{"0": "ok", "1": "failure or invalid", "2": "cannot determine, or a usage error", "3": "not authenticated"}}
	byGroup := map[Group][]cmdItem{}
	for _, c := range commands {
		it := cmdItem{Path: c.Path, Summary: c.Summary, Usage: c.Usage, Built: c.Built(), Owner: c.Owner, Hidden: c.Hidden, Flags: []flagItem{}}
		if c.Flags != nil {
			fs := flag.NewFlagSet(strings.Join(c.Path, " "), flag.ContinueOnError)
			c.Flags(fs)
			fs.VisitAll(func(f *flag.Flag) {
				fi := flagItem{Name: f.Name, Type: flagType(f), Default: f.DefValue, Usage: f.Usage}
				fi.Values = flagValues[strings.Join(c.Path, " ")+" --"+f.Name]
				it.Flags = append(it.Flags, fi)
			})
		}
		byGroup[c.Group] = append(byGroup[c.Group], it)
	}
	for g := Setup; g <= Dev; g++ {
		out.Groups = append(out.Groups, group{Name: g.String(), Commands: byGroup[g]})
	}
	enc := json.NewEncoder(e.Stdout)
	enc.SetEscapeHTML(false)
	return enc.Encode(out)
}

// flagType names a flag's type: bool, int, duration, float or string.
func flagType(f *flag.Flag) string {
	if b, ok := f.Value.(interface{ IsBoolFlag() bool }); ok && b.IsBoolFlag() {
		return "bool"
	}
	switch t := fmt.Sprintf("%T", f.Value); {
	case strings.Contains(t, "int"):
		return "int"
	case strings.Contains(t, "duration"):
		return "duration"
	case strings.Contains(t, "float"):
		return "float"
	}
	return "string"
}
