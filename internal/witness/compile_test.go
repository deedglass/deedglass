package witness

import (
	"fmt"
	"strings"
	"testing"
)

var ordersTools = map[string]ToolInfo{
	"get":           {Name: "get", SideEffect: "read", Idempotent: true, Args: []string{"id"}},
	"list":          {Name: "list", SideEffect: "read", Idempotent: false, Args: []string{"page"}},
	"update_status": {Name: "update_status", SideEffect: "write", Idempotent: true, Args: []string{"id", "status"}},
	"cancel":        {Name: "cancel", SideEffect: "destructive", Args: []string{"id"}},
}

const at = "config.tools[1]"

func compileSpec(rb *ReadBackSpec, post string) []FieldError {
	return CompileToolSpec(at, ordersTools["update_status"], rb, post, ordersTools)
}

func TestCompileToolSpecValid(t *testing.T) {
	rb := &ReadBackSpec{Tool: "get", Args: map[string]string{"id": "{{args.id}}"}, Expect: map[string]any{"body.status": "{{args.status}}", "body.id": "{{result.body.id}}", "body.items[0].qty": 1,
		"body.valueQuantity.value": "{{args.status}}"},
		Consistency: ConsistencyEventual, Attempts: 3, IntervalMS: 300, OnNotFound: OnNotFoundViolation}
	if errs := compileSpec(rb, `observation.body.status == args.status`); len(errs) != 0 {
		t.Fatalf("errors %+v", errs)
	}
	if errs := compileSpec(&ReadBackSpec{Tool: "get", Args: map[string]string{"id": "{{args.id}}"}}, ""); len(errs) != 0 {
		t.Fatalf("minimal: %+v", errs)
	}
	if errs := compileSpec(nil, ""); len(errs) != 0 {
		t.Fatalf("no read-back: %+v", errs)
	}
}

