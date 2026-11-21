package recordexport

import (
	"encoding/csv"
	"io"
	"strconv"
)

// CSV format: tabular audit trail export conforming to RFC 4180.
// Designed for hospital clinical governance committees, NABH/JCI auditors,
// and HIPAA §164.312(b) audit control security reviews.
// Zero plaintext PHI: exports identifiers, tool names, policy rules, and verification verdicts.

var csvHeader = []string{
	"seq",
	"issued_at",
	"deed_id",
	"kind",
	"org",
	"agent_id",
	"agent_name",
	"tool",
	"decision",
	"rule_id",
	"status",
	"error_code",
	"latency_ms",
	"witness_level",
	"witness_verdict",
	"cost_currency",
	"cost_amount",
	"on_behalf_of",
	"session",
	"amends",
	"seal_key_id",
}

type csvWriter struct {
	w             io.Writer
	cw            *csv.Writer
	opt           Options
	st            Stats
	headerWritten bool
}

func newCSVWriter(w io.Writer, opt Options) *csvWriter {
	return &csvWriter{
		w:   w,
		cw:  csv.NewWriter(w),
		opt: opt,
	}
}

func (x *csvWriter) Stats() Stats { return x.st }

func (x *csvWriter) Close() error {
	if !x.headerWritten {
		if err := x.cw.Write(csvHeader); err != nil {
			return err
		}
		x.headerWritten = true
	}
	x.cw.Flush()
	return x.cw.Error()
}

func (x *csvWriter) Write(raw []byte) error {
	x.st.Records++
	e, err := parse(raw)
	if err != nil {
		return err
	}
	if !x.headerWritten {
		if err := x.cw.Write(csvHeader); err != nil {
			return err
		}
		x.headerWritten = true
	}
	row := csvRow(&e)
	if err := x.cw.Write(row); err != nil {
		return err
	}
	x.st.Written++
	return nil
}

func csvRow(e *envelope) []string {
	b := &e.Body
	row := make([]string, len(csvHeader))

	row[0] = strconv.FormatInt(b.Seq, 10)
	row[1] = b.IssuedAt
	row[2] = b.DeedID
	row[3] = b.Kind
	row[4] = b.Org

	if b.Kind == "admin" && b.Admin != nil {
		row[5] = b.Admin.Actor.ID
		row[6] = b.Admin.Actor.Type
		row[7] = b.Admin.Event
		row[8] = b.Admin.Action
		if b.Admin.Reason != nil {
			row[9] = *b.Admin.Reason
		}
		row[10] = "succeeded"
	} else {
		if b.Agent != nil {
			row[5] = b.Agent.ID
			row[6] = b.Agent.Name
		}
		if b.Action != nil {
			row[7] = b.Action.Tool
		}
		if b.Policy != nil {
			row[8] = b.Policy.Decision
			if b.Policy.RuleID != nil {
				row[9] = *b.Policy.RuleID
			}
		}
		if b.Execution != nil {
			row[10] = b.Execution.Status
			if b.Execution.ErrorCode != nil {
				row[11] = *b.Execution.ErrorCode
			}
			row[12] = strconv.FormatInt(b.Execution.LatencyMS, 10)
		}
		if b.Witness != nil {
			row[13] = b.Witness.Level
			row[14] = b.Witness.Verdict
		}
		if b.Cost != nil {
			row[15] = b.Cost.Currency
			row[16] = strconv.FormatInt(b.Cost.Amount, 10)
		}
		if b.OnBehalfOf != nil {
			row[17] = *b.OnBehalfOf
		}
		if b.Session != nil {
			row[18] = *b.Session
		}
		if b.Amends != nil {
			row[19] = *b.Amends
		}
	}
	row[20] = e.SealKeyID

	return row
}
