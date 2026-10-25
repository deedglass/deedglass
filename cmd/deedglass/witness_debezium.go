package main

import (
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"hash"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/deedglass/deedglass/cmd/deedglass/internal/ui"
	"github.com/deedglass/deedglass/internal/witness"
	"github.com/deedglass/deedglass/internal/witness/feed"
	"github.com/deedglass/deedglass/internal/witness/feed/debezium"
	"github.com/deedglass/deedglass/internal/witness/l2"
)

// deedglass witness debezium (TY-05, ADR-114): one action's expected effect compared with what a
// Debezium change feed shows its database committed. Offline: it reads a file or stdin.

type witnessDebeziumOpts struct {
	events, sql, sqlFile, schema, action, org, agent, sqlDigest, tx, ledgerTable string
	reported                                                                     int64
	pks, maxRows                                                                 headerEnvs
}

var witnessDebeziumValues witnessDebeziumOpts

const witnessDebeziumHelp = `
What it does
  Reads Debezium change events (JSON lines: the value envelope, with or without its schema, a
  key/value pair, transaction BEGIN and END events, or the HTTP sink's batches), groups them into
  transactions, and compares what the statement should have changed with what the database
  committed: the same comparison as Proven (L2) on Postgres, labelled by its source, for example
  Proven · via Debezium (MySQL). It connects to nothing.

  A transaction that inserts one row naming the action into the ledger table is that action's.
  Without that row, the transactions that touch the statement's tables are compared, and the
  result says changes from any writer are included. A transaction the feed holds only part of is
  never compared. Turn on provide.transaction.metadata in the connector.

Input
  --events <file|->         the events (.jsonl, or .jsonl.gz); - reads stdin
  --sql <statement>         the statement the agent ran (or --sql-file <file>)
  --reported <n>            the row count the agent reported (-1: not reported)
  --schema <name>           the database unqualified table names belong to (MySQL: the database)
  --pk <table=col[,col]>    a table's primary key, when the events carry no key (repeatable)
Binding
  --action <act_…>          the action id the ledger row names
  --org <org_…>, --agent <agt_…>, --sql-digest <sha256:…>   compared with the ledger row when given
  --ledger-table <name>     the ledger table (default deedglass_ledger in any database)
  --tx <id>                 compare this transaction (a GTID), when there is no ledger row
Limits
  --max-rows <table=n>      the most rows one action may change in a table (repeatable)

Exit status: 0 Matches, 1 Doesn't match, 2 Can't check (or a usage error).

Example
  deedglass witness debezium --events feed.jsonl --schema shop --pk shop.orders=id \
    --sql "UPDATE orders SET status = 'shipped' WHERE id = 42" --reported 1 --action act_01k…

Learn more: https://docs.deedglass.com/reference/witness-debezium
`

func witnessDebeziumFlags(fs *flag.FlagSet) {
	o := &witnessDebeziumValues
	*o = witnessDebeziumOpts{}
	fs.StringVar(&o.events, "events", "", "the Debezium events: a JSON-lines file (.jsonl or .jsonl.gz), or - for stdin")
	fs.StringVar(&o.sql, "sql", "", "the statement the agent ran")
	fs.StringVar(&o.sqlFile, "sql-file", "", "a file holding the statement the agent ran")
	fs.Int64Var(&o.reported, "reported", -1, "the row count the agent reported (-1: not reported)")
	fs.StringVar(&o.schema, "schema", "", "the database or schema unqualified table names belong to")
	fs.Var(&o.pks, "pk", "table=col[,col]: a table's primary key, when the events carry no key (repeatable)")
	fs.StringVar(&o.action, "action", "", "the action id the ledger row names")
	fs.StringVar(&o.org, "org", "", "the org the ledger row must name")
	fs.StringVar(&o.agent, "agent", "", "the agent the ledger row must name")
	fs.StringVar(&o.sqlDigest, "sql-digest", "", "the statement digest the ledger row must name")
	fs.StringVar(&o.ledgerTable, "ledger-table", "", "the ledger table (default deedglass_ledger in any database)")
	fs.StringVar(&o.tx, "tx", "", "the transaction to compare when no ledger row names the action")
	fs.Var(&o.maxRows, "max-rows", "table=n: the most rows one action may change in that table (repeatable)")
}

