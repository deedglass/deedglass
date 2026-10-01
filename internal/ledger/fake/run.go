package fake

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/deedglass/deedglass/internal/deed"
	"github.com/deedglass/deedglass/internal/gate"
	"github.com/deedglass/deedglass/internal/ids"
	"github.com/deedglass/deedglass/internal/ledger"
	"github.com/deedglass/deedglass/internal/store"
)

// Options tunes Run.
type Options struct {
	Batch      int                   // actions per transaction (default 1)
	Pace       time.Duration         // pause between actions (live demos); forces Batch 1
	OnProgress func(done, total int) // after each sealing transaction
}

// bulkWorkers is how many transactions walk actions to their final state at once in bulk mode.
const bulkWorkers = 4

// Result is what a run made.
type Result struct {
	Actions  int
	Deeds    int
	FirstSeq int64 // 0 when nothing was sealed
	LastSeq  int64
	Counts   map[Kind]int
	Levels   map[string]int // allowed and verified, by level
	Rules    map[string]int // denied, by rule
	Errors   map[string]int // failed, by error code
	IDs      []string       // the action ids, in plan order
}

func (r *Result) add(id string, p Plan, envs []*deed.Envelope) {
	r.IDs = append(r.IDs, id)
	r.Actions++
	r.Counts[p.Kind]++
	switch p.Kind {
	case Allow:
		r.Levels[p.Level]++
	case Deny:
		r.Rules[p.RuleID]++
	case Failed:
		r.Errors[p.ErrorCode]++
	}
	for _, e := range envs {
		r.Deeds++
		if r.FirstSeq == 0 || e.Body.Seq < r.FirstSeq {
			r.FirstSeq = e.Body.Seq
		}
		r.LastSeq = max(r.LastSeq, e.Body.Seq)
	}
}

// Run makes plans in org through the Ledger, as agents (indexed by Plan.Agent). With Batch 1 each
// action is created, walked and sealed in one transaction, in plan order. With a larger Batch
// (bulk seeding) actions are walked to their final state in parallel transactions, then sealed in
// plan order, Batch per transaction, so deed seqs still follow plan order.
func Run(ctx context.Context, led *ledger.Ledger, org ids.OrgID, agents []store.Agent, plans []Plan, o Options) (*Result, error) {
	if o.Batch < 1 || o.Pace > 0 {
		o.Batch = 1
	}
	res := &Result{Counts: map[Kind]int{}, Levels: map[string]int{}, Rules: map[string]int{}, Errors: map[string]int{}}
	if o.Batch > 1 {
		return res, runBulk(ctx, led, org, agents, plans, o, res)
	}
	for i, p := range plans {
		var id string
		var envs []*deed.Envelope
		err := led.OrgTxRetry(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
			var err error
			if id, err = prepare(ctx, led, tx, org, agents[p.Agent%len(agents)], p); err != nil {
				return err
			}
			envs, err = finish(ctx, led, tx, id, p)
			return err
		})
		if err != nil {
			return res, fmt.Errorf("fake action %d (%s): %w", i+1, p.Kind, err)
		}
		res.add(id, p, envs)
		if o.OnProgress != nil {
			o.OnProgress(res.Actions, len(plans))
		}
		if o.Pace > 0 && i < len(plans)-1 {
			select {
			case <-ctx.Done():
				return res, ctx.Err()
			case <-time.After(o.Pace):
			}
		}
	}
	return res, nil
}

func runBulk(ctx context.Context, led *ledger.Ledger, org ids.OrgID, agents []store.Agent, plans []Plan, o Options, res *Result) error {
	byPlan := make([]string, len(plans))
	nb := (len(plans) + o.Batch - 1) / o.Batch
	ready := make([]chan error, nb) // batch k is walked (nil) or failed
	for k := range ready {
		ready[k] = make(chan error, 1)
	}
	// Phase 1, in the background: create and walk, Batch actions per transaction, bulkWorkers
	// transactions at a time.
	wctx, cancel := context.WithCancel(ctx)
	defer cancel()
	jobs := make(chan int)
	var wg sync.WaitGroup
	for range bulkWorkers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for k := range jobs {
				lo, hi := k*o.Batch, min((k+1)*o.Batch, len(plans))
				ready[k] <- led.DB().OrgTx(wctx, org, func(ctx context.Context, tx pgx.Tx) error {
					for i := lo; i < hi; i++ {
						id, err := prepare(ctx, led, tx, org, agents[plans[i].Agent%len(agents)], plans[i])
						if err != nil {
							return fmt.Errorf("fake action %d (%s): %w", i+1, plans[i].Kind, err)
						}
						byPlan[i] = id
					}
					return nil
				})
			}
		}()
	}
	go func() {
		defer close(jobs)
		for k := range nb {
			select {
			case jobs <- k:
			case <-wctx.Done():
				return
			}
		}
	}()
	defer wg.Wait()
	// Phase 2, as batches become ready: seal in plan order, Batch per transaction (one chain-lock
	// holder at a time), while later batches are still being walked.
	for k := range nb {
		var err error
		select {
		case err = <-ready[k]:
		case <-ctx.Done():
			err = ctx.Err()
		}
		if err != nil {
			cancel()
			return err
		}
		lo, hi := k*o.Batch, min((k+1)*o.Batch, len(plans))
		envs := make([][]*deed.Envelope, hi-lo)
		err = led.OrgTxRetry(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
			for i := lo; i < hi; i++ {
				e, err := finish(ctx, led, tx, byPlan[i], plans[i])
				if err != nil {
					return fmt.Errorf("fake action %d (%s): %w", i+1, plans[i].Kind, err)
				}
				envs[i-lo] = e
			}
			return nil
		})
		if err != nil {
			cancel()
			return err
		}
		for i := lo; i < hi; i++ {
			res.add(byPlan[i], plans[i], envs[i-lo])
		}
		if o.OnProgress != nil {
			o.OnProgress(res.Actions, len(plans))
		}
	}
	return nil
}

