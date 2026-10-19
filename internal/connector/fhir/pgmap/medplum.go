package pgmap

import (
	"encoding/json"
	"strings"

	"github.com/deedglass/deedglass/internal/connector"
)

// Medplum maps Medplum's tables (5.x; HC-P05 ran 5.2.6 on PostgreSQL 18.6). A write is one
// transaction: the "<Type>" row (INSERT or UPDATE) and a "<Type>_History" INSERT of versionId (a
// UUID the server makes), id, content (the full JSON, meta.author included) and lastUpdated, plus
// lookup-table rows. A delete writes a history row whose content is only resourceType, id and meta,
// and sets deleted and an empty content on the "<Type>" row.
type Medplum struct{}

// medplumHistory5 is "<Type>_History" in Medplum 5.x, exactly.
var medplumHistory5 = layout{"versionId": oidUUID, "id": oidUUID, "content": oidText, "lastUpdated": oidTimestamptz}

// AuthorPointer is where a Medplum version's tag (meta.author.reference) is among the leaves.
const AuthorPointer = "/meta/author/reference"

// Server implements connector.VersionMap.
func (Medplum) Server() string { return "medplum" }

// Required implements connector.VersionMap: none fixed; the history table of each allowed type is
// checked by the connector (MedplumRequired).
func (Medplum) Required() []string { return nil }

// MedplumRequired are the history tables for the allowed types.
func MedplumRequired(types []string) []string {
	out := make([]string, 0, len(types))
	for _, t := range types {
		out = append(out, "public."+t+"_History")
	}
	return out
}

func historyType(table string) (string, bool) {
	n := relName(table)
	if !strings.HasSuffix(n, "_History") {
		return "", false
	}
	t := strings.TrimSuffix(n, "_History")
	return t, reTypeName(t)
}

func reTypeName(t string) bool {
	if len(t) < 2 || len(t) > 64 || t[0] < 'A' || t[0] > 'Z' {
		return false
	}
	for _, r := range t {
		if (r < 'A' || r > 'Z') && (r < 'a' || r > 'z') {
			return false
		}
	}
	return true
}

// Keyed implements connector.VersionMap: the content of every table (history and main).
func (Medplum) Keyed(_, column string) bool { return column == "content" }

// Derived implements connector.VersionMap: every table but the history tables (the "<Type>" rows
// carry search columns, the lookup tables names and codes; only their counts are kept).
func (Medplum) Derived(table string) bool {
	_, ok := historyType(table)
	return !ok
}

// Map implements connector.VersionMap.
func (Medplum) Map(tables, changes json.RawMessage, flags []string) connector.VersionFrame {
	out := connector.VersionFrame{Layout: "medplum.5", Truncated: truncated(flags)}
	f, err := parse(tables, changes)
	if err != nil {
		out.Unknown = "the stored frame"
		return out
	}
	for t, ti := range f.tables {
		if _, ok := historyType(t); ok && !medplumHistory5.matches(ti, true) {
			out.Unknown = t
			return out
		}
	}
	for _, c := range f.changes {
		typ, ok := historyType(c.Table)
		if !ok {
			continue // a derived table (search columns, lookups): counted by the decoder, never judged
		}
		img := c.image()
		id, _ := img["id"].text()
		vid, _ := img["versionId"].text()
		updated, _ := img["lastUpdated"].text()
		v := connector.ResourceVersion{Type: typ, ID: id, Version: vid, Op: "write", Table: c.Table}
		if c.Op == "D" {
			v.Op = "expunge"
			out.Versions = append(out.Versions, v)
			continue
		}
		content := img["content"]
		switch {
		case content.K != nil:
			if d, ok := content.K[AuthorPointer]; ok {
				v.Tag, v.TagPointer = d, AuthorPointer
			}
			if onlyMeta(content.K) {
				v.Op = "delete"
			} else {
				v.Content = contentJSON(Content{Leaves: content.K, ID: id, Meta: &Meta{VersionID: vid, LastUpdated: updated}})
			}
		case content.U:
			v.Unreadable = "unchanged_toast"
		default:
			v.Unreadable = "external"
		}
		out.Versions = append(out.Versions, v)
	}
	sortVersions(out.Versions)
	return out
}

// onlyMeta: a delete's history content holds only resourceType, id and meta.
func onlyMeta(leaves map[string]string) bool {
	for p := range leaves {
		switch {
		case p == "/resourceType", p == "/id", strings.HasPrefix(p, "/meta/"), p == "/meta":
		default:
			return false
		}
	}
	return true
}
