package recordexport

import (
	"bufio"
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/deedglass/deedglass/internal/deed/archive"
	"github.com/deedglass/deedglass/internal/fhirexport"
	"github.com/google/jsonschema-go/jsonschema"
)

var update = flag.Bool("update", false, "rewrite testdata/golden")

// The release bundle every format is exported from: 12 records of one org (actions allowed,
// approved and denied, an admin stop, two amendments), sealed by v0.4.0's own code (TY-11).
const releaseBundle = "../../testdata/release-bundles/0.4.0/bundle.tar.gz"

var opt = Options{Producer: "0.0.0-test"}

// bundleRecords reads the release bundle's records, checking its manifest.
func bundleRecords(t *testing.T) [][]byte {
	t.Helper()
	f, err := os.Open(releaseBundle)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	r, err := archive.Open(f, archive.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	var out [][]byte
	sc := bufio.NewScanner(r.Deeds())
	for sc.Scan() {
		out = append(out, append([]byte(nil), sc.Bytes()...))
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := r.Finish(); err != nil {
		t.Fatal(err)
	}
	if len(out) != 12 {
		t.Fatalf("the release bundle has %d records, want 12", len(out))
	}
	return out
}

func lines(t *testing.T, path string) [][]byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var out [][]byte
	for _, l := range bytes.Split(bytes.TrimSpace(b), []byte("\n")) {
		out = append(out, l)
	}
	return out
}

func export(t *testing.T, format string, recs [][]byte) ([]byte, Stats) {
	t.Helper()
	var buf bytes.Buffer
	w, err := NewWriter(&buf, format, opt)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range recs {
		if err := w.Write(r); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes(), w.Stats()
}

func golden(t *testing.T, name string, got []byte) {
	t.Helper()
	path := filepath.Join("testdata", "golden", name)
	if *update {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run go test ./internal/recordexport -update)", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("%s drifted; review, then run go test ./internal/recordexport -update", path)
	}
}

// TestGoldens pins every format's output for the release bundle (and, for FHIR, for FHIR
// connector records), and the counts.
func TestGoldens(t *testing.T) {
	recs := bundleRecords(t)
	for _, c := range []struct {
		format string
		recs   [][]byte
		file   string
		want   Stats
	}{
		{FormatOTel, recs, "release-0.4.0.otlp.jsonl", Stats{Records: 12, Written: 11, Skipped: 1}},
		{FormatOCSF, recs, "release-0.4.0.ocsf.ndjson", Stats{Records: 12, Written: 11, Skipped: 1}},
		{FormatPROV, recs, "release-0.4.0.prov.json", Stats{Records: 12, Written: 12}},
		{FormatFHIR, recs, "release-0.4.0.fhir.ndjson", Stats{Records: 12, Skipped: 12}},
		{FormatFHIR, lines(t, "testdata/fhir-records.jsonl"), "fhir-records.fhir.ndjson", Stats{Records: 3, Written: 4}},
	} {
		got, st := export(t, c.format, c.recs)
		if st != c.want {
			t.Errorf("%s: stats %+v, want %+v", c.file, st, c.want)
		}
		golden(t, c.file, got)
		again, _ := export(t, c.format, c.recs)
		if !bytes.Equal(got, again) {
			t.Errorf("%s: two exports of the same records differ", c.file)
		}
	}
}

// TestFHIRIsTheExistingExporter: --format fhir is internal/fhirexport's AuditEvent then
// Provenance for each record, nothing else.
func TestFHIRIsTheExistingExporter(t *testing.T) {
	recs := lines(t, "testdata/fhir-records.jsonl")
	got, _ := export(t, FormatFHIR, recs)
	var want bytes.Buffer
	for _, r := range recs {
		for _, conv := range []func(fhirexport.Record, fhirexport.Options) (map[string]any, bool, error){fhirexport.AuditEvent, fhirexport.Provenance} {
			res, ok, err := conv(fhirexport.Record{Envelope: r}, fhirexport.Options{})
			if err != nil {
				t.Fatal(err)
			}
			if ok {
				b, _ := json.Marshal(res)
				want.Write(append(b, '\n'))
			}
		}
	}
	if !bytes.Equal(got, want.Bytes()) {
		t.Error("the fhir format isn't the fhirexport output")
	}
	if !bytes.Contains(got, []byte(`"resourceType":"AuditEvent"`)) || !bytes.Contains(got, []byte(`"resourceType":"Provenance"`)) {
		t.Error("expected both AuditEvent and Provenance in the output")
	}
}

// --- schemas ---

func loadSchema(t *testing.T, name string) *jsonschema.Resolved {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "schemas", name))
	if err != nil {
		t.Fatal(err)
	}
	var s jsonschema.Schema
	if err := json.Unmarshal(b, &s); err != nil {
		t.Fatal(err)
	}
	rs, err := s.Resolve(nil)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return rs
}

