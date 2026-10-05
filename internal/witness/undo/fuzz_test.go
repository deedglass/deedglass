package undo

import (
	"fmt"
	"maps"
	"slices"
	"strconv"
	"testing"
)

// The FuzzNetEffect model: one orders table over keys 1..6, rows 1, 2 and 3 there before the
// transaction. A consistent sequence is one Postgres could commit (insert a free key, update or
// delete a live one, move a live key to a free one), each change with a full old image. A raw
// sequence ignores the table's state.

const fuzzKeys = 6

func fuzzRow(id, status, note string) Image {
	return im("id", id, "status", status, "note", note, "total_cents", strconv.Itoa(len(status)*100))
}

// genChanges turns data into a sequence of row changes, and the table before and after it.
func genChanges(data []byte, raw bool) (changes []RowChange, before, after map[string]Image) {
	state := map[string]Image{}
	for i := 1; i <= 3; i++ {
		id := strconv.Itoa(i)
		state[id] = fuzzRow(id, "s0", "n-"+id)
	}
	before = maps.Clone(state)
	live := func(pick byte) (string, bool) {
		ids := slices.Sorted(maps.Keys(state))
		if len(ids) == 0 {
			return "", false
		}
		return ids[int(pick)%len(ids)], true
	}
	seq := 0
	for i := 0; i+1 < len(data); i += 2 {
		op, arg := data[i], data[i+1]
		k := strconv.Itoa(int(arg)%fuzzKeys + 1)
		k2 := strconv.Itoa(int(arg/fuzzKeys)%fuzzKeys + 1)
		seq++
		v := "v" + strconv.Itoa(seq)
		if raw {
			old := fuzzRow(k, "r", "n-"+k)
			switch op % 4 {
			case 0:
				changes = append(changes, ins(seq, ordersRef, fuzzRow(k, v, "n-"+k)))
			case 1:
				changes = append(changes, upd(seq, ordersRef, old, fuzzRow(k, v, "n-"+k)))
			case 2:
				changes = append(changes, upd(seq, ordersRef, old, fuzzRow(k2, "r", "n-"+k)))
			case 3:
				changes = append(changes, del(seq, ordersRef, old))
			}
			continue
		}
		switch op % 4 {
		case 0: // insert a free key
			if _, ok := state[k]; ok {
				continue
			}
			row := fuzzRow(k, v, "n-"+v)
			changes = append(changes, ins(seq, ordersRef, row))
			state[k] = row
		case 1: // update a live row's values; an unchanged note is sometimes not resent (TOAST)
			id, ok := live(arg)
			if !ok {
				continue
			}
			old := state[id]
			row := with(old, "status", v, "total_cents", strconv.Itoa(len(v)*100))
			if op&4 != 0 {
				row = with(old, "note", v)
			}
			sent := row
			if op&8 != 0 && Same(row["note"], old["note"]) {
				sent = with(row, "note", uv)
			}
			changes = append(changes, upd(seq, ordersRef, old, sent))
			state[id] = row
		case 2: // move a live row to a free key
			id, ok := live(arg)
			if _, taken := state[k2]; !ok || taken {
				continue
			}
			old := state[id]
			row := with(old, "id", k2)
			changes = append(changes, upd(seq, ordersRef, old, row))
			delete(state, id)
			state[k2] = row
		case 3: // delete a live row
			id, ok := live(arg)
			if !ok {
				continue
			}
			changes = append(changes, del(seq, ordersRef, state[id]))
			delete(state, id)
		}
	}
	return changes, before, state
}

// keyCycle reports whether a consistent sequence left rows in a circle of keys: the row at key a
// came from key b, the row at b came from c, …, back to a. Undo can't put such rows back one at a
// time. The planner follows a row by its key, so a row deleted at a key and a row inserted there
// later are one row that was replaced (TestCollapse), even when other rows passed through that key
// in between; a row inserted at a key nothing left breaks any circle, because undo deletes it.
func keyCycle(changes []RowChange) bool {
	origin := map[string]string{"1": "1", "2": "2", "3": "3"} // key now → key before ("" = inserted)
	gone := map[string]string{}                               // key → origin of the row deleted there
	for _, c := range changes {
		switch c.Op {
		case OpInsert:
			k := *c.New["id"].T
			o, replaced := gone[k]
			if !replaced {
				o = ""
			}
			origin[k] = o
			delete(gone, k)
		case OpUpdate:
			from, to := *c.Old["id"].T, *c.New["id"].T
			if from != to {
				origin[to] = origin[from]
				delete(origin, from)
			}
		case OpDelete:
			k := *c.Old["id"].T
			gone[k] = origin[k]
			delete(origin, k)
		}
	}
	for start, o := range origin {
		if o == "" || o == start {
			continue
		}
		at := start
		for range len(origin) {
			next, ok := origin[origin[at]]
			if !ok || next == "" || origin[at] == at {
				break
			}
			at = origin[at]
			if at == start {
				return true
			}
		}
	}
	return false
}

