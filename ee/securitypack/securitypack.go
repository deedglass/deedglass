// Package securitypack is Pro's security review pack (S51 §3.3): the print-ready HTML document,
// the same answers in Markdown and JSON, the period's records as an export bundle, aliases, and a
// seal over every file, written as one folder (a tar stream the CLI unpacks).
//
// It never reads a licence: serve asks cmd/deedglass/licence.go for security_pack and passes this
// builder, or nil. The answers, the readiness view and the offline check stay free
// (internal/securitypack).
//
// Licensed under ee/LICENSE (FSL-1.1-ALv2).
package securitypack

import (
	"archive/tar"
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/deedglass/deedglass/internal/embedgz"
	"github.com/deedglass/deedglass/internal/securitypack"
)

// pack.html is embedded gzip-compressed (pack.tar.gz, make embed-gz; the image budget).
//
//go:embed pack.tar.gz
var packGz []byte

var packHTML = embedgz.MustString(embedgz.NewFiles(packGz), "pack.html")()

var pageTmpl = template.Must(template.New("pack").Parse(packHTML))

// Builder writes pack folders.
type Builder struct{}

// New returns the builder.
func New() *Builder { return &Builder{} }

var _ securitypack.Builder = (*Builder)(nil)

// ErrIncomplete: the request is missing its facts or a port (a wiring bug; nothing was written).
var ErrIncomplete = errors.New("securitypack: the build request is incomplete")

// Build stages the folder in a temporary directory (the records bundle has to be hashed before
// it is sealed, and a tar header needs each size), seals every file, then streams the folder.
func (*Builder) Build(ctx context.Context, req securitypack.BuildRequest, w io.Writer) (*securitypack.BuildResult, error) {
	f := req.Facts
	if f == nil || req.Seal == nil || req.Keys == nil || (req.Records && req.Export == nil) {
		return nil, ErrIncomplete
	}
	dir, err := os.MkdirTemp(req.TempDir, "deedglass-pack-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)

	res := &securitypack.BuildResult{Aliases: req.Aliases}
	var recs *securitypack.RecordsRef
	if req.Records {
		recs, err = exportRecords(ctx, req, dir)
		if err != nil {
			return nil, err
		}
		if recs != nil {
			res.Records = recs.Deeds
			f.Records.BundleDigest, f.Records.BundleCount = recs.SHA256, recs.Deeds
		}
	}
	var aliases *securitypack.Aliases
	if req.Aliases {
		aliases = securitypack.NewAliases(f)
	}
	p, err := securitypack.Build(ctx, f, securitypack.Options{Records: recs, Aliases: aliases})
	if err != nil {
		return nil, err
	}
	res.Answers, res.Summary = len(p.Answers), p.Summary

	answers, err := securitypack.MarshalPack(p)
	if err != nil {
		return nil, err
	}
	files := map[string][]byte{securitypack.FileAnswers: answers}
	files[securitypack.FileMarkdown] = []byte(noteMD(req.Note) + Markdown(p))
	files[securitypack.FileReadme] = []byte(noteText(req.Note) + Readme(p, req.Aliases))
	page, err := htmlPage(p, securitypack.HashBytes(answers), req.Note)
	if err != nil {
		return nil, err
	}
	files[securitypack.FileIndex] = page
	for name, b := range files {
		if err := os.WriteFile(filepath.Join(dir, name), b, 0o600); err != nil {
			return nil, err
		}
	}

	seal := &securitypack.SealFile{Object: securitypack.ObjectSeal, V: 1, Org: p.Org, Files: []securitypack.SealedFile{}}
	for _, name := range securitypack.SealedOrder {
		path := filepath.Join(dir, name)
		if _, err := os.Stat(path); err != nil {
			continue // records.tar.gz with --no-records
		}
		sum, n, err := securitypack.HashFile(path)
		if err != nil {
			return nil, err
		}
		d := securitypack.FileData{SHA256: sum, Bytes: n, MediaType: securitypack.MediaTypes[name], Label: securitypack.FileLabel(name, p.Org)}
		env, err := req.Seal(ctx, d)
		if err != nil {
			return nil, fmt.Errorf("securitypack: sealing %s: %w", name, err)
		}
		seal.Files = append(seal.Files, securitypack.SealedFile{Path: name, SHA256: sum, Bytes: n, MediaType: d.MediaType, Label: d.Label, Deed: env})
	}
	if seal.Keys, err = req.Keys(ctx); err != nil {
		return nil, err
	}
	res.Sealed = len(seal.Files)
	sb, err := securitypack.MarshalSeal(seal)
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(dir, securitypack.FileSeal), sb, 0o600); err != nil {
		return nil, err
	}
	order := append(append([]string{}, securitypack.SealedOrder...), securitypack.FileSeal)
	if aliases != nil {
		ab, err := json.MarshalIndent(aliases, "", "  ")
		if err != nil {
			return nil, err
		}
		if err := os.WriteFile(filepath.Join(dir, securitypack.FileAliases), append(ab, '\n'), 0o600); err != nil {
			return nil, err
		}
		order = append(order, securitypack.FileAliases)
	}
	res.Folder = FolderName(p)
	mtime, _ := time.Parse(time.RFC3339, p.GeneratedAt)
	if err := writeTar(w, dir, res.Folder, order, mtime); err != nil {
		return nil, err
	}
	return res, nil
}