// prepare creates one action and walks it through legal edges only to its final state, setting the
// columns the Gate (S13) will set. It seals nothing.
func prepare(ctx context.Context, led *ledger.Ledger, tx pgx.Tx, org ids.OrgID, agent store.Agent, p Plan) (string, error) {
	obo, ses := p.OnBehalfOf, p.Session
	aid, err := led.CreateAction(ctx, tx, org, ledger.NewAction{Agent: agent.ID, Mode: "gateway", Tool: p.Tool,
		OnBehalfOf: &obo, Session: &ses, Request: p.Args, Actor: agent.ID.String()})
	if err != nil {
		return "", err
	}
	id := aid.String()
	step := func(from, to gate.State, d ledger.EventData) error { return led.Transition(ctx, tx, id, from, to, d) }
	exec := func(sql string, args ...any) error {
		_, err := tx.Exec(ctx, sql, append([]any{id}, args...)...)
		return err
	}
	var rule *string
	if p.RuleID != "" {
		rule = &p.RuleID
	}
	decision := "allow"
	switch p.Kind {
	case Deny:
		decision = "deny"
	case Pending:
		decision = "require_approval"
	}
	if err := step(gate.Received, gate.Authenticated, ledger.EventData{}); err != nil {
		return id, err
	}
	if err := step(gate.Authenticated, gate.Evaluated, ledger.EventData{}); err != nil {
		return id, err
	}
	if err := exec(`UPDATE deedglass.actions SET decision = $2, decision_rule_id = $3 WHERE id = $1`, decision, rule); err != nil {
		return id, err
	}
	policy := strp("policy")
	switch p.Kind {
	case Deny:
		return id, step(gate.Evaluated, gate.Denied, ledger.EventData{Reason: policy, RuleID: rule})
	case Pending:
		return id, step(gate.Evaluated, gate.PendingApproval, ledger.EventData{Reason: policy, RuleID: rule})
	}
	if err := step(gate.Evaluated, gate.Executing, ledger.EventData{RuleID: rule}); err != nil {
		return id, err
	}
	if p.Kind == Failed {
		if err := step(gate.Executing, gate.Failed, ledger.EventData{ErrorCode: &p.ErrorCode}); err != nil {
			return id, err
		}
		return id, exec(`UPDATE deedglass.actions SET execution_status = 'failed', error_code = $2, latency_ms = $3::integer,
		    executing_at = failed_at - $3::integer * interval '1 millisecond' WHERE id = $1`, p.ErrorCode, p.LatencyMS)
	}
	if err := step(gate.Executing, gate.Executed, ledger.EventData{}); err != nil {
		return id, err
	}
	rd, err := deed.DigestJSON(p.Result)
	if err != nil {
		return id, err
	}
	if err := exec(`UPDATE deedglass.actions SET execution_status = 'succeeded', result = $2::jsonb, result_digest = $3, latency_ms = $4::integer,
	    executing_at = executed_at - $4::integer * interval '1 millisecond' WHERE id = $1`, string(p.Result), rd, p.LatencyMS); err != nil {
		return id, err
	}
	if err := step(gate.Executed, gate.Witnessing, ledger.EventData{}); err != nil {
		return id, err
	}
	var detail, dd *string
	if p.Detail != nil {
		s := string(p.Detail)
		d, err := deed.DigestJSON(p.Detail)
		if err != nil {
			return id, err
		}
		detail, dd = &s, &d
	}
	if err := exec(`UPDATE deedglass.actions SET witness_level = $2, witness_verdict = $3, witness_method = $4,
	    witness_detail = $5::jsonb, witness_detail_digest = $6 WHERE id = $1`, p.Level, p.Verdict, p.Method, detail, dd); err != nil {
		return id, err
	}
	to := map[string]gate.State{"VERIFIED": gate.Verified, "MISMATCH": gate.Mismatch, "UNVERIFIABLE": gate.Unverifiable}[p.Verdict]
	return id, step(gate.Witnessing, to, ledger.EventData{Level: &p.Level, Verdict: &p.Verdict, DetailDigest: dd})
}

// finish seals a prepared action (pending ones stay unsealed for S22) and, for Amend, seals the
// later VERIFIED verdict as its own amendment deed.
func finish(ctx context.Context, led *ledger.Ledger, tx pgx.Tx, id string, p Plan) ([]*deed.Envelope, error) {
	if p.Kind == Pending {
		return nil, nil
	}
	env, err := led.SealAction(ctx, tx, id)
	if err != nil {
		return nil, err
	}
	if p.Kind != Amend {
		return []*deed.Envelope{env}, nil
	}
	am, err := led.SealAmendment(ctx, tx, id, deed.WitnessSection{Level: p.Level, Verdict: "VERIFIED", Method: p.Method},
		json.RawMessage(`{"reason":"late_evidence"}`))
	if err != nil {
		return nil, err
	}
	return []*deed.Envelope{env, am}, nil
}

func strp(s string) *string { return &s }
