package main

import (
	"bufio"
	"bytes"
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/deedglass/deedglass/internal/buildinfo"
	"github.com/deedglass/deedglass/internal/cli/ui"
	"github.com/deedglass/deedglass/internal/deed/archive"
	"github.com/deedglass/deedglass/internal/recordexport"
)

// deedglass export (TY-06): sealed records from a bundle or a .jsonl file, written as OpenTelemetry
// GenAI spans, OCSF API Activity events, a W3C PROV-JSON document or FHIR AuditEvent/Provenance.
// Offline: no config, database, network or key is read. It doesn't check seals (deedglass verify
// does), but a bundle whose files don't match its manifest is refused.

var recordExportValues struct {
	format, out string
}

func recordExportFlags(fs *flag.FlagSet) {
	o := &recordExportValues
	*o = struct{ format, out string }{}
	fs.StringVar(&o.format, "format", "", "otel-genai, ocsf, prov-json or fhir")
	fs.StringVar(&o.out, "out", "", "the file to write, or - for stdout")
}

const recordExportUsage = "deedglass export --format otel-genai deeds.tar.gz"

func runRecordExport(_ context.Context, e *Env, args []string) error {
	pos, err := mixedArgs("deedglass export", recordExportFlags, args)
	if err != nil {
		return err
	}
	o := recordExportValues
	if o.format == "" {
		return usageErr(fmt.Errorf("deedglass export needs --format (%s)", strings.Join(recordexport.Formats, ", ")), recordExportUsage)
	}
	known := false
	for _, f := range recordexport.Formats {
		known = known || f == o.format
	}
	if !known {
		return usageErr(fmt.Errorf("--format must be %s (got %q)", strings.Join(recordexport.Formats, ", "), o.format), recordExportUsage)
	}
	src, err := oneArg("deedglass export", "one bundle or .jsonl file (or - for stdin)", recordExportUsage, pos)
	if err != nil {
		return err
	}

	var in io.Reader
	if src == "-" {
		in = e.Stdin
		if in == nil {
			in = os.Stdin
		}
	} else {
		f, err := os.Open(src)
		if err != nil {
			return failNext(e, ExitUsage, "export_failed", fmt.Sprintf("Can't read %s.", src), "Check the path, or export a bundle first: deedglass deeds export.")
		}
		defer f.Close()
		in = f
	}
	br := bufio.NewReader(in)
	var (
		records io.Reader = br
		bundle  *archive.Reader
	)
	if head, _ := br.Peek(2); bytes.Equal(head, []byte{0x1f, 0x8b}) {
		bundle, err = archive.Open(br, archive.Limits{})
		if err != nil {
			return failNext(e, ExitFailure, "bundle_invalid", fmt.Sprintf("%s isn't a readable bundle.", src), "Check it with deedglass verify "+src+".")
		}
		records = bundle.Deeds()
	}

	name := o.out
	if name == "" {
		if src == "-" {
			name = "-"
		} else {
			base := filepath.Base(src)
			for _, ext := range []string{".tar.gz", ".jsonl"} {
				base = strings.TrimSuffix(base, ext)
			}
			name = base + recordexport.Extension(o.format)
		}
	}

	fill := func(w io.Writer) (recordexport.Stats, error) {
		xw, _ := recordexport.NewWriter(w, o.format, recordexport.Options{Producer: buildinfo.Version})
		sc := bufio.NewScanner(records)
		sc.Buffer(make([]byte, 0, 64<<10), 64<<10)
		for sc.Scan() {
			line := bytes.TrimSpace(sc.Bytes())
			if len(line) == 0 {
				continue
			}
			if err := xw.Write(append([]byte(nil), line...)); err != nil {
				return xw.Stats(), failNext(e, ExitFailure, "bundle_invalid", fmt.Sprintf("Record %d isn't a record this export can read.", xw.Stats().Records), "Check the file with deedglass verify "+src+".")
			}
		}
		if err := sc.Err(); err != nil {
			return xw.Stats(), failNext(e, ExitFailure, "bundle_invalid", "The records can't be read: "+err.Error()+".", "Check the file with deedglass verify "+src+".")
		}
		if bundle != nil {
			if _, _, err := bundle.Finish(); err != nil {
				return xw.Stats(), failNext(e, ExitFailure, "bundle_invalid", fmt.Sprintf("%s doesn't match its manifest.", src), "Check it with deedglass verify "+src+".")
			}
		}
		return xw.Stats(), xw.Close()
	}
	return writeRecordExport(e, name, o.format, fill)
}

// writeRecordExport runs fill into the named file (a temporary file renamed at the end, so a
// failed export leaves nothing) or to stdout for "-", then prints what was written.
func writeRecordExport(e *Env, name, format string, fill func(io.Writer) (recordexport.Stats, error)) error {
	if name == "-" {
		bw := bufio.NewWriter(e.Stdout)
		st, err := fill(bw)
		if err != nil {
			return err
		}
		if err := bw.Flush(); err != nil {
			return err
		}
		if !e.Globals.JSON {
			fmt.Fprintln(e.Stderr, e.Style.OKLine(recordExportSummary(st, format)+" to stdout"))
		}
		return nil
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
		return writeAPIJSON(e, map[string]any{"object": "record_export", "format": format, "file": name,
			"records": st.Records, "written": st.Written, "skipped": st.Skipped})
	}
	fmt.Fprintln(e.Stdout, e.Style.OKLine(recordExportSummary(st, format)))
	fmt.Fprintf(e.Stdout, "  %s %s\n", ui.Pad("file", 10), name)
	return nil
}

func recordExportSummary(st recordexport.Stats, format string) string {
	var what string
	switch format {
	case recordexport.FormatOTel:
		what = plural(st.Written, "span", "spans")
	case recordexport.FormatOCSF:
		what = plural(st.Written, "OCSF event", "OCSF events")
	case recordexport.FormatPROV:
		return fmt.Sprintf("1 PROV-JSON document %s %s read", ui.Sep, deedsWord(int64(st.Records)))
	default:
		what = plural(st.Written, "FHIR resource", "FHIR resources")
	}
	return fmt.Sprintf("%d %s %s %s read %s %d without one", st.Written, what, ui.Sep, deedsWord(int64(st.Records)), ui.Sep, st.Skipped)
}
