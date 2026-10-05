// Package agentscript is the scripted agent behind `deedglass dev agent-script` (S37 §3.3): a YAML
// file of scenes, each a list of steps (a tool call through the Gate as an agent, an allow-listed
// demo make target, a deedglass subcommand as argv, an approval, or a one-byte change to a file under
// .tmp/demo/), each with the outcome it expects from the real product. The first unmet expectation
// stops the run. The script never edits the product to fit; it only asserts what the product did.
//
// Safety is decided at load: no shell (a run step is an argv list, never a string), make targets and
// deedglass commands from allow-lists, and file writes only under .tmp/demo/.
package agentscript

import (
	"bytes"
	"errors"
	"fmt"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"
)

// Version is the only script format this package reads.
const Version = "agent-script/v1"

// DemoDir is the only directory a script may write to, relative to the working directory.
const DemoDir = ".tmp/demo"

// MakeTargets are the make targets a script may run (the demo's database toggles only).
var MakeTargets = []string{"demo-trigger-on", "demo-trigger-off", "demo-order-reset"}

// RunCommands are the deedglass commands a run step may start (the first one or two argv words).
var RunCommands = []string{"actions show", "undo preview", "undo run", "deeds export", "verify", "ledger check", "approvals list", "approvals show"}

// Needs a scene may declare; offline, every one of them is unmet and the scene is skipped.
var Needs = []string{"live", "slack", "stripe"}

// Script is one file.
type Script struct {
	Version string  `yaml:"deedglass"` // agent-script/v1
	Title   string  `yaml:"title"`     // the header line's name
	Org     string  `yaml:"org"`       // acme-demo
	Agent   string  `yaml:"agent"`     // the key name in the keys file that calls act as
	Scenes  []Scene `yaml:"scenes"`
}

// Scene is one numbered beat.
type Scene struct {
	ID    string   `yaml:"id"`
	Title string   `yaml:"title"`
	Say   []string `yaml:"say"`   // narration, printed dim in --step mode
	Needs []string `yaml:"needs"` // live, slack, stripe
	Steps []Step   `yaml:"steps"`
}

// Step is exactly one of Make, Call, Run, Approve or Tamper, with what it must show.
type Step struct {
	Make    string            `yaml:"make"`
	Call    *Call             `yaml:"call"`
	Run     []string          `yaml:"run"`
	As      string            `yaml:"as"` // agent (default) or admin: which key a call or run uses
	Approve *Approve          `yaml:"approve"`
	Tamper  *Tamper           `yaml:"tamper"`
	Expect  Expect            `yaml:"expect"`
	Capture map[string]string `yaml:"capture"` // name → id|deed|approval (calls) or a regexp (runs)
	Show    string            `yaml:"show"`    // run output: first (default), all or none
	Print   string            `yaml:"print"`   // a line printed once the step's expectations hold
}

// Call is POST /v1/actions.
type Call struct {
	Tool string         `yaml:"tool"`
	Args map[string]any `yaml:"args"`
}

// Approve countersigns a pending approval. Offline it runs `deedglass approve <id> --as <email>`
// (the dev path, S22); live it waits up to two minutes for a person on the phone or in Slack. With
// Action set, it then waits for that action to finish and checks the step's expectations on it.
type Approve struct {
	Approval string `yaml:"approval"`
	As       string `yaml:"as"`
	Reason   string `yaml:"reason"`
	Action   string `yaml:"action"`
}

// Tamper copies From to To with one byte changed: the last digit of execution.latency_ms in the
// deed whose deed_id is Deed. Both paths are under .tmp/demo/; From is never modified.
type Tamper struct {
	From string `yaml:"from"`
	To   string `yaml:"to"`
	Deed string `yaml:"deed"`
}

