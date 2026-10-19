package pgmap

import (
	"encoding/json"
	"strings"

	"github.com/deedglass/deedglass/internal/connector"
)

// HAPI maps HAPI FHIR JPA's tables (8.x; HC-P05 ran v8.12.0-2 on PostgreSQL 18.6). A write is one
// transaction: an hfj_res_ver INSERT and, in the same transaction, an UPDATE of that row (pid) that
// sets request_id; an hfj_resource INSERT or UPDATE (res_id, fhir_id, res_ver); tag rows
// (hfj_tag_def only on a tag's first use, hfj_history_tag per version); search-index and link rows.
// res_text_vc is the resource without id and meta; a delete is a version row with res_encoding DEL.
// Pre-7.0 rows keep the text in res_text, a large object the change log doesn't carry (JSONC).
type HAPI struct{}

// HAPI's tables, by relation name (any schema: the publication decides what is read).
const (
	hapiVer     = "hfj_res_ver"
	hapiRes     = "hfj_resource"
	hapiHistTag = "hfj_history_tag"
	hapiTagDef  = "hfj_tag_def"
)

// hapiVer8 is hfj_res_ver in HAPI 8.x, exactly: a new column could carry the content somewhere
// this mapping doesn't read, so any difference is unknown.
var hapiVer8 = layout{"partition_id": oidInt4, "pid": oidInt8, "res_deleted_at": oidTimestamp, "res_version": oidVarchar,
	"has_tags": oidBool, "res_published": oidTimestamp, "res_updated": oidTimestamp, "res_encoding": oidVarchar,
	"partition_date": oidDate, "request_id": oidVarchar, "res_text": oidOID, "res_id": oidInt8, "res_text_vc": oidText,
	"res_type": oidVarchar, "res_type_id": oidInt2, "res_ver": oidInt8, "source_uri": oidVarchar}

// The other tables: the columns this mapping reads (their other columns are search flags that come
// and go between releases).
var (
	hapiRes8     = layout{"res_id": oidInt8, "fhir_id": oidVarchar, "res_type": oidVarchar, "res_ver": oidInt8}
	hapiHistTag8 = layout{"res_ver_pid": oidInt8, "tag_id": oidInt8}
	hapiTagDef8  = layout{"tag_id": oidInt8, "tag_type": oidInt4, "tag_system": oidVarchar, "tag_code": oidVarchar}
)

// Server implements connector.VersionMap.
func (HAPI) Server() string { return "hapi" }

// Required implements connector.VersionMap: the version rows and the resource rows (for fhir_id).
func (HAPI) Required() []string { return []string{"public." + hapiRes, "public." + hapiVer} }

func relName(t string) string {
	if i := strings.LastIndexByte(t, '.'); i >= 0 {
		return t[i+1:]
	}
	return t
}

// Keyed implements connector.VersionMap: the resource text and the client's meta.source.
func (HAPI) Keyed(table, column string) bool {
	return relName(table) == hapiVer && (column == "res_text_vc" || column == "source_uri")
}

// Derived implements connector.VersionMap: every table but the version, resource and tag tables.
func (HAPI) Derived(table string) bool {
	switch relName(table) {
	case hapiVer, hapiRes, hapiHistTag, hapiTagDef:
		return false
	}
	return true
}

// Map implements connector.VersionMap.
func (h HAPI) Map(tables, changes json.RawMessage, flags []string) connector.VersionFrame {
	out := connector.VersionFrame{Layout: "hapi.8", Truncated: truncated(flags)}
	f, err := parse(tables, changes)
	if err != nil {
		out.Unknown = "the stored frame"
		return out
	}
	known := map[string]layout{hapiVer: hapiVer8, hapiRes: hapiRes8, hapiHistTag: hapiHistTag8, hapiTagDef: hapiTagDef8}
	for t, ti := range f.tables {
		l, ok := known[relName(t)]
		if !ok {
			continue
		}
		if !l.matches(ti, relName(t) == hapiVer) {
			out.Unknown = t
			return out
		}
	}
	type res struct{ fhirID, typ string }
	resources := map[string]res{} // res_id → fhir_id
	versions := map[string]map[string]value{}
	var order []string
	verTable := map[string]string{}
	expunged := map[string]bool{}
	histTags := map[string][]string{} // res_ver_pid → tag ids
	tagDefs := map[string][3]string{} // tag_id → type, system, code
	for _, c := range f.changes {
		img := c.image()
		switch relName(c.Table) {
		case hapiRes:
			id, _ := img["res_id"].text()
			fid, _ := img["fhir_id"].text()
			typ, _ := img["res_type"].text()
			if id != "" && fid != "" {
				resources[id] = res{fhirID: fid, typ: typ}
			}
		case hapiVer:
			pid, _ := img["pid"].text()
			if pid == "" {
				continue
			}
			if c.Op == "D" {
				expunged[pid] = true
			}
			if _, ok := versions[pid]; !ok {
				order = append(order, pid)
			}
			versions[pid] = merge(versions[pid], img)
			verTable[pid] = c.Table
		case hapiHistTag:
			if c.Op == "D" {
				continue
			}
			pid, _ := img["res_ver_pid"].text()
			tag, _ := img["tag_id"].text()
			if pid != "" && tag != "" {
				histTags[pid] = append(histTags[pid], tag)
			}
		case hapiTagDef:
			id, _ := img["tag_id"].text()
			typ, _ := img["tag_type"].text()
			sys, _ := img["tag_system"].text()
			code, _ := img["tag_code"].text()
			if id != "" {
				tagDefs[id] = [3]string{typ, sys, code}
			}
		}
	}
	for _, pid := range order {
		img := versions[pid]
		resID, _ := img["res_id"].text()
		typ, _ := img["res_type"].text()
		ver, _ := img["res_ver"].text()
		enc, _ := img["res_encoding"].text()
		v := connector.ResourceVersion{Type: typ, Version: ver, Table: verTable[pid]}
		if r, ok := resources[resID]; ok {
			v.ID = r.fhirID
			if v.Type == "" {
				v.Type = r.typ
			}
		}
		if rid, ok := img["request_id"].text(); ok {
			v.Tag = rid
		}
		updated, _ := img["res_updated"].text()
		switch {
		case expunged[pid]:
			v.Op = "expunge"
		case enc == "DEL":
			v.Op = "delete"
		case ver == "1":
			v.Op = "create"
		default:
			v.Op = "update"
		}
		if v.Op == "create" || v.Op == "update" {
			text := img["res_text_vc"]
			switch {
			case enc == "JSON" && text.K != nil:
				c := Content{Leaves: text.K, ID: v.ID, Meta: &Meta{VersionID: ver, LastUpdated: updated}}
				for _, tag := range histTags[pid] {
					d, ok := tagDefs[tag]
					if !ok {
						c.Meta.Unresolved++
						continue
					}
					switch d[0] {
					case "0":
						c.Meta.Tags = append(c.Meta.Tags, Coding{System: d[1], Code: d[2]})
					case "1":
						c.Meta.Profiles = append(c.Meta.Profiles, d[2])
					case "2":
						c.Meta.Security = append(c.Meta.Security, Coding{System: d[1], Code: d[2]})
					}
				}
				v.Content = contentJSON(c)
			case enc == "JSONC" || (text.N && !img["res_text"].N):
				v.Unreadable = "large_object"
			case text.U:
				v.Unreadable = "unchanged_toast"
			default:
				v.Unreadable = "external"
			}
		}
		out.Versions = append(out.Versions, v)
	}
	sortVersions(out.Versions)
	return out
}
