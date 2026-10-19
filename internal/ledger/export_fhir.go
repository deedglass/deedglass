package ledger

import (
	"context"
	"encoding/json"
	"io"

	"github.com/jackc/pgx/v5"

	"github.com/deedglass/deedglass/internal/fhirexport"
	"github.com/deedglass/deedglass/internal/ids"
)

// ExportFHIR writes org's records in r as FHIR R4 NDJSON (HC-P10): format is
// fhirexport.FormatAuditEvent or FormatProvenance. Each action record is joined to its action row
// and connector, so a FHIR connector's call carries its resource ids, the FHIR server and the SMART
// client id; the export holds ids and digests only (internal/fhirexport). The Ledger's issuer (this
// server's base URL) is the gateway the resources name. It reads one REPEATABLE READ READ ONLY
// snapshot and streams in seq order; when w is an ExportStarter it is told before the first byte.
func (l *Ledger) ExportFHIR(ctx context.Context, org ids.OrgID, r Range, format string, w io.Writer) (fhirexport.Stats, error) {
	fw, err := fhirexport.NewWriter(w, format, fhirexport.Options{Gateway: l.issuer})
	if err != nil {
		return fhirexport.Stats{}, paramErr("format", "format must be tar.gz, jsonl, fhir-auditevent or fhir-provenance.")
	}
	err = l.db.OrgSnapshotTx(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_catalog.set_config('statement_timeout', '600s', true)`); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT d.envelope, c.type, c.name, c.config, a.request, a.result
		    FROM deedglass.deeds d
		    LEFT JOIN deedglass.actions a ON a.org_id = d.org_id AND a.id = (d.body -> 'action' ->> 'id') AND d.kind = 'action'
		    LEFT JOIN deedglass.connectors c ON c.org_id = a.org_id AND (c.id = a.connector_id OR (a.connector_id IS NULL
		      AND c.name = pg_catalog.split_part(a.tool, '.', 1) AND c.deleted_at IS NULL))
		    WHERE d.org_id = $1 AND ($2::bigint = 0 OR d.seq >= $2) AND ($3::bigint = 0 OR d.seq <= $3)
		      AND ($4::timestamptz IS NULL OR d.issued_at >= $4) AND ($5::timestamptz IS NULL OR d.issued_at < $5)
		    ORDER BY d.seq LIMIT $6`, org, r.FromSeq, r.ToSeq, r.IssuedGTE, r.IssuedLT, MaxExportDeeds+1)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var env []byte
			var typ, name *string
			var cfg, req, res []byte
			if err := rows.Scan(&env, &typ, &name, &cfg, &req, &res); err != nil {
				return err
			}
			if fw.Stats.Records == MaxExportDeeds {
				return paramErr("to_seq", "Export at most 1,000,000 deeds at a time. Narrow from_seq and to_seq.")
			}
			if fw.Stats.Records == 0 {
				if s, ok := w.(ExportStarter); ok {
					s.ExportStarting(org.String(), 0, 0, 0)
				}
			}
			rec := fhirexport.Record{Envelope: env}
			if typ != nil && name != nil {
				rec.Call = fhirexport.CallFrom(*typ, *name, json.RawMessage(cfg), json.RawMessage(req), json.RawMessage(res))
			}
			if err := fw.Write(rec); err != nil {
				return err
			}
		}
		if err := rows.Err(); err != nil {
			return err
		}
		if fw.Stats.Records == 0 {
			var head int64
			if err := tx.QueryRow(ctx, `SELECT COALESCE(pg_catalog.max(seq), 0) FROM deedglass.deeds WHERE org_id = $1`, org).Scan(&head); err != nil {
				return err
			}
			return &EmptyRangeError{From: r.FromSeq, To: r.ToSeq, Head: head}
		}
		return nil
	})
	return fw.Stats, err
}
