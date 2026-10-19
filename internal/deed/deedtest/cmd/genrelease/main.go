// Command genrelease writes the release bundle for TY-11, the 2046 test: a small export bundle
// sealed by the deed and bundle code of one release, byte-identical every time it runs.
// scripts/release-bundles.sh copies this file into a checkout of the release's tag and runs it
// there, so the bundle is that release's writer and sealer output, not today's. TEST KEYS ONLY
// (testdata/deeds/keys/test-keys.json): the bundle says nothing about any real org.
//
//	go run ./internal/deed/deedtest/cmd/genrelease -version 0.4.0 -commit 8b8a162 -out testdata/release-bundles/0.4.0
//
// It writes <out>/bundle.tar.gz (12 deeds of one org: actions verified, approved, denied and with a
// Unicode agent name, an admin stop and two amendments; key a seals 1–10, key b 11–12 after the
// rotation, and key b seals the manifest) and <out>/keys.json (the test key set, the keys a reader
// trusts out of band). It uses only APIs that have been in internal/deed since v0.1.0; a later
// change to them changes this file, never a committed bundle.
package main

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/deedglass/deedglass/internal/deed"
	"github.com/deedglass/deedglass/internal/deed/archive"
	"github.com/deedglass/deedglass/internal/deed/deedtest"
)

// Deeds is how many deeds every release bundle holds.
const Deeds = 12

func main() {
	version := flag.String("version", "", "the release's version, without the v: 0.4.0")
	commit := flag.String("commit", "", "the release's short commit")
	out := flag.String("out", "", "the directory to write bundle.tar.gz and keys.json into")
	flag.Parse()
	if *version == "" || *commit == "" || *out == "" {
		fmt.Fprintln(os.Stderr, "usage: genrelease -version 0.4.0 -commit 8b8a162 -out testdata/release-bundles/0.4.0")
		os.Exit(2)
	}
	if err := run(*version, *commit, *out); err != nil {
		fmt.Fprintln(os.Stderr, "genrelease:", err)
		os.Exit(1)
	}
}

// body is deed seq's draft: one of each kind and shape a release writes.
func body(org string, seq int, sealed []*deed.Envelope) deed.Body {
	switch seq {
	case 2:
		return deedtest.ActionApproved(org, seq)
	case 3:
		return deedtest.ActionDenied(org, seq)
	case 4:
		return deedtest.UnicodeAgent(org, seq)
	case 5:
		return deedtest.AdminStopped(org, seq)
	case 6:
		return deedtest.Amendment(&sealed[0].Body) // a later L2 MISMATCH about deed 1
	case 12:
		return deedtest.Amendment(&sealed[10].Body) // the same, after the key rotation
	}
	return deedtest.ActionVerified(org, seq)
}

func run(version, commit, out string) error {
	org := deedtest.Org(4)
	a, b := deedtest.TestKey("a"), deedtest.TestKey("b")
	var sealed []*deed.Envelope
	var lines [][]byte
	var size int64
	prev := deed.Genesis(org)
	for i := 1; i <= Deeds; i++ {
		k := a
		if i > 10 {
			k = b // key a retires at T0+10s (deedtest.ARetired), when deed 11 is sealed
		}
		d := body(org, i, sealed)
		d.DeedID = deedtest.ID("deed", 400000+i)
		d.Seq = int64(i)
		d.PrevHash = prev
		d.IssuedAt = deed.FormatTime(deedtest.T0.Add(time.Duration(i-1) * time.Second))
		d.SealKeyID = k.ID
		env, err := deed.SealBody(k.Signer, d)
		if err != nil {
			return fmt.Errorf("seal deed %d: %w", i, err)
		}
		line, err := env.MarshalCanonical()
		if err != nil {
			return err
		}
		sealed, lines = append(sealed, env), append(lines, line)
		size += int64(len(line)) + 1
		prev = env.BodyHash
	}
	ks, err := deed.StrictCanonical(deedtest.KeySet())
	if err != nil {
		return err
	}
	if err := os.MkdirAll(out, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(out, "keys.json"), append(ks, '\n'), 0o644); err != nil {
		return err
	}
	f, err := os.Create(filepath.Join(out, "bundle.tar.gz"))
	if err != nil {
		return err
	}
	bw := bufio.NewWriter(f)
	w, err := archive.NewWriter(bw, archive.Header{Org: org, Issuer: deedtest.Issuer, CreatedAt: deedtest.T0.Add(time.Hour),
		Generator: archive.Generator{Name: "deedglass-genrelease", Version: version, Commit: commit},
		FirstSeq:  1, LastSeq: Deeds, From: sealed[0].Body.IssuedAt, To: sealed[Deeds-1].Body.IssuedAt},
		ks, size, Deeds)
	if err != nil {
		return err
	}
	for _, l := range lines {
		if err := w.WriteDeed(l); err != nil {
			return err
		}
	}
	if _, err := w.Close(b.Signer); err != nil {
		return err
	}
	if err := bw.Flush(); err != nil {
		return err
	}
	return f.Close()
}
