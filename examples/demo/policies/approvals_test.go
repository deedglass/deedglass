package demopolicies

import (
	"os"
	"reflect"
	"testing"

	"go.yaml.in/yaml/v3"
)

type file struct {
	Deedglass   string           `yaml:"deedglass"`
	Name        string           `yaml:"name"`
	Description string           `yaml:"description"`
	Defaults    map[string]any   `yaml:"defaults"`
	Budgets     []map[string]any `yaml:"budgets"`
	Rules       []map[string]any `yaml:"rules"`
}

func load(t *testing.T, path string) file {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var f file
	if err := yaml.Unmarshal(b, &f); err != nil {
		t.Fatal(err)
	}
	return f
}

// TestApprovalsVariant: acme-demo-approvals.yaml is acme-demo.yaml with one rule changed (S22 §3.8):
// orders-writes-need-approval moves above allow-order-status-updates, applies to claude-desktop,
// and asks for L2. Everything else is the same, in the same order.
func TestApprovalsVariant(t *testing.T) {
	base, alt := load(t, "acme-demo.yaml"), load(t, "acme-demo-approvals.yaml")
	if base.Name != alt.Name || base.Deedglass != alt.Deedglass || !reflect.DeepEqual(base.Defaults, alt.Defaults) ||
		!reflect.DeepEqual(base.Budgets, alt.Budgets) {
		t.Fatal("the files differ outside the rules")
	}
	const id = "orders-writes-need-approval"
	strip := func(rs []map[string]any) ([]map[string]any, map[string]any, int) {
		var out []map[string]any
		var r map[string]any
		at := -1
		for i, x := range rs {
			if x["id"] == id {
				r, at = x, i
				continue
			}
			out = append(out, x)
		}
		return out, r, at
	}
	bRest, bRule, _ := strip(base.Rules)
	aRest, aRule, aAt := strip(alt.Rules)
	if !reflect.DeepEqual(bRest, aRest) {
		t.Fatal("the other rules differ")
	}
	if aAt < 0 || alt.Rules[aAt+1]["id"] != "allow-order-status-updates" {
		t.Fatalf("%s isn't right above allow-order-status-updates", id)
	}
	if aRule["verify"] != "L2" {
		t.Errorf("verify %v", aRule["verify"])
	}
	am, bm := aRule["match"].(map[string]any), bRule["match"].(map[string]any)
	if !reflect.DeepEqual(am["tool"], bm["tool"]) || !reflect.DeepEqual(am["agent"], []any{"claude-desktop"}) {
		t.Errorf("match %v vs %v", am, bm)
	}
	for _, k := range []string{"when", "decision", "reason", "approval"} {
		if !reflect.DeepEqual(aRule[k], bRule[k]) {
			t.Errorf("%s: %v vs %v", k, aRule[k], bRule[k])
		}
	}
}