// exportRecords writes records.tar.gz for the period and describes it; nil when there are none.
func exportRecords(ctx context.Context, req securitypack.BuildRequest, dir string) (*securitypack.RecordsRef, error) {
	path := filepath.Join(dir, securitypack.FileRecords)
	out, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	n, err := req.Export(ctx, req.Facts.Period.From, req.Facts.Period.To, out)
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if errors.Is(err, securitypack.ErrNoRecords) {
		_ = os.Remove(path)
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	sum, _, err := securitypack.HashFile(path)
	if err != nil {
		return nil, err
	}
	return &securitypack.RecordsRef{File: securitypack.FileRecords, Deeds: n, SHA256: sum}, nil
}

// writeTar streams the folder: fixed order, 0644 files under one 0755 directory, owner 0, one mtime.
func writeTar(w io.Writer, dir, folder string, order []string, mtime time.Time) error {
	tw := tar.NewWriter(w)
	if err := tw.WriteHeader(&tar.Header{Typeflag: tar.TypeDir, Name: folder + "/", Mode: 0o755, ModTime: mtime, Format: tar.FormatPAX}); err != nil {
		return err
	}
	for _, name := range order {
		path := filepath.Join(dir, name)
		fi, err := os.Stat(path)
		if err != nil {
			continue
		}
		mode := int64(0o644)
		if name == securitypack.FileAliases {
			mode = 0o600
		}
		if err := tw.WriteHeader(&tar.Header{Typeflag: tar.TypeReg, Name: folder + "/" + name, Mode: mode, Size: fi.Size(),
			ModTime: mtime, Format: tar.FormatPAX}); err != nil {
			return err
		}
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		_, err = io.Copy(tw, f)
		f.Close()
		if err != nil {
			return err
		}
	}
	return tw.Close()
}

var reSlug = regexp.MustCompile(`[^a-z0-9]+`)

// FolderName is security-pack-<org slug>-<YYYY-MM-DD>.
func FolderName(p *securitypack.Pack) string {
	slug := strings.Trim(reSlug.ReplaceAllString(strings.ToLower(p.OrgName), "-"), "-")
	if len(slug) > 40 {
		slug = strings.Trim(slug[:40], "-")
	}
	if slug == "" {
		slug = "org"
	}
	day := p.Period.To
	if len(day) >= 10 {
		day = day[:10]
	}
	return "security-pack-" + slug + "-" + day
}

// --- the HTML document ---

type pageRow struct {
	ID, Question, Answer, Check, Refs string
	Status                            securitypack.Status
	Word                              string
	Evidence                          []securitypack.EvidenceItem
}

type pageGroup struct {
	Number    int
	ID, Title string
	Rows      []pageRow
}

type pageData struct {
	Org, OrgID, Version, Period, PeriodISO, FromWords, Built, Note string
	RecordsLine, ChainLine, SealedLine, AnswersShort               string
	Summary                                                        []struct {
		N    string
		Word string
	}
	Groups []pageGroup
}

func orgName(p *securitypack.Pack) string {
	if strings.TrimSpace(p.OrgName) == "" {
		return p.Org
	}
	return p.OrgName
}

func day(s string) string {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return s
	}
	return t.UTC().Format("2 January 2006")
}

