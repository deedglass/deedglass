package decoder

import (
	"context"
	"encoding/json"
	"sort"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/deedglass/deedglass/internal/connector"
	"github.com/deedglass/deedglass/internal/jsonleaf"
	"github.com/deedglass/deedglass/internal/store"
)

// Version-bound frames (ADR-108): the watched database is a FHIR server's own, which runs its own
// SQL, so no ledger row or marker can be in its transactions. A frame is bound to the action whose
// execution evidence names a version the frame holds (version_key), when exactly one action does
// and the row's own tag agrees. The decoder still never decides a verdict.

// The classes only version-bound connectors store (migration 0032).
const (
	ClassRequestIDReused    Class = "request_id_reused"    // a version carrying an action's token that the action never reported
	ClassVersionTagMismatch Class = "version_tag_mismatch" // the reported version's row carries another request's tag
)

// FlagOutOfBand marks an unattributed frame of a version-bound connector: a change on the server
// that no Deedglass action made.
const FlagOutOfBand = "out_of_band"

// ReasonSchemaUnknown halts a version-bound runner whose server's tables have a layout the mapping
// doesn't know (ADR-105 §7: Checked still runs; nothing is guessed).
const ReasonSchemaUnknown = "schema_unknown"

// Claims are what the Gate has recorded about a batch's versions: the actions whose execution
// evidence names each version key, each such action's binding token, and the action that sent
// each token.
type Claims struct {
	ByKey   map[string][]string // version key → action ids
	Tokens  map[string]string   // action id → binding token
	ByToken map[string]string   // binding token → action id
}

// ClassifyVersions sets f's class and action from its versions and the claims (ADR-108 §4). It is
// pure. keep is false for a frame that wrote no resource version (search cache, reindex: nothing
// to keep); claimed is false for an unattributed frame, which the runner may hold while an action
// could still record it. author and key check Medplum's meta.author (empty: not checked).
func ClassifyVersions(f *Frame, vf connector.VersionFrame, c Claims, author string, key []byte) (keep, claimed bool) {
	if len(vf.Versions) == 0 {
		return false, true
	}
	if vf.Truncated {
		f.flag("images_truncated")
	}
	named := map[string][]connector.ResourceVersion{} // action → the versions it reported
	for _, v := range vf.Versions {
		if v.ID == "" || v.Version == "" {
			continue
		}
		for _, a := range c.ByKey[v.Key()] {
			named[a] = append(named[a], v)
		}
	}
	switch len(named) {
	case 0:
		for _, v := range vf.Versions {
			if v.Tag == "" || v.TagPointer != "" {
				continue
			}
			if a, ok := c.ByToken[v.Tag]; ok {
				f.Class, f.ActionID = ClassRequestIDReused, a
				return true, true
			}
		}
		f.Class, f.ActionID = ClassUnattributed, ""
		f.flag(FlagOutOfBand)
		return true, false
	case 1:
	default:
		f.Class, f.ActionID = ClassMarkerCount, ""
		f.flag("versions_claimed:" + strconv.Itoa(len(named)))
		return true, true
	}
	var a string
	for k := range named {
		a = k
	}
	for _, v := range named[a] {
		if !tagAgrees(v, c.Tokens[a], author, key) {
			f.Class, f.ActionID = ClassVersionTagMismatch, a
			return true, true
		}
	}
	if author == "" {
		for _, v := range named[a] {
			if v.TagPointer != "" {
				f.flag("tag_unchecked")
				break
			}
		}
	}
	f.Class, f.ActionID = ClassBound, a
	return true, true
}

// tagAgrees is ADR-108 §4 rule 3: HAPI's request_id equals the action's token when HAPI stored one
// and the action sent one;
// Medplum's meta.author equals the connector's client when the connector knows it.
func tagAgrees(v connector.ResourceVersion, token, author string, key []byte) bool {
	if v.TagPointer == "" {
		// HAPI stores a request id of its own when the call sent none, so only a token the
		// action sent can disagree.
		return v.Tag == "" || token == "" || v.Tag == token //deedglass:allow-compare the binding token is a request id the client sends and the server echoes, not a secret
	}
	if author == "" {
		return true
	}
	return v.Tag == jsonleaf.Digest(key, v.TagPointer, "s:"+author)
}

