// Command packgen writes internal/packs/snapshot/index.json from the pack directories next to it
// (S47 §3.8): every pack compiles, passes its cases and carries at least 12. With -check it only
// reports whether the committed index is current (CI). The index it writes is unsigned; the
// policy-packs release workflow signs it with deedglass policy packs sign.
//
//	go run ./cmd/packgen            # rewrite the snapshot index
//	go run ./cmd/packgen -check     # fail when it is stale
//	go run ./cmd/packgen -dir <dir> # any directory of packs (the policy-packs repo)
package main

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/deedglass/deedglass/internal/packs"
)

func main() {
	dir := flag.String("dir", "internal/packs/snapshot", "the directory of packs")
	check := flag.Bool("check", false, "fail when index.json is stale instead of writing it")
	flag.Parse()
	if err := run(*dir, *check); err != nil {
		fmt.Fprintln(os.Stderr, "packgen:", err)
		os.Exit(1)
	}
}

func run(dir string, check bool) error {
	src, err := packs.FromDir(dir)
	if err != nil {
		return err
	}
	ix, err := packs.BuildIndex(context.Background(), src)
	if err != nil {
		return err
	}
	want, err := packs.Canonical(ix)
	if err != nil {
		return err
	}
	path := filepath.Join(dir, packs.IndexFile)
	if check {
		got, _ := os.ReadFile(path)
		if old, err := packs.ParseIndex(got); err == nil && old.KeyID != "" {
			// A signed index carries key_id and signed_at; compare the packs only.
			old.KeyID, old.SignedAt = "", ""
			got, _ = packs.Canonical(old)
		}
		if !bytes.Equal(got, want) {
			return fmt.Errorf("%s is stale; run go run ./cmd/packgen", path)
		}
		return nil
	}
	if err := os.WriteFile(path, want, 0o644); err != nil {
		return err
	}
	// The old seal no longer matches new bytes.
	_ = os.Remove(filepath.Join(dir, packs.SealFile))
	fmt.Printf("%s · %d packs\n", path, len(ix.Packs))
	return nil
}