func decode(t *testing.T, b []byte) map[string]any {
	t.Helper()
	var v map[string]any
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatal(err)
	}
	return v
}

func jsonLines(t *testing.T, b []byte) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, l := range bytes.Split(bytes.TrimSpace(b), []byte("\n")) {
		if len(l) > 0 {
			out = append(out, decode(t, l))
		}
	}
	return out
}

// --- OCSF ---

type ocsfRules struct {
	Version        string                       `json:"ocsf_version"`
	ClassUID       int                          `json:"class_uid"`
	ClassName      string                       `json:"class_name"`
	CategoryUID    int                          `json:"category_uid"`
	CategoryName   string                       `json:"category_name"`
	ClassProfiles  []string                     `json:"class_profiles"`
	EnumCaptions   map[string]map[string]string `json:"enum_captions"`
	SiblingStrings map[string]string            `json:"sibling_strings"`
}

// checkOCSF returns every way ev breaks the pinned OCSF 1.9.0 schema and its class rules.
func checkOCSF(rs *jsonschema.Resolved, r ocsfRules, ev map[string]any) []string {
	var bad []string
	if err := rs.Validate(ev); err != nil {
		bad = append(bad, "schema: "+err.Error())
	}
	num := func(k string) int { f, _ := ev[k].(float64); return int(f) }
	if num("class_uid") != r.ClassUID || ev["class_name"] != r.ClassName || num("category_uid") != r.CategoryUID || ev["category_name"] != r.CategoryName {
		bad = append(bad, "class or category")
	}
	if num("type_uid") != num("class_uid")*100+num("activity_id") {
		bad = append(bad, "type_uid isn't class_uid*100+activity_id")
	}
	if ev["type_name"] != fmt.Sprintf("API Activity: %v", ev["activity_name"]) {
		bad = append(bad, "type_name")
	}
	for id, sib := range r.SiblingStrings {
		v, ok := ev[id]
		if !ok {
			continue
		}
		n := strconv.Itoa(int(v.(float64)))
		if n == "99" {
			if s, _ := ev[sib].(string); s == "" {
				bad = append(bad, id+" 99 without its own label in "+sib)
			}
			continue
		}
		if ev[sib] != r.EnumCaptions[id][n] {
			bad = append(bad, fmt.Sprintf("%s %s: %s is %v, want %q", id, n, sib, ev[sib], r.EnumCaptions[id][n]))
		}
	}
	if _, ok := ev["count"]; !ok {
		for _, k := range []string{"start_time", "end_time", "duration"} {
			if _, has := ev[k]; has {
				bad = append(bad, k+" without count > 1")
			}
		}
	}
	md, _ := ev["metadata"].(map[string]any)
	if md["version"] != r.Version {
		bad = append(bad, "metadata.version isn't "+r.Version)
	}
	profiles, _ := md["profiles"].([]any)
	for _, p := range profiles {
		if !slices.Contains(r.ClassProfiles, p.(string)) {
			bad = append(bad, fmt.Sprintf("profile %v isn't one of API Activity's", p))
		}
	}
	uses := map[string]string{"ai_agent": "ai_operation", "delegation": "ai_operation", "policy": "security_control", "action_id": "security_control", "disposition_id": "security_control", "is_alert": "security_control"}
	for k, p := range uses {
		if _, ok := ev[k]; ok && !slices.Contains(profiles, any(p)) {
			bad = append(bad, k+" needs profile "+p)
		}
	}
	if a, ok := ev["ai_agent"].(map[string]any); ok {
		n := strconv.Itoa(int(a["type_id"].(float64)))
		if a["type"] != nil && a["type"] != r.EnumCaptions["ai_agent.type_id"][n] {
			bad = append(bad, "ai_agent.type")
		}
	}
	if act, ok := ev["actor"].(map[string]any); ok {
		if u, ok := act["user"].(map[string]any); ok && u["type"] != nil {
			n := strconv.Itoa(int(u["type_id"].(float64)))
			if u["type"] != r.EnumCaptions["user.type_id"][n] {
				bad = append(bad, "actor.user.type")
			}
		}
	}
	return bad
}