// versionFrame maps f with m through the frame's stored JSON (the same bytes the Gate reads later).
func versionFrame(m connector.VersionMap, f *Frame) connector.VersionFrame {
	tables, err := json.Marshal(f.Tables)
	if err != nil {
		return connector.VersionFrame{Unknown: "the frame"}
	}
	changes := f.Changes
	if changes == nil {
		changes = []Change{}
	}
	ch, err := json.Marshal(changes)
	if err != nil {
		return connector.VersionFrame{Unknown: "the frame"}
	}
	return m.Map(tables, ch, f.Flags)
}

// claimInputs are a batch's version keys and plain tags, sorted, for the claim lookups.
func claimInputs(vfs []connector.VersionFrame) (keys, tags []string) {
	ks, ts := map[string]bool{}, map[string]bool{}
	for _, vf := range vfs {
		for _, v := range vf.Versions {
			if v.ID != "" && v.Version != "" {
				ks[v.Key()] = true
			}
			if v.Tag != "" && v.TagPointer == "" {
				ts[v.Tag] = true
			}
		}
	}
	for k := range ks {
		keys = append(keys, k)
	}
	for t := range ts {
		tags = append(tags, t)
	}
	sort.Strings(keys)
	sort.Strings(tags)
	return keys, tags
}

// hold is ADR-108 §5's rule: a frame no action names waits while an action of the connector is
// executing (its record may still claim it), at most grace after it was first read.
func hold(claimed, busy bool, first, now time.Time, grace time.Duration) bool {
	return !claimed && busy && now.Sub(first) < grace
}

// DefaultBindGrace is witness.bind_grace's default (ADR-108 §5).
const DefaultBindGrace = 5 * time.Second

// classifyVersioned classifies a version-bound batch (ADR-108 §4–5) in commit order. It stops at
// the first frame no action names while an action of the connector is still executing and the
// frame was first read less than the grace ago: that frame and everything after it are read again
// next poll. upto is the end LSN of the last frame decided (the checkpoint while holding).
func (r *Runner) classifyVersioned(ctx context.Context, frames []*Frame, pubChanged bool) (keep []*Frame, holding bool, upto LSN, err error) {
	vm := r.target.Versions
	vfs := make([]connector.VersionFrame, len(frames))
	for i, f := range frames {
		vfs[i] = versionFrame(vm, f)
		if vfs[i].Unknown != "" {
			return nil, false, 0, halt(ReasonSchemaUnknown, true, "server", vm.Server(), "table", vfs[i].Unknown)
		}
	}
	keys, tags := claimInputs(vfs)
	c := Claims{ByKey: map[string][]string{}, Tokens: map[string]string{}, ByToken: map[string]string{}}
	busy := false
	if len(keys) > 0 || len(tags) > 0 {
		err = r.d.DB.OrgReadTx(ctx, r.org, func(ctx context.Context, tx pgx.Tx) error {
			vc, err := store.VersionClaims(ctx, tx, r.org, r.name, keys)
			if err != nil {
				return err
			}
			for _, x := range vc {
				c.ByKey[x.VersionKey] = append(c.ByKey[x.VersionKey], x.ActionID)
				c.Tokens[x.ActionID] = x.Token
			}
			if c.ByToken, err = store.TokenClaims(ctx, tx, r.org, r.name, tags); err != nil {
				return err
			}
			busy, err = store.ConnectorBusy(ctx, tx, r.org, r.name)
			return err
		})
		if err != nil {
			return nil, false, 0, err
		}
	}
	grace := r.target.BindGrace
	if grace <= 0 {
		grace = DefaultBindGrace
	}
	now := r.d.Now()
	upto = r.lastTarget
	seen := map[LSN]bool{}
	for i, f := range frames {
		seen[f.CommitLSN] = true
		k, claimed := ClassifyVersions(f, vfs[i], c, r.target.Author, r.target.Key)
		if pubChanged && k {
			f.flag("publication_changed")
		}
		if !claimed && busy {
			first, ok := r.held[f.CommitLSN]
			if !ok {
				first = now
				r.held[f.CommitLSN] = now
			}
			if hold(claimed, busy, first, now, grace) {
				return keep, true, upto, nil
			}
		}
		delete(r.held, f.CommitLSN)
		if k {
			keep = append(keep, f)
		}
		upto = max(upto, f.EndLSN)
	}
	for l := range r.held {
		if !seen[l] {
			delete(r.held, l) // advanced past in an earlier poll
		}
	}
	return keep, false, upto, nil
}
