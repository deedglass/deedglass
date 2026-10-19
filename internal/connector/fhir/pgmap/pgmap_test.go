package pgmap_test

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/deedglass/deedglass/internal/connector"
	"github.com/deedglass/deedglass/internal/connector/fhir/pgmap"
	"github.com/deedglass/deedglass/internal/jsonleaf"
)

func keys(vf connector.VersionFrame) []string {
	var out []string
	for _, v := range vf.Versions {
		out = append(out, v.Key()+" "+v.Op+" "+v.Tag)
	}
	return out
}

func content(t *testing.T, v connector.ResourceVersion) pgmap.Content {
	t.Helper()
	var c pgmap.Content
	if err := json.Unmarshal(v.Content, &c); err != nil {
		t.Fatal(err)
	}
	return c
}

// TestHAPI8 maps HAPI FHIR JPA v8.12.0-2's real transactions (testdata/hapi8-writes.json, recorded
// by TestCaptureFixtures through the decoder's own code): a create that committed a placeholder
// Patient too, a create whose tag definition was written earlier, two updates (one without a
// request id) and a delete.
func TestHAPI8(t *testing.T) {
	fs := loadFixture(t, "hapi8-writes")
	m := pgmap.HAPI{}
	want := [][]string{
		{"Observation/1003/1 create tok0000000000001", "Patient/ghost-cap/1 create tok0000000000001"},
		{"Observation/1004/1 create tok0000000000002"},
		{"Observation/1004/2 update tok0000000000003"},
		{"Observation/1004/3 update jRXidMesex5l4UxS"}, // no X-Request-ID: HAPI stored one it made
		{"Observation/1004/4 delete tok0000000000005"},
	}
	if len(fs) != len(want) {
		t.Fatalf("%d frames, want %d", len(fs), len(want))
	}
	for i, f := range fs {
		vf := m.Map(f.Tables, f.Changes, f.Flags)
		if vf.Layout != "hapi.8" || vf.Unknown != "" || strings.Join(keys(vf), "|") != strings.Join(want[i], "|") {
			t.Errorf("frame %d: %+v, want %v", i, keys(vf), want[i])
		}
	}
	// The create's content: leaf digests of what HAPI stored (no id, no meta), and meta rebuilt
	// from the tag tables: the tag and the profile were defined in this transaction.
	vf := m.Map(fs[0].Tables, fs[0].Changes, fs[0].Flags)
	obs := content(t, vf.Versions[0])
	if obs.ID != "1003" || obs.Meta.VersionID != "1" || obs.Meta.Unresolved != 0 || len(obs.Meta.Tags) != 1 || obs.Meta.Tags[0].Code != "y" ||
		len(obs.Meta.Profiles) != 1 || obs.Meta.Profiles[0] != "http://example.org/p2" {
		t.Fatalf("rebuilt meta: %+v", obs.Meta)
	}
	if obs.Leaves["/status"] != jsonleaf.Digest(fixtureKey, "/status", "s:final") || obs.Leaves["/valueQuantity/value"] != jsonleaf.Digest(fixtureKey, "/valueQuantity/value", "n:72") {
		t.Fatalf("leaves: %v", obs.Leaves)
	}
	if _, ok := obs.Leaves["/meta/tag/0/code"]; ok {
		t.Fatal("HAPI's stored text holds meta")
	}
	// The second create reuses the tag: its definition isn't in the transaction.
	vf = m.Map(fs[1].Tables, fs[1].Changes, fs[1].Flags)
	if c := content(t, vf.Versions[0]); c.Meta.Unresolved != 1 {
		t.Fatalf("unresolved tags: %+v", c.Meta)
	}
	// A delete has no content to compare.
	vf = m.Map(fs[4].Tables, fs[4].Changes, fs[4].Flags)
	if vf.Versions[0].Content != nil {
		t.Fatal("a delete has content")
	}
	// The fixtures hold no clinical value: every content column is digests.
	raw, _ := os.ReadFile("testdata/hapi8-writes.json")
	for _, s := range []string{"8867-4", "beats/minute", "preliminary"} {
		if strings.Contains(string(raw), s) {
			t.Errorf("the recorded frames hold %q in clear", s)
		}
	}
}

