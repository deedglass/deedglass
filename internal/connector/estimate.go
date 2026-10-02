package connector

import (
	"context"
	"fmt"
	"runtime/debug"
	"time"

	"github.com/deedglass/deedglass/internal/vault"
)

// Estimate is a connector's guess at what one call would change, made before it runs, for the
// person who countersigns it (S22). It is never a promise: the approval page says "about" and
// "planner estimate", and the Witness still compares what actually happened.
type Estimate struct {
	Rows   int64         `json:"rows"`
	Method string        `json:"method"`           // EstimateExplain | EstimateNone
	Detail string        `json:"detail,omitempty"` // why there is none: timeout, refused, unsupported, error
	Took   time.Duration `json:"-"`
}

// Estimate methods and the reasons an estimate is missing.
const (
	EstimateExplain = "explain"
	EstimateNone    = "none"

	EstimateTimeout     = "timeout"
	EstimateRefused     = "refused"
	EstimateUnsupported = "unsupported"
	EstimateError       = "error"
)

// NoEstimate is a missing estimate with its reason.
func NoEstimate(why string) Estimate { return Estimate{Method: EstimateNone, Detail: why} }

// Estimator is an optional connector type interface (S22): the Postgres connector answers with
// EXPLAIN (FORMAT JSON), never ANALYZE, in a READ ONLY transaction as the agent role. An Estimate
// is best effort: a failure is an Estimate with method none, never a refusal of the action.
type Estimator interface {
	Estimate(ctx context.Context, inst *Instance, call Call) (Estimate, error)
}

// Estimating is an Executor whose connector type estimates (the bound form the Gate holds).
type Estimating interface {
	CanEstimate() bool
	Estimate(ctx context.Context, call Call) (Estimate, error)
}

// CanEstimate reports whether the bound connector type is an Estimator.
func (b *bound) CanEstimate() bool {
	_, ok := b.impl.(Estimator)
	return ok
}

// Estimate runs the connector's estimate with the per-call redactor and a recovered panic. Errors
// become an Estimate with method none; the error is returned only for the caller's log.
func (b *bound) Estimate(ctx context.Context, call Call) (est Estimate, err error) {
	e, ok := b.impl.(Estimator)
	if !ok {
		return NoEstimate(EstimateUnsupported), nil
	}
	start := time.Now()
	red := vault.NewRedactor()
	ctx = vault.WithRedactor(ctx, red)
	defer func() {
		if p := recover(); p != nil {
			b.r.log.ErrorContext(ctx, "connector: an estimate panicked", "connector", b.inst.Name, "type", b.impl.Type(),
				"panic", fmt.Sprint(p), "stack", string(debug.Stack()))
			est, err = NoEstimate(EstimateError), fmt.Errorf("connector: the estimate panicked")
		}
		if err != nil && est.Method != EstimateNone {
			est = NoEstimate(EstimateError)
		}
		if est.Method == "" {
			est.Method = EstimateNone
		}
		est.Took = time.Since(start)
	}()
	return e.Estimate(ctx, b.inst, call)
}
