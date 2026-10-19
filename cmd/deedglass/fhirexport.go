package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/deedglass/deedglass/cmd/deedglass/internal/ui"
	"github.com/deedglass/deedglass/internal/deed/archive"
	"github.com/deedglass/deedglass/internal/fhirexport"
	"github.com/deedglass/deedglass/internal/ledger"
)

// exportFHIR writes the org's records in r as FHIR R4 NDJSON from the database (HC-P10): each
// FHIR connector call with its resource ids, server and SMART client id.
func exportFHIR(ctx context.Context, e *Env, s *ledgerSession, r ledger.Range, o exportOpts) error {
	name := o.out
	if name == "" {
		name = fmt.Sprintf("deedglass-%s-%s.ndjson", s.org.ID.String(), o.format)
	}
	return writeFHIR(e, name, o.format, func(w io.Writer) (fhirexport.Stats, error) {
		st, err := s.led.ExportFHIR(ctx, s.org.ID, r, o.format, w)
		var empty *ledger.EmptyRangeError
		if errors.As(err, &empty) {
			return st, &ExitError{Code: ExitFailure, ErrCode: "not_found", Err: errors.New("No records in this range"), Next: "Widen --since, or leave it out."}
		}
		if err != nil {
			return st, ledgerErr(e, err)
		}
		return st, nil
	})
}

// exportFHIROffline converts a bundle or a .jsonl file of records with no database: each record
// alone, so a FHIR call's resource, server and client are unknown and the action id stands in for
// the target. It doesn't check seals; deedglass verify does.
func exportFHIROffline(e *Env, o exportOpts) error {
	f, err := os.Open(o.src)
	if err != nil {
		return failNext(e, ExitUsage, "export_failed", fmt.Sprintf("Can't read %s.", o.src), "Check the path, or export a bundle first: deedglass deeds export.")
	}
	defer f.Close()
	br := bufio.NewReader(f)
	var src io.Reader = br
	if head, _ := br.Peek(2); bytes.Equal(head, []byte{0x1f, 0x8b}) {
		ar, err := archive.Open(br, archive.Limits{})
		if err != nil {
			return failNext(e, ExitFailure, "bundle_invalid", fmt.Sprintf("%s isn't a readable bundle.", o.src), "Check it with deedglass verify "+o.src+".")
		}
		src = ar.Deeds()
	}
	name := o.out
	if name == "" {
		base := filepath.Base(o.src)
		for _, ext := range []string{".tar.gz", ".jsonl"} {
			if len(base) > len(ext) && base[len(base)-len(ext):] == ext {
				base = base[:len(base)-len(ext)]
			}
		}
		name = base + "-" + o.format + ".ndjson"
	}
	return writeFHIR(e, name, o.format, func(w io.Writer) (fhirexport.Stats, error) {
		fw, _ := fhirexport.NewWriter(w, o.format, fhirexport.Options{})
		sc := bufio.NewScanner(src)
		sc.Buffer(make([]byte, 0, 64<<10), 64<<10)
		for sc.Scan() {
			line := bytes.TrimSpace(sc.Bytes())
			if len(line) == 0 {
				continue
			}
			if err := fw.Write(fhirexport.Record{Envelope: append([]byte(nil), line...)}); err != nil {
				return fw.Stats, failNext(e, ExitFailure, "bundle_invalid", fmt.Sprintf("Record %d isn't a deed.", fw.Stats.Records), "Check the file with deedglass verify "+o.src+".")
			}
		}
		if err := sc.Err(); err != nil {
			return fw.Stats, failNext(e, ExitFailure, "bundle_invalid", "The records can't be read: "+err.Error()+".", "Check the file with deedglass verify "+o.src+".")
		}
		return fw.Stats, nil
	})
}

// writeFHIR runs fill into the named file (through a temporary file, renamed at the end) or to
// stdout for "-", then prints what was written.
func writeFHIR(e *Env, name, format string, fill func(io.Writer) (fhirexport.Stats, error)) error {
	if name == "-" {
		st, err := fill(e.Stdout)
		if err == nil && !e.Globals.JSON {
			fmt.Fprintln(e.Stderr, e.Style.OKLine(fhirSummary(st, format)+" to stdout"))
		}
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(name), ".deedglass-export-*.tmp")
	if err != nil {
		return failNext(e, ExitFailure, "export_failed", fmt.Sprintf("Can't write in %s.", filepath.Dir(name)), "Choose another directory with --out.")
	}
	defer func() {
		tmp.Close()
		os.Remove(tmp.Name())
	}()
	bw := bufio.NewWriter(tmp)
	st, err := fill(bw)
	if err != nil {
		return err
	}
	if err := bw.Flush(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	_ = os.Chmod(tmp.Name(), 0o644) // ids and digests only, like any export
	if err := os.Rename(tmp.Name(), name); err != nil {
		return failNext(e, ExitFailure, "export_failed", fmt.Sprintf("Can't write %s.", name), "Choose another path with --out.")
	}
	if e.Globals.JSON {
		return writeAPIJSON(e, map[string]any{"object": "deed_export", "format": format, "file": name,
			"records": st.Records, "written": st.Written, "skipped": st.Skipped})
	}
	fmt.Fprintln(e.Stdout, e.Style.OKLine(fhirSummary(st, format)))
	fmt.Fprintf(e.Stdout, "  %s %s\n", ui.Pad("file", 10), name)
	return nil
}

func fhirSummary(st fhirexport.Stats, format string) string {
	kind := plural(st.Written, "AuditEvent", "AuditEvents")
	if format == fhirexport.FormatProvenance {
		kind = plural(st.Written, "Provenance", "Provenances")
	}
	return fmt.Sprintf("%d %s %s %s read %s %d without one", st.Written, kind, ui.Sep, deedsWord(int64(st.Records)), ui.Sep, st.Skipped)
}
