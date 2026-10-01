// Command genchain writes a test chain for benchmarks: n deeds of one org, sealed by the published
// test keys (key a for seq 1–10, key b after; with -keys 1, key a only for n ≤ 10). TEST KEYS ONLY:
// the output means nothing outside benchmarks.
//
//	go run ./internal/deed/deedtest/cmd/genchain -n 100000 -keys 2 -out .tmp/deeds-100k.jsonl [-bundle .tmp/deeds-100k.tar.gz]
package main

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/deedglass/deedglass/internal/deed"
	"github.com/deedglass/deedglass/internal/deed/archive"
	"github.com/deedglass/deedglass/internal/deed/deedtest"
)

func main() {
	n := flag.Int("n", 100_000, "deeds to write")
	nkeys := flag.Int("keys", 2, "seal keys to use: 1 (key a, n ≤ 10) or 2 (a then b)")
	out := flag.String("out", "", "the .jsonl file to write")
	bundle := flag.String("bundle", "", "also write an export bundle here, sealed by key b")
	flag.Parse()
	if *out == "" || *n < 1 || (*nkeys == 1 && *n > 10) || *nkeys < 1 || *nkeys > 2 {
		fmt.Fprintln(os.Stderr, "usage: genchain -n 100000 -keys 2 -out deeds.jsonl [-bundle deeds.tar.gz]")
		os.Exit(2)
	}
	start := time.Now()
	c := deedtest.NewChain(*n, deedtest.ChainOpts{})
	if err := writeJSONL(*out, c.Lines); err != nil {
		fail(err)
	}
	if *bundle != "" {
		if err := writeBundle(*bundle, c); err != nil {
			fail(err)
		}
	}
	fmt.Printf("wrote %d deeds (%s keys) in %s\n", *n, fmt.Sprint(*nkeys), time.Since(start).Round(time.Millisecond))
}

func writeJSONL(path string, lines [][]byte) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	w := bufio.NewWriterSize(f, 1<<20)
	for _, l := range lines {
		w.Write(l)
		w.WriteByte('\n')
	}
	if err := w.Flush(); err != nil {
		return err
	}
	return f.Close()
}

func writeBundle(path string, c *deedtest.Chain) error {
	ks, err := deed.StrictCanonical(deedtest.KeySet())
	if err != nil {
		return err
	}
	var size int64
	for _, l := range c.Lines {
		size += int64(len(l)) + 1
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	bw := bufio.NewWriterSize(f, 1<<20)
	last := len(c.Deeds)
	w, err := archive.NewWriter(bw, archive.Header{Org: c.Org, Issuer: deedtest.Issuer, CreatedAt: deedtest.T0.Add(24 * time.Hour),
		Generator: archive.Generator{Name: "genchain", Version: "test", Commit: "none"},
		FirstSeq:  1, LastSeq: int64(last), From: c.Deeds[0].Body.IssuedAt, To: c.Deeds[last-1].Body.IssuedAt},
		ks, size, int64(last))
	if err != nil {
		return err
	}
	for _, l := range c.Lines {
		if err := w.WriteDeed(l); err != nil {
			return err
		}
	}
	if _, err := w.Close(deedtest.TestKey("b").Signer); err != nil {
		return err
	}
	if err := bw.Flush(); err != nil {
		return err
	}
	return f.Close()
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "genchain:", err)
	os.Exit(1)
}
