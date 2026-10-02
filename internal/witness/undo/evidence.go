package undo

import (
	"sort"
	"strings"
	"time"

	"github.com/deedglass/deedglass/internal/witness/l2"
)

// Op is a row operation in the original transaction and in a plan step.
type Op string

const (
	OpInsert Op = "insert"
	OpUpdate Op = "update"
	OpDelete Op = "delete"
)

// Value is one column value as the decoder stored it (l2.Value: text, NULL, unchanged TOAST, a
// sensitive column's digest, or non-UTF-8 text as hex).
type Value = l2.Value

// Image is one row image: column → value.
type Image = map[string]Value

// TableRef names a table by relid, with the evidence's schema and name.
type TableRef struct {
	RelID  uint32 `json:"relid"`
	Schema string `json:"schema"`
	Name   string `json:"name"`
}

// String is "schema.name".
func (t TableRef) String() string { return t.Schema + "." + t.Name }

// RowChange is one row change of the original transaction, normalized from the decoder's shape
// (S23 §3.2). Old is complete with REPLICA IDENTITY FULL; New may hold Unchanged values (an
// unchanged external TOAST column), whose value is the old image's.
type RowChange struct {
	Seq   int
	Op    Op
	Table TableRef
	Old   Image // update, delete
	New   Image // insert, update
}

// EvidenceRef pins the evidence a plan was built from.
type EvidenceRef struct {
	DecodedTx     string `json:"decoded_tx"`
	Xid           int64  `json:"xid"`
	CommitLSN     string `json:"commit_lsn"`
	CommittedAt   string `json:"committed_at"` // RFC 3339 UTC, milliseconds
	Changes       int    `json:"changes"`
	ChangesDigest string `json:"changes_digest"`
}

// Evidence is the original action's transaction as the decoder stored it, normalized.
type Evidence struct {
	Action      string
	ConnectorID string
	Ref         EvidenceRef
	Class       string
	Flags       []string
	Tables      map[string]l2.TableInfo // "schema.table" → relid, primary key, replica identity, columns
	Changes     []RowChange
	Total       int // every row change, past the image cap too (the ledger's own insert excluded)
	// Problems are why the evidence can't be planned from, in a person's words (the evidence check).
	Problems []Finding
}

// FrameInput is everything FromFrames reads: the frames that name the action (S21's input) and
// the action's own facts.
type FrameInput struct {
	Action      string
	ConnectorID string
	Frames      []l2.Frame
}

