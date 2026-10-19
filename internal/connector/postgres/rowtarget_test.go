package postgres

import (
	"encoding/json"
	"testing"

	"github.com/deedglass/deedglass/internal/connector"
	"github.com/deedglass/deedglass/internal/sqlguard"
)

func TestTargetOf(t *testing.T) {
	cases := []struct {
		sql, key, table string
	}{
		{`UPDATE public.orders SET status = 'shipped' WHERE id = 42`, "id = 42", "public.orders"},
		{`UPDATE orders SET status = 'x' WHERE code = 'a''b'`, "code = 'a''b'", "app.orders"},
		{`UPDATE public.orders SET total = total + 1 WHERE id = 7`, "id = 7", "public.orders"},
	}
	for _, c := range cases {
		tg, why := TargetOf(c.sql, "app")
		if why != "" || tg.Key != c.key || tg.Shape.Table != c.table {
			t.Errorf("%s: %+v %q", c.sql, tg, why)
		}
	}
	if got := valueText(sqlguard.Value{Kind: sqlguard.ShapeValueNull}); got != "NULL" {
		t.Errorf("valueText(null) = %q", got)
	}
}

// TestRowTarget: the row read-back's target comes from the connector (TY-04), not the Witness.
func TestRowTarget(t *testing.T) {
	c := &Connector{}
	call := func(tool, sql string) connector.Call {
		args, _ := json.Marshal(map[string]string{"sql": sql})
		return connector.Call{Tool: connector.ToolSpec{Name: tool}, Args: args}
	}
	got, ok := c.RowTarget(call(ToolExecute, `UPDATE public.orders SET status = 'shipped' WHERE id = 42`), "public")
	if !ok || got != (connector.RowTarget{Table: "public.orders", Key: "id = 42"}) {
		t.Fatalf("simple update: %+v %v", got, ok)
	}
	for _, sql := range []string{
		`UPDATE public.orders SET status = 'x' WHERE id IN (1, 2, 3)`,
		`WITH x AS (SELECT 1) UPDATE public.orders SET status = 'x' WHERE id = 1`,
		`DELETE FROM public.orders WHERE id = 1`,
		`INSERT INTO public.orders (id) VALUES (1)`,
		`UPDATE public.orders SET status = 'x'`,
		`SELECT 1`,
		``,
	} {
		got, ok := c.RowTarget(call(ToolExecute, sql), "public")
		if !ok || got.Why != WhyNotRowReadBack || got.Table != "" || got.Key != "" {
			t.Errorf("%q: %+v %v", sql, got, ok)
		}
	}
	bad := call(ToolExecute, "")
	bad.Args = json.RawMessage(`{`)
	if got, ok := c.RowTarget(bad, "public"); !ok || got.Why != WhyNotRowReadBack {
		t.Errorf("bad args: %+v %v", got, ok)
	}
	if _, ok := c.RowTarget(call("query", `UPDATE public.orders SET status = 'x' WHERE id = 1`), "public"); ok {
		t.Error("query offered the row read-back")
	}
	if MethodRow != "readback.postgres.row" {
		t.Errorf("MethodRow = %q", MethodRow)
	}
}
