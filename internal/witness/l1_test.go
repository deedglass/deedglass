package witness

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/deedglass/deedglass/internal/sqlguard"
)

// fakeObserver answers read-backs from fn and records every request.
type fakeObserver struct {
	mu   sync.Mutex
	fn   func(ctx context.Context, attempt int) (Observation, error)
	reqs []ReadRequest
}

func (f *fakeObserver) ReadBack(ctx context.Context, _ Subject, req ReadRequest, attempt int) (Observation, error) {
	f.mu.Lock()
	f.reqs = append(f.reqs, req)
	f.mu.Unlock()
	return f.fn(ctx, attempt)
}

func (f *fakeObserver) calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.reqs)
}

// answer is a read-back that returns body with status 200.
func answer(body string) func(context.Context, int) (Observation, error) {
	return func(context.Context, int) (Observation, error) {
		return Observation{Status: 200, Body: decode([]byte(body)), Source: "orders.get", RequestDigest: "sha256:5d1c",
			ResponseDigest: "sha256:a07e", LatencyMS: 12}, nil
	}
}

func fail(err error) func(context.Context, int) (Observation, error) {
	return func(context.Context, int) (Observation, error) { return Observation{}, err }
}

// sequence answers attempt i with bodies[i-1] (the last one repeats).
func sequence(bodies ...string) func(context.Context, int) (Observation, error) {
	return func(ctx context.Context, attempt int) (Observation, error) {
		b := bodies[min(attempt, len(bodies))-1]
		if b == "404" {
			return Observation{}, ErrNotFound
		}
		return answer(b)(ctx, attempt)
	}
}

func ordersReadBack() *ReadBackSpec {
	return &ReadBackSpec{Tool: "get", Args: map[string]string{"id": "{{args.id}}"}, Expect: map[string]any{"body.status": "{{args.status}}"}}
}

func restSubject(args string, rb *ReadBackSpec, post string) Subject {
	return Subject{ActionID: "act_1", Org: "org_1", Agent: "agt_1", Tool: "orders.update_status", ConnectorType: "rest",
		Mode: ModeGateway, SideEffect: "write", Args: json.RawMessage(args),
		Result:   ResultView{Status: StatusSucceeded, Output: json.RawMessage(`{"status":200,"body":{"id":"42","status":"shipped"}}`)},
		Spec:     ToolWitnessSpec{Connector: "orders", Tool: "update_status", ReadBack: rb, Postcondition: post},
		Required: L1}
}

const shippedArgs = `{"id":"42","status":"shipped"}`

