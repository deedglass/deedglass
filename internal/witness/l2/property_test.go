package l2

import (
	"fmt"
	mrand "math/rand/v2"
	"testing"

	"github.com/deedglass/deedglass/internal/witness"
)

// TestCompareProperty is S21 §6's property: for random frames that fit a random footprint exactly
// (observed ⊆ footprint, equal counts), never a violation; add one extra row and there always is.
func TestCompareProperty(t *testing.T) {
	r := mrand.New(mrand.NewPCG(21, 7))
	for i := 0; i < 2000; i++ {
		n := 1 + r.IntN(40)
		exact := r.IntN(2) == 0
		cols := []string{"status"}
		if r.IntN(3) == 0 {
			cols = append(cols, "total_cents")
		}
		te := TableExpect{Table: "public.orders", Ops: []string{OpUpdate}, Rows: ip(int64(n)), Columns: cols}
		fp := Footprint{Derivation: DerivationCountOnly, Tables: []TableExpect{te}}
		var cs []Change
		used := map[int]bool{}
		for j := 0; j < n; j++ {
			id := 1 + r.IntN(100000)
			for used[id] {
				id++
			}
			used[id] = true
			ch := map[string][2]string{"status": {"paid", "shipped"}}
			if len(cols) == 2 && r.IntN(2) == 0 {
				ch["total_cents"] = [2]string{"1200", fmt.Sprint(r.IntN(9999))}
			}
			cs = append(cs, upd(j+1, id, ch))
			if exact {
				fp.Tables[0].Keys = append(fp.Tables[0].Keys, map[string]string{"id": fmt.Sprint(id)})
			}
		}
		if exact {
			fp.Derivation = DerivationExactKeys
		}
		f := frame(cs...)
		for _, x := range Compare(fp, &f, Config{}) {
			if x.Class == witness.Violation || x.Class == witness.Gap {
				t.Fatalf("case %d: a fitting frame gave %s: %s", i, x.Code, x.Message)
			}
		}
		// One extra row: another table, another order, or another column.
		switch r.IntN(3) {
		case 0:
			f.add(Change{Seq: n + 1, Op: "I", Table: "public.audit", Key: map[string]*string{"id": sp("1")}, New: map[string]Value{"id": txt("1")}})
		case 1:
			f.add(upd(n+1, 200001, map[string][2]string{"status": {"paid", "shipped"}}))
		default:
			f.add(upd(n+1, 200002, map[string][2]string{"customer_id": {"7", "8"}}))
		}
		violation := false
		for _, x := range Compare(fp, &f, Config{}) {
			violation = violation || x.Class == witness.Violation
		}
		if !violation {
			t.Fatalf("case %d: one extra row gave no violation", i)
		}
	}
}
