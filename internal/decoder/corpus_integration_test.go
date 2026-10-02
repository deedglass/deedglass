//go:build integration

package decoder_test

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/deedglass/deedglass/internal/connector/postgres/pgtest"
	"github.com/deedglass/deedglass/internal/decoder"
	"github.com/deedglass/deedglass/internal/ids"
)

var updateCorpus = flag.Bool("update-corpus", false, "rewrite testdata/decoder/pgoutput from this server (then update testdata/SHA256SUMS)")

// TestCaptureCorpus captures real pgoutput messages of all 11 kinds (B C O R Y I U D T M, with U
// carrying both an O and a K old tuple, and transactional and non-transactional M) from this
// server, and checks each through SafeParse. With -update-corpus it writes them to
// testdata/decoder/pgoutput, the seed of FuzzSafeParse and TestSafeParsePrefixes.
func TestCaptureCorpus(t *testing.T) {
	admin := pgtest.AdminURL(t)
	c, err := pgx.Connect(bg, admin)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close(bg)
	const db = "deedglass_it_dec_corpus"
	_, _ = c.Exec(bg, `SELECT pg_catalog.pg_drop_replication_slot(slot_name) FROM pg_catalog.pg_replication_slots WHERE slot_name = 'deedglass_it_corpus'`)
	if _, err := c.Exec(bg, "DROP DATABASE IF EXISTS "+db+" WITH (FORCE)"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Exec(bg, "CREATE DATABASE "+db); err != nil {
		t.Fatal(err)
	}
	conn, err := pgx.Connect(bg, pgtest.WithDB(admin, db))
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = conn.Exec(bg, `SELECT pg_catalog.pg_drop_replication_slot('deedglass_it_corpus')`)
		_ = conn.Close(bg)
		_, _ = c.Exec(bg, "DROP DATABASE IF EXISTS "+db+" WITH (FORCE)")
	}()
	var ver int
	_ = conn.QueryRow(bg, `SELECT current_setting('server_version_num')::int`).Scan(&ver)
	big := `(SELECT pg_catalog.string_agg(pg_catalog.md5(g::text), '') FROM pg_catalog.generate_series(1, 300) AS g)` // 9,600 characters that don't compress
	exec := func(q string) {
		t.Helper()
		if _, err := conn.Exec(bg, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	exec(`CREATE TYPE public.mood AS ENUM ('calm', 'busy')`)
	exec(`CREATE TABLE public.wide (id bigint PRIMARY KEY, mood public.mood NOT NULL, note text, blob text)`)
	exec(`ALTER TABLE public.wide REPLICA IDENTITY FULL`)
	exec(`ALTER TABLE public.wide ALTER COLUMN blob SET STORAGE EXTERNAL`)
	exec(`CREATE TABLE public.plain (id int PRIMARY KEY, v text)`)
	exec(`CREATE PUBLICATION deedglass_it_corpus_pub FOR TABLE public.wide, public.plain`)
	exec(`SELECT pg_catalog.pg_create_logical_replication_slot('deedglass_it_corpus', 'pgoutput')`)
	exec(`INSERT INTO public.wide VALUES (9007199254740993, 'calm', NULL, ` + big + `)`)
	exec(`UPDATE public.wide SET mood = 'busy', note = 'tab	and "quote"' WHERE id = 9007199254740993`)
	exec(`INSERT INTO public.plain VALUES (1, 'one')`)
	exec(`UPDATE public.plain SET id = 2 WHERE id = 1`)
	exec(`DELETE FROM public.plain WHERE id = 2`)
	exec(`UPDATE public.wide SET blob = blob || 'z' WHERE id = 9007199254740993`)
	exec(`DELETE FROM public.wide WHERE id = 9007199254740993`)
	exec(`INSERT INTO public.wide VALUES (1, 'calm', 'x', 'y')`)
	exec(`TRUNCATE public.wide`)
	act := ids.New[ids.ActionKind]().String()
	tx, _ := conn.Begin(bg)
	_, _ = tx.Exec(bg, `INSERT INTO public.plain VALUES (3, 'three')`)
	_, _ = tx.Exec(bg, `SELECT pg_catalog.pg_logical_emit_message(true, 'deedglass', $1::text)`, act)
	_ = tx.Commit(bg)
	exec(`SELECT pg_catalog.pg_logical_emit_message(false, 'deedglass', 'hb1:not-a-real-heartbeat')`)
	exec(`SELECT pg_catalog.pg_replication_origin_create('deedglass_it_corpus_origin')`)
	defer func() {
		_, _ = conn.Exec(bg, `SELECT pg_catalog.pg_replication_origin_drop('deedglass_it_corpus_origin')`)
	}()
	exec(`SELECT pg_catalog.pg_replication_origin_session_setup('deedglass_it_corpus_origin')`)
	otx, _ := conn.Begin(bg)
	_, _ = otx.Exec(bg, `SELECT pg_catalog.pg_replication_origin_xact_setup('0/1000'::pg_lsn, pg_catalog.now())`)
	_, _ = otx.Exec(bg, `INSERT INTO public.plain VALUES (4, 'from an origin')`)
	_ = otx.Commit(bg)
	exec(`SELECT pg_catalog.pg_replication_origin_session_reset()`)

	rows, err := conn.Query(bg, `SELECT data FROM pg_catalog.pg_logical_slot_peek_binary_changes('deedglass_it_corpus', NULL, NULL,
	    'proto_version', '1', 'publication_names', 'deedglass_it_corpus_pub', 'messages', 'true', 'origin', 'any')`)
	if err != nil {
		t.Fatal(err)
	}
	keep := map[string][]byte{}
	for rows.Next() {
		var b []byte
		if err := rows.Scan(&b); err != nil {
			t.Fatal(err)
		}
		if _, err := decoder.SafeParse(b); err != nil {
			t.Fatalf("a real message was refused: %v (%x)", err, b[:min(len(b), 40)])
		}
		label := string(b[0])
		switch b[0] {
		case 'U':
			switch b[5] {
			case 'O', 'K':
				label += "-old-" + string(b[5])
			default:
				label += "-new-only"
			}
		case 'D':
			label += "-old-" + string(b[5])
		case 'M':
			if b[1] == 1 {
				label += "-tx"
			} else {
				label += "-nontx"
			}
		}
		// Keep the largest of each kind: long messages give TestSafeParsePrefixes its volume.
		if len(b) > len(keep[label]) {
			keep[label] = append([]byte{}, b...)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	labels := make([]string, 0, len(keep))
	for k := range keep {
		labels = append(labels, k)
	}
	sort.Strings(labels)
	t.Logf("PG %d: %s", ver, strings.Join(labels, " "))
	for _, want := range []string{"B", "C", "O", "R", "Y", "I", "U-old-O", "U-old-K", "D-old-O", "D-old-K", "T", "M-tx", "M-nontx"} {
		if keep[want] == nil {
			t.Errorf("no %s message captured", want)
		}
	}
	if !*updateCorpus {
		return
	}
	dir := filepath.Join("..", "..", "testdata", "decoder", "pgoutput")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, l := range labels {
		name := fmt.Sprintf("pg%d-%s.bin", ver/10000, l)
		if err := os.WriteFile(filepath.Join(dir, name), keep[l], 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Logf("wrote %d files to %s; update testdata/SHA256SUMS", len(labels), dir)
}