func loadOCSF(t *testing.T) (*jsonschema.Resolved, ocsfRules) {
	rs := loadSchema(t, "ocsf-1.9.0-api_activity.schema.json")
	var r ocsfRules
	b, err := os.ReadFile("testdata/schemas/ocsf-1.9.0-api_activity.rules.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &r); err != nil {
		t.Fatal(err)
	}
	if r.Version != OCSFVersion {
		t.Fatalf("rules pin OCSF %s, the export writes %s", r.Version, OCSFVersion)
	}
	return rs, r
}

func TestOCSFValidates(t *testing.T) {
	rs, r := loadOCSF(t)
	out, _ := export(t, FormatOCSF, bundleRecords(t))
	evs := jsonLines(t, out)
	alerts := 0
	for i, ev := range evs {
		for _, b := range checkOCSF(rs, r, ev) {
			t.Errorf("event %d: %s", i+1, b)
		}
		if ev["is_alert"] == true {
			alerts++
		}
	}
	if alerts != 2 {
		t.Errorf("%d alerts, want 2 (the two Doesn't-match amendments)", alerts)
	}
}

// TestOCSFCheckCatches shows the checker fails on what it is there to catch.
func TestOCSFCheckCatches(t *testing.T) {
	rs, r := loadOCSF(t)
	out, _ := export(t, FormatOCSF, bundleRecords(t)[:1])
	for name, edit := range map[string]func(map[string]any){
		"invented attribute":   func(ev map[string]any) { ev["verdict"] = "VERIFIED" },
		"missing required":     func(ev map[string]any) { delete(ev, "src_endpoint") },
		"wrong caption":        func(ev map[string]any) { ev["status"] = "OK" },
		"type_uid":             func(ev map[string]any) { ev["type_uid"] = 600301.0 },
		"ai_agent attribute":   func(ev map[string]any) { ev["ai_agent"].(map[string]any)["model"] = "x" },
		"empty ai_agent":       func(ev map[string]any) { ev["ai_agent"] = map[string]any{"type_id": 0.0} },
		"metadata version":     func(ev map[string]any) { ev["metadata"].(map[string]any)["version"] = "1.8.0" },
		"profile not declared": func(ev map[string]any) { ev["metadata"].(map[string]any)["profiles"] = []any{"security_control"} },
		"bad enum":             func(ev map[string]any) { ev["severity_id"] = 7.0 },
		"aggregate time":       func(ev map[string]any) { ev["start_time"] = 1.0 },
	} {
		ev := decode(t, bytes.TrimSpace(out))
		edit(ev)
		if len(checkOCSF(rs, r, ev)) == 0 {
			t.Errorf("%s: the checker passed a broken event", name)
		}
	}
}

// --- PROV-JSON ---

var provRefKeys = []string{"prov:entity", "prov:activity", "prov:agent", "prov:plan", "prov:delegate", "prov:responsible",
	"prov:influencee", "prov:influencer", "prov:generatedEntity", "prov:usedEntity", "prov:informant", "prov:informed"}

func checkPROV(rs *jsonschema.Resolved, doc map[string]any) []string {
	var bad []string
	if err := rs.Validate(doc); err != nil {
		bad = append(bad, "schema: "+err.Error())
	}
	defined := map[string]bool{}
	for _, sec := range []string{"entity", "activity", "agent"} {
		m, _ := doc[sec].(map[string]any)
		for id, v := range m {
			defined[id] = true
			for _, k := range []string{"prov:startTime", "prov:endTime"} {
				if s, ok := v.(map[string]any)[k].(string); ok {
					if _, err := time.Parse(time.RFC3339Nano, s); err != nil {
						bad = append(bad, id+": "+k+" isn't a date-time")
					}
				}
			}
		}
	}
	prefixes, _ := doc["prefix"].(map[string]any)
	qname := regexp.MustCompile(`^([A-Za-z0-9_-]+):([A-Za-z0-9_.:-]|%[0-9A-F]{2})+$`)
	for id := range defined {
		m := qname.FindStringSubmatch(id)
		if m == nil || (m[1] != "prov" && prefixes[m[1]] == nil) {
			bad = append(bad, "id "+id+" isn't a qualified name with a declared prefix")
		}
	}
	for sec, v := range doc {
		if sec == "prefix" || sec == "entity" || sec == "activity" || sec == "agent" {
			continue
		}
		for id, r := range v.(map[string]any) {
			rec := r.(map[string]any)
			for _, k := range provRefKeys {
				if ref, ok := rec[k].(string); ok && !defined[ref] {
					bad = append(bad, fmt.Sprintf("%s %s: %s names %s, which isn't defined", sec, id, k, ref))
				}
			}
			if s, ok := rec["prov:time"].(string); ok {
				if _, err := time.Parse(time.RFC3339Nano, s); err != nil {
					bad = append(bad, id+": prov:time isn't a date-time")
				}
			}
		}
	}
	return bad
}

