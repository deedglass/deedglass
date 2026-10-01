package witness

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

const ordersSchema = `{"type":"object","properties":{"status":{"type":"integer"},"body":{"type":"object","properties":{"id":{"type":"string"},"status":{"type":"string"}}}}}`

func codesOf(rs []Reason) []Code {
	out := make([]Code, len(rs))
	for i, r := range rs {
		out[i] = r.Code
	}
	return out
}

func TestEvaluateL0(t *testing.T) {
	ok := json.RawMessage(`{"status":200,"body":{"id":"42","status":"shipped"}}`)
	big := `{"status":200,"body":{"blob":"` + strings.Repeat("x", 300<<10) + `"}}`
	cases := []struct {
		name    string
		mode    string
		result  ResultView
		schema  string
		want    []Code
		method  string
		message string // a substring of the last reason's message
	}{
		{"gateway success, no schema", ModeGateway, ResultView{Status: StatusSucceeded, Output: ok, OutputBytes: len(ok)}, "",
			[]Code{CodeToolReportedSuccess}, MethodReported, "The tool reported success."},
		{"schema valid", ModeGateway, ResultView{Status: StatusSucceeded, Output: ok, OutputBytes: len(ok)}, ordersSchema,
			[]Code{CodeToolReportedSuccess, CodeResponseSchemaValid}, MethodResponseSchema, "matched the tool's output schema"},
		{"schema invalid", ModeGateway, ResultView{Status: StatusSucceeded, Output: json.RawMessage(`{"status":200,"body":{"id":"42","status":5}}`)}, ordersSchema,
			[]Code{CodeToolReportedSuccess, CodeResponseSchemaInvalid}, MethodReported, "output schema at body.status, so"},
		{"broken schema", ModeGateway, ResultView{Status: StatusSucceeded, Output: ok}, `{"type":"thing"}`,
			[]Code{CodeToolReportedSuccess, CodeResponseSchemaInvalid}, MethodReported, "at (schema)"},
		{"not JSON", ModeGateway, ResultView{Status: StatusSucceeded, Output: json.RawMessage(`{`)}, ordersSchema,
			[]Code{CodeToolReportedSuccess, CodeResponseSchemaInvalid}, MethodReported, "at (response)"},
		{"proxy success", ModeProxy, ResultView{Status: StatusSucceeded}, "",
			[]Code{CodeEdgeReportedSuccess}, MethodEdgeReported, "The edge reported success."},
		{"unknown, timeout", ModeGateway, ResultView{Status: StatusUnknown, Cause: "timeout"}, ordersSchema,
			[]Code{CodeOutcomeUnknown}, MethodReported, "(timeout)"},
		{"unknown, no cause", ModeGateway, ResultView{Status: StatusUnknown}, "",
			[]Code{CodeOutcomeUnknown}, MethodReported, "(outcome_unknown)"},
		{"300 KiB output, digested", ModeGateway, ResultView{Status: StatusSucceeded, OutputBytes: 300 << 10}, ordersSchema,
			[]Code{CodeToolReportedSuccess, CodeSchemaSkipped}, MethodReported, "too large"},
		{"300 KiB output, kept", ModeGateway, ResultView{Status: StatusSucceeded, Output: json.RawMessage(big)}, ordersSchema,
			[]Code{CodeToolReportedSuccess, CodeSchemaSkipped}, MethodReported, "too large"},
		{"no output kept", ModeGateway, ResultView{Status: StatusSucceeded}, ordersSchema,
			[]Code{CodeToolReportedSuccess}, MethodReported, "reported success"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := Subject{Tool: "orders.update_status", Mode: c.mode, Result: c.result, Spec: ToolWitnessSpec{OutputSchema: json.RawMessage(c.schema)}}
			rs, method := EvaluateL0(s)
			if got := codesOf(rs); !slices.Equal(got, c.want) {
				t.Fatalf("reasons %v, want %v", got, c.want)
			}
			if method != c.method {
				t.Errorf("method %q, want %q", method, c.method)
			}
			last := rs[len(rs)-1]
			if !strings.Contains(last.Message, c.message) {
				t.Errorf("message %q doesn't contain %q", last.Message, c.message)
			}
			for _, r := range rs {
				if r.Level != L0 {
					t.Errorf("%s at %s", r.Code, r.Level)
				}
			}
		})
	}
}

func TestEvaluateL0Evidence(t *testing.T) {
	rs, _ := EvaluateL0(Subject{Mode: ModeGateway, Result: ResultView{Status: StatusUnknown, Cause: "edge_lost"}})
	if len(rs) != 1 || rs[0].Evidence["cause"] != "edge_lost" {
		t.Fatalf("got %+v", rs)
	}
	// An invalid response adds no pass beyond the tool's own report, so L0 isn't clean.
	rs, _ = EvaluateL0(Subject{Mode: ModeGateway, Result: ResultView{Status: StatusSucceeded, Output: json.RawMessage(`{"body":{"status":5}}`)},
		Spec: ToolWitnessSpec{OutputSchema: json.RawMessage(ordersSchema)}})
	if Precedence(rs) != Unverifiable {
		t.Errorf("schema invalid gave %s", Precedence(rs))
	}
}

func TestOutputPath(t *testing.T) {
	for in, want := range map[string]string{"args.body.status": "body.status", "args": "(response)", "args.status": "status", "": "(response)"} {
		if got := outputPath(in); got != want {
			t.Errorf("outputPath(%q) = %q, want %q", in, got, want)
		}
	}
}
