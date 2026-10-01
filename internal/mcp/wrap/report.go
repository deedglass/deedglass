package wrap

import (
	"context"
	"encoding/json"
	"time"

	"github.com/deedglass/deedglass/internal/deed"
	"github.com/deedglass/deedglass/internal/gate"
)

// report sends one call's outcome to the Gate in the background (S14 §3.6 step 6), once. A report
// that never lands leaves the action executing; the Gate's sweeper seals it UNVERIFIABLE (edge
// lost) at its deadline, which is the honest outcome.
func (w *Wrapper) report(cs *callState, status gate.ExecStatus, code string, result json.RawMessage, _ error) {
	if cs == nil || cs.failOpen || cs.actionID == "" || !cs.reported.CompareAndSwap(false, true) {
		return
	}
	started := cs.started
	if started.IsZero() {
		started = time.Now()
	}
	r := gate.EdgeResult{Status: status, StartedAt: started.UTC(), FinishedAt: time.Now().UTC(), ErrorCode: code}
	if len(result) > 0 {
		if d, err := deed.DigestJSON(result); err == nil {
			r.ResultDigest = d
			if w.cfg.ResultMode == "full" && len(result) <= MaxResultBytes {
				r.Result = result
			}
		}
	}
	w.reports.Add(1)
	go func() {
		defer w.reports.Done()
		ctx, cancel := context.WithTimeout(context.WithoutCancel(w.ctx), 30*time.Second)
		defer cancel()
		if err := w.gc.Report(ctx, cs.actionID, r); err != nil {
			w.log.sym(symUnknown, "could not report the result of "+cs.actionID+" ("+shortErr(err)+")",
				"Deedglass seals it UNVERIFIABLE at its deadline")
		}
	}()
}