// finalKey is the key a step leaves a row at: an insert's key, an update's key with its key
// columns put back; a delete leaves none.
func finalKey(s Step) (string, bool) {
	switch s.Op {
	case OpInsert:
		return s.Key["id"], true
	case OpUpdate:
		if v, ok := s.Set["id"]; ok && v.T != nil {
			return *v.T, true
		}
		return s.Key["id"], true
	}
	return "", false
}

// checkOneStepPerKey: no two steps act on one row, and no two steps leave a row at one key. The
// identity-always pair (delete at the new key, insert at the old one) is one of each.
func checkOneStepPerKey(t *testing.T, p *Plan) {
	t.Helper()
	targets, finals := map[string]bool{}, map[string]bool{}
	for _, s := range p.Steps {
		if s.Op != OpInsert {
			id := s.Key["id"]
			if targets[id] {
				t.Fatalf("two steps act on id=%s: %q", id, stepsText(p))
			}
			targets[id] = true
		}
		if id, ok := finalKey(s); ok {
			if finals[id] {
				t.Fatalf("two steps leave a row at id=%s: %q", id, stepsText(p))
			}
			finals[id] = true
		}
	}
}

// applySteps runs the plan's steps on rows the way Postgres would with a deferred primary key:
// every statement must affect exactly one row; a GENERATED ALWAYS identity can't be updated or
// inserted without OVERRIDING SYSTEM VALUE. collided reports a key held by two rows between steps.
func applySteps(p *Plan, rows map[string]Image, identity bool) (out []Image, collided bool, err error) {
	for _, r := range rows {
		out = append(out, maps.Clone(r))
	}
	find := func(id string) []int {
		var at []int
		for i, r := range out {
			if *r["id"].T == id {
				at = append(at, i)
			}
		}
		return at
	}
	for _, s := range p.Steps {
		switch s.Op {
		case OpDelete:
			at := find(s.Key["id"])
			if len(at) != 1 {
				return nil, collided, fmt.Errorf("delete id=%s affects %d rows", s.Key["id"], len(at))
			}
			out = slices.Delete(out, at[0], at[0]+1)
		case OpUpdate:
			at := find(s.Key["id"])
			if len(at) != 1 {
				return nil, collided, fmt.Errorf("update id=%s affects %d rows", s.Key["id"], len(at))
			}
			if _, ok := s.Set["id"]; ok && identity {
				return nil, collided, fmt.Errorf("update id=%s sets a GENERATED ALWAYS identity", s.Key["id"])
			}
			for c, v := range s.Set {
				out[at[0]][c] = v
			}
			collided = collided || len(find(*out[at[0]]["id"].T)) > 1
		case OpInsert:
			if identity && !s.Overriding {
				return nil, collided, fmt.Errorf("insert id=%s without OVERRIDING SYSTEM VALUE", s.Key["id"])
			}
			out = append(out, maps.Clone(s.Set))
			collided = collided || len(find(s.Key["id"])) > 1
		}
	}
	return out, collided, nil
}

// sameRows compares rows by key, generated columns aside.
func sameRows(got []Image, want map[string]Image) error {
	if len(got) != len(want) {
		return fmt.Errorf("%d rows, want %d", len(got), len(want))
	}
	for _, r := range got {
		id := *r["id"].T
		w, ok := want[id]
		if !ok {
			return fmt.Errorf("row id=%s shouldn't exist", id)
		}
		for _, c := range []string{"id", "status", "note"} {
			if !Same(r[c], w[c]) {
				return fmt.Errorf("row id=%s: %s is %s, want %s", id, c, ValueText(r[c]), ValueText(w[c]))
			}
		}
	}
	return nil
}

func fuzzSeeds(f *testing.F) {
	for _, s := range []struct {
		data          []byte
		identity, raw bool
	}{
		{[]byte{0, 3, 1, 0, 3, 1}, false, false},        // insert 4, update, delete
		{[]byte{2, 18, 1, 1, 9, 0}, false, false},       // move 1 to 4, update, TOAST update
		{[]byte{2, 18, 0, 0, 3, 0}, false, false},       // move 1 to 4, insert 1, delete
		{[]byte{3, 0, 0, 0, 13, 0}, false, false},       // delete 1, insert 1 (replaced), TOAST update
		{[]byte{0, 4, 3, 3, 2, 30, 5, 2}, false, false}, // insert 5, delete it, move, update note
		{[]byte{2, 18, 1, 0, 3, 1}, true, false},        // identity: move, update, delete
		{[]byte{0, 3, 3, 0, 0, 0}, true, false},         // identity: insert 4, delete 1, insert 1
		{[]byte{0, 3, 1, 3, 3, 4, 2, 7}, false, true},   // raw: insert, update, delete, move
		{[]byte{0, 1, 0, 1}, false, true},               // raw: the same key inserted twice
		{[]byte{2, 0, 1, 255}, true, true},              // raw: a move onto itself
	} {
		f.Add(s.data, s.identity, s.raw)
	}
}

