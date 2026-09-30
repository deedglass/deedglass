package httpx

import (
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestEncoding(t *testing.T) {
	type obj struct {
		Object string   `json:"object"`
		ID     string   `json:"id"`
		Tags   []string `json:"tags"`
		When   Time     `json:"when"`
		Maybe  *Time    `json:"maybe"`
		Cost   Money    `json:"cost"`
		HTML   string   `json:"html"`
	}
	b, err := MarshalJSON(obj{Object: "thing", ID: "agt_1", When: Time(time.Date(2026, 10, 1, 9, 30, 0, 123456789, time.FixedZone("IST", 19800))),
		Cost: Money{Amount: 12000, Currency: "usd"}, HTML: "<a>&"})
	if err != nil {
		t.Fatal(err)
	}
	want := `{
  "object": "thing",
  "id": "agt_1",
  "tags": [],
  "when": "2026-10-01T04:00:00.123Z",
  "maybe": null,
  "cost": {
    "amount": 12000,
    "currency": "usd"
  },
  "html": "<a>&"
}
`
	if string(b) != want {
		t.Fatalf("got\n%s\nwant\n%s", b, want)
	}
	var back Time
	if err := back.UnmarshalText([]byte("2026-10-01T09:30:00+05:30")); err != nil || time.Time(back).Format(time.RFC3339) != "2026-10-01T04:00:00Z" {
		t.Fatalf("UnmarshalText: %v %v", err, time.Time(back))
	}
}

func TestLists(t *testing.T) {
	c, err := NewCursor("agt_01j9zr3m5k8q2w7x4t6v0b1c9d", Time(time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)), 42)
	if err != nil {
		t.Fatal(err)
	}
	l := NewList([]string{"a"}, true, c)
	if l.Object != "list" || !l.HasMore || l.NextCursor == nil {
		t.Fatalf("%+v", l)
	}
	back, err := DecodeCursor(*l.NextCursor)
	if err != nil || back.ID != c.ID {
		t.Fatalf("round trip: %v %+v", err, back)
	}
	var n int
	var ts Time
	if err := back.Key(0, &ts); err != nil || time.Time(ts).Year() != 2026 {
		t.Fatalf("key 0: %v", err)
	}
	if err := back.Key(1, &n); err != nil || n != 42 {
		t.Fatalf("key 1: %v %d", err, n)
	}
	if err := back.Key(2, &n); err == nil {
		t.Fatal("key 2 must fail")
	}
	empty := NewList[string](nil, false, c)
	b, _ := MarshalJSON(empty)
	if !strings.Contains(string(b), `"data": []`) || !strings.Contains(string(b), `"next_cursor": null`) {
		t.Fatalf("empty list: %s", b)
	}

	for _, bad := range []string{"", "!!!", "e30", "eyJ2IjoyLCJrIjpbXSwiaWQiOiJ4In0", "eyJ2IjoxLCJrIjpbXSwiaWQiOiIiLCJ4IjoxfQ", strings.Repeat("A", 2000)} {
		if _, err := DecodeCursor(bad); err == nil {
			t.Errorf("DecodeCursor(%q) accepted", bad)
		} else if e := AsError(err); e == nil || e.Code != CodeValidationFailed || e.Param != "cursor" {
			t.Errorf("DecodeCursor(%q): %v", bad, err)
		}
	}

	for q, want := range map[string]int{"": 20, "?limit=1": 1, "?limit=100": 100} {
		p, err := ParseListParams(httptest.NewRequest("GET", "/v1/x"+q, nil))
		if err != nil || p.Limit != want {
			t.Errorf("%q: %v %d", q, err, p.Limit)
		}
	}
	for _, q := range []string{"?limit=0", "?limit=101", "?limit=x", "?limit=", "?limit=-1"} {
		_, err := ParseListParams(httptest.NewRequest("GET", "/v1/x"+q, nil))
		if e := AsError(err); e == nil || e.Param != "limit" || e.Message != "`limit` must be between 1 and 100." {
			t.Errorf("%q: %v", q, err)
		}
	}
	p, err := ParseListParams(httptest.NewRequest("GET", "/v1/x?cursor="+*l.NextCursor, nil))
	if err != nil || p.Cursor == nil || p.Cursor.ID != c.ID {
		t.Fatalf("cursor param: %v", err)
	}
	if _, err := ParseListParams(httptest.NewRequest("GET", "/v1/x?cursor=nope", nil)); AsError(err) == nil {
		t.Fatal("bad cursor param accepted")
	}
}

func TestDecodePositions(t *testing.T) {
	for _, c := range []struct {
		in        string
		line, col int
	}{
		{`{"a":`, 1, 6},
		{"{\n  \"a\": ,\n}", 2, 8},
		{"{\n\n  x", 3, 3},
	} {
		type v struct {
			A int `json:"a"`
		}
		err := decodeBytes([]byte(c.in), &v{})
		e := AsError(err)
		want := "(line " + strconv.Itoa(c.line) + ", column " + strconv.Itoa(c.col) + ")"
		if e == nil || e.Code != CodeInvalidJSON || !strings.Contains(e.Message, want) {
			t.Errorf("%q: %v, want %s", c.in, err, want)
		}
	}
	type nested struct {
		Items []struct {
			Name string `json:"name"`
		} `json:"items"`
	}
	e := AsError(decodeBytes([]byte(`{"items":[{"name":1}]}`), &nested{}))
	if e == nil || e.Param != "items.0.name" || !strings.Contains(e.Message, "a string") {
		t.Fatalf("nested: %+v", e)
	}
	e = AsError(decodeBytes([]byte(`{"items":[{"nme":"x"}]}`), &nested{}))
	if e == nil || e.Param != "items.0.nme" {
		t.Fatalf("nested unknown: %+v", e)
	}
	e = AsError(decodeBytes([]byte(`[1]`), &nested{}))
	if e == nil || e.Code != CodeValidationFailed {
		t.Fatalf("array for object: %+v", e)
	}
}
