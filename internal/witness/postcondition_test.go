package witness

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCompilePostconditionGoldens(t *testing.T) {
	cases := []struct {
		name, src string
		contains  string
	}{
		{"request_status", `request.status == "x"`, "request.status isn't a field. Did you mean args.status?"},
		{"request_status_line2", "args.a == 1 &&\n  request.status == 2", "2:3: request.status isn't a field"},
		{"syntax", `args.a ==`, "Syntax error"},
		{"non_bool", `args.id`, "must be true or false"},
		{"non_bool_string", `"shipped"`, "must be true or false"},
		{"unknown_variable", `order.status == "shipped"`, "order isn't a variable"},
		{"undeclared_function", `shout(args.id) == "X"`, "1:"},
		{"empty", "  ", "The postcondition is empty."},
		{"too_long", "args.a == " + strings.Repeat("1+", 1100) + "1", "longer than 2,048 characters"},
		{"cost_bomb", `observation.body.items.all(a, observation.body.items.all(b, observation.body.items.all(c, a == b)))`,
			"units per call; the limit is 100,000"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := CompilePostcondition(c.src)
			var ce *CompileError
			if !errors.As(err, &ce) {
				t.Fatalf("got %v, want a CompileError", err)
			}
			got := err.Error()
			if !strings.Contains(got, c.contains) {
				t.Errorf("%q doesn't contain %q", got, c.contains)
			}
			if strings.Contains(got, "!") {
				t.Errorf("exclamation mark: %s", got)
			}
			golden(t, filepath.Join("testdata", "postcondition", c.name+".golden"), got+"\n")
		})
	}
	// The request.status hint carries the line and column of "request".
	_, err := CompilePostcondition(`observation.body.status == request.status`)
	var ce *CompileError
	if !errors.As(err, &ce) || ce.Line != 1 || ce.Column != 28 {
		t.Errorf("got %#v", err)
	}
	if _, err := CompilePostcondition(`observation.body.items.all(a, observation.body.items.all(b, observation.body.items.all(c, a == b)))`); !strings.Contains(err.Error(), "could cost up to ") {
		t.Errorf("cost message: %v", err)
	}
}

func golden(t *testing.T, path, got string) {
	t.Helper()
	if *update {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (go test ./internal/witness -run %s -update)", err, t.Name())
	}
	if string(want) != got {
		t.Errorf("%s differs:\n got: %q\nwant: %q\n(go test ./internal/witness -run %s -update)", path, got, want, t.Name())
	}
}

func TestCompilePostconditionAccepts(t *testing.T) {
	for _, src := range []string{
		`observation.body.status == args.status`,
		`observation.status == 200 && observation.body.qty >= args.qty`,
		`has(observation.body.id) && observation.body.id == result.output.body.id`,
		`observation.body.items.all(i, i.qty > 0)`,
		`observation.body.tags.map(x, x.size()).all(n, n < 10)`,
		`request.tool == "orders.update_status" && request.args.id == args.id && request.mode == "gateway"`,
		`observation.body.paid == true`,
	} {
		if _, err := CompilePostcondition(src); err != nil {
			t.Errorf("%s: %v", src, err)
		}
	}
}

func evalSubject(args, output string) Subject {
	return Subject{Tool: "orders.update_status", Mode: ModeGateway, Args: json.RawMessage(args),
		Result: ResultView{Status: StatusSucceeded, Output: json.RawMessage(output), Meta: map[string]any{"http_status": 200}}}
}

func obsOf(body string) Observation {
	return Observation{Status: 200, Body: decode([]byte(body)), Meta: map[string]any{"latency_ms": 3}}
}

