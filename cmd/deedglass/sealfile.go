package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/deedglass/deedglass/cmd/deedglass/internal/ui"
	"github.com/deedglass/deedglass/internal/deed"
	"github.com/deedglass/deedglass/internal/ledger"
)

// deedglass deeds seal-file (S38, carried over from S37 §3.9): seal a file you hold as the admin
// deed file.sealed. The file is hashed here; only its sha256, size, media type and label go to
// POST /v1/deeds/files. The deed envelope is written next to the file (<path>.deed.json), where
// deedglass verify <deed.json> --file <path> checks the two together, offline.

type sealFileOpts struct {
	label, mediaType, out string
}

// parseSealFileArgs reads flags before and after the path (the command has RawArgs).
func parseSealFileArgs(args []string) (sealFileOpts, []string, error) {
	var o sealFileOpts
	var paths []string
	for len(args) > 0 {
		if args[0] == "--" {
			paths = append(paths, args[1:]...)
			break
		}
		fs := flag.NewFlagSet("deedglass deeds seal-file", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		var more sealFileOpts
		fs.StringVar(&more.label, "label", "", "what the file is, 1 to 120 characters (it goes into the deed)")
		fs.StringVar(&more.mediaType, "media-type", "", "application/pdf, text/markdown, application/json, text/plain or text/html (default: from the extension)")
		fs.StringVar(&more.out, "out", "", "where to write the deed (default <path>.deed.json)")
		if err := fs.Parse(args); err != nil {
			return o, nil, usageErr(fmt.Errorf("deedglass deeds seal-file: %v", err), "Run deedglass help deeds seal-file for its flags.")
		}
		fs.Visit(func(f *flag.Flag) {
			switch f.Name {
			case "label":
				o.label = more.label
			case "media-type":
				o.mediaType = more.mediaType
			case "out":
				o.out = more.out
			}
		})
		args = fs.Args()
		if len(args) > 0 && args[0] != "--" {
			paths = append(paths, args[0])
			args = args[1:]
		}
	}
	return o, paths, nil
}

// mediaTypeOf is the media type a file's extension implies, or "".
func mediaTypeOf(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".pdf":
		return "application/pdf"
	case ".md", ".markdown":
		return "text/markdown"
	case ".json":
		return "application/json"
	case ".txt":
		return "text/plain"
	case ".html", ".htm":
		return "text/html"
	}
	return ""
}

// hashFile streams a file through SHA-256: the digest as sha256:<hex> and the size in bytes.
func hashFile(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return "", 0, err
	}
	if !fi.Mode().IsRegular() {
		return "", 0, errors.New("not a regular file")
	}
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return "", 0, err
	}
	return deed.DigestPrefix + hex.EncodeToString(h.Sum(nil)), n, nil
}

const sealFileExample = `deedglass deeds seal-file report.pdf --label "Full scan report · Acme"`

func runDeedsSealFile(ctx context.Context, e *Env, args []string) error {
	o, paths, err := parseSealFileArgs(args)
	if err != nil {
		return err
	}
	if len(paths) != 1 {
		return usageErr(errors.New("deedglass deeds seal-file takes one file"), "For example: "+sealFileExample)
	}
	path := paths[0]
	if strings.TrimSpace(o.label) == "" {
		return usageErr(errors.New("--label is required: it says what the file is, inside the deed"), "For example: "+sealFileExample)
	}
	if !ledger.ValidFileLabel(o.label) {
		return usageErr(fmt.Errorf("--label must be 1 to %d characters with no line breaks or control characters", ledger.MaxFileLabel),
			"Shorten the label, for example \"Full scan report · Acme\".")
	}
	mt := o.mediaType
	if mt == "" {
		mt = mediaTypeOf(path)
	}
	if !ledger.ValidFileMediaType(mt) {
		what := fmt.Sprintf("%q isn't a media type a sealed file can have", clean(mt))
		if mt == "" {
			what = fmt.Sprintf("%s has no extension that names its media type", clean(filepath.Base(path)))
		}
		return usageErr(errors.New(what), "Pass --media-type application/pdf, text/markdown, application/json, text/plain or text/html.")
	}
	out := o.out
	if out == "" {
		out = path + ".deed.json"
	}
	sum, size, err := hashFile(path)
	if err != nil {
		return inputErr("input_unreadable", fmt.Errorf("Can't read %s: %s", clean(path), reason(err)), "Check the path.")
	}
	c, err := apiClient(e)
	if err != nil {
		return err
	}
	var res ledger.SealedFile
	if _, err := c.Do(ctx, "POST", "/v1/deeds/files", ledger.SealFileBody{SHA256: sum, Bytes: &size, MediaType: mt, Label: o.label}, &res); err != nil {
		return apiErr(e, c, err)
	}
	if res.Deed == nil || res.SHA256 != sum || res.Bytes != size {
		return &ExitError{Code: ExitFailure, ErrCode: "internal_error", Err: errors.New("The server's answer doesn't match the file that was sent"),
			Next: "Run seal-file again; if it repeats, check the server version with deedglass version --server."}
	}
	raw, err := res.Deed.MarshalCanonical()
	if err != nil {
		return err
	}
	if err := os.WriteFile(out, append(raw, '\n'), 0o644); err != nil {
		return &ExitError{Code: ExitFailure, ErrCode: "write_failed", Err: fmt.Errorf("Sealed as %s, but %s couldn't be written: %s", res.Deed.ID, clean(out), reason(err)),
			Next: "Fetch the deed with GET /v1/deeds/" + res.Deed.ID + " and save it next to the file."}
	}
	footer := fmt.Sprintf("Sealed as %s %s %s %s check it with deedglass verify", res.Deed.ID, ui.Sep, shortHash(sum), ui.Sep)
	if e.Globals.JSON {
		return writeJSON(e, map[string]any{"object": "sealed_file", "file": path, "sha256": sum, "bytes": size, "media_type": mt,
			"label": o.label, "deed": res.Deed.ID, "out": out, "footer": footer})
	}
	s := e.Style
	fmt.Fprintln(e.Stdout, s.OKLine(fmt.Sprintf("Sealed %s %s %s bytes %s %s", clean(filepath.Base(path)), ui.Sep, deed.FormatCount(size), ui.Sep, shortHash(sum))))
	fmt.Fprintf(e.Stdout, "  %s  written to %s\n", res.Deed.ID, clean(out))
	fmt.Fprintf(e.Stdout, "  Footer: %s\n", footer)
	return nil
}