func TestEvaluateL1ReadBack(t *testing.T) {
	ctx := context.Background()
	t.Run("match", func(t *testing.T) {
		obs := &fakeObserver{fn: answer(`{"id":"42","status":"shipped"}`)}
		r := EvaluateL1(ctx, restSubject(shippedArgs, ordersReadBack(), ""), obs, true)
		one(t, r.Reasons, CodeReadbackMatched, `Read back orders.get: status is "shipped".`)
		if r.Method != "readback.rest.get" || r.Deferred {
			t.Errorf("method %q deferred %v", r.Method, r.Deferred)
		}
		want := &ReadbackDetail{Tool: "orders.get", Target: `id "42"`, RequestDigest: "sha256:5d1c", Status: 200, ResponseDigest: "sha256:a07e", Attempts: 1, LatencyMS: 12}
		if !reflect.DeepEqual(r.Readback, want) {
			t.Errorf("readback %+v, want %+v", r.Readback, want)
		}
		if len(obs.reqs) != 1 || obs.reqs[0].Tool != "orders.get" || string(obs.reqs[0].Args) != `{"id":"42"}` || obs.reqs[0].Target != `id "42"` {
			t.Errorf("request %+v", obs.reqs)
		}
	})
	t.Run("mismatch", func(t *testing.T) {
		obs := &fakeObserver{fn: answer(`{"id":"42","status":"pending"}`)}
		r := EvaluateL1(ctx, restSubject(shippedArgs, ordersReadBack(), ""), obs, true)
		one(t, r.Reasons, CodePostconditionFailed, `orders.update_status reported success, but orders.get says status is "pending", not "shipped".`)
		want := []FieldDiff{{Path: "body.status", Expected: "shipped", Observed: "pending"}}
		if !reflect.DeepEqual(r.Reasons[0].Diff, want) || r.Reasons[0].Class != Violation || r.Reasons[0].Check != "readback.expect" {
			t.Errorf("reason %+v", r.Reasons[0])
		}
	})
	t.Run(`"42" vs 42`, func(t *testing.T) {
		rb := &ReadBackSpec{Tool: "get", Args: map[string]string{"id": "{{args.id}}"}, Expect: map[string]any{"body.id": "{{args.id}}"}}
		r := EvaluateL1(ctx, restSubject(`{"id":"42"}`, rb, ""), &fakeObserver{fn: answer(`{"id":42}`)}, true)
		one(t, r.Reasons, CodeReadbackMatched, `Read back orders.get: id is 42.`)
		r = EvaluateL1(ctx, restSubject(`{"id":"042"}`, rb, ""), &fakeObserver{fn: answer(`{"id":42}`)}, true)
		one(t, r.Reasons, CodePostconditionFailed, `says id is 42, not "042".`)
	})
	t.Run("not found", func(t *testing.T) {
		for mode, want := range map[string]Code{"": CodeReadbackNotFound, OnNotFoundGap: CodeReadbackNotFound, OnNotFoundViolation: CodeReadbackAbsent} {
			rb := ordersReadBack()
			rb.OnNotFound = mode
			r := EvaluateL1(ctx, restSubject(shippedArgs, rb, ""), &fakeObserver{fn: fail(ErrNotFound)}, true)
			if len(r.Reasons) != 1 || r.Reasons[0].Code != want {
				t.Errorf("on_not_found %q: %+v", mode, r.Reasons)
			}
		}
		rb := ordersReadBack()
		rb.OnNotFound = OnNotFoundViolation
		r := EvaluateL1(ctx, restSubject(shippedArgs, rb, ""), &fakeObserver{fn: fail(ErrNotFound)}, true)
		one(t, r.Reasons, CodeReadbackAbsent, `orders.update_status reported success, but orders.get found nothing at id "42".`)
		r = EvaluateL1(ctx, restSubject(shippedArgs, ordersReadBack(), ""), &fakeObserver{fn: fail(ErrNotFound)}, true)
		one(t, r.Reasons, CodeReadbackNotFound, `orders.get found nothing at id "42". It may not be visible yet.`)
	})
	t.Run("eventual, right on attempt 2 of 3", func(t *testing.T) {
		rb := ordersReadBack()
		rb.Consistency, rb.Attempts, rb.IntervalMS = ConsistencyEventual, 3, MinIntervalMS
		obs := &fakeObserver{fn: sequence(`{"status":"pending"}`, `{"status":"shipped"}`)}
		r := EvaluateL1(ctx, restSubject(shippedArgs, rb, ""), obs, true)
		one(t, r.Reasons, CodeReadbackMatched, "")
		if r.Readback.Attempts != 2 || obs.calls() != 2 || r.Readback.LatencyMS != 24 {
			t.Errorf("attempts %d, calls %d, latency %d", r.Readback.Attempts, obs.calls(), r.Readback.LatencyMS)
		}
	})
	t.Run("eventual, found on attempt 2", func(t *testing.T) {
		rb := ordersReadBack()
		rb.Consistency, rb.IntervalMS = ConsistencyEventual, MinIntervalMS
		obs := &fakeObserver{fn: sequence("404", `{"status":"shipped"}`)}
		r := EvaluateL1(ctx, restSubject(shippedArgs, rb, ""), obs, true)
		one(t, r.Reasons, CodeReadbackMatched, "")
		if r.Readback.Attempts != 2 {
			t.Errorf("attempts %d", r.Readback.Attempts)
		}
	})
	t.Run("eventual, wrong every time", func(t *testing.T) {
		rb := ordersReadBack()
		rb.Consistency, rb.IntervalMS = ConsistencyEventual, MinIntervalMS
		obs := &fakeObserver{fn: answer(`{"status":"pending"}`)}
		r := EvaluateL1(ctx, restSubject(shippedArgs, rb, ""), obs, true)
		one(t, r.Reasons, CodePostconditionFailed, "")
		if r.Readback.Attempts != DefaultEventual || obs.calls() != DefaultEventual {
			t.Errorf("attempts %d calls %d", r.Readback.Attempts, obs.calls())
		}
	})
	t.Run("strong, wrong on attempt 1", func(t *testing.T) {
		rb := ordersReadBack()
		rb.Attempts = 3 // ignored without eventual
		obs := &fakeObserver{fn: sequence(`{"status":"pending"}`, `{"status":"shipped"}`)}
		r := EvaluateL1(ctx, restSubject(shippedArgs, rb, ""), obs, true)
		one(t, r.Reasons, CodePostconditionFailed, "")
		if r.Readback.Attempts != 1 || obs.calls() != 1 {
			t.Errorf("attempts %d calls %d", r.Readback.Attempts, obs.calls())
		}
	})
	t.Run("upstream error", func(t *testing.T) {
		r := EvaluateL1(ctx, restSubject(shippedArgs, ordersReadBack(), ""), &fakeObserver{fn: fail(&ReadError{Code: "upstream_error"})}, true)
		one(t, r.Reasons, CodeReadbackFailed, `The read-back orders.get failed (upstream_error), so the result couldn't be confirmed.`)
		r = EvaluateL1(ctx, restSubject(shippedArgs, ordersReadBack(), ""), &fakeObserver{fn: fail(&ReadError{Code: "validation_failed", Err: errors.New("path")})}, true)
		one(t, r.Reasons, CodeReadbackFailed, "(validation_failed)")
		r = EvaluateL1(ctx, restSubject(shippedArgs, ordersReadBack(), ""), &fakeObserver{fn: fail(&ReadError{})}, true)
		one(t, r.Reasons, CodeReadbackFailed, "(upstream_error)")
		r = EvaluateL1(ctx, restSubject(shippedArgs, ordersReadBack(), ""), &fakeObserver{fn: fail(errors.New("boom"))}, true)
		one(t, r.Reasons, CodeReadbackFailed, "(upstream_error)")
	})
	t.Run("skip", func(t *testing.T) {
		r := EvaluateL1(ctx, restSubject(shippedArgs, ordersReadBack(), ""), &fakeObserver{fn: fail(&SkipError{Why: "the read tool is paused"})}, true)
		one(t, r.Reasons, CodeReadbackSkipped, "No read-back: the read tool is paused.")
	})
	t.Run("budget", func(t *testing.T) {
		slow := func(ctx context.Context, _ int) (Observation, error) {
			select {
			case <-time.After(300 * time.Millisecond):
				return answer(`{"status":"shipped"}`)(ctx, 1)
			case <-ctx.Done():
				return Observation{}, ctx.Err()
			}
		}
		for _, final := range []bool{true, false} {
			bctx, cancel := WithBudget(ctx, 100*time.Millisecond)
			start := time.Now()
			r := EvaluateL1(bctx, restSubject(shippedArgs, ordersReadBack(), ""), &fakeObserver{fn: slow}, final)
			cancel()
			if el := time.Since(start); el > 250*time.Millisecond {
				t.Errorf("final %v took %s", final, el)
			}
			if final {
				one(t, r.Reasons, CodeReadbackTimeout, "The read-back orders.get took longer than 100 ms.")
			} else if !r.Deferred || r.Reasons != nil {
				t.Errorf("not deferred: %+v", r)
			}
		}
		// A deadline without WithBudget still reads well.
		dctx, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
		defer cancel()
		r := EvaluateL1(dctx, restSubject(shippedArgs, ordersReadBack(), ""), &fakeObserver{fn: slow}, true)
		one(t, r.Reasons, CodeReadbackTimeout, "took longer than its time limit.")
	})
	t.Run("eventual never waits past the budget", func(t *testing.T) {
		rb := ordersReadBack()
		rb.Consistency, rb.Attempts, rb.IntervalMS = ConsistencyEventual, 5, MaxIntervalMS
		bctx, cancel := WithBudget(ctx, 200*time.Millisecond)
		defer cancel()
		start := time.Now()
		r := EvaluateL1(bctx, restSubject(shippedArgs, rb, ""), &fakeObserver{fn: answer(`{"status":"pending"}`)}, true)
		if time.Since(start) > 150*time.Millisecond || r.Readback.Attempts != 1 {
			t.Errorf("waited %s, %d attempts", time.Since(start), r.Readback.Attempts)
		}
		one(t, r.Reasons, CodePostconditionFailed, "")
	})
	t.Run("CEL, one conjunct wrong", func(t *testing.T) {
		rb := &ReadBackSpec{Tool: "get", Args: map[string]string{"id": "{{args.id}}"}}
		r := EvaluateL1(ctx, restSubject(`{"id":"42","a":1}`, rb, `args.a == 1 && observation.body.b == "x"`), &fakeObserver{fn: answer(`{"b":"y"}`)}, true)
		one(t, r.Reasons, CodePostconditionFailed, `orders.update_status reported success, but orders.get says b is "y", not "x".`)
		if d := r.Reasons[0].Diff; len(d) != 1 || d[0].Path != "observation.body.b" {
			t.Errorf("diff %+v", d)
		}
		if r.Method != "postcondition.rest" || r.Reasons[0].Check != "postcondition" {
			t.Errorf("method %q check %q", r.Method, r.Reasons[0].Check)
		}
	})
	t.Run("CEL false without a comparison", func(t *testing.T) {
		rb := &ReadBackSpec{Tool: "get", Args: map[string]string{"id": "{{args.id}}"}}
		r := EvaluateL1(ctx, restSubject(`{"id":"42"}`, rb, `has(observation.body.paid)`), &fakeObserver{fn: answer(`{"b":"y"}`)}, true)
		one(t, r.Reasons, CodePostconditionFailed, "says the postcondition is false, not true.")
	})
	t.Run("expect and CEL", func(t *testing.T) {
		r := EvaluateL1(ctx, restSubject(shippedArgs, ordersReadBack(), `observation.body.status == args.status`), &fakeObserver{fn: answer(`{"status":"shipped"}`)}, true)
		if got := codesOf(r.Reasons); !reflect.DeepEqual(got, []Code{CodeReadbackMatched, CodePostconditionHeld}) {
			t.Fatalf("reasons %v", got)
		}
		if r.Method != "readback.rest.get" {
			t.Errorf("method %q", r.Method)
		}
		if r.Reasons[1].Message != "Postcondition held: observation.body.status == args.status." {
			t.Errorf("message %q", r.Reasons[1].Message)
		}
	})
	t.Run("nothing to compare", func(t *testing.T) {
		rb := &ReadBackSpec{Tool: "get", Args: map[string]string{"id": "{{args.id}}"}}
		s := restSubject(shippedArgs, rb, "")
		s.ConnectorType = ""
		r := EvaluateL1(ctx, s, &fakeObserver{fn: answer(`{}`)}, true)
		one(t, r.Reasons, CodeReadbackMatched, "Read back orders.get: the record is there.")
		if r.Method != "readback.connector.get" {
			t.Errorf("method %q", r.Method)
		}
	})
	t.Run("template ../admin goes to the connector as written", func(t *testing.T) {
		obs := &fakeObserver{fn: fail(&ReadError{Code: "validation_failed"})}
		r := EvaluateL1(ctx, restSubject(`{"id":"../admin","status":"shipped"}`, ordersReadBack(), ""), obs, true)
		if len(obs.reqs) != 1 || string(obs.reqs[0].Args) != `{"id":"../admin"}` {
			t.Fatalf("requests %+v", obs.reqs)
		}
		one(t, r.Reasons, CodeReadbackFailed, "(validation_failed)")
	})
	t.Run("args from the result and literals", func(t *testing.T) {
		rb := &ReadBackSpec{Tool: "get", Args: map[string]string{"id": "{{result.body.id}}", "view": "full", "gone": "{{args.nope}}"}}
		obs := &fakeObserver{fn: answer(`{}`)}
		EvaluateL1(ctx, restSubject(`{}`, rb, ""), obs, true)
		if string(obs.reqs[0].Args) != `{"id":"42","view":"full"}` || obs.reqs[0].Target != `id "42", view "full"` {
			t.Errorf("request %+v", obs.reqs[0])
		}
	})
}

