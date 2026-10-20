package ledger

import (
	"context"
	"errors"
	"io"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/deedglass/deedglass/internal/deed"
	"github.com/deedglass/deedglass/internal/deed/archive"
	"github.com/deedglass/deedglass/internal/deed/stamp"
	"github.com/deedglass/deedglass/internal/ids"
)

// ExportStarter is implemented by an Export writer that needs the range before the first byte
// (the HTTP handler sets Content-Disposition from it). Export calls it once, after the range is
// known and before writing.
type ExportStarter interface {
	ExportStarting(org string, firstSeq, lastSeq, count int64)
}

// ExportStampReporter is implemented by an Export writer that wants to know whether the export was
// timestamped (TY-03): Export calls it once with nil when timestamps.json was added, or with the
// reason it wasn't. It is not called when timestamps are off.
type ExportStampReporter interface {
	ExportStamp(err error)
}

// EmptyRangeError is ErrEmptyRange with where the chain ends, for the next step.
type EmptyRangeError struct {
	From, To int64 // the range asked for (0: open)
	Head     int64 // the org's last seq (0: no deeds)
}

func (e *EmptyRangeError) Error() string { return ErrEmptyRange.Error() }
func (e *EmptyRangeError) Unwrap() error { return ErrEmptyRange }

// Export writes org's deeds in r as an export bundle v1 (archive.Writer) to w, from one REPEATABLE
// READ READ ONLY snapshot: pass 1 counts the deeds and their bytes (the tar header needs the exact
// size), pass 2 streams them in seq order without holding them in memory, and Close seals the
// manifest with the Ledger's signer. If ctx ends mid-stream the transaction rolls back and the
// partial bundle fails `deedglass verify` with bundle_invalid, which is the honest outcome.
func (l *Ledger) Export(ctx context.Context, org ids.OrgID, r Range, w io.Writer) (*archive.Manifest, error) {
	if l.signer == nil {
		return nil, ErrNoSigner
	}
	var (
		man     *archive.Manifest
		written int64
		count   int64
	)
	err := l.db.OrgSnapshotTx(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_catalog.set_config('statement_timeout', '600s', true)`); err != nil {
			return err
		}
		var (
			bytes    int64
			lo, hi   *int64
			from, to *time.Time
			head     int64
		)
		err := tx.QueryRow(ctx, `SELECT pg_catalog.count(*), COALESCE(pg_catalog.sum(pg_catalog.octet_length(envelope) + 1), 0)::bigint,
		    pg_catalog.min(seq), pg_catalog.max(seq)
		  FROM deedglass.deeds WHERE org_id = $1 AND ($2::bigint = 0 OR seq >= $2) AND ($3::bigint = 0 OR seq <= $3)
		    AND ($4::timestamptz IS NULL OR issued_at >= $4) AND ($5::timestamptz IS NULL OR issued_at < $5)`,
			org, r.FromSeq, r.ToSeq, r.IssuedGTE, r.IssuedLT).Scan(&count, &bytes, &lo, &hi)
		if err != nil {
			return err
		}
		if count == 0 {
			if err := tx.QueryRow(ctx, `SELECT COALESCE(pg_catalog.max(seq), 0) FROM deedglass.deeds WHERE org_id = $1`, org).Scan(&head); err != nil {
				return err
			}
			return &EmptyRangeError{From: r.FromSeq, To: r.ToSeq, Head: head}
		}
		if count > MaxExportDeeds {
			return paramErr("to_seq", "Export at most 1,000,000 deeds at a time. Narrow from_seq and to_seq.")
		}
		if count != *hi-*lo+1 {
			// A gap inside the range: export what is there; the verifier names the missing deed.
			l.log.WarnContext(ctx, "export range has a gap", "org_id", org.String(), "first_seq", *lo, "last_seq", *hi, "count", count)
		}
		if err := tx.QueryRow(ctx, `SELECT (SELECT issued_at FROM deedglass.deeds WHERE org_id = $1 AND seq = $2),
		    (SELECT issued_at FROM deedglass.deeds WHERE org_id = $1 AND seq = $3)`, org, *lo, *hi).Scan(&from, &to); err != nil {
			return err
		}
		// keys.json: the keys that sealed these deeds, plus the key that seals the manifest.
		want := map[string]bool{l.signer.KeyID(): true}
		rows, err := tx.Query(ctx, `SELECT DISTINCT seal_key_id FROM deedglass.deeds WHERE org_id = $1 AND seq BETWEEN $2 AND $3`, org, *lo, *hi)
		if err != nil {
			return err
		}
		keyIDs, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			return err
		}
		for _, k := range keyIDs {
			want[k] = true
		}
		// A v2 deed names one key per seal (ADR-109); seal_key_id holds only the primary one.
		rows, err = tx.Query(ctx, `SELECT DISTINCT k.value ->> 'id' FROM deedglass.deeds AS d,
		    pg_catalog.jsonb_array_elements(d.body -> 'seal_keys') AS k
		  WHERE d.org_id = $1 AND d.seq BETWEEN $2 AND $3 AND d.body ? 'seal_keys'`, org, *lo, *hi)
		if err != nil {
			return err
		}
		if keyIDs, err = pgx.CollectRows(rows, pgx.RowTo[string]); err != nil {
			return err
		}
		for _, k := range keyIDs {
			want[k] = true
		}
		ks, err := l.keySet(ctx, want)
		if err != nil {
			return err
		}
		keysJSON, err := deed.StrictCanonical(ks)
		if err != nil {
			return err
		}
		if s, ok := w.(ExportStarter); ok {
			s.ExportStarting(org.String(), *lo, *hi, count)
		}
		var stamps []byte
		if l.stamper != nil {
			var serr error
			stamps, serr = l.stampHead(ctx, tx, org, *hi)
			if serr != nil {
				// The export goes ahead without timestamps.json: a TSA being down must not stop
				// anyone getting their records. The writer is told why.
				l.log.WarnContext(ctx, "export_not_timestamped", "org_id", org.String(), "seq", *hi, "error", serr.Error())
			}
			if r, ok := w.(ExportStampReporter); ok {
				r.ExportStamp(serr)
			}
		}
		aw, err := archive.NewWriter(w, archive.Header{Org: org.String(), Issuer: l.issuer, CreatedAt: l.now().UTC(),
			Generator: l.gen, FirstSeq: *lo, LastSeq: *hi, From: deed.FormatTime(*from), To: deed.FormatTime(*to), Timestamps: stamps},
			keysJSON, bytes, count)
		if err != nil {
			return err
		}
		rows, err = tx.Query(ctx, `SELECT envelope FROM deedglass.deeds WHERE org_id = $1 AND seq BETWEEN $2 AND $3 ORDER BY seq`, org, *lo, *hi)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var env []byte
			if err := rows.Scan(&env); err != nil {
				return err
			}
			if err := aw.WriteDeed(env); err != nil {
				return err
			}
			written++
		}
		if err := rows.Err(); err != nil {
			return err
		}
		man, err = aw.Close(l.signer)
		return err
	})
	if err != nil {
		if ctx.Err() != nil {
			l.log.WarnContext(ctx, "export_aborted", "org_id", org.String(), "written", written, "count", count, "error", err.Error())
		}
		return nil, err
	}
	l.log.InfoContext(ctx, "export", "org_id", org.String(), "first_seq", man.Range.FirstSeq, "last_seq", man.Range.LastSeq, "count", man.Range.Count)
	return man, nil
}

// Head returns the head statement of org's chain at seq (its last deed when seq is 0), for
// `deedglass deeds timestamp`. ErrNotFound when the org has no such deed.
func (l *Ledger) Head(ctx context.Context, org ids.OrgID, seq int64) (stamp.Head, error) {
	var h stamp.Head
	err := l.db.OrgSnapshotTx(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		h, err = headAt(ctx, tx, org, seq)
		return err
	})
	return h, err
}

func headAt(ctx context.Context, tx pgx.Tx, org ids.OrgID, seq int64) (stamp.Head, error) {
	var (
		s                int64
		id, hash, issued string
		at               time.Time
	)
	err := tx.QueryRow(ctx, `SELECT seq, id::text, body_hash::text, issued_at FROM deedglass.deeds
	  WHERE org_id = $1 AND ($2::bigint = 0 OR seq = $2) ORDER BY seq DESC LIMIT 1`, org, seq).Scan(&s, &id, &hash, &at)
	if errors.Is(err, pgx.ErrNoRows) {
		return stamp.Head{}, ErrNotFound
	}
	if err != nil {
		return stamp.Head{}, err
	}
	issued = deed.FormatTime(at)
	return stamp.NewHead(org.String(), s, id, hash, issued), nil
}

// stampHead timestamps the deed at seq with the Ledger's stamper and returns timestamps.json.
func (l *Ledger) stampHead(ctx context.Context, tx pgx.Tx, org ids.OrgID, seq int64) ([]byte, error) {
	h, err := headAt(ctx, tx, org, seq)
	if err != nil {
		return nil, err
	}
	e, err := l.stamper(ctx, h)
	if err != nil {
		return nil, err
	}
	f := stamp.NewFile(org.String())
	if err := f.Add(e); err != nil {
		return nil, err
	}
	return f.Marshal()
}

// ExportJSONL writes the deeds in r as deeds.jsonl (one stored envelope per line), for
// `deeds export --format jsonl`. It returns the first and last seq written.
func (l *Ledger) ExportJSONL(ctx context.Context, org ids.OrgID, r Range, w io.Writer) (first, last, count int64, err error) {
	err = l.db.OrgSnapshotTx(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_catalog.set_config('statement_timeout', '600s', true)`); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT seq, envelope FROM deedglass.deeds WHERE org_id = $1 AND ($2::bigint = 0 OR seq >= $2)
		    AND ($3::bigint = 0 OR seq <= $3) AND ($4::timestamptz IS NULL OR issued_at >= $4) AND ($5::timestamptz IS NULL OR issued_at < $5)
		    ORDER BY seq LIMIT $6`, org, r.FromSeq, r.ToSeq, r.IssuedGTE, r.IssuedLT, MaxExportDeeds+1)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var seq int64
			var env []byte
			if err := rows.Scan(&seq, &env); err != nil {
				return err
			}
			if count == MaxExportDeeds {
				return paramErr("to_seq", "Export at most 1,000,000 deeds at a time. Narrow from_seq and to_seq.")
			}
			if count == 0 {
				first = seq
			}
			last = seq
			count++
			if _, err := w.Write(append(env, '\n')); err != nil {
				return err
			}
		}
		if err := rows.Err(); err != nil {
			return err
		}
		if count == 0 {
			var head int64
			if err := tx.QueryRow(ctx, `SELECT COALESCE(pg_catalog.max(seq), 0) FROM deedglass.deeds WHERE org_id = $1`, org).Scan(&head); err != nil {
				return err
			}
			return &EmptyRangeError{From: r.FromSeq, To: r.ToSeq, Head: head}
		}
		return nil
	})
	return first, last, count, err
}

// ExportName is the bundle's file name for a range.
func ExportName(org string, first, last int64) string { return archive.FileName(org, first, last) }
