package ledger

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/deedglass/deedglass/internal/deed"
	"github.com/deedglass/deedglass/internal/ids"
)

// Chain check statuses.
const (
	CheckIntact     = "intact"
	CheckBroken     = "broken"
	CheckIncomplete = "incomplete"
)

// checkChunk is how many deeds one read transaction streams; the time budget is checked between
// chunks.
const checkChunk = 20_000

// ChainCheck is GET /v1/ledger/check and `deedglass ledger check`.
type ChainCheck struct {
	Object      string      `json:"object"` // "ledger_check"
	Org         string      `json:"org"`
	Status      string      `json:"status"` // intact | broken | incomplete
	Checked     int64       `json:"checked"`
	FirstSeq    *int64      `json:"first_seq"`
	LastSeq     *int64      `json:"last_seq"`
	Anchor      CheckAnchor `json:"anchor"`
	Head        *CheckHead  `json:"head"`
	SealKeys    []string    `json:"seal_keys"`
	Break       *ChainBreak `json:"break"`
	NextFromSeq *int64      `json:"next_from_seq"`
	DurationMS  int64       `json:"duration_ms"`
	CheckedAt   Time        `json:"checked_at"`
}

// CheckAnchor is what the first checked deed links to: the org's genesis, or the deed before it.
type CheckAnchor struct {
	Kind     string  `json:"kind"` // genesis | deed
	Seq      int64   `json:"seq"`
	BodyHash *string `json:"body_hash"`
}

// CheckHead is the last intact deed checked. Keep it somewhere outside Deedglass: it is how a
// deletion from the end of the chain becomes visible.
type CheckHead struct {
	Seq      int64  `json:"seq"`
	Deed     string `json:"deed"`
	BodyHash string `json:"body_hash"`
	IssuedAt string `json:"issued_at"`
}

// ChainBreak is the first deed that failed.
type ChainBreak struct {
	Seq     *int64  `json:"seq"`
	Deed    *string `json:"deed"`
	Code    string  `json:"code"`
	Message string  `json:"message"`
	Next    string  `json:"next"`
}

// CheckOptions tunes CheckChain (tests shorten the budget).
type CheckOptions struct {
	Budget   time.Duration // default CheckBudget (30 s)
	MaxDeeds int64         // default MaxCheckDeeds
	Chunk    int64         // deeds per read transaction; default 20,000
}

// CheckChain verifies org's deeds in seq order with the server's own key set (every registered
// seal key), through deed.Verifier: seals, hashes, links and seq continuity. A range that doesn't
// start at 1 is anchored on the stored body_hash of the deed before it. A broken chain is a result,
// not an error (and is logged at ERROR, because it means the database was changed underneath
// Deedglass). When the budget runs out the status is incomplete and NextFromSeq says where to
// resume.
func (l *Ledger) CheckChain(ctx context.Context, org ids.OrgID, r Range, o CheckOptions) (*ChainCheck, error) {
	start := l.now()
	if o.Budget <= 0 {
		o.Budget = CheckBudget
	}
	if o.MaxDeeds <= 0 {
		o.MaxDeeds = MaxCheckDeeds
	}
	if o.Chunk <= 0 {
		o.Chunk = checkChunk
	}
	res := &ChainCheck{Object: "ledger_check", Org: org.String(), Status: CheckIntact, SealKeys: []string{},
		Anchor: CheckAnchor{Kind: "genesis"}}
	from, to, err := l.resolveRange(ctx, org, r)
	if errors.Is(err, ErrEmptyRange) && r == (Range{}) {
		res.DurationMS, res.CheckedAt = l.now().Sub(start).Milliseconds(), Time(l.now())
		return res, nil // an org with no deeds yet: nothing to break
	}
	if err != nil {
		return nil, err
	}
	ks, err := l.keySet(ctx, nil)
	if err != nil {
		return nil, err
	}
	opt := deed.StreamOptions{Keys: ks, KeysPinned: true, Org: org.String()}
	if from > 1 {
		var hash string
		err := l.readTx(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
			return tx.QueryRow(ctx, `SELECT body_hash FROM deedglass.deeds WHERE org_id = $1 AND seq = $2`, org, from-1).Scan(&hash)
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("%w: deed %d, the anchor of this range, is missing", ErrNotFound, from-1)
		}
		if err != nil {
			return nil, err
		}
		opt.Anchor = &deed.Anchor{Seq: from - 1, BodyHash: hash}
		res.Anchor = CheckAnchor{Kind: "deed", Seq: from - 1, BodyHash: &hash}
	}

	v := deed.NewVerifier(opt)
	pr, pw := io.Pipe()
	var next int64 // the first seq not streamed, when the budget or the limit stopped us
	prodErr := make(chan error, 1)
	go func() {
		bw := bufio.NewWriterSize(pw, 256<<10)
		err := func() error {
			var sent int64
			for lo := from; lo <= to; lo += o.Chunk {
				if sent > 0 && (l.now().Sub(start) > o.Budget || sent >= o.MaxDeeds) {
					next = lo
					return nil
				}
				hi := min(lo+o.Chunk-1, to)
				err := l.db.OrgReadTx(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
					if _, err := tx.Exec(ctx, `SELECT pg_catalog.set_config('statement_timeout', '60s', true)`); err != nil {
						return err
					}
					rows, err := tx.Query(ctx, `SELECT envelope FROM deedglass.deeds WHERE org_id = $1 AND seq BETWEEN $2 AND $3 ORDER BY seq`, org, lo, hi)
					if err != nil {
						return err
					}
					defer rows.Close()
					for rows.Next() {
						var env []byte
						if err := rows.Scan(&env); err != nil {
							return err
						}
						if _, err := bw.Write(env); err != nil {
							return err
						}
						if err := bw.WriteByte('\n'); err != nil {
							return err
						}
						sent++
					}
					return rows.Err()
				})
				if err != nil {
					return err
				}
			}
			return nil
		}()
		if err == nil {
			err = bw.Flush()
		}
		pw.CloseWithError(err)
		prodErr <- err
	}()
	streamErr := v.Stream(ctx, pr, "ledger")
	_ = pr.CloseWithError(errors.New("ledger: check stopped")) // unblock the producer after a break
	perr := <-prodErr
	rep := v.Report()
	if streamErr != nil && rep.Result == deed.ResultValid {
		return nil, streamErr
	}
	if perr != nil && rep.Result == deed.ResultValid && !errors.Is(perr, io.ErrClosedPipe) {
		return nil, perr
	}

	in := v.Input()
	res.Checked = rep.Intact
	if rep.FirstSeq != nil {
		res.FirstSeq = rep.FirstSeq
	}
	if rep.LastSeq != nil {
		res.LastSeq = rep.LastSeq
	}
	for _, k := range rep.SealKeys {
		res.SealKeys = append(res.SealKeys, k.ID)
	}
	if in.HeadSeq > 0 {
		res.Head = &CheckHead{Seq: in.HeadSeq, Deed: in.HeadDeedID, BodyHash: in.HeadBodyHash, IssuedAt: in.To}
	}
	if f := rep.FirstFailure(); f != nil {
		res.Status = CheckBroken
		res.Break = &ChainBreak{Seq: f.Seq, Deed: f.Deed, Code: f.Code, Message: f.Message, Next: brokenNext}
		var seq int64
		if f.Seq != nil {
			seq = *f.Seq
		}
		l.log.ErrorContext(ctx, "deed chain broken", "org_id", org.String(), "seq", seq, "code", f.Code)
	} else if next > 0 {
		res.Status = CheckIncomplete
		res.NextFromSeq = &next
	}
	res.DurationMS, res.CheckedAt = l.now().Sub(start).Milliseconds(), Time(l.now())
	return res, nil
}