// one checks that rs is exactly one reason with code, whose message contains msg.
func one(t *testing.T, rs []Reason, code Code, msg string) {
	t.Helper()
	if len(rs) != 1 || rs[0].Code != code {
		t.Fatalf("reasons %+v, want one %s", rs, code)
	}
	if rs[0].Level != L1 {
		t.Errorf("level %s", rs[0].Level)
	}
	if !strings.Contains(rs[0].Message, msg) {
		t.Errorf("message %q doesn't contain %q", rs[0].Message, msg)
	}
}

func TestEvaluateL1NotPlanned(t *testing.T) {
	ctx := context.Background()
	obs := &fakeObserver{fn: answer(`{}`)}
	proxy := restSubject(shippedArgs, ordersReadBack(), "")
	proxy.Mode = ModeProxy
	unknown := restSubject(shippedArgs, ordersReadBack(), "")
	unknown.Result.Status = StatusUnknown
	none := restSubject(shippedArgs, nil, "")
	for name, s := range map[string]Subject{"proxy": proxy, "unknown": unknown, "no read-back": none} {
		if r := EvaluateL1(ctx, s, obs, true); r.Reasons != nil || r.Readback != nil {
			t.Errorf("%s: %+v", name, r)
		}
	}
	if obs.calls() != 0 {
		t.Errorf("%d read-backs ran", obs.calls())
	}
	r := EvaluateL1(ctx, restSubject(shippedArgs, ordersReadBack(), ""), nil, true)
	one(t, r.Reasons, CodeInternalError, "")
}

