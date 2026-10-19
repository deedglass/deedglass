package connector

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// Version-bound L2 (ADR-108): a server that runs its own SQL (a FHIR server on Postgres) keeps one
// row per resource version. The decoder reads that server's database, a VersionMap turns each
// transaction into resource versions, and a transaction is bound to the action whose execution
// evidence names a version it holds (EvidenceVersionKey). The connector judges the bound
// transaction (L2VersionJudge); the Gate and the Witness learn nothing about the server.

// Execution evidence keys of a version-bound write (gate.EvidenceJSON keeps them).
const (
	EvidenceVersionKey   = "version_key"   // "Type/id/version", the version the server reported
	EvidenceBindingToken = "binding_token" // the token the connector sent with the write
)

// ResourceVersion is one resource version a server's transaction wrote.
type ResourceVersion struct {
	Type    string
	ID      string // "" when the transaction doesn't say (the version can't be bound)
	Version string
	Op      string // create | update | delete | write (the server's rows don't say which)
	// Tag is the server's own mark on the row: HAPI's request_id, Medplum's meta.author; "" when none.
	// When TagPointer is set, Tag is the keyed leaf digest at that pointer (internal/jsonleaf), and
	// is compared by digesting the expected value with the same key.
	Tag        string
	TagPointer string
	// Content is the version as the server stored it, rebuilt to a resource; nil when the change log
	// doesn't hold it (Unreadable says why) or the version is a delete.
	Content    json.RawMessage
	Unreadable string // "large_object", "external", "unchanged_toast"
	Table      string
}

// Key is the version's binding key, "Type/id/version".
func (v ResourceVersion) Key() string { return v.Type + "/" + v.ID + "/" + v.Version }

// Resource is "Type/id".
func (v ResourceVersion) Resource() string { return v.Type + "/" + v.ID }

// VersionFrame is a transaction as resource versions.
type VersionFrame struct {
	Layout   string // the layout the mapping recognised: "hapi.8", "medplum.5"
	Versions []ResourceVersion
	Unknown  string // a mapped table whose columns the mapping doesn't know (fhir_schema_unknown)
	// Truncated: the transaction had more changes than the decoder keeps images for, so versions may
	// be missing.
	Truncated bool
}

// VersionMap is one server's table mapping (internal/connector/fhir/pgmap).
type VersionMap interface {
	// Server names the mapping ("hapi", "medplum").
	Server() string
	// Required are the tables the publication must hold (schema.table).
	Required() []string
	// Keyed reports a column that holds resource content: the decoder keeps it only as keyed leaf
	// digests, so a frame never holds a clinical value.
	Keyed(table, column string) bool
	// Derived reports a table that is counted, never judged: its images aren't kept.
	Derived(table string) bool
	// Map reads a frame's stored tables and changes (the decoder's JSON) and flags.
	Map(tables, changes json.RawMessage, flags []string) VersionFrame
}

// L2Frame is a stored frame as a version-bound judge reads it.
type L2Frame struct {
	ID         string
	Class      string // bound, request_id_reused, version_tag_mismatch, marker_count, unattributed
	ActionID   string
	SystemID   string
	Timeline   int
	CommitLSN  string
	CommitTime time.Time
	Xid        int64
	Total      int
	Digest     string
	Tables     json.RawMessage
	Changes    json.RawMessage
	Flags      []string
}

// L2Finding is one fact a version-bound judge found. Code is a Witness catalog code; the catalog,
// not the connector, gives it its class.
type L2Finding struct {
	Code     string
	Vars     map[string]any
	Evidence map[string]any
	Diff     []L2Diff
}

// L2Diff is one compared field that differs.
type L2Diff struct {
	Path     string
	Expected any
	Observed any
}

// L2Judgement is a version-bound judge's answer for one action.
type L2Judgement struct {
	Method   string // "l2.fhir.hapi.8"
	Primary  string // the id of the bound frame the comparison read; "" when none
	Findings []L2Finding
}

// L2VersionJudge is a connector type whose writes are version-bound at L2 (ADR-108).
type L2VersionJudge interface {
	// L2Judge judges the frames that concern call: those naming the action (bound,
	// request_id_reused, version_tag_mismatch, marker_count) and the unattributed frames that
	// committed while its call was open. It never decides a verdict.
	L2Judge(ctx context.Context, inst *Instance, call Call, res Result, frames []L2Frame) (L2Judgement, error)
}

// L2VersionJudging is an Executor whose connector is version-bound at L2.
type L2VersionJudging interface {
	L2Judge(ctx context.Context, call Call, res Result, frames []L2Frame) (L2Judgement, error)
}

// L2Judge implements L2VersionJudging; a panic is an error (the Gate decides internal_error).
func (b *bound) L2Judge(ctx context.Context, call Call, res Result, frames []L2Frame) (j L2Judgement, err error) {
	impl, ok := b.impl.(L2VersionJudge)
	if !ok {
		return L2Judgement{}, nil
	}
	defer func() {
		if p := recover(); p != nil {
			j, err = L2Judgement{}, fmt.Errorf("connector: the %s connector's L2 judge stopped: %v", b.impl.Type(), p)
		}
	}()
	return impl.L2Judge(ctx, b.inst, call, res, frames)
}