func runWitnessDebezium(_ context.Context, e *Env, args []string) error {
	o := witnessDebeziumValues
	if len(args) > 0 {
		return usageErr(fmt.Errorf("unexpected argument %q", args[0]), "Run deedglass witness debezium --help.")
	}
	if o.events == "" {
		return usageErr(errors.New("--events is required"), "Pass the Debezium events file, or - to read stdin.")
	}
	if (o.sql == "") == (o.sqlFile == "") {
		return usageErr(errors.New("pass exactly one of --sql and --sql-file"), "Pass the statement the agent ran.")
	}
	stmt := o.sql
	if o.sqlFile != "" {
		b, err := os.ReadFile(o.sqlFile)
		if err != nil {
			return usageErr(fmt.Errorf("can't read --sql-file: %w", err), "Check the path.")
		}
		stmt = string(b)
	}
	pk, err := parsePKs(o.pks)
	if err != nil {
		return err
	}
	maxRows, err := parseMaxRows(o.maxRows)
	if err != nil {
		return err
	}
	r, closeFn, err := openEvents(e, o.events)
	if err != nil {
		return err
	}
	in := &capReader{r: r, h: sha256.New(), left: debeziumMaxInput}
	f, err := debezium.Read(in, debezium.Options{PK: pk, LedgerTable: o.ledgerTable})
	closeFn()
	if in.over {
		return &ExitError{Code: ExitUsage, ErrCode: "feed_too_large", Err: fmt.Errorf("The events are larger than %d MiB after gunzip", debeziumMaxInput>>20),
			Next: "Pass a smaller feed: dump only the time window around the action."}
	}
	if err != nil {
		return &ExitError{Code: ExitUsage, ErrCode: "feed_unreadable", Err: fmt.Errorf("The events don't parse: %w", err),
			Next: "Pass Debezium's JSON output, one event per line (docs/WITNESS_DEBEZIUM.md)."}
	}
	if f.Source.Method == "" {
		f.Source = feed.Source{Via: "Debezium", Database: "the database", Method: "debezium"}
	}
	var reported *int64
	if o.reported >= 0 {
		reported = &o.reported
	}
	rep, err := feed.Evaluate(feed.Input{
		Source: f.Source, Txs: f.Txs, Tx: o.tx,
		Action: l2.Action{ID: o.action, Org: o.org, Agent: o.agent, SQLDigest: o.sqlDigest},
		Derive: l2.DeriveInput{Tool: l2.ToolExecute, SQL: stmt, DefaultSchema: o.schema, Reported: reported},
		Config: l2.Config{MaxRows: maxRows, DiffRows: l2.DefaultDiffRows, DiffColumns: l2.DefaultDiffColumns},
	})
	if err != nil {
		return err
	}
	if e.Globals.JSON {
		if err := writeJSON(e, debeziumJSON(f, rep, in)); err != nil {
			return err
		}
	} else {
		for _, l := range debeziumText(e, f, rep) {
			fmt.Fprintln(e.Stdout, l)
		}
	}
	switch rep.Outcome.Decision.Verdict {
	case witness.Verified:
		return nil
	case witness.Mismatch:
		return &ExitError{Code: ExitFailure, ErrCode: "feed_mismatch", Err: errors.New("The feed doesn't match what the action reported"), printed: true}
	}
	return &ExitError{Code: ExitUsage, ErrCode: "feed_unverifiable", Err: errors.New("The feed can't confirm what the action reported"), printed: true}
}

// debeziumMaxInput is debezium.MaxInput; tests lower it.
var debeziumMaxInput int64 = debezium.MaxInput

