package httpx

import (
	"encoding/json/v2"
	"fmt"
	"slices"
	"sort"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/deedglass/deedglass/internal/buildinfo"
)

// The OpenAPI document (api/openapi.yaml) is written by hand. CheckSpec keeps it honest: the tests
// here run it on the system routes, and cmd/deedglass runs it on everything `serve` mounts, so a
// route and its documentation can't drift apart.

type specDoc struct {
	OpenAPI string                           `yaml:"openapi"`
	Info    struct{ Version string }         `yaml:"info"`
	Paths   map[string]map[string]specOp     `yaml:"paths"`
	Comps   struct{ Schemas map[string]any } `yaml:"components"`
}

type specOp struct {
	OperationID string         `yaml:"operationId"`
	Summary     string         `yaml:"summary"`
	Tags        []string       `yaml:"tags"`
	Auth        string         `yaml:"x-deedglass-auth"`
	Since       string         `yaml:"x-deedglass-since"`
	Responses   map[string]any `yaml:"responses"`
}

var specMethods = []string{"get", "head", "post", "put", "patch", "delete"}

// CheckSpec compares the document with the mounted routes (test-only routes are skipped) and the
// error catalog. It returns every problem found: checks 1–6 of S03 §3.10.
func CheckSpec(spec []byte, routes []Route) []error {
	var errs []error
	fail := func(format string, args ...any) { errs = append(errs, fmt.Errorf(format, args...)) }
	var d specDoc
	if err := yaml.Unmarshal(spec, &d); err != nil {
		return []error{fmt.Errorf("api/openapi.yaml doesn't parse: %w", err)}
	}
	if d.OpenAPI != "3.1.1" {
		fail("openapi is %q, want 3.1.1 (ADR-023)", d.OpenAPI)
	}
	// 6. info.version is the latest API version.
	if d.Info.Version != buildinfo.LatestAPIVersion {
		fail("info.version is %q, want %s", d.Info.Version, buildinfo.LatestAPIVersion)
	}
	mounted := map[string]Route{}
	for _, r := range routes {
		if r.Extension == "test" {
			continue
		}
		mounted[strings.ToLower(r.Method)+" "+r.Pattern] = r
	}
	// 1 and 3. Every mounted route is documented with its operationId and auth.
	for key, r := range mounted {
		m, p, _ := strings.Cut(key, " ")
		op, ok := d.Paths[p][m]
		if !ok {
			fail("%s %s is mounted but not in api/openapi.yaml", r.Method, r.Pattern)
			continue
		}
		if op.OperationID != r.OperationID {
			fail("%s %s: operationId %q in the spec, %q in the route", r.Method, r.Pattern, op.OperationID, r.OperationID)
		}
		if op.Auth != r.Auth.String() {
			fail("%s %s: x-deedglass-auth %q in the spec, %s in the route", r.Method, r.Pattern, op.Auth, r.Auth)
		}
		if op.Since != r.Since {
			fail("%s %s: x-deedglass-since %q in the spec, %q in the route", r.Method, r.Pattern, op.Since, r.Since)
		}
	}
	// 2 and 5. No phantom operations; every operation is complete and has the default error.
	ids := map[string]string{}
	for p, item := range d.Paths {
		for m, op := range item {
			if !slices.Contains(specMethods, m) {
				continue // parameters, summary, …
			}
			where := strings.ToUpper(m) + " " + p
			if _, ok := mounted[m+" "+p]; !ok {
				fail("%s is in api/openapi.yaml but not mounted (planned routes stay out until they are)", where)
			}
			if op.OperationID == "" || op.Summary == "" || len(op.Tags) == 0 || op.Auth == "" || op.Since == "" {
				fail("%s: needs operationId, summary, tags, x-deedglass-auth and x-deedglass-since", where)
			}
			if prev, dup := ids[op.OperationID]; dup {
				fail("operationId %q is used by %s and %s", op.OperationID, prev, where)
			}
			ids[op.OperationID] = where
			def, ok := op.Responses["default"].(map[string]any)
			if !ok || def["$ref"] != "#/components/responses/Error" {
				fail("%s: the default response must be $ref #/components/responses/Error", where)
			}
		}
	}
	// 4. The code enum is the catalog, in order.
	var enum []string
	if env, ok := d.Comps.Schemas["ErrorEnvelope"]; ok {
		enum = stringsAt(env, "properties", "error", "properties", "code", "enum")
	}
	var codes []string
	for _, c := range catalog {
		codes = append(codes, string(c.Code))
	}
	if !slices.Equal(enum, codes) {
		fail("ErrorEnvelope's code enum differs from the catalog in errors.go:\n spec:    %v\n catalog: %v", enum, codes)
	}
	sort.Slice(errs, func(i, j int) bool { return errs[i].Error() < errs[j].Error() })
	return errs
}

// CheckRequired reports every property the schema marks required that body (JSON) lacks,
// following nested object schemas and local $refs. It is check 7 of S03 §3.10.
func CheckRequired(spec []byte, schema string, body []byte) []error {
	var d specDoc
	if err := yaml.Unmarshal(spec, &d); err != nil {
		return []error{err}
	}
	var v any
	if err := json.Unmarshal(body, &v); err != nil {
		return []error{fmt.Errorf("body isn't JSON: %w", err)}
	}
	var errs []error
	var walk func(s any, v any, path string)
	resolve := func(s any) any {
		for i := 0; i < 8; i++ {
			m, ok := s.(map[string]any)
			if !ok {
				return s
			}
			ref, ok := m["$ref"].(string)
			if !ok {
				return s
			}
			s = d.Comps.Schemas[strings.TrimPrefix(ref, "#/components/schemas/")]
		}
		return s
	}
	walk = func(s any, v any, path string) {
		m, ok := resolve(s).(map[string]any)
		if !ok {
			return
		}
		if v == nil && nullable(m) {
			return // a nullable object that is null has no required properties
		}
		for _, sub := range anySlice(m["allOf"]) {
			walk(sub, v, path)
		}
		obj, isObj := v.(map[string]any)
		for _, r := range anySlice(m["required"]) {
			name, _ := r.(string)
			if !isObj {
				errs = append(errs, fmt.Errorf("%s: want an object with %q", path, name))
				continue
			}
			if _, ok := obj[name]; !ok {
				errs = append(errs, fmt.Errorf("%s: required property %q is missing", path, name))
			}
		}
		props, _ := m["properties"].(map[string]any)
		for name, ps := range props {
			if isObj {
				if child, ok := obj[name]; ok {
					walk(ps, child, path+"."+name)
				}
			}
		}
	}
	s, ok := d.Comps.Schemas[schema]
	if !ok {
		return []error{fmt.Errorf("schema %q isn't in the spec", schema)}
	}
	walk(s, v, schema)
	return errs
}

func stringsAt(v any, path ...string) []string {
	for _, p := range path {
		m, ok := v.(map[string]any)
		if !ok {
			return nil
		}
		v = m[p]
	}
	var out []string
	for _, x := range anySlice(v) {
		s, _ := x.(string)
		out = append(out, s)
	}
	return out
}

func anySlice(v any) []any { s, _ := v.([]any); return s }

// nullable reports whether a schema's type list allows null (type: [object, "null"]).
func nullable(m map[string]any) bool {
	for _, t := range anySlice(m["type"]) {
		if t == "null" {
			return true
		}
	}
	return false
}