func TestPROVValidates(t *testing.T) {
	rs := loadSchema(t, "prov-json-2013-04-24.schema.json")
	out, _ := export(t, FormatPROV, bundleRecords(t))
	doc := decode(t, out)
	for _, b := range checkPROV(rs, doc) {
		t.Error(b)
	}
	// The amended records exist, so each revision names two records.
	if n := len(doc["wasDerivedFrom"].(map[string]any)); n != 2 {
		t.Errorf("%d revisions, want 2", n)
	}
	if n := len(doc["actedOnBehalfOf"].(map[string]any)); n == 0 {
		t.Error("no actedOnBehalfOf for records with on_behalf_of")
	}
}

func TestPROVCheckCatches(t *testing.T) {
	rs := loadSchema(t, "prov-json-2013-04-24.schema.json")
	out, _ := export(t, FormatPROV, bundleRecords(t))
	for name, edit := range map[string]func(map[string]any){
		"unknown section": func(d map[string]any) { d["wasCheckedBy"] = map[string]any{} },
		"missing required": func(d map[string]any) {
			first(d, "used")["prov:activity"] = nil
			delete(first(d, "used"), "prov:activity")
		},
		"dangling ref":      func(d map[string]any) { first(d, "wasAttributedTo")["prov:agent"] = "dg:nobody" },
		"bad prefix":        func(d map[string]any) { d["prefix"].(map[string]any)["d g"] = "urn:x:" },
		"bad literal":       func(d map[string]any) { first(d, "entity")["dg:x"] = map[string]any{"type": "xsd:string"} },
		"bad time":          func(d map[string]any) { first(d, "wasGeneratedBy")["prov:time"] = "yesterday" },
		"undeclared prefix": func(d map[string]any) { d["entity"].(map[string]any)["zz:x"] = map[string]any{} },
	} {
		doc := decode(t, out)
		edit(doc)
		if len(checkPROV(rs, doc)) == 0 {
			t.Errorf("%s: the checker passed a broken document", name)
		}
	}
}

func first(d map[string]any, sec string) map[string]any {
	m := d[sec].(map[string]any)
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return m[keys[0]].(map[string]any)
}

// --- OTel ---

type semconv struct {
	Version     string            `json:"semconv_version"`
	Attributes  []string          `json:"attributes"`
	Deprecated  map[string]string `json:"deprecated"`
	Stable      []string          `json:"stable_attributes"`
	Ops         []string          `json:"operation_names"`
	ToolTypes   []string          `json:"tool_types"`
	StringAttrs []string          `json:"string_attributes"`
	ExecuteTool struct {
		Required []string `json:"required"`
	} `json:"execute_tool"`
}

func loadSemconv(t *testing.T) (*jsonschema.Resolved, semconv) {
	rs := loadSchema(t, "otlp-1.11.1-trace.schema.json")
	var sc semconv
	b, err := os.ReadFile("testdata/schemas/otel-semconv-1.41.0-genai.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &sc); err != nil {
		t.Fatal(err)
	}
	if sc.Version != OTelSemconvVersion || OTelSchemaURL != "https://opentelemetry.io/schemas/"+sc.Version {
		t.Fatalf("the registry pins %s, the export writes %s (%s)", sc.Version, OTelSemconvVersion, OTelSchemaURL)
	}
	return rs, sc
}

