package main

import (
	"compress/gzip"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// The recorded feeds of internal/witness/feed/debezium (make debezium-it RECORD=1).
var debeziumFixtures = filepath.Join("..", "..", "internal", "witness", "feed", "debezium", "testdata")

func TestWitnessDebeziumGoldens(t *testing.T) {
	trigger := filepath.Join(debeziumFixtures, "mysql-trigger.jsonl.gz")
	resumed := filepath.Join(debeziumFixtures, "mysql-resumed.jsonl.gz")
	pk := []string{"--schema", "shop", "--pk", "shop.orders=id", "--pk", "shop.shipments=order_id"}
	plain := map[string]string{"NO_COLOR": "1"}
	cases := []struct {
		name string
		args []string
		code int
	}{
		{"witness/debezium_bound", append([]string{"witness", "debezium", "--events", trigger, "--reported", "1",
			"--sql", "UPDATE orders SET status = 'shipped' WHERE id = 42", "--action", "act_01k82m4v9q5n3d7f8g2h6j0kxp"}, pk...), 1},
		{"witness/debezium_any_writer", append([]string{"witness", "debezium", "--events", trigger, "--reported", "1",
			"--sql", "UPDATE orders SET status = 'shipped' WHERE id = 43"}, pk...), 1},
		{"witness/debezium_matches", append([]string{"witness", "debezium", "--events", trigger, "--reported", "1",
			"--sql", "UPDATE orders SET total_cents = 2000 WHERE id = 7", "--action", "act_01k82m4v9q5n3d7f8g2h6j0kxq"}, pk...), 0},
		{"witness/debezium_resumed", append([]string{"witness", "debezium", "--events", resumed, "--reported", "1",
			"--sql", "UPDATE orders SET status = 'shipped' WHERE id = 42", "--tx", "268a5cb3-cd2a-11f1-a3c7-cebe93e39bd1:16"}, pk...), 2},
		{"witness/debezium_no_events", []string{"witness", "debezium", "--sql", "UPDATE orders SET status = 'x' WHERE id = 1"}, 2},
		{"witness/debezium_bad_pk", []string{"witness", "debezium", "--events", trigger, "--sql", "UPDATE orders SET status = 'x' WHERE id = 1", "--pk", "orders"}, 2},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := cliIn(t, nil, plain, false, c.args...)
			if r.code != c.code {
				t.Fatalf("exit %d, want %d\n%s%s", r.code, c.code, r.stdout, r.stderr)
			}
			golden(t, c.name, r)
		})
	}

	// stdin, and --json: the foreign_witness object with the witness_detail inside.
	fh, err := os.Open(trigger)
	if err != nil {
		t.Fatal(err)
	}
	zr, err := gzip.NewReader(fh)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := io.ReadAll(zr)
	fh.Close()
	if err != nil {
		t.Fatal(err)
	}
	if r := cliIn(t, raw, plain, false, append([]string{"witness", "debezium", "--events", "-", "--reported", "1",
		"--sql", "UPDATE orders SET total_cents = 2000 WHERE id = 7", "--action", "act_01k82m4v9q5n3d7f8g2h6j0kxq"}, pk...)...); r.code != 0 {
		t.Fatalf("stdin: exit %d\n%s%s", r.code, r.stdout, r.stderr)
	}
	r := cliIn(t, nil, plain, false, append([]string{"witness", "debezium", "--events", trigger, "--json", "--reported", "1",
		"--sql", "UPDATE orders SET status = 'shipped' WHERE id = 42", "--action", "act_01k82m4v9q5n3d7f8g2h6j0kxp"}, pk...)...)
	var out struct {
		Object      string   `json:"object"`
		Label       string   `json:"label"`
		Attribution string   `json:"attribution"`
		Verdict     string   `json:"verdict"`
		Level       string   `json:"level"`
		Codes       []string `json:"reason_codes"`
		Digest      string   `json:"detail_digest"`
		Compared    []struct {
			Rows     int  `json:"rows"`
			Complete bool `json:"complete"`
		} `json:"compared"`
		Detail struct {
			Claim struct {
				Method string `json:"method"`
			} `json:"claim"`
		} `json:"detail"`
	}
	if err := json.Unmarshal([]byte(r.stdout), &out); err != nil || r.code != 1 {
		t.Fatalf("%v %d %s", err, r.code, r.stdout)
	}
	if out.Object != "foreign_witness" || out.Label != "Proven · via Debezium (MySQL)" || out.Attribution != "action" ||
		out.Verdict != "MISMATCH" || out.Level != "L2" || !slices.Contains(out.Codes, "unexpected_table") ||
		len(out.Compared) != 1 || out.Compared[0].Rows != 301 || !out.Compared[0].Complete || out.Detail.Claim.Method != "debezium.mysql" || out.Digest == "" {
		t.Fatalf("%+v", out)
	}
}
