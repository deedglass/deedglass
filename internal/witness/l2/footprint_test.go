package l2

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

type footprintCase struct {
	Name        string              `json:"name"`
	Tool        string              `json:"tool"`
	SQL         string              `json:"sql"`
	Params      []json.RawMessage   `json:"params"`
	Reported    *int64              `json:"reported"`
	PK          map[string][]string `json:"pk"`
	Returning   []map[string]any    `json:"returning"`
	Explicit    *Footprint          `json:"explicit"`
	Contract    []ContractWrite     `json:"contract"`
	AutoColumns map[string][]string `json:"auto_columns"`
	MaxRows     map[string]int64    `json:"max_rows"`
	Watched     []string            `json:"watched"`
	Want        Footprint           `json:"want"`
	Reasons     []string            `json:"reasons"`
}

// TestFootprintTable is S21 §6's footprint table (testdata/footprint/cases.json): every row of
// §3.3 plus the edge rows.
func TestFootprintTable(t *testing.T) {
	raw, err := os.ReadFile("testdata/footprint/cases.json")
	if err != nil {
		t.Fatal(err)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	dec.UseNumber()
	var cases []footprintCase
	if err := dec.Decode(&cases); err != nil {
		t.Fatal(err)
	}
	if len(cases) < 26 {
		t.Fatalf("%d footprint cases; S21 §6 asks for at least 26", len(cases))
	}
	for _, c := range cases {
		t.Run(c.Name, func(t *testing.T) {
			pk := map[string][]string{"public.orders": {"id"}, "public.refunds": {"id"}}
			for k, v := range c.PK {
				pk[k] = v
			}
			watched := []string{"public.orders", "public.refunds", "public.order_items", "public.customers"}
			if c.Watched != nil {
				watched = c.Watched
			}
			fp, rs := Derive(DeriveInput{Tool: c.Tool, SQL: c.SQL, Params: c.Params, Reported: c.Reported, Returning: c.Returning,
				Explicit: c.Explicit, Contract: c.Contract, PK: pk,
				Config: Config{AutoColumns: c.AutoColumns, MaxRows: c.MaxRows, Watched: watched}})
			got, _ := json.Marshal(fp)
			want, _ := json.Marshal(c.Want)
			if !bytes.Equal(got, want) {
				t.Errorf("footprint\n got %s\nwant %s", got, want)
			}
			if g, w := strings.Join(codes(rs), ","), strings.Join(c.Reasons, ","); g != w {
				t.Errorf("reasons %q, want %q", g, w)
			}
		})
	}
}