// checkOTel returns every way one OTLP/JSON line breaks the OTLP schema or the pinned GenAI
// conventions. spans collects span ids by trace, for the parent check.
func checkOTel(rs *jsonschema.Resolved, sc semconv, req map[string]any, spans map[string]bool) []string {
	var bad []string
	if err := rs.Validate(req); err != nil {
		bad = append(bad, "otlp: "+err.Error())
		return bad
	}
	for _, r := range req["resourceSpans"].([]any) {
		rsp := r.(map[string]any)
		if rsp["schemaUrl"] != OTelSchemaURL {
			bad = append(bad, "resource schemaUrl")
		}
		for _, s := range rsp["scopeSpans"].([]any) {
			ss := s.(map[string]any)
			if ss["schemaUrl"] != OTelSchemaURL {
				bad = append(bad, "scope schemaUrl")
			}
			for _, sp := range ss["spans"].([]any) {
				bad = append(bad, checkSpan(sc, sp.(map[string]any), spans)...)
			}
		}
	}
	return bad
}

func checkSpan(sc semconv, sp map[string]any, spans map[string]bool) []string {
	var bad []string
	attrs := map[string]map[string]any{}
	for _, a := range sp["attributes"].([]any) {
		kv := a.(map[string]any)
		k := kv["key"].(string)
		if _, dup := attrs[k]; dup {
			bad = append(bad, "duplicate attribute "+k)
		}
		attrs[k] = kv["value"].(map[string]any)
		switch {
		case strings.HasPrefix(k, "deedglass."):
		case slices.Contains(sc.Attributes, k) || slices.Contains(sc.Stable, k):
			if why, dep := sc.Deprecated[k]; dep {
				bad = append(bad, k+" is deprecated: "+why)
			}
			if slices.Contains(sc.StringAttrs, k) && attrs[k]["stringValue"] == nil {
				bad = append(bad, k+" must be a string")
			}
		default:
			bad = append(bad, k+" is neither a semantic-conventions attribute nor under deedglass.")
		}
	}
	sv := func(k string) string { s, _ := attrs[k]["stringValue"].(string); return s }
	name := sp["name"].(string)
	if opName, ok := attrs["gen_ai.operation.name"]; ok {
		if !slices.Contains(sc.Ops, opName["stringValue"].(string)) {
			bad = append(bad, "gen_ai.operation.name isn't a well-known value")
		}
		for _, k := range sc.ExecuteTool.Required {
			if _, ok := attrs[k]; !ok {
				bad = append(bad, "execute_tool span without "+k)
			}
		}
		if name != "execute_tool "+sv("gen_ai.tool.name") {
			bad = append(bad, "span name isn't execute_tool {gen_ai.tool.name}")
		}
		if sp["kind"].(float64) != spanKindInternal {
			bad = append(bad, "execute_tool span isn't INTERNAL")
		}
		st, _ := sp["status"].(map[string]any)
		isErr := st != nil && st["code"].(float64) == statusError
		if _, hasET := attrs["error.type"]; isErr != hasET {
			bad = append(bad, "error.type must be set exactly when the span ended in an error")
		}
	} else if p, _ := sp["parentSpanId"].(string); p == "" || !spans[sp["traceId"].(string)+"/"+p] {
		bad = append(bad, "a non-execute_tool span must be a child of a span already written")
	}
	if tt := sv("gen_ai.tool.type"); tt != "" && !slices.Contains(sc.ToolTypes, tt) {
		bad = append(bad, "gen_ai.tool.type "+tt)
	}
	s, _ := strconv.ParseUint(sp["startTimeUnixNano"].(string), 10, 64)
	e, _ := strconv.ParseUint(sp["endTimeUnixNano"].(string), 10, 64)
	if e < s {
		bad = append(bad, "end before start")
	}
	spans[sp["traceId"].(string)+"/"+sp["spanId"].(string)] = true
	return bad
}

func TestOTelValidates(t *testing.T) {
	rs, sc := loadSemconv(t)
	out, _ := export(t, FormatOTel, bundleRecords(t))
	spans := map[string]bool{}
	reqs := jsonLines(t, out)
	if len(reqs) != 11 {
		t.Fatalf("%d lines, want 11", len(reqs))
	}
	for i, req := range reqs {
		for _, b := range checkOTel(rs, sc, req, spans) {
			t.Errorf("line %d: %s", i+1, b)
		}
	}
}

