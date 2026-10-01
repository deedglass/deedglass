package witness

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// S19 §3.11 budgets: Decide < 2 µs; L0 with a 64 KiB schema check p99 < 200 µs; expect over 32
// fields p99 < 50 µs; a CEL postcondition p99 < 100 µs.

func BenchmarkDecide(b *testing.B) {
	in := map[Level][]Reason{
		L0: {R(L0, CodeToolReportedSuccess, "status", nil), R(L0, CodeResponseSchemaValid, "response.schema", nil)},
		L1: {R(L1, CodeReadbackMatched, "readback.expect", map[string]any{"readback": "orders.get", "fields": "status"})},
		L2: {R(L2, CodeInternalError, "l2", nil)},
	}
	methods := map[Level]string{L0: MethodResponseSchema, L1: "readback.rest.get"}
	b.ReportAllocs()
	for b.Loop() {
		d := Decide(L1, in, methods)
		if d.Verdict != Verified {
			b.Fatal(d.Verdict)
		}
	}
}

func BenchmarkL0Schema64KiB(b *testing.B) {
	items := make([]string, 0, 700)
	for i := range 700 {
		items = append(items, fmt.Sprintf(`{"sku":"sku-%05d","name":"%s","qty":%d}`, i, strings.Repeat("n", 50), i))
	}
	out := `{"status":200,"body":{"id":"42","status":"shipped","items":[` + strings.Join(items, ",") + `]}}`
	if len(out) < 60<<10 || len(out) > 70<<10 {
		b.Fatalf("output is %d bytes", len(out))
	}
	schema := `{"type":"object","properties":{"status":{"type":"integer"},"body":{"type":"object","properties":{"id":{"type":"string"},"status":{"type":"string","enum":["pending","shipped","cancelled"]},` +
		`"items":{"type":"array","items":{"type":"object","properties":{"sku":{"type":"string","pattern":"^sku-[0-9]+$"},"name":{"type":"string","maxLength":100},"qty":{"type":"integer","minimum":0}}}}}}}}`
	s := Subject{Mode: ModeGateway, Result: ResultView{Status: StatusSucceeded, Output: json.RawMessage(out), OutputBytes: len(out)},
		Spec: ToolWitnessSpec{OutputSchema: json.RawMessage(schema)}}
	b.SetBytes(int64(len(out)))
	b.ReportAllocs()
	for b.Loop() {
		if _, m := EvaluateL0(s); m != MethodResponseSchema {
			b.Fatal(m)
		}
	}
}

func BenchmarkExpect32Fields(b *testing.B) {
	args := map[string]any{}
	body := map[string]any{}
	spec := map[string]any{}
	for i := range MaxExpectPaths {
		k := fmt.Sprintf("f%02d", i)
		args[k] = fmt.Sprintf("value-%d", i)
		body[k] = fmt.Sprintf("value-%d", i)
		spec["body."+k] = "{{args." + k + "}}"
	}
	a, _ := json.Marshal(args)
	bb, _ := json.Marshal(body)
	env := newTemplateEnv(Subject{Args: a})
	o := Observation{Status: 200, Body: decode(bb)}
	b.ReportAllocs()
	for b.Loop() {
		if r := runExpect(spec, env, o); len(r.diffs) != 0 {
			b.Fatal(r.diffs)
		}
	}
}

func BenchmarkPostcondition(b *testing.B) {
	pc, err := CompilePostcondition(`observation.body.status == args.status && observation.status == 200 && observation.body.qty >= 1`)
	if err != nil {
		b.Fatal(err)
	}
	s := evalSubject(`{"id":"42","status":"shipped"}`, `{"status":200,"body":{"id":"42"}}`)
	o := obsOf(`{"id":"42","status":"shipped","qty":2}`)
	b.ReportAllocs()
	for b.Loop() {
		if held, _, err := pc.Eval(s, o); err != nil || !held {
			b.Fatal(held, err)
		}
	}
}
