package agency

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/deedglass/deedglass/internal/weekly"
)

// Formats a bulk run writes.
const (
	FormatMD   = "md"
	FormatHTML = "html"
	FormatJSON = "json"
)

// Summarizer builds one client's summary (the same call the single-org report makes), inside that
// client's own transaction.
type Summarizer func(ctx context.Context, c Row) (weekly.Summary, error)

// BulkResult is one bulk run.
type BulkResult struct {
	Files  []string // written, relative to the folder
	Index  string   // index.md
	Failed []string // client slugs whose report couldn't be built (named in index.md)
}

// WriteReports writes one report per client (<client-slug>-<period>.<ext>) and index.md into dir,
// which must not hold those files already. Each report is the client's own single-org report plus
// the brand lines (TestBulkEqualsSingle).
func WriteReports(ctx context.Context, dir string, clients []Row, p weekly.Period, formats []string, brand *weekly.Brand, sum Summarizer, opts weekly.Options) (BulkResult, error) {
	if len(formats) == 0 {
		formats = []string{FormatHTML, FormatMD, FormatJSON}
	}
	for _, f := range formats {
		if f != FormatMD && f != FormatHTML && f != FormatJSON {
			return BulkResult{}, fmt.Errorf("agency: unknown format %q; use md, html or json", f)
		}
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return BulkResult{}, err
	}
	var res BulkResult
	type line struct{ slug, name, files, numbers string }
	var lines []line
	opts.Brand = brand
	for _, c := range clients {
		if ctx.Err() != nil {
			return res, ctx.Err()
		}
		slug := safeSlug(c.Slug, c.Org)
		s, err := sum(ctx, c)
		if err != nil {
			res.Failed = append(res.Failed, slug)
			lines = append(lines, line{slug: slug, name: c.Name, numbers: "Couldn't build this report: run deedglass report --org " + slug})
			continue
		}
		var names []string
		for _, f := range formats {
			name := slug + "-" + p.Key + "." + f
			var body []byte
			switch f {
			case FormatMD:
				body = []byte(weekly.Markdown(s, opts))
			case FormatHTML:
				h, err := weekly.HTML(s, opts)
				if err != nil {
					return res, err
				}
				body = []byte(h)
			case FormatJSON:
				body, err = weekly.JSON(s)
				if err != nil {
					return res, err
				}
			}
			if err := writeNew(filepath.Join(dir, name), body); err != nil {
				return res, err
			}
			res.Files = append(res.Files, name)
			names = append(names, name)
		}
		lines = append(lines, line{slug: slug, name: s.Org.Name, files: strings.Join(names, ", "), numbers: s.NumbersLine() + " " + s.CheckLine() + "."})
	}
	var b strings.Builder
	b.WriteString("# Client reports · " + p.Label + "\n\n")
	if brand != nil {
		b.WriteString("Prepared by " + brand.Name + " with Deedglass.\n\n")
	}
	fmt.Fprintf(&b, "%d %s. Each report is built from that client's own records, in its own org; no report holds another client's data.\n\n",
		len(clients), plural(len(clients), "client", "clients"))
	b.WriteString("| Client | Summary | Files |\n|---|---|---|\n")
	for _, l := range lines {
		b.WriteString("| " + cell(l.name) + " (" + cell(l.slug) + ") | " + cell(l.numbers) + " | " + cell(l.files) + " |\n")
	}
	if err := writeNew(filepath.Join(dir, "index.md"), []byte(b.String())); err != nil {
		return res, err
	}
	res.Index = "index.md"
	return res, nil
}

// ErrExists is a report file that is already there: a bulk run never overwrites.
var ErrExists = errors.New("agency: a report file already exists")

func writeNew(path string, b []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return fmt.Errorf("%w: %s", ErrExists, filepath.Base(path))
		}
		return err
	}
	_, werr := f.Write(b)
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	return werr
}

func safeSlug(slug, id string) string {
	ok := slug != "" && len(slug) <= 64
	for _, r := range slug {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-') {
			ok = false
		}
	}
	if ok {
		return slug
	}
	return id
}

func cell(s string) string {
	return strings.NewReplacer("|", `\|`, "\n", " ", "\r", " ").Replace(s)
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