func TestOTelCheckCatches(t *testing.T) {
	rs, sc := loadSemconv(t)
	out, _ := export(t, FormatOTel, bundleRecords(t))
	l := jsonLines(t, out)
	span := func(req map[string]any) map[string]any {
		return req["resourceSpans"].([]any)[0].(map[string]any)["scopeSpans"].([]any)[0].(map[string]any)["spans"].([]any)[0].(map[string]any)
	}
	addAttr := func(req map[string]any, k string) {
		sp := span(req)
		sp["attributes"] = append(sp["attributes"].([]any), map[string]any{"key": k, "value": map[string]any{"stringValue": "x"}})
	}
	for name, c := range map[string]struct {
		line int
		edit func(map[string]any)
	}{
		"invented attribute": {0, func(r map[string]any) { addAttr(r, "gen_ai.tool.verdict") }},
		"deprecated":         {0, func(r map[string]any) { addAttr(r, "gen_ai.system") }},
		"no namespace":       {0, func(r map[string]any) { addAttr(r, "verdict") }},
		"base64 trace id":    {0, func(r map[string]any) { span(r)["traceId"] = "AAECAwQFBgcICQoLDA0ODw==" }},
		"enum as string":     {0, func(r map[string]any) { span(r)["kind"] = "SPAN_KIND_INTERNAL" }},
		"bad name":           {0, func(r map[string]any) { span(r)["name"] = "postgres.execute" }},
		"orphan child":       {4, func(r map[string]any) { span(r)["parentSpanId"] = "0102030405060708" }},
		"error without type": {2, func(r map[string]any) {
			sp := span(r)
			var keep []any
			for _, a := range sp["attributes"].([]any) {
				if a.(map[string]any)["key"] != "error.type" {
					keep = append(keep, a)
				}
			}
			sp["attributes"] = keep
		}},
	} {
		spans := map[string]bool{}
		for i := 0; i < c.line; i++ {
			checkOTel(rs, sc, l[i], spans)
		}
		req := decode(t, bytes.Split(bytes.TrimSpace(out), []byte("\n"))[c.line])
		c.edit(req)
		if len(checkOTel(rs, sc, req, spans)) == 0 {
			t.Errorf("%s: the checker passed a broken span", name)
		}
	}
}

// TestOTelIDs: an amendment lands in its action's trace, under the amended record's span.
func TestOTelIDs(t *testing.T) {
	out, _ := export(t, FormatOTel, bundleRecords(t))
	byRecord := map[string]map[string]any{}
	var amendments []map[string]any
	for _, req := range jsonLines(t, out) {
		sp := req["resourceSpans"].([]any)[0].(map[string]any)["scopeSpans"].([]any)[0].(map[string]any)["spans"].([]any)[0].(map[string]any)
		byRecord[sp["spanId"].(string)] = sp
		if _, ok := sp["parentSpanId"]; ok {
			amendments = append(amendments, sp)
		}
	}
	if len(amendments) != 2 {
		t.Fatalf("%d amendment spans, want 2", len(amendments))
	}
	for _, a := range amendments {
		p := byRecord[a["parentSpanId"].(string)]
		if p == nil || p["traceId"] != a["traceId"] {
			t.Errorf("amendment span %s isn't in its action's trace", a["spanId"])
		}
	}
}

func TestRefusals(t *testing.T) {
	if _, err := NewWriter(&bytes.Buffer{}, "csv", opt); err != ErrFormat {
		t.Errorf("csv: %v", err)
	}
	for _, f := range Formats {
		for name, raw := range map[string]string{
			"not json":   "{",
			"no deed id": `{"body":{"kind":"action","org":"org_x","seq":1,"issued_at":"2026-10-02T09:00:00.000Z"}}`,
			"bad kind":   `{"body":{"kind":"other","deed_id":"deed_x","org":"org_x","seq":1,"issued_at":"2026-10-02T09:00:00.000Z"}}`,
			"bad time":   `{"body":{"kind":"admin","deed_id":"deed_x","org":"org_x","seq":1,"issued_at":"yesterday","admin":{}}}`,
			"no action":  `{"body":{"kind":"action","deed_id":"deed_x","org":"org_x","seq":1,"issued_at":"2026-10-02T09:00:00.000Z"}}`,
		} {
			w, _ := NewWriter(&bytes.Buffer{}, f, opt)
			if err := w.Write([]byte(raw)); err == nil {
				t.Errorf("%s, %s: accepted", f, name)
			}
		}
	}
}

func TestEscapeLocal(t *testing.T) {
	for in, want := range map[string]string{"usr_ref:priya": "usr_ref%3Apriya", "a/b+c=d~e@f": "a%2Fb%2Bc%3Dd%7Ee%40f", "deed_01m3": "deed_01m3"} {
		if got := escapeLocal(in); got != want {
			t.Errorf("escapeLocal(%q) = %q, want %q", in, got, want)
		}
	}
}