// FromFrames picks the one bound frame of the action and normalizes it. It never fails: what it
// can't use becomes a Problem, and the evidence check fails with it.
func FromFrames(in FrameInput) *Evidence {
	ev := &Evidence{Action: in.Action, ConnectorID: in.ConnectorID, Tables: map[string]l2.TableInfo{}}
	var bound []*l2.Frame
	var other []string
	for i := range in.Frames {
		f := &in.Frames[i]
		switch f.Class {
		case l2.ClassBound:
			bound = append(bound, f)
		case l2.ClassMissing:
		default:
			other = append(other, f.Class)
		}
	}
	switch {
	case len(bound) == 0 && len(other) > 0:
		ev.Problems = append(ev.Problems, ProblemNotBound(other[0]))
		return ev
	case len(bound) == 0:
		ev.Problems = append(ev.Problems, ProblemNoFrame)
		return ev
	case len(bound) > 1 || len(other) > 0:
		ev.Problems = append(ev.Problems, ProblemManyFrames(len(bound)+len(other)))
		return ev
	}
	f := bound[0]
	ev.Class, ev.Flags, ev.Total = f.Class, append([]string{}, f.Flags...), f.Total
	ev.Ref = EvidenceRef{DecodedTx: f.ID, Xid: f.Xid, CommitLSN: f.CommitLSN, CommittedAt: f.CommitTime.UTC().Format("2006-01-02T15:04:05.000Z"),
		Changes: f.Total, ChangesDigest: f.Digest}
	for t, ti := range f.Tables {
		if t != l2.LedgerTable {
			ev.Tables[t] = ti
		}
	}
	for _, fl := range f.Flags {
		switch {
		case strings.HasPrefix(fl, "truncate:"):
			ev.Problems = append(ev.Problems, ProblemTruncate(strings.TrimPrefix(fl, "truncate:")))
		case strings.HasPrefix(fl, "relation_changed:"):
			ev.Problems = append(ev.Problems, ProblemRelationChanged(strings.TrimPrefix(fl, "relation_changed:")))
		case fl == "images_truncated":
			ev.Problems = append(ev.Problems, ProblemImagesTruncated(f.Total, len(f.Changes)))
		}
	}
	for t, c := range f.Counts {
		if t != l2.LedgerTable && c.Truncate > 0 && !f.HasFlag("truncate:"+t) {
			ev.Problems = append(ev.Problems, ProblemTruncate(t))
		}
	}
	if f.Total > len(f.Changes) && !f.HasFlag("images_truncated") {
		ev.Problems = append(ev.Problems, ProblemImagesTruncated(f.Total, len(f.Changes)))
	}
	changes := append([]l2.Change{}, f.Changes...)
	sort.SliceStable(changes, func(i, j int) bool { return changes[i].Seq < changes[j].Seq })
	seen := map[string]bool{}
	for _, c := range changes {
		if c.Table == l2.LedgerTable {
			continue
		}
		ti, ok := ev.Tables[c.Table]
		schema, name, _ := strings.Cut(c.Table, ".")
		if !ok {
			if !seen[c.Table] {
				ev.Problems = append(ev.Problems, ProblemNoRelation(c.Table))
				seen[c.Table] = true
			}
			continue
		}
		rc := RowChange{Seq: c.Seq, Table: TableRef{RelID: ti.RelID, Schema: schema, Name: name}, Old: c.Old, New: c.New}
		switch c.Op {
		case "I":
			rc.Op = OpInsert
		case "U":
			rc.Op = OpUpdate
		case "D":
			rc.Op = OpDelete
		default:
			if !seen["op:"+c.Op] {
				ev.Problems = append(ev.Problems, ProblemTruncate(c.Table))
				seen["op:"+c.Op] = true
			}
			continue
		}
		if (rc.Op == OpUpdate || rc.Op == OpDelete) && ti.Identity != "f" && !seen["identity:"+c.Table] {
			ev.Problems = append(ev.Problems, ProblemOldImage(c.Table, ti.Identity))
			seen["identity:"+c.Table] = true
		}
		ev.Changes = append(ev.Changes, rc)
	}
	ev.Problems = sortFindings(ev.Problems)
	return ev
}

// TableNames are the evidence's tables (the ledger aside), sorted.
func (ev *Evidence) TableNames() []string {
	out := make([]string, 0, len(ev.Tables))
	for t := range ev.Tables {
		out = append(out, t)
	}
	sort.Strings(out)
	return out
}

// CommittedAt parses the evidence's commit time.
func (ev *Evidence) CommittedAt() time.Time {
	t, _ := time.Parse("2006-01-02T15:04:05.000Z", ev.Ref.CommittedAt)
	return t
}

// sortFindings orders findings by check, then text, without duplicates.
func sortFindings(fs []Finding) []Finding {
	order := map[string]int{}
	for i, c := range CheckOrder {
		order[c] = i
	}
	sort.SliceStable(fs, func(i, j int) bool {
		if fs[i].Check != fs[j].Check {
			return order[fs[i].Check] < order[fs[j].Check]
		}
		return fs[i].Text < fs[j].Text
	})
	out := fs[:0]
	for i, f := range fs {
		if i == 0 || f.Text != fs[i-1].Text || f.Check != fs[i-1].Check {
			out = append(out, f)
		}
	}
	return out
}