// Expect is what a step must show. Empty fields aren't checked.
type Expect struct {
	Status   string `yaml:"status"`   // executed, denied, pending_approval, failed, …
	Decision string `yaml:"decision"` // allow, deny, require_approval
	Rule     string `yaml:"rule"`
	Level    string `yaml:"level"`
	Verdict  string `yaml:"verdict"`
	Error    string `yaml:"error"` // the API error code, or the execution's error code
	Exit     *int   `yaml:"exit"`  // a run step's exit code (default 0)
	Output   string `yaml:"output"`
}

// Kind names a step's kind.
func (s Step) Kind() string {
	switch {
	case s.Make != "":
		return "make"
	case s.Call != nil:
		return "call"
	case s.Run != nil:
		return "run"
	case s.Approve != nil:
		return "approve"
	case s.Tamper != nil:
		return "tamper"
	}
	return ""
}

var (
	reVar     = regexp.MustCompile(`\$\{([a-z][a-z0-9_]*)\}`)
	reName    = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
	reSceneID = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)
	reShell   = regexp.MustCompile("[;|&<>`\\n\\\\]|\\$\\(|\\$[A-Za-z_(]")
)

// Parse reads a script and refuses anything unsafe or unknown, with the reason and its place.
func Parse(name string, b []byte) (*Script, error) {
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	var s Script
	if err := dec.Decode(&s); err != nil {
		return nil, fmt.Errorf("%s: %s", name, friendlyYAML(err))
	}
	if s.Version != Version {
		return nil, fmt.Errorf("%s: deedglass: must be %q (got %q)", name, Version, s.Version)
	}
	if s.Org == "" || s.Agent == "" {
		return nil, fmt.Errorf("%s: org and agent are required", name)
	}
	if len(s.Scenes) == 0 {
		return nil, fmt.Errorf("%s: the script has no scenes", name)
	}
	seen := map[string]bool{}
	for i, sc := range s.Scenes {
		at := fmt.Sprintf("%s: scene %d", name, i+1)
		if !reSceneID.MatchString(sc.ID) || seen[sc.ID] {
			return nil, fmt.Errorf("%s: id %q must be unique, lowercase letters, digits and dashes", at, sc.ID)
		}
		seen[sc.ID] = true
		if sc.Title == "" {
			return nil, fmt.Errorf("%s (%s): a title is required", at, sc.ID)
		}
		for _, n := range sc.Needs {
			if !slices.Contains(Needs, n) {
				return nil, fmt.Errorf("%s (%s): needs %q is unknown; use %s", at, sc.ID, n, strings.Join(Needs, ", "))
			}
		}
		if len(sc.Steps) == 0 {
			return nil, fmt.Errorf("%s (%s): the scene has no steps", at, sc.ID)
		}
		for j, st := range sc.Steps {
			if err := checkStep(st); err != nil {
				return nil, fmt.Errorf("%s (%s) step %d: %w", at, sc.ID, j+1, err)
			}
		}
	}
	return &s, nil
}

func checkStep(st Step) error {
	n := 0
	for _, set := range []bool{st.Make != "", st.Call != nil, st.Run != nil, st.Approve != nil, st.Tamper != nil} {
		if set {
			n++
		}
	}
	if n != 1 {
		return errors.New("a step is exactly one of make, call, run, approve or tamper")
	}
	if st.As != "" && st.As != "agent" && st.As != "admin" {
		return fmt.Errorf("as must be agent or admin (got %q)", st.As)
	}
	switch st.Show {
	case "", "first", "all", "none":
	default:
		return fmt.Errorf("show must be first, all or none (got %q)", st.Show)
	}
	for k := range st.Capture {
		if !reName.MatchString(k) {
			return fmt.Errorf("capture name %q must be lowercase letters, digits and underscores", k)
		}
	}
	switch st.Kind() {
	case "make":
		if !slices.Contains(MakeTargets, st.Make) {
			return fmt.Errorf("make %q isn't allowed; a script may run only %s", st.Make, strings.Join(MakeTargets, ", "))
		}
	case "call":
		if st.Call.Tool == "" {
			return errors.New("call needs a tool")
		}
		for k, v := range st.Capture {
			if v != "id" && v != "deed" && v != "approval" {
				return fmt.Errorf("a call captures id, deed or approval (got %s: %q)", k, v)
			}
		}
	case "run":
		return checkRun(st.Run)
	case "approve":
		if st.Approve.Approval == "" || st.Approve.As == "" || !strings.Contains(st.Approve.As, "@") {
			return errors.New("approve needs approval and as (the approver's email)")
		}
	case "tamper":
		for _, p := range []string{st.Tamper.From, st.Tamper.To} {
			if err := checkDemoPath(p); err != nil {
				return fmt.Errorf("tamper: %w", err)
			}
		}
		if path.Clean(st.Tamper.From) == path.Clean(st.Tamper.To) {
			return errors.New("tamper writes a copy: from and to must differ")
		}
		if st.Tamper.Deed == "" {
			return errors.New("tamper needs the deed to change")
		}
	}
	return nil
}