// brokenNext is the next step for a broken chain (docs/LEDGER.md §9).
const brokenNext = "Deeds are append-only, so this took a database-level change. Treat it as an incident: docs/LEDGER.md#when-the-chain-is-broken"

// resolveRange turns r into a seq range [from, to] that exists in org. ErrEmptyRange when no deed
// falls in it.
func (l *Ledger) resolveRange(ctx context.Context, org ids.OrgID, r Range) (from, to int64, err error) {
	var lo, hi *int64
	err = l.readTx(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT pg_catalog.min(seq), pg_catalog.max(seq) FROM deedglass.deeds
		    WHERE org_id = $1 AND ($2::bigint = 0 OR seq >= $2) AND ($3::bigint = 0 OR seq <= $3)
		      AND ($4::timestamptz IS NULL OR issued_at >= $4) AND ($5::timestamptz IS NULL OR issued_at < $5)`,
			org, r.FromSeq, r.ToSeq, r.IssuedGTE, r.IssuedLT).Scan(&lo, &hi)
	})
	if err != nil {
		return 0, 0, err
	}
	if lo == nil {
		return 0, 0, ErrEmptyRange
	}
	from, to = *lo, *hi
	switch {
	case r.FromSeq > 0 && r.FromSeq < from:
		from = r.FromSeq // a gap at the start of the range is itself a finding
	case r.FromSeq == 0 && r.IssuedGTE == nil:
		from = 1 // the whole chain starts at deed 1, whatever is stored
	}
	return from, to, nil
}

// keySet is every registered seal key as a KeySet (the server's own keys), or only those in want.
func (l *Ledger) keySet(ctx context.Context, want map[string]bool) (*deed.KeySet, error) {
	rows, err := l.db.SealKeys(ctx)
	if err != nil {
		return nil, err
	}
	recs := make([]deed.KeyRecord, 0, len(rows))
	for _, r := range rows {
		if want == nil || want[r.ID] {
			recs = append(recs, r.Record())
		}
	}
	return deed.NewKeySet(l.issuer, recs)
}

// HeadSeq returns the seq of org's last deed (0 for an empty chain).
func (l *Ledger) HeadSeq(ctx context.Context, org ids.OrgID) (int64, error) {
	var n int64
	err := l.readTx(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT COALESCE(pg_catalog.max(seq), 0) FROM deedglass.deeds WHERE org_id = $1`, org).Scan(&n)
	})
	return n, err
}

// SealKeySet is the server's registered seal keys as keys.json publishes them, limited to ids when
// it is non-empty (S47 deeds share carries the one key that sealed the deed).
func (l *Ledger) SealKeySet(ctx context.Context, ids ...string) (*deed.KeySet, error) {
	var want map[string]bool
	if len(ids) > 0 {
		want = map[string]bool{}
		for _, id := range ids {
			want[id] = true
		}
	}
	return l.keySet(ctx, want)
}
