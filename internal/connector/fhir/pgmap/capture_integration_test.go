//go:build integration

package pgmap_test

import (
	"context"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/deedglass/deedglass/internal/connector/fhir/pgmap"
	"github.com/deedglass/deedglass/internal/decoder"
)

var (
	captureDSN    = flag.String("capture-dsn", "", "a FHIR server's database (with the slot dg_capture and the publication dg_capture_pub)")
	captureServer = flag.String("capture-server", "hapi", "hapi or medplum")
	captureName   = flag.String("capture-name", "", "testdata/<name>.json")
)

// TestCaptureFixtures records a FHIR server's real transactions through the decoder's own code
// (SafeParse, the assembler with the mapping's keyed and count-only tables) as the fixtures the
// unit tests read: go test -tags integration ./internal/connector/fhir/pgmap -run TestCaptureFixtures
// -capture-dsn=… -capture-server=hapi -capture-name=hapi8-writes. Synthetic data only, and content is
// kept as leaf digests under fixtureKey.
func TestCaptureFixtures(t *testing.T) {
	if *captureDSN == "" || *captureName == "" {
		t.Skip("-capture-dsn and -capture-name are not set")
	}
	m, err := pgmap.For(*captureServer)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := pgx.ParseConfig(*captureDSN)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	s, err := decoder.Connect(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close(ctx)
	asm := decoder.NewAssembler(decoder.AssembleOptions{Keyed: m.Keyed, Key: fixtureKey, CountOnly: m.Derived})
	if _, err := s.Peek(ctx, "dg_capture", "dg_capture_pub", 100000, func(r decoder.Raw) error {
		msg, err := decoder.SafeParse(r.Data)
		if err != nil {
			return err
		}
		return asm.Add(r.LSN, msg)
	}); err != nil {
		t.Fatal(err)
	}
	b, err := asm.Finish()
	if err != nil {
		t.Fatal(err)
	}
	var out []fixtureFrame
	for _, f := range b.Frames {
		if f.Total == 0 {
			continue
		}
		tables, _ := json.Marshal(f.Tables)
		changes, _ := json.Marshal(f.Changes)
		out = append(out, fixtureFrame{Tables: tables, Changes: changes, Flags: f.Flags, Total: f.Total})
	}
	data, _ := json.MarshalIndent(out, "", " ")
	if err := os.WriteFile(filepath.Join("testdata", *captureName+".json"), append(data, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Logf("wrote %d frames to testdata/%s.json", len(out), *captureName)
}
