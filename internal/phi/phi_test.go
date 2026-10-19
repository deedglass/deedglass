package phi

import (
	"encoding/json"
	"strings"
	"testing"
)

// planted values: what a FHIR request or result might carry about a patient. None may survive a
// projection unless it sits at a kept identifier path.
var planted = []string{"Jane Q Synthetic", "MRN-0042-SYN", "1975-03-09", "72.5", "Heart rate is high", "ABHA 91-1234-5678-9012"}

func mustNotContain(t *testing.T, what string, b []byte) {
	t.Helper()
	for _, p := range planted {
		if strings.Contains(string(b), p) {
			t.Errorf("%s keeps %q: %s", what, p, b)
		}
	}
}

func TestMinimizeArgs(t *testing.T) {
	raw := json.RawMessage(`{"id":"1001","resource_type":"Observation","status":"final",
	  "subject":{"reference":"Patient/MRN-0042-SYN","display":"Jane Q Synthetic"},
	  "value_quantity":{"value":72.5,"unit":"beats/minute"},"note":"Heart rate is high"}`)
	out := MinimizeArgs(raw, []string{"id"})
	mustNotContain(t, "args", out)
	var m map[string]string
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	if m["id"] != "1001" {
		t.Fatalf("the id arg isn't kept: %s", out)
	}
	for _, k := range []string{"resource_type", "status", "subject", "value_quantity", "note"} {
		if !strings.HasPrefix(m[k], "sha256:") || len(m[k]) != 71 {
			t.Errorf("%s = %q, want a digest", k, m[k])
		}
	}
	// The same value always gives the same digest (so two stored rows can be compared).
	if again := MinimizeArgs(raw, []string{"id"}); string(again) != string(out) {
		t.Fatalf("not deterministic:\n%s\n%s", out, again)
	}
	// An id arg that isn't a short identifier is digested too.
	out = MinimizeArgs(json.RawMessage(`{"id":"Jane Q Synthetic"}`), []string{"id"})
	mustNotContain(t, "free-text id", out)
	// Not an object, not JSON, empty.
	for _, in := range []string{`["Jane Q Synthetic"]`, `Jane Q Synthetic`, `"MRN-0042-SYN"`} {
		out := MinimizeArgs(json.RawMessage(in), []string{"id"})
		mustNotContain(t, in, out)
		var s string
		if json.Unmarshal(out, &s) != nil || !strings.HasPrefix(s, "sha256:") {
			t.Errorf("%s → %s, want one digest string", in, out)
		}
	}
	if out := MinimizeArgs(nil, nil); len(out) != 0 {
		t.Errorf("empty → %s", out)
	}
}

func TestMinimizeResult(t *testing.T) {
	raw := json.RawMessage(`{"status":201,"headers":{"content-type":"application/fhir+json","etag":"W/\"1\"",
	  "location":"http://fhir.example/fhir/Observation/1001/_history/1","x-patient":"MRN-0042-SYN"},
	  "body":{"resourceType":"Observation","id":"1001","subject":{"reference":"Patient/MRN-0042-SYN"},"valueQuantity":{"value":72.5}}}`)
	keep := Paths{"status", "headers.content-type", "headers.etag", "headers.location"}
	out := Minimize(raw, keep)
	mustNotContain(t, "result", out)
	var r struct {
		Status  int               `json:"status"`
		Headers map[string]string `json:"headers"`
		Body    string            `json:"body"`
	}
	if err := json.Unmarshal(out, &r); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	if r.Status != 201 || r.Headers["etag"] != `W/"1"` || !strings.HasSuffix(r.Headers["location"], "/Observation/1001/_history/1") {
		t.Fatalf("kept paths lost: %s", out)
	}
	// A subtree with no kept path is one digest, keys included.
	if !strings.HasPrefix(r.Body, "sha256:") || strings.Contains(string(out), "valueQuantity") {
		t.Fatalf("body: %s", out)
	}
	if !strings.HasPrefix(r.Headers["x-patient"], "sha256:") {
		t.Fatalf("an unkept header: %s", out)
	}
	// No kept paths at all: the whole value is one digest.
	var s string
	if json.Unmarshal(Minimize(raw, nil), &s) != nil || !strings.HasPrefix(s, "sha256:") {
		t.Fatalf("nothing kept: %s", Minimize(raw, nil))
	}
}

func TestMinimizeWitnessDetail(t *testing.T) {
	raw := json.RawMessage(`{"object":"witness_detail","version":1,
	  "claim":{"level":"L1","verdict":"MISMATCH","method":"readback.rest.get_observation","required_level":"L1"},
	  "summary":"fhir.update_observation reported success, but fhir.get_observation says valueQuantity.value is 72.5, not 80.",
	  "levels":{"L0":{"verdict":"VERIFIED","method":"reported"},
	    "L1":{"verdict":"MISMATCH","method":"readback.rest.get_observation",
	      "readback":{"tool":"fhir.get_observation","target":"Observation MRN-0042-SYN","request_digest":"sha256:aa","status":200,
	        "response_digest":"sha256:bb","attempts":1,"latency_ms":12}},"L2":null},
	  "reasons":[{"code":"postcondition_failed","class":"violation","level":"L1","check":"readback.expect",
	    "message":"… valueQuantity.value is 72.5 …","diff":[{"path":"body.valueQuantity.value","expected":80,"observed":72.5}]}],
	  "witnessed_at":"2026-10-19T10:00:00.000Z"}`)
	out := Minimize(raw, WitnessDetail)
	mustNotContain(t, "witness detail", out)
	for _, want := range []string{`"verdict":"MISMATCH"`, `"method":"readback.rest.get_observation"`, `"code":"postcondition_failed"`,
		`"path":"body.valueQuantity.value"`, `"response_digest":"sha256:bb"`, `"attempts":1`, `"L2":null`, `"witnessed_at":"2026-10-19T10:00:00.000Z"`} {
		if !strings.Contains(string(out), want) {
			t.Errorf("lost %s: %s", want, out)
		}
	}
	for _, gone := range []string{`"expected":80`, `"target":"Observation`} {
		if strings.Contains(string(out), gone) {
			t.Errorf("kept %s: %s", gone, out)
		}
	}
}

func TestIdentifier(t *testing.T) {
	for v, want := range map[string]bool{`"1001"`: true, `"Patient/abc"`: true, `""`: true, `null`: true, `true`: true, `42`: true,
		`72.5`: false, `1e3`: false, `"Jane Doe"`: false, `"a\tb"`: false, `{"a":1}`: false} {
		var x any
		d := json.NewDecoder(strings.NewReader(v))
		d.UseNumber()
		if err := d.Decode(&x); err != nil {
			t.Fatal(err)
		}
		if got := identifier(x); got != want {
			t.Errorf("identifier(%s) = %v, want %v", v, got, want)
		}
	}
	if identifier(strings.Repeat("a", MaxKept+1)) {
		t.Error("a string over MaxKept is kept")
	}
}

func TestText(t *testing.T) {
	if d := Text("Heart rate is high"); !strings.HasPrefix(d, "sha256:") || strings.Contains(d, "Heart") {
		t.Fatal(d)
	}
}