// capReader hashes what it reads and stops after left bytes (over is then set): the input's
// sha256 goes into the --json answer so a result can be tied to the exact feed it read.
type capReader struct {
	r    io.Reader
	h    hash.Hash
	n    int64
	left int64
	over bool
}

func (c *capReader) Read(p []byte) (int, error) {
	if c.left <= 0 {
		var one [1]byte
		if n, _ := c.r.Read(one[:]); n > 0 {
			c.over = true
			return 0, errors.New("input too large")
		}
		return 0, io.EOF
	}
	if int64(len(p)) > c.left {
		p = p[:c.left]
	}
	n, err := c.r.Read(p)
	c.h.Write(p[:n])
	c.n += int64(n)
	c.left -= int64(n)
	return n, err
}

func (c *capReader) digest() string { return "sha256:" + hex.EncodeToString(c.h.Sum(nil)) }

func openEvents(e *Env, path string) (io.Reader, func(), error) {
	if path == "-" {
		return e.stdin(), func() {}, nil
	}
	fh, err := os.Open(path)
	if err != nil {
		return nil, nil, usageErr(fmt.Errorf("can't open --events: %w", err), "Check the path.")
	}
	if !strings.HasSuffix(path, ".gz") {
		return fh, func() { fh.Close() }, nil
	}
	gz, err := gzip.NewReader(fh)
	if err != nil {
		fh.Close()
		return nil, nil, usageErr(fmt.Errorf("--events isn't gzip: %w", err), "Pass a .jsonl file, or a .jsonl.gz that gunzip reads.")
	}
	return gz, func() { gz.Close(); fh.Close() }, nil
}

func parsePKs(vs []string) (map[string][]string, error) {
	out := map[string][]string{}
	for _, v := range vs {
		t, cols, ok := strings.Cut(v, "=")
		if !ok || t == "" || cols == "" {
			return nil, usageErr(fmt.Errorf("--pk %q isn't table=col[,col]", v), "For example --pk shop.orders=id.")
		}
		out[t] = strings.Split(cols, ",")
	}
	return out, nil
}

func parseMaxRows(vs []string) (map[string]int64, error) {
	out := map[string]int64{}
	for _, v := range vs {
		t, n, ok := strings.Cut(v, "=")
		i, err := strconv.ParseInt(n, 10, 64)
		if !ok || t == "" || err != nil || i < 0 {
			return nil, usageErr(fmt.Errorf("--max-rows %q isn't table=n", v), "For example --max-rows shop.orders=10.")
		}
		out[t] = i
	}
	return out, nil
}

// debeziumOutput is the --json answer.
type debeziumOutput struct {
	Object      string                 `json:"object"` // "foreign_witness"
	Source      feed.Source            `json:"source"`
	Label       string                 `json:"label"`
	Attribution string                 `json:"attribution"`
	Feed        debeziumFeedSummary    `json:"feed"`
	Compared    []debeziumTxSummary    `json:"compared"`
	Detail      witness.Detail         `json:"detail"`
	Digest      string                 `json:"detail_digest"`
	Verdict     witness.Verdict        `json:"verdict"`
	Level       witness.Level          `json:"level"`
	Reasons     []string               `json:"reason_codes"`
	Counts      map[string]l2.OpCounts `json:"observed_counts,omitempty"`
}

type debeziumFeedSummary struct {
	Transactions int    `json:"transactions"`
	Events       int    `json:"events"`
	Skipped      int    `json:"skipped"`
	InputBytes   int64  `json:"input_bytes"`
	InputSHA256  string `json:"input_sha256"` // of the events as read (after gunzip)
}

type debeziumTxSummary struct {
	ID          string `json:"id"`
	Position    string `json:"position,omitempty"`
	CommittedAt string `json:"committed_at,omitempty"`
	Rows        int    `json:"rows"`
	Complete    bool   `json:"complete"`
	Incomplete  string `json:"incomplete,omitempty"`
	Digest      string `json:"digest"`
}