func recordsLine(p *securitypack.Pack) string {
	if p.Records == nil {
		return "Not included in this pack."
	}
	return fmt.Sprintf("%s sealed records in records.tar.gz, sha256 %s", securitypack.Count(p.Records.Deeds), securitypack.Short(p.Records.SHA256))
}

func chainLine(p *securitypack.Pack) string {
	if !p.Chain.Intact {
		return "Not intact over the period (see Records below)."
	}
	if p.Chain.Head == "" {
		return "Intact (no records in the period)."
	}
	return "Intact, head " + securitypack.Short(p.Chain.Head)
}

// HTML renders index.html. answersDigest is answers.json's sha256, printed in the running footer.
func HTML(p *securitypack.Pack, answersDigest string) ([]byte, error) {
	return htmlPage(p, answersDigest, "")
}

func noteMD(n string) string {
	if strings.TrimSpace(n) == "" {
		return ""
	}
	return "> " + mdEscape(strings.TrimSpace(n)) + "\n\n"
}

func noteText(n string) string {
	if strings.TrimSpace(n) == "" {
		return ""
	}
	return strings.TrimSpace(n) + "\n\n"
}

func htmlPage(p *securitypack.Pack, answersDigest, note string) ([]byte, error) {
	d := pageData{Note: strings.TrimSpace(note), Org: orgName(p), OrgID: p.Org, Version: p.DeedglassVersion, Period: securitypack.PeriodWords(p.Period),
		PeriodISO: p.Period.From[:min(10, len(p.Period.From))] + " to " + p.Period.To[:min(10, len(p.Period.To))],
		FromWords: day(p.Period.From), Built: day(p.GeneratedAt), RecordsLine: recordsLine(p), ChainLine: chainLine(p),
		AnswersShort: securitypack.Short(answersDigest),
		SealedLine:   "pack.seal.json"}
	for _, it := range p.Summary.Items() {
		d.Summary = append(d.Summary, struct {
			N    string
			Word string
		}{securitypack.Count(int64(it.N)), strings.ToLower(it.Status.Word())})
	}
	for _, g := range p.Groups() {
		pg := pageGroup{Number: g.Number, ID: g.ID, Title: g.Title}
		for _, a := range g.Answers {
			pg.Rows = append(pg.Rows, pageRow{ID: a.ID, Question: a.Question, Answer: a.Answer, Check: a.Check,
				Refs: securitypack.RefLine(a.Refs), Status: a.Status, Word: a.Status.Word(), Evidence: securitypack.EvidenceItems(a)})
		}
		d.Groups = append(d.Groups, pg)
	}
	var buf bytes.Buffer
	if err := pageTmpl.Execute(&buf, d); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// --- Markdown and README ---

// mdEscape keeps text from turning into Markdown markup.
func mdEscape(s string) string {
	r := strings.NewReplacer(`\`, `\\`, "*", `\*`, "_", `\_`, "`", "'", "[", `\[`, "]", `\]`, "<", "&lt;", ">", "&gt;", "|", `\|`)
	return r.Replace(s)
}

// Markdown renders answers.md: the same answers as index.html.
func Markdown(p *securitypack.Pack) string {
	var b strings.Builder
	org := orgName(p)
	fmt.Fprintf(&b, "# How %s controls its AI agents\n\n", mdEscape(org))
	fmt.Fprintf(&b, "Answers built from %s's own Deedglass records, %s. Every number below is counted from those records; the last section shows how to check them.\n\n",
		mdEscape(org), securitypack.PeriodWords(p.Period))
	fmt.Fprintf(&b, "**%s**\n\n", p.Summary.Line())
	fmt.Fprintf(&b, "- Org: `%s`\n- Built: %s by Deedglass %s\n- Records: %s\n- Chain: %s\n\n", p.Org, day(p.GeneratedAt), mdEscape(p.DeedglassVersion),
		mdEscape(recordsLine(p)), mdEscape(chainLine(p)))
	for _, g := range p.Groups() {
		fmt.Fprintf(&b, "## %d. %s\n\n", g.Number, g.Title)
		for _, a := range g.Answers {
			fmt.Fprintf(&b, "### %s\n\n**%s.** %s\n\n", mdEscape(a.Question), a.Status.Word(), mdEscape(a.Answer))
			if line := securitypack.EvidenceLine(a); line != "" {
				fmt.Fprintf(&b, "Evidence: `%s`\n\n", strings.ReplaceAll(line, "`", "'"))
			}
			fmt.Fprintf(&b, "How to check: %s\n\n", mdEscape(a.Check))
			if refs := securitypack.RefLine(a.Refs); refs != "" {
				fmt.Fprintf(&b, "Related: %s\n\n", mdEscape(refs))
			}
		}
	}
	b.WriteString("## What this pack doesn't show\n\n")
	b.WriteString("- Agents or tools that never go through Deedglass.\n")
	b.WriteString("- Changes on databases or tables Deedglass doesn't watch.\n")
	b.WriteString("- Whether the rules suit your business: the pack shows what the enforced rules do on four fixed inputs, not on every input.\n")
	fmt.Fprintf(&b, "- Anything before %s or outside this org, and anything about models or prompts.\n", day(p.Period.From))
	b.WriteString("- An outside opinion: Deedglass wrote this pack from the org's own records; no third party reviewed it.\n\n")
	b.WriteString("## How to check this pack\n\n")
	b.WriteString("Run `deedglass security-pack verify <folder>` (no account needed). It checks every file against its sealed record in pack.seal.json and every record in records.tar.gz. Pin the org's own keys with `--keys keys.json`.\n")
	return b.String()
}

// Readme renders README.txt.
func Readme(p *securitypack.Pack, aliases bool) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Security review pack · %s · %s\n", orgName(p), securitypack.PeriodWords(p.Period))
	fmt.Fprintf(&b, "Built by Deedglass %s on %s for org %s.\n\n", p.DeedglassVersion, day(p.GeneratedAt), p.Org)
	b.WriteString("What's here\n")
	row := func(name, what string) { fmt.Fprintf(&b, "  %-22s %s\n", name, what) }
	row(securitypack.FileIndex, "The document. Open it in a browser; print it or save it as PDF from there.")
	row(securitypack.FileMarkdown, "The same answers in Markdown.")
	row(securitypack.FileAnswers, "The same answers as data (security_pack v1).")
	if p.Records != nil {
		row(securitypack.FileRecords, fmt.Sprintf("The period's %s sealed records (export bundle v1).", securitypack.Count(p.Records.Deeds)))
	} else {
		row(securitypack.FileRecords, "Not included in this pack.")
	}
	row(securitypack.FileSeal, "The sealed records over the files above, and the public keys that check them.")
	if aliases {
		row(securitypack.FileAliases, "Which alias is which real agent, connector or rule name. Keep it: don't send it.")
	}
	b.WriteString("\nHow to check it (offline, no account needed)\n")
	b.WriteString("  deedglass security-pack verify <this folder>\n")
	b.WriteString("  One changed byte in any file fails the check and names the file. To be sure the records\n")
	b.WriteString("  were sealed by this org's own server, ask it for its public keys and add --keys keys.json.\n")
	b.WriteString("\nWhat to send\n")
	if aliases {
		b.WriteString("  Everything except aliases.private.json.\n")
	} else {
		b.WriteString("  The whole folder.\n")
	}
	return b.String()
}