// checkRun refuses a shell, a command outside the allow-list and an --out outside .tmp/demo/.
func checkRun(argv []string) error {
	if len(argv) == 0 {
		return errors.New("run needs a deedglass subcommand as a list of words")
	}
	for _, a := range argv {
		if reShell.MatchString(reVar.ReplaceAllString(a, "x")) {
			return fmt.Errorf("run %q looks like shell; a run step is a deedglass subcommand as argv, never a shell", a)
		}
	}
	if argv[0] == "deedglass" || argv[0] == "sh" || argv[0] == "bash" || strings.Contains(argv[0], "/") {
		return fmt.Errorf("run starts with the subcommand (like verify), not a program (got %q)", argv[0])
	}
	cmd := argv[0]
	if len(argv) > 1 && !strings.HasPrefix(argv[1], "-") && slices.Contains(RunCommands, argv[0]+" "+argv[1]) {
		cmd = argv[0] + " " + argv[1]
	}
	if !slices.Contains(RunCommands, cmd) {
		return fmt.Errorf("run %q isn't allowed; a script may run only deedglass %s", cmd, strings.Join(RunCommands, ", "))
	}
	for i, a := range argv {
		var p string
		switch {
		case a == "--out" || a == "-out":
			if i+1 >= len(argv) {
				return errors.New("--out needs a path")
			}
			p = argv[i+1]
		case strings.HasPrefix(a, "--out="):
			p = strings.TrimPrefix(a, "--out=")
		default:
			continue
		}
		if p == "-" {
			continue
		}
		if err := checkDemoPath(p); err != nil {
			return fmt.Errorf("--out: %w", err)
		}
	}
	return nil
}

// checkDemoPath allows only relative paths inside .tmp/demo/.
func checkDemoPath(p string) error {
	if p == "" || filepath.IsAbs(p) || strings.Contains(p, "\\") {
		return fmt.Errorf("%q must be a relative path under %s/", p, DemoDir)
	}
	c := path.Clean(p)
	if !strings.HasPrefix(c, DemoDir+"/") || strings.Contains(c, "..") {
		return fmt.Errorf("%q is outside %s/; a script writes only there", p, DemoDir)
	}
	return nil
}

func friendlyYAML(err error) string {
	m := err.Error()
	m = strings.TrimPrefix(m, "yaml: ")
	if strings.Contains(m, "cannot unmarshal !!str") && strings.Contains(m, "[]string") {
		return m + " (a run step is a list of words, never a shell string)"
	}
	if strings.Contains(m, "not found in type") {
		return m + " (an unknown field or step kind)"
	}
	return m
}

// expand replaces ${name} with a captured value; an unknown name is an error.
func expand(s string, vars map[string]string) (string, error) {
	var missing string
	out := reVar.ReplaceAllStringFunc(s, func(m string) string {
		k := reVar.FindStringSubmatch(m)[1]
		v, ok := vars[k]
		if !ok && missing == "" {
			missing = k
		}
		return v
	})
	if missing != "" {
		return "", fmt.Errorf("${%s} isn't captured by an earlier step", missing)
	}
	return out, nil
}
