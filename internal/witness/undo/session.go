package undo

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strings"

	"github.com/deedglass/deedglass/internal/witness/l2"
)

// SessionInput is a watched session's transactions (S50): the unattributed frames it holds, the
// ones a person left out already removed, in any order.
type SessionInput struct {
	Session     string
	ConnectorID string
	Frames      []l2.Frame
}

// FromSessionFrames normalizes a watched session's transactions into one Evidence, so one plan puts
// back every row the session changed, newest first (S50 §3.6: one engine, two sources). Changes
// keep their order across transactions (commit LSN, then each frame's own sequence), so Collapse
// folds a row changed in several transactions into one net effect. What can't be planned from
// becomes a Problem, exactly as for one action: a TRUNCATE, DDL, images past the cap, a table
// without full old images, a table whose relid changed between transactions.
//
// The Evidence's Ref names the session: decoded_tx is the session id, commit_lsn and committed_at
// the newest transaction's, changes the total, and changes_digest sha256 over each frame's id and
// changes digest in commit order (the seal's changes_digest).
func FromSessionFrames(in SessionInput) *Evidence {
	ev := &Evidence{Action: in.Session, ConnectorID: in.ConnectorID, Tables: map[string]l2.TableInfo{}, Class: "observed"}
	frames := append([]l2.Frame{}, in.Frames...)
	sort.SliceStable(frames, func(i, j int) bool {
		a, b := frames[i], frames[j]
		if !a.CommitTime.Equal(b.CommitTime) {
			return a.CommitTime.Before(b.CommitTime)
		}
		if la, lb := lsnKey(a.CommitLSN), lsnKey(b.CommitLSN); la != lb {
			return la < lb
		}
		return a.ID < b.ID
	})
	if len(frames) == 0 {
		ev.Problems = append(ev.Problems, ProblemNoSessionFrames)
		return ev
	}
	seen := map[string]bool{}
	problem := func(key string, f Finding) {
		if !seen[key] {
			seen[key] = true
			ev.Problems = append(ev.Problems, f)
		}
	}
	var digests []string
	seq := 0
	for _, f := range frames {
		if f.Class != l2.ClassUnattributed {
			problem("class:"+f.Class, ProblemNotBound(f.Class))
			continue
		}
		ev.Total += f.Total
		digests = append(digests, f.ID+" "+f.Digest)
		for t, ti := range f.Tables {
			if t == l2.LedgerTable {
				continue
			}
			if prev, ok := ev.Tables[t]; ok && prev.RelID != ti.RelID {
				problem("relid:"+t, ProblemRelationChanged(t))
				continue
			}
			ev.Tables[t] = ti
		}
		for _, fl := range f.Flags {
			switch {
			case strings.HasPrefix(fl, "truncate:"):
				t := strings.TrimPrefix(fl, "truncate:")
				problem("truncate:"+t, ProblemTruncate(t))
			case strings.HasPrefix(fl, "relation_changed:"):
				t := strings.TrimPrefix(fl, "relation_changed:")
				problem("relation:"+t, ProblemRelationChanged(t))
			case fl == "images_truncated":
				problem("images:"+f.ID, ProblemImagesTruncated(f.Total, len(f.Changes)))
			}
		}
		for t, c := range f.Counts {
			if t != l2.LedgerTable && c.Truncate > 0 {
				problem("truncate:"+t, ProblemTruncate(t))
			}
		}
		if f.Total > len(f.Changes) && !f.HasFlag("images_truncated") {
			problem("images:"+f.ID, ProblemImagesTruncated(f.Total, len(f.Changes)))
		}
		changes := append([]l2.Change{}, f.Changes...)
		sort.SliceStable(changes, func(i, j int) bool { return changes[i].Seq < changes[j].Seq })
		for _, c := range changes {
			if c.Table == l2.LedgerTable {
				continue
			}
			ti, ok := f.Tables[c.Table]
			if !ok {
				problem("norel:"+c.Table, ProblemNoRelation(c.Table))
				continue
			}
			schema, name, _ := strings.Cut(c.Table, ".")
			seq++
			rc := RowChange{Seq: seq, Table: TableRef{RelID: ti.RelID, Schema: schema, Name: name}, Old: c.Old, New: c.New}
			switch c.Op {
			case "I":
				rc.Op = OpInsert
			case "U":
				rc.Op = OpUpdate
			case "D":
				rc.Op = OpDelete
			default:
				problem("op:"+c.Table, ProblemTruncate(c.Table))
				continue
			}
			if (rc.Op == OpUpdate || rc.Op == OpDelete) && ti.Identity != "f" {
				problem("identity:"+c.Table, ProblemOldImage(c.Table, ti.Identity))
			}
			ev.Changes = append(ev.Changes, rc)
		}
	}
	last := frames[len(frames)-1]
	h := sha256.Sum256([]byte(strings.Join(digests, "\n")))
	ev.Ref = EvidenceRef{DecodedTx: in.Session, CommitLSN: last.CommitLSN, CommittedAt: last.CommitTime.UTC().Format("2006-01-02T15:04:05.000Z"),
		Changes: ev.Total, ChangesDigest: "sha256:" + hex.EncodeToString(h[:])}
	ev.Problems = sortFindings(ev.Problems)
	return ev
}

// ProblemNoSessionFrames: nothing left to undo (no transactions, or every one was left out or has
// aged out of the record).
var ProblemNoSessionFrames = Finding{Check: CheckEvidence,
	Text: "This session holds no transactions Deedglass still has the row images for, so there is nothing to undo."}

// lsnKey orders text LSNs (X/Y, hex): the high and low halves, zero-padded.
func lsnKey(s string) string {
	hi, lo, ok := strings.Cut(s, "/")
	if !ok {
		return s
	}
	pad := func(x string) string { return strings.Repeat("0", max(0, 8-len(x))) + strings.ToUpper(x) }
	return pad(hi) + "/" + pad(lo)
}