func TestPostconditionEval(t *testing.T) {
	s := evalSubject(`{"id":"42","status":"shipped","a":1,"qty":2}`, `{"status":200,"body":{"id":"42"}}`)
	cases := []struct {
		name, src, body string
		held            bool
		diffs           []string // diff paths
		err             bool
	}{
		{"equal", `observation.body.status == args.status`, `{"status":"shipped"}`, true, nil, false},
		{"not equal", `observation.body.status == args.status`, `{"status":"pending"}`, false, []string{"observation.body.status"}, false},
		{"one conjunct wrong", `args.a == 1 && observation.body.b == "x"`, `{"b":"y"}`, false, []string{"observation.body.b"}, false},
		{"two conjuncts wrong", `observation.body.a == 1 && observation.body.b == "x"`, `{"a":2,"b":"y"}`, false, []string{"observation.body.a", "observation.body.b"}, false},
		{"not a comparison", `args.a == 1 && has(observation.body.c)`, `{"b":"y"}`, false, nil, false},
		{"numbers across types", `observation.body.qty == args.qty && observation.body.price > 1`, `{"qty":2.0,"price":1.5}`, true, nil, false},
		{"result and request", `result.output.body.id == observation.body.id && request.tool == "orders.update_status" && result.meta.http_status == 200`, `{"id":"42"}`, true, nil, false},
		{"status and meta", `observation.status == 200 && observation.meta.latency_ms == 3`, `{}`, true, nil, false},
		{"divide by zero", `observation.body.n / 0 == 1`, `{"n":4}`, false, nil, true},
		{"missing key", `observation.body.missing == "x"`, `{"b":"y"}`, false, nil, true},
		{"null body", `observation.body.status == "x"`, `null`, false, nil, true},
		// CEL's && absorbs the error beside a false: the condition is false, a violation.
		{"error beside false", `observation.body.missing == 1 && observation.body.b == "x"`, `{"b":"y"}`, false, []string{"observation.body.b"}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			pc, err := CompilePostcondition(c.src)
			if err != nil {
				t.Fatal(err)
			}
			held, diffs, err := pc.Eval(s, obsOf(c.body))
			if (err != nil) != c.err {
				t.Fatalf("err %v, want error %v", err, c.err)
			}
			if err != nil {
				if !errors.Is(err, errPostcondition) {
					t.Errorf("%v isn't errPostcondition", err)
				}
				return
			}
			if held != c.held {
				t.Errorf("held %v", held)
			}
			var paths []string
			for _, d := range diffs {
				paths = append(paths, d.Path)
			}
			if strings.Join(paths, ",") != strings.Join(c.diffs, ",") {
				t.Errorf("diffs %v, want %v", paths, c.diffs)
			}
		})
	}
	pc, _ := CompilePostcondition(`args.a == 1 && observation.body.b == "x"`)
	_, diffs, _ := pc.Eval(s, obsOf(`{"b":"y"}`))
	if len(diffs) != 1 || diffs[0].Expected != "x" || diffs[0].Observed != "y" {
		t.Errorf("diff %+v", diffs)
	}
}

func TestPostconditionErrorThroughL1(t *testing.T) {
	for _, src := range []string{`observation.body.n / 0 == 1`, `observation.body.missing == "x"`} {
		s := restSubject(`{"id":"42","status":"shipped"}`, &ReadBackSpec{Tool: "get", Args: map[string]string{"id": "{{args.id}}"}}, src)
		obs := &fakeObserver{fn: answer(`{"id":"42","n":4}`)}
		r := EvaluateL1(context.Background(), s, obs, true)
		if len(r.Reasons) != 1 || r.Reasons[0].Code != CodePostconditionError || r.Reasons[0].Class != Gap {
			t.Fatalf("%s: %+v", src, r.Reasons)
		}
		if !strings.HasPrefix(r.Reasons[0].Message, "The postcondition couldn't be evaluated: ") || strings.Contains(r.Reasons[0].Message, "witness:") {
			t.Errorf("message %q", r.Reasons[0].Message)
		}
	}
	// A postcondition that no longer compiles (a config saved before a rule changed) is a gap too.
	s := restSubject(`{"id":"42"}`, &ReadBackSpec{Tool: "get", Args: map[string]string{"id": "{{args.id}}"}}, `args.id`)
	r := EvaluateL1(context.Background(), s, &fakeObserver{fn: answer(`{"id":"42"}`)}, true)
	if len(r.Reasons) != 1 || r.Reasons[0].Code != CodePostconditionError {
		t.Fatalf("%+v", r.Reasons)
	}
}

func TestPostconditionCache(t *testing.T) {
	a, err := cachedPostcondition("cfg1", "orders.update_status", `args.a == 1`)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := cachedPostcondition("cfg1", "orders.update_status", `args.a == 1`)
	if a != b {
		t.Error("the cache compiled twice")
	}
	c, _ := cachedPostcondition("cfg2", "orders.update_status", `args.a == 1`)
	if c == a {
		t.Error("a different config hash shared the program")
	}
	if _, err := cachedPostcondition("cfg1", "orders.update_status", `args.a`); err == nil {
		t.Error("the cache accepted a bad postcondition")
	}
	d, _ := cachedPostcondition("", "t", `args.a == 1`)
	e, _ := cachedPostcondition("", "t", `args.a == 1`)
	if d == e {
		t.Error("an empty hash was cached")
	}
}

func TestPostconditionHelpers(t *testing.T) {
	for in, want := range map[uint64]string{0: "0", 999: "999", 1000: "1,000", 100000: "100,000", 1234567: "1,234,567"} {
		if got := thousands(in); got != want {
			t.Errorf("thousands(%d) = %q", in, got)
		}
	}
	if got := celMessage(errors.New("operation cancelled: actual cost limit exceeded")); got != "the cost limit of 100,000 was reached" {
		t.Errorf("celMessage: %q", got)
	}
	if got := celMessage(errors.New(strings.Repeat("x", 200))); len(got) != 120 {
		t.Errorf("celMessage didn't clip: %d", len(got))
	}
	if (&CompileError{Message: "m."}).Error() != "m." {
		t.Error("CompileError without a position")
	}
	if got := errText(&CompileError{Line: 1, Column: 2, Message: "Bad."}); got != "Bad" {
		t.Errorf("errText: %q", got)
	}
}
