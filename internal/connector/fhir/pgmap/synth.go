package pgmap

import (
	"encoding/json"
	"sort"
	"strconv"

	"github.com/deedglass/deedglass/internal/jsonleaf"
)

// SynthVersion is one HAPI version row for SynthHAPI.
type SynthVersion struct {
	Type, ID  string
	Version   int
	ResID     int
	Encoding  string         // JSON (default), JSONC (text in a large object) or DEL
	Resource  map[string]any // as HAPI stores it: no id, no meta
	RequestID string
	Tags      [][3]string // tag rows: type ("0" tag, "1" profile, "2" security), system, code; defined in the frame
}

// SynthHAPI builds a frame (tables and changes, the decoder's stored JSON) holding the versions as
// HAPI 8.x writes them, with the resource text as keyed leaf digests under key. For tests: the
// connector's judge and the decoder's binding read frames this way.
func SynthHAPI(key []byte, vs ...SynthVersion) (tables, changes json.RawMessage) {
	ti := func(l layout) map[string]any {
		cols := make([]string, 0, len(l))
		for c := range l {
			cols = append(cols, c)
		}
		sort.Strings(cols)
		oids := make([]uint32, len(cols))
		for i, c := range cols {
			oids[i] = l[c]
		}
		return map[string]any{"relid": 1, "pk": []string{}, "identity": "d", "columns": cols, "type_oids": oids}
	}
	tables, _ = json.Marshal(map[string]any{"public." + hapiRes: ti(hapiRes8), "public." + hapiVer: ti(hapiVer8),
		"public." + hapiHistTag: ti(hapiHistTag8), "public." + hapiTagDef: ti(hapiTagDef8)})
	t := func(s string) map[string]any { return map[string]any{"t": s} }
	null := map[string]any{"n": true}
	var cs []map[string]any
	seq, tagID := 0, 100
	add := func(op, table string, img map[string]any) {
		seq++
		cs = append(cs, map[string]any{"seq": seq, "op": op, "table": "public." + table, "key": nil, "new": img})
	}
	for i, v := range vs {
		enc := v.Encoding
		if enc == "" {
			enc = "JSON"
		}
		pid := strconv.Itoa(5000 + i)
		resID := strconv.Itoa(v.ResID)
		ver := strconv.Itoa(v.Version)
		row := map[string]any{"pid": t(pid), "res_id": t(resID), "res_type": t(v.Type), "res_ver": t(ver), "res_encoding": t(enc),
			"res_updated": t("2026-10-19 06:17:56.218"), "res_text": null, "res_text_vc": null, "request_id": null, "res_deleted_at": null,
			"partition_id": null, "partition_date": null, "res_version": t("R4"), "has_tags": t("f"), "res_published": t("2026-10-19 06:17:56.218"),
			"res_type_id": t("130"), "source_uri": null}
		switch enc {
		case "JSON":
			b, _ := json.Marshal(v.Resource)
			l, _ := jsonleaf.Parse(key, b)
			row["res_text_vc"] = map[string]any{"k": l}
		case "JSONC":
			row["res_text"] = t("16405")
		case "DEL":
			row["res_deleted_at"] = t("2026-10-19 06:18:24.667")
		}
		add("I", hapiVer, row)
		if v.RequestID != "" {
			upd := map[string]any{}
			for k, x := range row {
				upd[k] = x
			}
			upd["request_id"] = t(v.RequestID)
			add("U", hapiVer, upd)
		}
		op := "U"
		if v.Version == 1 {
			op = "I"
		}
		add(op, hapiRes, map[string]any{"res_id": t(resID), "fhir_id": t(v.ID), "res_type": t(v.Type), "res_ver": t(ver)})
		for _, tag := range v.Tags {
			tagID++
			id := strconv.Itoa(tagID)
			add("I", hapiTagDef, map[string]any{"tag_id": t(id), "tag_type": t(tag[0]), "tag_system": t(tag[1]), "tag_code": t(tag[2])})
			add("I", hapiHistTag, map[string]any{"res_ver_pid": t(pid), "tag_id": t(id)})
		}
	}
	changes, _ = json.Marshal(cs)
	return tables, changes
}