func TestCompileToolSpecRefusals(t *testing.T) {
	base := func() *ReadBackSpec {
		return &ReadBackSpec{Tool: "get", Args: map[string]string{"id": "{{args.id}}"}, Expect: map[string]any{"body.status": "{{args.status}}"}}
	}
	many := map[string]any{}
	for i := range MaxExpectPaths + 1 {
		many[fmt.Sprintf("body.f%d", i)] = i
	}
	cases := []struct {
		name  string
		edit  func(*ReadBackSpec)
		post  string
		param string
		msg   string // the whole message, or a prefix ending in "…"
	}{
		{"read-back that writes", func(r *ReadBackSpec) { r.Tool = "update_status" }, "",
			"config.tools[1].readback.tool", `"update_status" writes, so it can't be a read-back. Name a read tool such as "get".`},
		{"destructive read-back", func(r *ReadBackSpec) { r.Tool = "cancel"; r.Args = nil }, "",
			"config.tools[1].readback.tool", `"cancel" writes, so it can't be a read-back. Name a read tool such as "get".`},
		{"unknown tool", func(r *ReadBackSpec) { r.Tool = "fetch" }, "",
			"config.tools[1].readback.tool", `"fetch" isn't a tool of this connector. Name a read tool such as "get".`},
		{"no tool", func(r *ReadBackSpec) { r.Tool = "" }, "",
			"config.tools[1].readback.tool", `Name the read tool the read-back calls, such as "get".`},
		{"non-idempotent read", func(r *ReadBackSpec) { r.Tool = "list"; r.Args = nil }, "",
			"config.tools[1].readback.tool", `"list" isn't idempotent, so it can't be a read-back. Name a read tool that is.`},
		{"argument the read tool lacks", func(r *ReadBackSpec) { r.Args["view"] = "full" }, "",
			"config.tools[1].readback.args.view", `"view" isn't an argument of "get".`},
		{"undeclared arg in a template", func(r *ReadBackSpec) { r.Expect["body.status"] = "{{args.state}}" }, "",
			"config.tools[1].readback.expect.body.status", `{{args.state}} names args.state, which update_status doesn't declare. Use one of its args, or result.body.<field>.`},
		{"undeclared nested arg", func(r *ReadBackSpec) { r.Args["id"] = "{{args.order.id}}" }, "",
			"config.tools[1].readback.args.id", `{{args.order.id}} names args.order, which…`},
		{"interpolated template", func(r *ReadBackSpec) { r.Args["id"] = "x{{args.id}}" }, "",
			"config.tools[1].readback.args.id", `Templates are whole values only, like "{{args.id}}" or "{{result.body.id}}".`},
		{"interpolated expect", func(r *ReadBackSpec) { r.Expect["body.status"] = "{{args.status}}!" }, "",
			"config.tools[1].readback.expect.body.status", `Templates are whole values only…`},
		{"too many expect paths", func(r *ReadBackSpec) { r.Expect = many }, "",
			"config.tools[1].readback.expect", `expect has 33 paths; the limit is 32.`},
		{"bad path", func(r *ReadBackSpec) { r.Expect = map[string]any{"body..status": "x"} }, "",
			"config.tools[1].readback.expect", `"body..status" isn't a path. Write it like body.status or body.items[0].id.`},
		{"upper-case path", func(r *ReadBackSpec) { r.Expect = map[string]any{"Body.status": "x"} }, "",
			"config.tools[1].readback.expect", `"Body.status" isn't a path…`},
		{"consistency", func(r *ReadBackSpec) { r.Consistency = "sometimes" }, "",
			"config.tools[1].readback.consistency", `consistency must be strong or eventual.`},
		{"attempts too many", func(r *ReadBackSpec) { r.Consistency = ConsistencyEventual; r.Attempts = 6 }, "",
			"config.tools[1].readback.attempts", `attempts must be from 1 to 5.`},
		{"attempts negative", func(r *ReadBackSpec) { r.Attempts = -1 }, "",
			"config.tools[1].readback.attempts", `attempts must be from 1 to 5.`},
		{"attempts without eventual", func(r *ReadBackSpec) { r.Attempts = 2 }, "",
			"config.tools[1].readback.attempts", `attempts above 1 need consistency: eventual.`},
		{"interval low", func(r *ReadBackSpec) { r.IntervalMS = 50 }, "",
			"config.tools[1].readback.interval_ms", `interval_ms must be from 100 to 2000.`},
		{"interval high", func(r *ReadBackSpec) { r.IntervalMS = 2001 }, "",
			"config.tools[1].readback.interval_ms", `interval_ms must be from 100 to 2000.`},
		{"on_not_found", func(r *ReadBackSpec) { r.OnNotFound = "ignore" }, "",
			"config.tools[1].readback.on_not_found", `on_not_found must be gap or violation.`},
		{"postcondition request.status", nil, `observation.body.status == request.status`,
			"config.tools[1].postcondition", `1:28: request.status isn't a field. Did you mean args.status?`},
		{"postcondition not a bool", nil, `args.id`,
			"config.tools[1].postcondition", `The postcondition must be true or false…`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rb := base()
			if c.edit != nil {
				c.edit(rb)
			}
			errs := compileSpec(rb, c.post)
			if len(errs) != 1 {
				t.Fatalf("errors %+v, want one", errs)
			}
			e := errs[0]
			if e.Param != c.param {
				t.Errorf("param %q, want %q", e.Param, c.param)
			}
			if p, ok := strings.CutSuffix(c.msg, "…"); ok {
				if !strings.HasPrefix(e.Message, p) {
					t.Errorf("message %q, want prefix %q", e.Message, p)
				}
			} else if e.Message != c.msg {
				t.Errorf("message %q, want %q", e.Message, c.msg)
			}
			if strings.Contains(e.Message, "!") && !strings.Contains(c.msg, "!") {
				t.Errorf("exclamation mark: %s", e.Message)
			}
		})
	}
}

func TestCompileToolSpecPostconditionWithoutReadBack(t *testing.T) {
	errs := compileSpec(nil, `observation.body.status == args.status`)
	if len(errs) != 1 || errs[0].Param != "config.tools[1].postcondition" || !strings.HasPrefix(errs[0].Message, "A postcondition needs a read-back") {
		t.Fatalf("errors %+v", errs)
	}
}

func TestCompileToolSpecSuggestions(t *testing.T) {
	tools := map[string]ToolInfo{"write": {Name: "write", SideEffect: "write"}, "fetch": {Name: "fetch", SideEffect: "read", Idempotent: true}, "a_read": {Name: "a_read", SideEffect: "read", Idempotent: true}}
	errs := CompileToolSpec(at, tools["write"], &ReadBackSpec{Tool: "write"}, "", tools)
	if len(errs) != 1 || !strings.HasSuffix(errs[0].Message, `such as "a_read".`) {
		t.Errorf("errors %+v", errs)
	}
	errs = CompileToolSpec(at, ToolInfo{Name: "w"}, &ReadBackSpec{Tool: strings.Repeat("x", 100)}, "", map[string]ToolInfo{})
	if len(errs) != 1 || !strings.Contains(errs[0].Message, `such as "get"`) || !strings.Contains(errs[0].Message, "…") {
		t.Errorf("errors %+v", errs)
	}
}
