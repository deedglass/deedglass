package decoder

import (
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/deedglass/deedglass/internal/connector/fhir/pgmap"
	"github.com/deedglass/deedglass/internal/jsonleaf"
)

// hapiFrames are HAPI FHIR JPA v8.12.0-2's recorded transactions (pgmap's fixtures, keyed with
// pgmap's fixture key): [0] a create that committed a placeholder Patient too (token …01), [1] a
// create (…02), [2] an update (…03), [3] an update with no X-Request-ID, [4] a delete (…05).
func hapiFrames(t *testing.T, file string) []*Frame {
	t.Helper()
	b, err := os.ReadFile("../connector/fhir/pgmap/testdata/" + file)
	if err != nil {
		t.Fatal(err)
	}
	var raw []struct {
		Tables  map[string]TableInfo `json:"tables"`
		Changes []Change             `json:"changes"`
		Flags   []string             `json:"flags"`
		Total   int                  `json:"total"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatal(err)
	}
	var out []*Frame
	for _, r := range raw {
		out = append(out, &Frame{Tables: r.Tables, Changes: r.Changes, Flags: r.Flags, Total: r.Total})
	}
	return out
}

var fixtureKey = []byte("pgmap fixtures key, not a secret")

// TestClassifyVersions is ADR-108's binding table (B1–B5, B7's classification; B6 and the hold are
// TestHold, B8 is pgmap's TestSchemaUnknown).
func TestClassifyVersions(t *testing.T) {
	m := pgmap.HAPI{}
	const actA, actB = "act_01m58nq2jthtfkkv8qdejqts5a", "act_01m58nq2jthtfkkv8qdejqts5b"
	cases := []struct {
		name    string
		frame   int
		claims  Claims
		class   Class
		action  string
		claimed bool
	}{
		{"B1 the reported version, the action's token stored", 1,
			Claims{ByKey: map[string][]string{"Observation/1004/1": {actA}}, Tokens: map[string]string{actA: "tok0000000000002"}},
			ClassBound, actA, true},
		{"B1 the placeholder's transaction is still the action's (judged later as extra versions)", 0,
			Claims{ByKey: map[string][]string{"Observation/1003/1": {actA}}, Tokens: map[string]string{actA: "tok0000000000001"}},
			ClassBound, actA, true},
		{"B1 HAPI made its own request id because the call sent none", 3,
			Claims{ByKey: map[string][]string{"Observation/1004/3": {actA}}, Tokens: map[string]string{actA: ""}},
			ClassBound, actA, true},
		{"B2 the reported version carries another request's id", 2,
			Claims{ByKey: map[string][]string{"Observation/1004/2": {actA}}, Tokens: map[string]string{actA: "tokAAAAAAAAAAAAA"}},
			ClassVersionTagMismatch, actA, true},
		{"B3 two actions name versions in one transaction", 0,
			Claims{ByKey: map[string][]string{"Observation/1003/1": {actA}, "Patient/ghost-cap/1": {actB}}},
			ClassMarkerCount, "", true},
		{"B4 no action names a version; one carries an action's token", 2,
			Claims{ByToken: map[string]string{"tok0000000000003": actB}},
			ClassRequestIDReused, actB, true},
		{"B5 no action names a version: out of band", 2, Claims{}, ClassUnattributed, "", false},
		{"B5 a token nobody sent", 2, Claims{ByToken: map[string]string{"tokZZZZZZZZZZZZZ": actB}}, ClassUnattributed, "", false},
	}
	for _, c := range cases {
		f := hapiFrames(t, "hapi8-writes.json")[c.frame]
		vf := versionFrame(m, f)
		keep, claimed := ClassifyVersions(f, vf, c.claims, "", fixtureKey)
		if !keep || claimed != c.claimed || f.Class != c.class || f.ActionID != c.action {
			t.Errorf("%s: keep %v claimed %v class %s action %q", c.name, keep, claimed, f.Class, f.ActionID)
		}
		if c.class == ClassUnattributed && !f.HasFlag(FlagOutOfBand) {
			t.Errorf("%s: no out_of_band flag", c.name)
		}
	}
	// A transaction that wrote no version (search cache, reindex) isn't kept.
	if keep, _ := ClassifyVersions(&Frame{}, versionFrame(m, &Frame{Tables: map[string]TableInfo{}}), Claims{}, "", fixtureKey); keep {
		t.Error("a frame with no version is kept")
	}
}

// TestClassifyMedplumAuthor: Medplum's meta.author is compared by digest under the connector's key.
func TestClassifyMedplumAuthor(t *testing.T) {
	b, _ := os.ReadFile("../connector/fhir/pgmap/testdata/medplum5-writes.json")
	var raw []struct {
		Tables  map[string]TableInfo `json:"tables"`
		Changes []Change             `json:"changes"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatal(err)
	}
	f := &Frame{Tables: raw[0].Tables, Changes: raw[0].Changes}
	vf := versionFrame(pgmap.Medplum{}, f)
	v := vf.Versions[0]
	const act = "act_01m58nq2jthtfkkv8qdejqts5a"
	c := Claims{ByKey: map[string][]string{v.Key(): {act}}}
	var author string
	for _, a := range []string{"ClientApplication/beb7d9d8-2fc8-4e57-9ac2-17d06ad39051", "ClientApplication/844a9153-9508-40d4-9dae-638fa9e7172f"} {
		if v.Tag == jsonleaf.Digest(fixtureKey, pgmap.AuthorPointer, "s:"+a) {
			author = a
		}
	}
	if author == "" {
		t.Skip("the recorded author isn't one of the fixture's clients")
	}
	if _, _ = ClassifyVersions(f, vf, c, author, fixtureKey); f.Class != ClassBound {
		t.Fatalf("the right author: %s", f.Class)
	}
	f = &Frame{Tables: raw[0].Tables, Changes: raw[0].Changes}
	if _, _ = ClassifyVersions(f, vf, c, "ClientApplication/someone-else", fixtureKey); f.Class != ClassVersionTagMismatch || f.ActionID != act {
		t.Fatalf("another author: %s %s", f.Class, f.ActionID)
	}
	f = &Frame{Tables: raw[0].Tables, Changes: raw[0].Changes}
	if _, _ = ClassifyVersions(f, vf, c, "", fixtureKey); f.Class != ClassBound || !f.HasFlag("tag_unchecked") {
		t.Fatalf("no author configured: %s %v", f.Class, f.Flags)
	}
}

// TestHold is ADR-108 §5 (B6, B7): a frame no action names waits while an action is executing, at
// most the grace; a named frame, or one read while nothing executes, is decided at once.
func TestHold(t *testing.T) {
	now := time.Now()
	g := 5 * time.Second
	for _, c := range []struct {
		claimed, busy bool
		age           time.Duration
		want          bool
	}{
		{false, true, 0, true},
		{false, true, 4 * time.Second, true},
		{false, true, 5 * time.Second, false},
		{false, false, 0, false},
		{true, true, 0, false},
	} {
		if got := hold(c.claimed, c.busy, now.Add(-c.age), now, g); got != c.want {
			t.Errorf("claimed %v busy %v age %v: %v", c.claimed, c.busy, c.age, got)
		}
	}
}
