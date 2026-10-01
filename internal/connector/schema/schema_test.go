package schema

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestValidate(t *testing.T) {
	s, err := Compile(json.RawMessage(`{"type":"object","additionalProperties":false,"required":["id"],"properties":{
	  "id":{"type":"string","pattern":"^[0-9]{1,12}$","description":"the order"},
	  "status":{"type":"string","enum":["pending","shipped"]},
	  "n":{"type":"integer","minimum":1,"maximum":10},
	  "x":{"type":"number"},
	  "tags":{"type":"array","items":{"type":"string","maxLength":3}},
	  "ok":{"type":"boolean"},
	  "meta":{"type":"object"}}}`))
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]string{
		`{"id":"42"}`: "",
		`{"id":"42","status":"shipped","n":3,"x":1.5,"tags":["a"],"ok":true,"meta":{}}`: "",
		`{}`:                         "args.id is required",
		`{"id":"4a"}`:                "args.id doesn't have the expected format",
		`{"id":42}`:                  "args.id must be a string",
		`{"id":"1","status":"lost"}`: `args.status must be one of "pending", "shipped"`,
		`{"id":"1","n":0}`:           "args.n must be at least 1",
		`{"id":"1","n":11}`:          "args.n must be at most 10",
		`{"id":"1","n":1.5}`:         "args.n must be a whole number",
		`{"id":"1","tags":["abcd"]}`: "args.tags[0] must be at most 3 characters",
		`{"id":"1","tags":"a"}`:      "args.tags must be an array",
		`{"id":"1","ok":"yes"}`:      "args.ok must be true or false",
		`{"id":"1","other":1}`:       "args.other isn't an argument this tool takes",
		`[]`:                         "args must be an object",
		`nope`:                       "args isn't valid JSON",
	}
	for in, want := range cases {
		err := s.Validate(json.RawMessage(in))
		switch {
		case want == "" && err != nil:
			t.Errorf("%s: %v", in, err)
		case want != "" && (err == nil || err.Error() != want):
			t.Errorf("%s: %v, want %q", in, err, want)
		}
	}
}

func TestCompileRefusesUnknownKeywords(t *testing.T) {
	for _, bad := range []string{`{"type":"object","oneOf":[]}`, `{"type":"thing"}`, `{"properties":{"a":{"$ref":"#/x"}}}`,
		`{"type":"string","pattern":"("}`, `{"additionalProperties":{"type":"string"}}`, `[]`} {
		if _, err := Compile(json.RawMessage(bad)); err == nil {
			t.Errorf("%s compiled", bad)
		}
	}
	deep := strings.Repeat(`{"type":"array","items":`, 20) + `{"type":"string"}` + strings.Repeat(`}`, 20)
	if _, err := Compile(json.RawMessage(deep)); err == nil {
		t.Error("a schema nested 20 levels compiled")
	}
	if s, err := Compile(nil); err != nil || s.Validate(json.RawMessage(`{"any":1}`)) != nil {
		t.Errorf("an empty schema: %v", err)
	}
}
