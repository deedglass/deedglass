package witness

import (
	"fmt"
	"sort"
	"strings"
)

// ToolInfo is what config validation knows about one tool of the connector.
type ToolInfo struct {
	Name       string
	SideEffect string // read | write | destructive
	Idempotent bool
	Args       []string // declared argument names
}

// FieldError is one refused field of a connector config ("config.tools[1].readback.tool").
type FieldError struct {
	Param   string
	Message string
}

// CompileToolSpec checks a write tool's read-back and postcondition when the connector config is
// saved (S19 §3.5): the read tool exists, is a read and is idempotent; templates name declared
// arguments or result.body; expect has at most 32 well-formed paths; the CEL compiles, returns a
// bool and passes the cost estimate. at is the tool's param path ("config.tools[1]").
func CompileToolSpec(at string, tool ToolInfo, rb *ReadBackSpec, postcondition string, tools map[string]ToolInfo) []FieldError {
	var errs []FieldError
	add := func(param, format string, a ...any) {
		errs = append(errs, FieldError{Param: param, Message: fmt.Sprintf(format, a...)})
	}
	if rb == nil {
		if strings.TrimSpace(postcondition) != "" {
			add(at+".postcondition", "A postcondition needs a read-back to read from. Add readback: { tool: get, args: … } to this tool.")
		}
		return errs
	}
	rt, ok := tools[rb.Tool]
	switch {
	case rb.Tool == "":
		add(at+".readback.tool", "Name the read tool the read-back calls, such as %q.", suggestRead(tools))
	case !ok:
		add(at+".readback.tool", "%q isn't a tool of this connector. Name a read tool such as %q.", clipText(rb.Tool), suggestRead(tools))
	case rb.Tool == tool.Name || rt.SideEffect != "read":
		add(at+".readback.tool", "%q writes, so it can't be a read-back. Name a read tool such as %q.", rb.Tool, suggestRead(tools))
	case !rt.Idempotent:
		add(at+".readback.tool", "%q isn't idempotent, so it can't be a read-back. Name a read tool that is.", rb.Tool)
	}
	declared := map[string]bool{}
	for _, a := range tool.Args {
		declared[a] = true
	}
	readArgs := map[string]bool{}
	for _, a := range rt.Args {
		readArgs[a] = true
	}
	checkTemplate := func(param string, v any) {
		s, isString := v.(string)
		if !isString {
			return
		}
		if ref := templateRef(s); ref != "" {
			if rest, ok := strings.CutPrefix(ref, "args"); ok {
				name := strings.TrimPrefix(rest, ".")
				if i := strings.IndexAny(name, ".["); i >= 0 {
					name = name[:i]
				}
				if !declared[name] {
					add(param, "%s names args.%s, which %s doesn't declare. Use one of its args, or result.body.<field>.", clipText(s), clipText(name), tool.Name)
				}
			}
			return
		}
		if strings.Contains(s, "{{") {
			add(param, "Templates are whole values only, like \"{{args.id}}\" or \"{{result.body.id}}\".")
		}
	}
	for _, k := range sortedKeys(rb.Args) {
		if ok && !readArgs[k] {
			add(at+".readback.args."+k, "%q isn't an argument of %q.", clipText(k), rb.Tool)
		}
		checkTemplate(at+".readback.args."+k, rb.Args[k])
	}
	if len(rb.Expect) > MaxExpectPaths {
		add(at+".readback.expect", "expect has %d paths; the limit is %d.", len(rb.Expect), MaxExpectPaths)
	}
	for _, p := range sortedAny(rb.Expect) {
		if !reExpectPath.MatchString(p) {
			add(at+".readback.expect", "%q isn't a path. Write it like body.status or body.items[0].id.", clipText(p))
			continue
		}
		checkTemplate(at+".readback.expect."+p, rb.Expect[p])
	}
	switch rb.Consistency {
	case "", ConsistencyStrong, ConsistencyEventual:
	default:
		add(at+".readback.consistency", "consistency must be strong or eventual.")
	}
	if rb.Attempts < 0 || rb.Attempts > MaxAttempts {
		add(at+".readback.attempts", "attempts must be from 1 to %d.", MaxAttempts)
	}
	if rb.Attempts > 1 && rb.Consistency != ConsistencyEventual {
		add(at+".readback.attempts", "attempts above 1 need consistency: eventual.")
	}
	if rb.IntervalMS != 0 && (rb.IntervalMS < MinIntervalMS || rb.IntervalMS > MaxIntervalMS) {
		add(at+".readback.interval_ms", "interval_ms must be from %d to %d.", MinIntervalMS, MaxIntervalMS)
	}
	switch rb.OnNotFound {
	case "", OnNotFoundGap, OnNotFoundViolation:
	default:
		add(at+".readback.on_not_found", "on_not_found must be gap or violation.")
	}
	if strings.TrimSpace(postcondition) != "" {
		if _, err := CompilePostcondition(postcondition); err != nil {
			add(at+".postcondition", "%s", err.Error())
		}
	}
	return errs
}

func suggestRead(tools map[string]ToolInfo) string {
	names := make([]string, 0, len(tools))
	for n, t := range tools {
		if t.SideEffect == "read" {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	for _, n := range names {
		if n == "get" {
			return n
		}
	}
	if len(names) > 0 {
		return names[0]
	}
	return "get"
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func sortedAny(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func clipText(s string) string {
	if len(s) > 64 {
		return s[:61] + "…"
	}
	return s
}