// TestHAPILargeObject is I08's row: pre-7.0 HAPI kept the text in res_text, a large object the
// change log doesn't carry (JSONC). The version is still bound by its columns; its content isn't
// there to compare.
func TestHAPILargeObject(t *testing.T) {
	f := loadFixture(t, "hapi8-writes")[1]
	var changes []map[string]any
	if err := json.Unmarshal(f.Changes, &changes); err != nil {
		t.Fatal(err)
	}
	for _, c := range changes {
		if c["table"] != "public.hfj_res_ver" {
			continue
		}
		img := c["new"].(map[string]any)
		img["res_encoding"] = map[string]any{"t": "JSONC"}
		img["res_text_vc"] = map[string]any{"n": true}
		img["res_text"] = map[string]any{"t": "16405"}
	}
	b, _ := json.Marshal(changes)
	vf := pgmap.HAPI{}.Map(f.Tables, b, nil)
	if len(vf.Versions) != 1 || vf.Versions[0].Content != nil || vf.Versions[0].Unreadable != "large_object" || vf.Versions[0].Key() != "Observation/1004/1" {
		t.Fatalf("%+v", vf.Versions)
	}
}

// TestSchemaUnknown (ADR-108 B8): a version table with a column the mapping doesn't know, or a
// column of another type, is unknown: nothing is guessed.
func TestSchemaUnknown(t *testing.T) {
	f := loadFixture(t, "hapi8-writes")[0]
	var tables map[string]map[string]any
	_ = json.Unmarshal(f.Tables, &tables)
	ver := tables["public.hfj_res_ver"]
	ver["columns"] = append(ver["columns"].([]any), "res_text_lz")
	ver["type_oids"] = append(ver["type_oids"].([]any), 17)
	b, _ := json.Marshal(tables)
	if vf := (pgmap.HAPI{}).Map(b, f.Changes, nil); vf.Unknown != "public.hfj_res_ver" {
		t.Fatalf("an extra column: %+v", vf)
	}
	_ = json.Unmarshal(f.Tables, &tables)
	res := tables["public.hfj_resource"]
	cols := res["columns"].([]any)
	oids := res["type_oids"].([]any)
	for i, c := range cols {
		if c == "fhir_id" {
			oids[i] = 25 // text instead of varchar
		}
	}
	b, _ = json.Marshal(tables)
	if vf := (pgmap.HAPI{}).Map(b, f.Changes, nil); vf.Unknown != "public.hfj_resource" {
		t.Fatalf("a changed type: %+v", vf)
	}
	m := loadFixture(t, "medplum5-writes")[0]
	_ = json.Unmarshal(m.Tables, &tables)
	h := tables["public.Observation_History"]
	h["columns"] = append(h["columns"].([]any), "compartments")
	h["type_oids"] = append(h["type_oids"].([]any), 2951)
	b, _ = json.Marshal(tables)
	if vf := (pgmap.Medplum{}).Map(b, m.Changes, nil); vf.Unknown != "public.Observation_History" {
		t.Fatalf("medplum: %+v", vf)
	}
}

// TestMedplum5 maps Medplum 5.2.6's real transactions (testdata/medplum5-writes.json): a create, an
// update and a delete of one Observation. The tag is meta.author's leaf digest.
func TestMedplum5(t *testing.T) {
	fs := loadFixture(t, "medplum5-writes")
	if len(fs) != 3 {
		t.Fatalf("%d frames", len(fs))
	}
	var id string
	var vids []string
	for i, f := range fs {
		vf := pgmap.Medplum{}.Map(f.Tables, f.Changes, f.Flags)
		if vf.Layout != "medplum.5" || vf.Unknown != "" || len(vf.Versions) != 1 {
			t.Fatalf("frame %d: %+v", i, vf)
		}
		v := vf.Versions[0]
		if id == "" {
			id = v.ID
		}
		if v.ID != id || v.Type != "Observation" || v.TagPointer != pgmap.AuthorPointer || v.Tag == "" || len(v.Version) != 36 {
			t.Fatalf("frame %d: %+v", i, v)
		}
		vids = append(vids, v.Version)
		switch i {
		case 2:
			if v.Op != "delete" || v.Content != nil {
				t.Fatalf("the delete: %+v", v)
			}
		default:
			c := content(t, v)
			if v.Op != "write" || c.Leaves["/status"] == "" || c.Meta.VersionID != v.Version {
				t.Fatalf("frame %d: %+v %+v", i, v, c)
			}
		}
	}
	if vids[0] == vids[1] || vids[1] == vids[2] {
		t.Fatalf("versions repeat: %v", vids)
	}
	if !(pgmap.Medplum{}).Derived("public.Observation") || (pgmap.Medplum{}).Derived("public.Observation_History") ||
		!(pgmap.Medplum{}).Keyed("public.Observation", "content") {
		t.Fatal("derived and keyed tables")
	}
	if !(pgmap.HAPI{}).Derived("public.hfj_spidx_token") || (pgmap.HAPI{}).Derived("public.hfj_res_ver") || !(pgmap.HAPI{}).Keyed("public.hfj_res_ver", "res_text_vc") {
		t.Fatal("hapi derived and keyed tables")
	}
	if _, err := pgmap.For("aidbox"); err == nil {
		t.Fatal("an unknown server has a mapping")
	}
}
