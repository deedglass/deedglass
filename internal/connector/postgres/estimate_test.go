package postgres

import "testing"

func TestPlanRows(t *testing.T) {
	cases := []struct {
		plan string
		rows int64
		ok   bool
	}{
		{`[{"Plan":{"Node Type":"ModifyTable","Plan Rows":0,"Plans":[{"Node Type":"Index Scan","Plan Rows":1}]}}]`, 1, true},
		{`[{"Plan":{"Node Type":"Seq Scan","Plan Rows":600}}]`, 600, true},
		{`[{"Plan":{"Node Type":"Seq Scan","Plan Rows":2.2}}]`, 3, true},
		{`[{"Plan":{"Node Type":"ModifyTable","Plan Rows":5}}]`, 5, true},
		{`[{"Plan":{"Node Type":"Seq Scan","Plan Rows":-1}}]`, 0, false},
		{`[{"Plan":{"Node Type":"Seq Scan"}}]`, 0, false},
		{`[]`, 0, false},
		{`[{"Plan":{}},{"Plan":{}}]`, 0, false},
		{`not json`, 0, false},
	}
	for _, c := range cases {
		rows, ok := PlanRows([]byte(c.plan))
		if rows != c.rows || ok != c.ok {
			t.Errorf("%s: %d %v", c.plan, rows, ok)
		}
	}
}