// FuzzNetEffect is S23 §6's fuzz: random change sequences give a plan or findings, never a panic,
// never two steps for one final key; and a consistent sequence always gives a plan whose guards
// hold on the table it left and whose steps put the table back exactly.
func FuzzNetEffect(f *testing.F) {
	fuzzSeeds(f)
	f.Fuzz(func(t *testing.T, data []byte, identity, raw bool) {
		if len(data) > 400 {
			data = data[:400]
		}
		changes, before, after := genChanges(data, raw)
		ident := ""
		if identity {
			ident = "a"
		}
		p, fs := build(evidence(changes...), catalog(ordersCat(ident)))
		if (p == nil) == (len(fs) == 0) {
			t.Fatalf("plan %v with findings %v", p != nil, fs)
		}
		if p == nil {
			// A consistent sequence may leave rows that swapped keys (three moves are enough:
			// 1→4, 3→1, 4→3). That is refused by design (TestKeySwapCycle), and only then.
			cycle := hasFinding(fs, CheckEvidence, ProblemCycle([]string{"public.orders"}).Text)
			if !raw && !(cycle && len(fs) == 1 && keyCycle(changes)) {
				t.Fatalf("a consistent sequence was refused: %v", fs)
			}
			return
		}
		if d, err := ComputeDigest(p); err != nil || d != p.Digest {
			t.Fatalf("digest %s, recomputed %s %v", p.Digest, d, err)
		}
		checkOneStepPerKey(t, p)
		if raw {
			return
		}
		if bad, _ := Compare(p, Current{100: after}); len(bad) > 0 {
			t.Fatalf("a guard fails on the table the transaction left: %+v", bad)
		}
		got, _, err := applySteps(p, after, identity)
		if err != nil {
			t.Fatalf("%v; steps %q", err, stepsText(p))
		}
		if err := sameRows(got, before); err != nil {
			t.Fatalf("after the undo: %v; steps %q", err, stepsText(p))
		}
	})
}

// TestKeySwapCycle: a transaction that swaps two keys (1 to 4, 2 to 1, 4 to 2). Put back one step at
// a time in reverse order, the first update moves row 2 onto key 1 while row 1 still holds it: a
// primary key that isn't DEFERRABLE refuses it, and a deferred one makes the next "WHERE id = 1"
// match two rows. Either way the undo is offered and then always fails.
func TestKeySwapCycle(t *testing.T) {
	changes, _, _ := genChanges([]byte{2, 18, 2, 0, 2, 8}, false)
	if len(changes) != 3 {
		t.Fatalf("%d changes", len(changes))
	}
	// Rows that swapped keys can't be put back one row at a time: refused, never offered.
	p, fs := build(evidence(changes...), catalog(ordersCat("")))
	if p != nil || !hasFinding(fs, CheckEvidence, ProblemCycle([]string{"public.orders"}).Text) {
		t.Fatalf("plan %v, findings %v; want the cycle refused", p != nil, fs)
	}
}

// TestKeyReusedByAnotherRow: row 1 moved to 5, a new row inserted at 1, then row 5 deleted. Row 1's
// last change (its delete, seq 3) is later than the new row's insert (seq 2), so reverse order
// re-inserts row 1 while the new row still holds key 1, and only then deletes "id = 1".
func TestKeyReusedByAnotherRow(t *testing.T) {
	changes, before, after := genChanges([]byte{2, 24, 0, 0, 3, 3}, false)
	if len(changes) != 3 {
		t.Fatalf("%d changes", len(changes))
	}
	p, fs := build(evidence(changes...), catalog(ordersCat("")))
	if p == nil {
		t.Fatal(fs)
	}
	got, collided, err := applySteps(p, after, false)
	if err == nil {
		err = sameRows(got, before)
	}
	if err != nil || collided {
		t.Fatalf("steps %q re-insert a key before the step that frees it (%v); "+
			"want the delete of id=1 first", stepsText(p), err)
	}
}

// TestIdentityMovedThenReplaced: on a GENERATED ALWAYS identity, row 1 moved to 4, deleted at 4,
// and a new row inserted at 4. Collapse marks the row replaced, so Build plans an UPDATE that sets
// the identity column back to 1, which Postgres refuses (only DEFAULT is allowed there).
func TestIdentityMovedThenReplaced(t *testing.T) {
	changes, before, after := genChanges([]byte{2, 18, 3, 2, 0, 3}, false)
	if len(changes) != 3 {
		t.Fatalf("%d changes: %+v", len(changes), changes)
	}
	p, fs := build(evidence(changes...), catalog(ordersCat("a")))
	if p == nil {
		t.Fatal(fs)
	}
	got, _, err := applySteps(p, after, true)
	if err == nil {
		err = sameRows(got, before)
	}
	if err != nil {
		t.Fatalf("%v; steps %q (want DELETE id=4 + INSERT id=1 OVERRIDING SYSTEM VALUE)",
			err, stepsText(p))
	}
}