// --- verify --file ---

// fileCheck is verify --file's result, added to the verification report.
type fileCheck struct {
	Object    string `json:"object"` // "file_check"
	Path      string `json:"path"`
	Matches   bool   `json:"matches"`
	SHA256    string `json:"sha256"`
	Bytes     int64  `json:"bytes"`
	MediaType string `json:"media_type,omitempty"`
	Label     string `json:"label"`
	Deed      string `json:"deed"`
}

// checkSealedFile compares a file with a verified file.sealed deed: its sha256 and size, with the
// label the deed carries and each media type a sealed file may have, must give the deed's
// data_digest. A deed of another kind is a usage error (exit 2).
func checkSealedFile(raw []byte, path string) (*fileCheck, error) {
	env, err := deed.ParseEnvelope(raw)
	if err != nil {
		return nil, err
	}
	b := env.Body
	if b.Kind != "admin" || b.Admin == nil || b.Admin.Action != ledger.AdminFileSealed || b.Admin.Reason == nil || b.Admin.DataDigest == nil {
		what := string(b.Kind)
		if b.Admin != nil {
			what = b.Admin.Action
		}
		return nil, &ExitError{Code: ExitUsage, ErrCode: "not_a_sealed_file", Err: fmt.Errorf("%s isn't a sealed file (it records %s)", env.ID, clean(what)),
			Next: "Pass the .deed.json that deedglass deeds seal-file wrote next to the file."}
	}
	sum, size, err := hashFile(path)
	if err != nil {
		return nil, inputErr("input_unreadable", fmt.Errorf("Can't read %s: %s", clean(path), reason(err)), "Check the path.")
	}
	fc := &fileCheck{Object: "file_check", Path: path, SHA256: sum, Bytes: size, Label: *b.Admin.Reason, Deed: env.ID}
	for _, mt := range ledger.FileMediaTypes {
		d, err := ledger.FileDataDigest(ledger.FileSealedData{Bytes: size, Label: *b.Admin.Reason, MediaType: mt, SHA256: sum})
		if err != nil {
			return nil, err
		}
		if d == *b.Admin.DataDigest {
			fc.Matches, fc.MediaType = true, mt
			break
		}
	}
	return fc, nil
}

// printFileCheck adds verify --file's line after the deed's own.
func printFileCheck(e *Env, fc *fileCheck) {
	s := e.Style
	name := clean(filepath.Base(fc.Path))
	if fc.Matches {
		fmt.Fprintln(e.Stdout, s.OKLine(fmt.Sprintf("File matches %s %s %s %s bytes %s %s", ui.Sep, name, ui.Sep, deed.FormatCount(fc.Bytes), ui.Sep, shortHash(fc.SHA256))))
		fmt.Fprintln(e.Stdout, vrow("label", clean(fc.Label)))
		return
	}
	fmt.Fprintln(e.Stdout, s.FailLine(fmt.Sprintf("%s doesn't match the deed. Its sha256 is %s (%s bytes); the deed sealed another file labelled %q.",
		name, shortHash(fc.SHA256), deed.FormatCount(fc.Bytes), clean(fc.Label))))
	fmt.Fprintln(e.Stdout, vrow("next", "Check that you have the file that was sealed. One changed byte gives a different sha256."))
}