func TestReadBackSpecDefaults(t *testing.T) {
	cases := []struct {
		rb       ReadBackSpec
		attempts int
		interval time.Duration
	}{
		{ReadBackSpec{}, 1, 300 * time.Millisecond},
		{ReadBackSpec{Attempts: 4}, 1, 300 * time.Millisecond},
		{ReadBackSpec{Consistency: ConsistencyEventual}, 3, 300 * time.Millisecond},
		{ReadBackSpec{Consistency: ConsistencyEventual, Attempts: 9, IntervalMS: 50}, 5, 100 * time.Millisecond},
		{ReadBackSpec{Consistency: ConsistencyEventual, Attempts: 2, IntervalMS: 9000}, 2, 2 * time.Second},
	}
	for _, c := range cases {
		if got := c.rb.attempts(); got != c.attempts {
			t.Errorf("%+v: attempts %d", c.rb, got)
		}
		if got := c.rb.interval(); got != c.interval {
			t.Errorf("%+v: interval %s", c.rb, got)
		}
	}
}

func TestL1Helpers(t *testing.T) {
	for d, want := range map[time.Duration]string{100 * time.Millisecond: "100 ms", 10 * time.Second: "10 s", 1500 * time.Millisecond: "1.5 s"} {
		if got := humanDuration(d); got != want {
			t.Errorf("humanDuration(%s) = %q", d, got)
		}
	}
	if got := clipMethod("readback.REST API.get"); got != "readback.rest_api.get" {
		t.Errorf("clipMethod: %q", got)
	}
	if got := clipMethod(strings.Repeat("a", 80)); len(got) != 64 {
		t.Errorf("clipMethod length %d", len(got))
	}
	for in, want := range map[string]string{"observation.body.b": "b", "observation.status": "status", "args.a": "args.a"} {
		if got := displayCEL(in); got != want {
			t.Errorf("displayCEL(%q) = %q", in, got)
		}
	}
	if (&ReadError{Code: "x"}).Error() == "" || (&ReadError{Code: "x", Err: errors.New("y")}).Error() == "" || (&SkipError{Why: "z"}).Error() == "" {
		t.Error("empty error text")
	}
	if !errors.Is(&ReadError{Err: ErrNotFound}, ErrNotFound) {
		t.Error("ReadError doesn't unwrap")
	}
	if errText(errors.New("witness: the postcondition couldn't be evaluated: no such key: b.")) != "no such key: b" {
		t.Error("errText didn't trim")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if wait(ctx, time.Millisecond) {
		t.Error("wait ignored a cancelled context")
	}
}

// Postgres row read-back.

func pgSubject(sql string) Subject {
	args, _ := json.Marshal(map[string]string{"sql": sql})
	return Subject{ActionID: "act_2", Tool: "pg.execute", ConnectorType: "postgres", Mode: ModeGateway, SideEffect: "write",
		Args: args, Result: ResultView{Status: StatusSucceeded, Evidence: map[string]string{"xid8": "777"}},
		Spec: ToolWitnessSpec{Connector: "pg", Tool: "execute", PostgresRow: true}, Required: L1}
}

const pgUpdate = `UPDATE public.orders SET status = 'shipped' WHERE id = 42`

func rowAnswer(row any) func(context.Context, int) (Observation, error) {
	return func(context.Context, int) (Observation, error) { return Observation{Body: row, LatencyMS: 4}, nil }
}

func TestEvaluateL1PostgresRow(t *testing.T) {
	ctx := context.Background()
	col := func(match bool, observed any) []ColumnResult {
		return []ColumnResult{{Column: "status", Compared: true, Match: match, Expected: "shipped", Observed: observed},
			{Column: "updated_at", Compared: false}}
	}
	written := RowObservation{RowFound: true, XminMatches: true, Table: "public.orders", Key: "id = 42", Columns: col(true, "shipped")}
	t.Run("row_written", func(t *testing.T) {
		obs := &fakeObserver{fn: rowAnswer(written)}
		r := EvaluateL1(ctx, pgSubject(pgUpdate), obs, true)
		one(t, r.Reasons, CodeRowWritten, "Postgres shows row id = 42 in public.orders was last written by this action's transaction.")
		if r.Method != MethodPostgresRow || r.Readback.Tool != "postgres.row" || r.Readback.Target != "id = 42 in public.orders" || r.Readback.LatencyMS != 4 {
			t.Errorf("result %+v %+v", r, r.Readback)
		}
		if r.Reasons[0].Evidence["compared"] != "1 of 2 columns" || r.Reasons[0].Class != Pass {
			t.Errorf("reason %+v", r.Reasons[0])
		}
		if obs.reqs[0].Tool != "" || obs.reqs[0].Target != "id = 42 in public.orders" {
			t.Errorf("request %+v", obs.reqs[0])
		}
	})
	t.Run("body as a pointer or a map", func(t *testing.T) {
		var m map[string]any
		b, _ := json.Marshal(written)
		_ = json.Unmarshal(b, &m)
		for _, body := range []any{&written, m} {
			r := EvaluateL1(ctx, pgSubject(pgUpdate), &fakeObserver{fn: rowAnswer(body)}, true)
			one(t, r.Reasons, CodeRowWritten, "")
		}
		r := EvaluateL1(ctx, pgSubject(pgUpdate), &fakeObserver{fn: rowAnswer("not a row")}, true)
		one(t, r.Reasons, CodeInternalError, "")
	})
	t.Run("value_mismatch", func(t *testing.T) {
		row := written
		row.Columns = col(false, "cancelled")
		r := EvaluateL1(ctx, pgSubject(pgUpdate), &fakeObserver{fn: rowAnswer(row)}, true)
		one(t, r.Reasons, CodeValueMismatch, `Postgres has status = "cancelled" on row id = 42 in public.orders. The statement set "shipped".`)
		want := []FieldDiff{{Path: "status", Expected: "shipped", Observed: "cancelled"}}
		if r.Reasons[0].Class != Violation || !reflect.DeepEqual(r.Reasons[0].Diff, want) {
			t.Errorf("reason %+v", r.Reasons[0])
		}
	})
	t.Run("row_superseded", func(t *testing.T) {
		row := written
		row.XminMatches = false
		row.Columns = col(false, "cancelled") // another transaction's value is never a violation
		r := EvaluateL1(ctx, pgSubject(pgUpdate), &fakeObserver{fn: rowAnswer(row)}, true)
		one(t, r.Reasons, CodeRowSuperseded, "Another transaction changed row id = 42 in public.orders after this action")
		if r.Reasons[0].Class != Gap {
			t.Errorf("class %s", r.Reasons[0].Class)
		}
	})
	t.Run("row_missing", func(t *testing.T) {
		r := EvaluateL1(ctx, pgSubject(pgUpdate), &fakeObserver{fn: rowAnswer(RowObservation{})}, true)
		one(t, r.Reasons, CodeRowMissing, "Row id = 42 in public.orders isn't there any more")
		r = EvaluateL1(ctx, pgSubject(pgUpdate), &fakeObserver{fn: fail(ErrNotFound)}, true)
		one(t, r.Reasons, CodeRowMissing, "")
	})
	t.Run("skip", func(t *testing.T) {
		r := EvaluateL1(ctx, pgSubject(pgUpdate), &fakeObserver{fn: fail(&SkipError{Why: WhyNotRowReadBack})}, true)
		one(t, r.Reasons, CodeReadbackSkipped, "No read-back: the statement isn't a single-row update by primary key")
		if r.Reasons[0].Class != Note {
			t.Errorf("class %s", r.Reasons[0].Class)
		}
	})
	t.Run("errors", func(t *testing.T) {
		r := EvaluateL1(ctx, pgSubject(pgUpdate), &fakeObserver{fn: fail(&ReadError{Code: "statement_timeout"})}, true)
		one(t, r.Reasons, CodeReadbackFailed, "The read-back the row read-back failed (statement_timeout)")
		r = EvaluateL1(ctx, pgSubject(pgUpdate), &fakeObserver{fn: fail(errors.New("x"))}, true)
		one(t, r.Reasons, CodeReadbackFailed, "(upstream_error)")
		bctx, cancel := WithBudget(ctx, time.Millisecond)
		defer cancel()
		<-bctx.Done()
		r = EvaluateL1(bctx, pgSubject(pgUpdate), &fakeObserver{fn: fail(bctx.Err())}, true)
		one(t, r.Reasons, CodeReadbackTimeout, "took longer than 1 ms.")
		r = EvaluateL1(bctx, pgSubject(pgUpdate), &fakeObserver{fn: fail(bctx.Err())}, false)
		if !r.Deferred || r.Method != MethodPostgresRow {
			t.Errorf("not deferred: %+v", r)
		}
	})
	t.Run("not a simple single-row update", func(t *testing.T) {
		for _, sql := range []string{
			`UPDATE public.orders SET status = 'x' WHERE id IN (1, 2, 3)`,
			`WITH x AS (SELECT 1) UPDATE public.orders SET status = 'x' WHERE id = 1`,
			`DELETE FROM public.orders WHERE id = 1`,
			`INSERT INTO public.orders (id) VALUES (1)`,
			`UPDATE public.orders SET status = 'x'`,
			`SELECT 1`,
		} {
			s := pgSubject(sql)
			p := PlanFor(s)
			if p.L1 || p.Why != WhyNotRowReadBack {
				t.Errorf("%s: plan %+v", sql, p)
			}
			obs := &fakeObserver{fn: rowAnswer(written)}
			r := EvaluateL1(ctx, s, obs, true)
			one(t, r.Reasons, CodeReadbackSkipped, "L2 covers it when the connector is witnessed")
			if obs.calls() != 0 {
				t.Errorf("%s: the observer ran", sql)
			}
		}
		s := pgSubject(pgUpdate)
		s.Spec.Tool = "query"
		if p := PlanFor(s); p.L1 || p.Why == "" {
			t.Errorf("another tool: %+v", p)
		}
		s = pgSubject(pgUpdate)
		s.Args = json.RawMessage(`{`)
		if p := PlanFor(s); p.L1 || p.Why == "" {
			t.Errorf("bad args: %+v", p)
		}
	})
	t.Run("plan", func(t *testing.T) {
		if p := PlanFor(pgSubject(pgUpdate)); !p.L1 || p.Kind != KindPostgresRow {
			t.Errorf("plan %+v", p)
		}
		if p := PlanFor(restSubject(shippedArgs, ordersReadBack(), "")); !p.L1 || p.Kind != KindReadBack {
			t.Errorf("plan %+v", p)
		}
	})
}

func TestPostgresTarget(t *testing.T) {
	cases := []struct {
		sql, key, table string
	}{
		{pgUpdate, "id = 42", "public.orders"},
		{`UPDATE orders SET status = 'x' WHERE code = 'a''b'`, "code = 'a''b'", "app.orders"},
		{`UPDATE public.orders SET total = total + 1 WHERE id = 7`, "id = 7", "public.orders"},
	}
	for _, c := range cases {
		tg, why := PostgresTarget(c.sql, "app")
		if why != "" || tg.Key != c.key || tg.Shape.Table != c.table {
			t.Errorf("%s: %+v %q", c.sql, tg, why)
		}
	}
	if got := valueText(sqlguardNull()); got != "NULL" {
		t.Errorf("valueText(null) = %q", got)
	}
}

func sqlguardNull() sqlguard.Value { return sqlguard.Value{Kind: sqlguard.ShapeValueNull} }
