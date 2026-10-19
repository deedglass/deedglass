package gate

import (
	"context"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/deedglass/deedglass/internal/connector"
	"github.com/deedglass/deedglass/internal/ids"
	"github.com/deedglass/deedglass/internal/store"
	"github.com/deedglass/deedglass/internal/witness"
	"github.com/deedglass/deedglass/internal/witness/l2"
)

// Version-bound L2 (ADR-108): the connector's server runs its own SQL (a FHIR server on Postgres),
// so the decoder binds its transaction by the version the server reported to the action, and the
// connector judges it. The Gate only gathers the frames, turns the connector's findings into catalog
// reasons and decides as for every L2 result. It learns nothing about the server.

// versionClasses are the frame classes that name an action on a version-bound connector.
var versionClasses = map[string]bool{"bound": true, "request_id_reused": true, "version_tag_mismatch": true, "marker_count": true}

// namesVersioned reports whether a stored frame naming the action is one a version-bound judge reads.
func namesVersioned(fs []store.DecodedFrame) bool {
	for _, f := range fs {
		if versionClasses[f.Class] {
			return true
		}
	}
	return false
}

// evaluateVersions is L2 for a version-bound write: the frames that name the action, plus the
// unattributed frames that committed while its call was open (from the action's start to the bound
// transaction's commit), judged by the connector. No reasons means nothing is bound yet.
func (p *Pipeline) evaluateVersions(ctx context.Context, wc witnessCase, t *connector.L2Target, org ids.OrgID, stored []store.DecodedFrame) (l2.Result, error) {
	judge, ok := wc.ex.(connector.L2VersionJudging)
	if !ok {
		return l2.Result{}, nil
	}
	var boundAt time.Time
	for _, f := range stored {
		if f.Class == "bound" && f.CommitTime != nil && f.CommitTime.After(boundAt) {
			boundAt = *f.CommitTime
		}
	}
	all := append([]store.DecodedFrame{}, stored...)
	if !boundAt.IsZero() {
		from := wc.call.ActionID.Time().Add(-time.Second)
		err := p.o.DB.OrgReadTx(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
			oob, err := store.UnattributedFrames(ctx, tx, t.ConnectorID, from, boundAt)
			all = append(all, oob...)
			return err
		})
		if err != nil {
			return l2.Result{}, err
		}
	}
	frames := make([]connector.L2Frame, 0, len(all))
	for _, f := range all {
		x := connector.L2Frame{ID: f.ID, Class: f.Class, SystemID: f.SystemID, Timeline: f.TimelineID, Total: f.ChangeCount,
			Tables: f.Tables, Changes: f.Changes, Flags: f.Flags}
		if f.ActionID != nil {
			x.ActionID = *f.ActionID
		}
		if f.CommitLSN != nil {
			x.CommitLSN = *f.CommitLSN
		}
		if f.CommitTime != nil {
			x.CommitTime = *f.CommitTime
		}
		if f.Xid != nil {
			x.Xid = *f.Xid
		}
		if f.ChangesDigest != nil {
			x.Digest = *f.ChangesDigest
		}
		frames = append(frames, x)
	}
	jd, err := judge.L2Judge(ctx, wc.call, wc.res, frames)
	if err != nil {
		return l2.Result{}, err
	}
	return versionResult(jd, frames), nil
}

// versionResult turns a judgement into L2's reasons and evidence: the findings become catalog
// reasons (the catalog gives each its class; an unknown code fails closed as internal_error), and
// the evidence names the bound transaction and the frames that named the action.
func versionResult(jd connector.L2Judgement, frames []connector.L2Frame) l2.Result {
	res := l2.Result{Method: jd.Method}
	for _, f := range jd.Findings {
		r := witness.R(witness.L2, witness.Code(f.Code), "versions", f.Vars)
		if len(f.Diff) > 0 {
			d := make([]witness.FieldDiff, 0, len(f.Diff))
			for _, x := range f.Diff {
				d = append(d, witness.FieldDiff{Path: x.Path, Expected: x.Expected, Observed: x.Observed})
			}
			r = r.WithDiff(d)
		}
		res.Reasons = append(res.Reasons, r.WithEvidence(f.Evidence))
	}
	if len(res.Reasons) == 0 {
		return res
	}
	var named []string
	ev := &witness.L2Evidence{Rows: []witness.L2Row{}, Redacted: []string{}}
	for _, f := range frames {
		if !versionClasses[f.Class] {
			continue
		}
		named = append(named, f.ID)
		if f.ID == jd.Primary {
			ev.Transaction = &witness.L2Transaction{SystemID: f.SystemID, Timeline: f.Timeline, CommitLSN: f.CommitLSN, Xid: f.Xid,
				CommittedAt: f.CommitTime.UTC().Format("2006-01-02T15:04:05.000Z"), ChangesDigest: f.Digest}
		}
	}
	sort.Strings(named)
	ev.Frames, ev.FrameIDs = len(named), named
	res.Evidence = ev
	return res
}
