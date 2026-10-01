package fake

import (
	"encoding/json"
	"reflect"
	"slices"
	"testing"
)

// The same seed gives the same tools, kinds, verdicts, latencies and arguments.
func TestGenerateDeterministic(t *testing.T) {
	a, b := Generate(42, 500, nil, 3), Generate(42, 500, nil, 3)
	if !reflect.DeepEqual(a, b) {
		t.Fatal("seed 42 twice gave different plans")
	}
	if reflect.DeepEqual(a, Generate(43, 500, nil, 3)) {
		t.Fatal("seeds 42 and 43 gave the same plans")
	}
}

func TestGenerateMix(t *testing.T) {
	plans := Generate(7, 20_000, nil, 3)
	n := map[Kind]int{}
	for _, p := range plans {
		n[p.Kind]++
		if !slices.Contains(Tools, p.Tool) {
			t.Fatalf("tool %q isn't a canonical tool", p.Tool)
		}
		if p.Agent < 0 || p.Agent >= 3 {
			t.Fatalf("agent %d", p.Agent)
		}
		if !json.Valid(p.Args) {
			t.Fatalf("args %s", p.Args)
		}
		switch p.Kind {
		case Mismatch:
			if p.Level != "L2" || p.Tool != "postgres.execute" || p.Verdict != "MISMATCH" {
				t.Fatalf("%+v", p)
			}
		case Deny, Pending:
			if p.LatencyMS != 0 || p.Result != nil {
				t.Fatalf("a %s action never ran: %+v", p.Kind, p)
			}
		case Failed:
			if p.ErrorCode != "upstream_error" && p.ErrorCode != "timeout" {
				t.Fatalf("%+v", p)
			}
		case Allow:
			if p.Verdict != "VERIFIED" || (p.Level == "L2") != (p.Method == "postgres.pgoutput") {
				t.Fatalf("%+v", p)
			}
		}
	}
	for _, k := range Kinds {
		got := float64(n[k.Kind]) / 200 // percent
		if got < float64(k.Weight)-2 || got > float64(k.Weight)+2 {
			t.Errorf("%s: %.1f%%, want about %d%%", k.Kind, got, k.Weight)
		}
	}
}

func TestKinds(t *testing.T) {
	ks, err := ParseKinds("deny, mismatch,deny")
	if err != nil || !slices.Equal(ks, []Kind{Deny, Mismatch}) {
		t.Fatal(ks, err)
	}
	if _, err := ParseKinds("approve"); err == nil {
		t.Fatal("unknown kind accepted")
	}
	for _, p := range Generate(1, 200, ks, 2) {
		if p.Kind != Deny && p.Kind != Mismatch {
			t.Fatalf("kind %s outside --kinds", p.Kind)
		}
	}
	if ks, err := ParseKinds(""); err != nil || ks != nil {
		t.Fatal("empty kinds")
	}
}