func debeziumJSON(f *debezium.Feed, rep feed.Report, in *capReader) debeziumOutput {
	out := debeziumOutput{Object: "foreign_witness", Source: rep.Source, Label: rep.Source.Label(), Attribution: rep.Attribution,
		Feed:   debeziumFeedSummary{Transactions: len(f.Txs), Events: f.Events, Skipped: f.Skipped, InputBytes: in.n, InputSHA256: in.digest()},
		Detail: rep.Outcome.Detail, Digest: rep.Outcome.Digest,
		Verdict: rep.Outcome.Decision.Verdict, Level: rep.Outcome.Decision.Level, Compared: []debeziumTxSummary{}, Reasons: []string{}}
	for _, r := range rep.Outcome.Decision.Reasons {
		out.Reasons = append(out.Reasons, string(r.Code))
	}
	for _, t := range rep.Txs {
		s := debeziumTxSummary{ID: t.ID, Position: t.Position, Rows: t.Total, Complete: t.Incomplete == "", Incomplete: t.Incomplete, Digest: t.Digest}
		if !t.CommitTime.IsZero() {
			s.CommittedAt = t.CommitTime.UTC().Format("2006-01-02T15:04:05.000Z")
		}
		out.Compared = append(out.Compared, s)
	}
	if len(rep.Txs) == 1 {
		out.Counts = rep.Txs[0].Counts
	}
	return out
}

// debeziumText is the terminal answer: the claim, the reasons, the L2 diff and a footer naming
// the source and the transaction.
func debeziumText(e *Env, f *debezium.Feed, rep feed.Report) []string {
	st := e.Style
	d := rep.Outcome.Decision
	det := rep.Outcome.Detail
	var out []string
	out = append(out, string(d.Level)+" "+ui.Sep+" "+verdictWord(st, string(d.Verdict))+" "+ui.Sep+" "+rep.Source.Label())
	if det.Summary != "" {
		out = append(out, clean(det.Summary))
	}
	var rs []string
	for _, r := range d.Reasons {
		if r.Message == det.Summary {
			continue // the line above says it
		}
		rs = append(rs, "  "+classSymbol(st, r.Class)+" "+clean(r.Message))
	}
	if len(rs) > 0 {
		out = append(out, "")
		out = append(out, rs...)
	}
	if det.Levels.L2 != nil && det.Levels.L2.L2Evidence != nil {
		dd := det
		ev := *dd.Levels.L2.L2Evidence
		ev.Transaction = nil // the footer below names the source, not Postgres
		lv := *dd.Levels.L2
		lv.L2Evidence = &ev
		dd.Levels.L2 = &lv
		w := e.Width
		if w <= 0 {
			w = 120
		}
		if lines := l2.Render(dd, l2.RenderOptions{Width: w, Style: st, Indent: "  "}); len(lines) > 0 {
			out = append(out, "")
			out = append(out, lines...)
		}
	}
	out = append(out, "")
	ids := make([]string, 0, len(rep.Txs))
	for _, t := range rep.Txs {
		line := rep.Source.Label() + " " + ui.Sep + " transaction " + clean(t.ID)
		if t.Position != "" {
			line += " " + ui.Sep + " " + clean(t.Position)
		}
		if !t.CommitTime.IsZero() {
			line += " " + ui.Sep + " " + t.CommitTime.UTC().Format("2006-01-02 15:04:05") + " UTC"
		}
		ids = append(ids, line)
	}
	sort.Strings(ids)
	for _, l := range ids {
		out = append(out, st.Dim(l))
	}
	out = append(out, st.Dim(fmt.Sprintf("Read %d %s, %d change %s.", len(f.Txs), plural(len(f.Txs), "transaction", "transactions"),
		f.Events, plural(f.Events, "event", "events"))))
	return out
}
