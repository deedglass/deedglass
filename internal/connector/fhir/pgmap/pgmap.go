// Package pgmap reads a Postgres-backed FHIR server's own tables out of the decoder's frames
// (ADR-105 §7, ADR-108): each committed transaction becomes the resource versions it wrote. It is
// pure (no I/O) and fails closed: a mapped table whose columns aren't the layout a mapping knows
// makes the frame "unknown", never a guess. One file per server: hapi.go (HAPI FHIR JPA 8.x),
// medplum.go (Medplum 5.x).
//
// Resource content arrives as keyed leaf digests (internal/jsonleaf; the decoder never stores a
// clinical value), so a version's Content is the JSON object of those digests:
// {"leaves": {pointer: digest}}, plus the rebuilt id and meta in clear (ids, versions, times and tag
// codes, never clinical values).
package pgmap

import (
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/deedglass/deedglass/internal/connector"
)

// Servers are the mappings, by witness.server.
var Servers = []string{"hapi", "medplum"}

// For returns the mapping for server.
func For(server string) (connector.VersionMap, error) {
	switch server {
	case "hapi":
		return HAPI{}, nil
	case "medplum":
		return Medplum{}, nil
	}
	return nil, fmt.Errorf("pgmap: no mapping for server %q (%s)", server, strings.Join(Servers, ", "))
}

// Content is a version's Content: the leaf digests of what the server stored, and the id and meta it
// rebuilt (HAPI keeps them in columns and tag tables, Medplum inside the document).
type Content struct {
	Leaves map[string]string `json:"leaves"`
	ID     string            `json:"id,omitempty"`
	Meta   *Meta             `json:"meta,omitempty"`
}

// Meta is the rebuilt meta: ids, times and coded tags only.
type Meta struct {
	VersionID   string   `json:"versionId,omitempty"`
	LastUpdated string   `json:"lastUpdated,omitempty"`
	Tags        []Coding `json:"tag,omitempty"`
	Profiles    []string `json:"profile,omitempty"`
	Security    []Coding `json:"security,omitempty"`
	// Unresolved counts tag rows whose definition isn't in the transaction (HAPI writes a tag's
	// definition once, on first use): meta can't be compared when it is above zero.
	Unresolved int `json:"unresolved,omitempty"`
}

// Coding is one meta.tag or meta.security entry.
type Coding struct {
	System string `json:"system,omitempty"`
	Code   string `json:"code,omitempty"`
}

// value is a stored column value (decoder.Value's JSON).
type value struct {
	T *string           `json:"t,omitempty"`
	N bool              `json:"n,omitempty"`
	U bool              `json:"u,omitempty"`
	D string            `json:"d,omitempty"`
	X string            `json:"x,omitempty"`
	K map[string]string `json:"k,omitempty"`
}

func (v value) text() (string, bool) {
	if v.T != nil {
		return *v.T, true
	}
	return "", false
}

type change struct {
	Seq   int              `json:"seq"`
	Op    string           `json:"op"`
	Table string           `json:"table"`
	Old   map[string]value `json:"old,omitempty"`
	New   map[string]value `json:"new,omitempty"`
}

type tableInfo struct {
	Columns  []string `json:"columns"`
	TypeOIDs []uint32 `json:"type_oids"`
}

// Postgres type OIDs the layouts use.
const (
	oidBool        = 16
	oidInt8        = 20
	oidInt2        = 21
	oidInt4        = 23
	oidText        = 25
	oidOID         = 26
	oidVarchar     = 1043
	oidDate        = 1082
	oidTimestamp   = 1114
	oidTimestamptz = 1184
	oidUUID        = 2950
)

// layout is a table's exact columns and their types.
type layout map[string]uint32

// matches reports whether t has exactly these columns (exact) or at least them (subset), each with
// its type.
func (l layout) matches(t tableInfo, exact bool) bool {
	if exact && len(t.Columns) != len(l) {
		return false
	}
	found := 0
	for i, c := range t.Columns {
		want, ok := l[c]
		if !ok {
			continue
		}
		if i >= len(t.TypeOIDs) || t.TypeOIDs[i] != want {
			return false
		}
		found++
	}
	return found == len(l)
}

type frame struct {
	tables  map[string]tableInfo
	changes []change
}

func parse(tables, changes json.RawMessage) (frame, error) {
	var f frame
	if err := json.Unmarshal(tables, &f.tables); err != nil {
		return f, err
	}
	if len(changes) > 0 && string(changes) != "null" {
		if err := json.Unmarshal(changes, &f.changes); err != nil {
			return f, err
		}
	}
	sort.SliceStable(f.changes, func(i, j int) bool { return f.changes[i].Seq < f.changes[j].Seq })
	return f, nil
}

// image is a change's row as it ends: the new image, or the old one for a delete.
func (c change) image() map[string]value {
	if c.Op == "D" {
		return c.Old
	}
	return c.New
}

// merge lays later images over earlier ones (an unchanged TOAST value keeps the earlier value).
func merge(dst, src map[string]value) map[string]value {
	if dst == nil {
		dst = map[string]value{}
	}
	for k, v := range src {
		if v.U {
			if _, ok := dst[k]; ok {
				continue
			}
		}
		dst[k] = v
	}
	return dst
}

func contentJSON(c Content) json.RawMessage {
	if c.Leaves == nil {
		c.Leaves = map[string]string{}
	}
	b, _ := json.Marshal(c)
	return b
}

func sortVersions(vs []connector.ResourceVersion) {
	slices.SortStableFunc(vs, func(a, b connector.ResourceVersion) int { return strings.Compare(a.Key(), b.Key()) })
}

// truncated reports the decoder's image cap flag.
func truncated(flags []string) bool { return slices.Contains(flags, "images_truncated") }
