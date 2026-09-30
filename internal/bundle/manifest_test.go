package bundle

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/gowebpki/jcs"

	"github.com/deedglass/deedglass/internal/intent"
)

func read(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func sum(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// prosrc returns the function body exactly as PostgreSQL stores it in
// pg_proc.prosrc: the text between the $fn$ dollar-quote delimiters.
func prosrc(t *testing.T, sql string) string {
	t.Helper()
	const open, close = "AS $fn$", "$fn$;"
	i := strings.Index(sql, open)
	if i < 0 {
		t.Fatal("no AS $fn$ in 003_transfer_v1.sql")
	}
	body := sql[i+len(open):]
	j := strings.Index(body, close)
	if j < 0 {
		t.Fatal("no closing $fn$; in 003_transfer_v1.sql")
	}
	return body[:j]
}

// TestManifestHashes keeps bundles/transfer_v1/manifest.json bound to the
// files it describes (spec §6). The integration smoke run checks the same
// fn_src_sha256 against the live pg_proc.prosrc.
func TestManifestHashes(t *testing.T) {
	raw := read(t, "../../bundles/transfer_v1/manifest.json")
	if _, err := intent.ParseStrict(raw); err != nil {
		t.Fatalf("manifest must satisfy the strict JSON profile: %v", err)
	}
	var m struct {
		V                  string `json:"v"`
		Op                 string `json:"op"`
		OpV                int    `json:"op_v"`
		IntentSchemaSHA256 string `json:"intent_schema_sha256"`
		EntryFn            string `json:"entry_fn"`
		FnSrcSHA256        string `json:"fn_src_sha256"`
		SchemaSHA256       string `json:"schema_sha256"`
		Marker             struct {
			Prefix string `json:"prefix"`
			Count  int    `json:"count"`
		} `json:"marker"`
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	if m.V != "deedglass.bundle/1" || m.Op != "TRANSFER" || m.OpV != 1 || m.EntryFn != "deedglass.execute_transfer_v1(bytea,bytea)" ||
		m.Marker.Prefix != "deedglass" || m.Marker.Count != 1 {
		t.Errorf("manifest identity fields drifted: %+v", m)
	}

	schema, err := jcs.Transform(read(t, "../../bundles/transfer_v1/intent.schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	checks := []struct{ field, got, want string }{
		{"intent_schema_sha256", m.IntentSchemaSHA256, sum(schema)},
		{"fn_src_sha256", m.FnSrcSHA256, sum([]byte(prosrc(t, string(read(t, "../../sql/003_transfer_v1.sql")))))},
		// Provisional: raw bytes of 002_schema.sql until the DDL normalization rule lands
		// with the installer (see bundles/transfer_v1/README.md).
		{"schema_sha256", m.SchemaSHA256, sum(read(t, "../../sql/002_schema.sql"))},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s: manifest has %s, files hash to %s (update the manifest; a changed bundle needs an ADR)", c.field, c.got, c.want)
		}
	}

	canon, err := jcs.Transform(raw)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("bundle_hash = sha256:%s", sum(canon))
}
