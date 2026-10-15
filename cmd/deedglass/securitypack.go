package main

import (
	"archive/tar"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/deedglass/deedglass/cmd/deedglass/internal/ui"
	"github.com/deedglass/deedglass/internal/connector/egress"
	"github.com/deedglass/deedglass/internal/deed"
	"github.com/deedglass/deedglass/internal/deed/archive"
	"github.com/deedglass/deedglass/internal/securitypack"
	"github.com/deedglass/deedglass/pkg/deedglass"
)

// deedglass security-pack readiness|build|verify (S51 §5): the security review pack. readiness is
// free (the 24 questions, statuses and fixes); build is Pro (the sealed folder for a reviewer,
// through the API with an org admin key); verify is free and offline (no account, no network
// unless --keys names a URL).

// parsePeriod reads --period: 90d, 90 or 12w, 7 to 365 days.
func parsePeriod(s string) (int, error) {
	s = strings.TrimSpace(strings.ToLower(s))
	mult := 1
	switch {
	case strings.HasSuffix(s, "d"):
		s = strings.TrimSuffix(s, "d")
	case strings.HasSuffix(s, "w"):
		s, mult = strings.TrimSuffix(s, "w"), 7
	}
	n, err := strconv.Atoi(s)
	if err != nil || n*mult < 7 || n*mult > 365 {
		return 0, usageErr(fmt.Errorf("--period must be 7d to 365d (got %q)", clean(s)), "For example --period 90d.")
	}
	return n * mult, nil
}

// --- readiness ---

var readinessValues struct {
	period string
	all    bool
}

func readinessFlags(fs *flag.FlagSet) {
	readinessValues = struct {
		period string
		all    bool
	}{}
	fs.StringVar(&readinessValues.period, "period", "90d", "the window to count, 7d to 365d")
	fs.BoolVar(&readinessValues.all, "all", false, "list every question, not only the ones with something to fix")
}

// statusWord pads and colours a status word; the word carries the meaning, colour only repeats it.
func statusWord(s ui.Style, st securitypack.Status) string {
	w := ui.Pad(st.Word(), 14)
	switch st {
	case securitypack.InPlace:
		return s.Teal(w)
	case securitypack.Partly, securitypack.CantTell:
		return s.Amber(w)
	case securitypack.NotSetUp:
		return s.Red(w)
	}
	return s.Dim(w)
}

// readinessOrder puts the gaps first: not set up, partly, can't tell, then the rest.
var readinessOrder = map[securitypack.Status]int{securitypack.NotSetUp: 0, securitypack.Partly: 1, securitypack.CantTell: 2,
	securitypack.InPlace: 3, securitypack.NotApplicable: 4}

func runSecurityPackReadiness(ctx context.Context, e *Env, args []string) error {
	pos, err := mixedArgs("deedglass security-pack readiness", readinessFlags, args)
	if err != nil {
		return err
	}
	if len(pos) != 0 {
		return usageErr(fmt.Errorf("deedglass security-pack readiness takes no arguments (got %q)", clean(pos[0])), "deedglass security-pack readiness [--period 90d]")
	}
	days, err := parsePeriod(readinessValues.period)
	if err != nil {
		return err
	}
	c, err := apiClient(e)
	if err != nil {
		return err
	}
	var rd securitypack.Readiness
	if _, err := c.Do(ctx, "GET", "/v1/security_pack/readiness?period_days="+url.QueryEscape(strconv.Itoa(days)), nil, &rd); err != nil {
		return apiErr(e, c, err)
	}
	if e.Globals.JSON {
		return writeJSON(e, rd)
	}
	s := e.Style
	org := rd.OrgName
	if org == "" {
		org = rd.Org
	}
	fmt.Fprintf(e.Stdout, "Security review readiness %s %s %s last %d days\n", ui.Sep, clean(org), ui.Sep, days)
	fmt.Fprintln(e.Stdout, rd.Summary.Line())
	rows := append([]securitypack.ReadinessRow(nil), rd.Rows...)
	stable := func(i, j int) bool { return readinessOrder[rows[i].Status] < readinessOrder[rows[j].Status] }
	sortStable(rows, stable)
	shown := 0
	for _, r := range rows {
		gap := r.Status == securitypack.NotSetUp || r.Status == securitypack.Partly || r.Status == securitypack.CantTell
		if !gap && !readinessValues.all {
			continue
		}
		shown++
		line := "  " + statusWord(s, r.Status) + " " + ui.Pad(r.ID, 26)
		if r.Fix != "" {
			line += " " + s.Dim(clean(r.Fix))
		} else {
			line += " " + s.Dim(clean(r.Question))
		}
		fmt.Fprintln(e.Stdout, line)
	}
	if shown == 0 {
		fmt.Fprintln(e.Stdout, s.OKLine("Nothing to fix. deedglass security-pack readiness --all lists every question."))
	}
	fmt.Fprintln(e.Stdout, "Build the document for a reviewer (Pro): deedglass security-pack build")
	return nil
}

// sortStable is an insertion sort (24 rows), stable and dependency-free.
func sortStable(rows []securitypack.ReadinessRow, less func(i, j int) bool) {
	for i := 1; i < len(rows); i++ {
		for j := i; j > 0 && less(j, j-1); j-- {
			rows[j], rows[j-1] = rows[j-1], rows[j]
		}
	}
}

// --- build ---

var buildValues struct {
	period, out                 string
	aliases, noRecords, records bool
}

func buildFlags(fs *flag.FlagSet) {
	buildValues = struct {
		period, out                 string
		aliases, noRecords, records bool
	}{}
	fs.StringVar(&buildValues.period, "period", "90d", "the window to count, 7d to 365d")
	fs.StringVar(&buildValues.out, "out", ".", "the directory to write the pack folder in")
	fs.BoolVar(&buildValues.aliases, "aliases", false, "replace agent, connector and rule names with aliases (agent-1, connector-2); the mapping goes only to aliases.private.json")
	fs.BoolVar(&buildValues.noRecords, "no-records", false, "leave out records.tar.gz")
	fs.BoolVar(&buildValues.records, "records", false, "include records.tar.gz with --aliases (its sealed records carry the real names)")
}

// MaxPackFile bounds one file of a pack folder the CLI unpacks; records.tar.gz may be large.
const (
	maxPackFile    = 4 << 30
	maxPackEntries = 16
	packTimeout    = 11 * time.Minute
)

var rePackFolder = regexp.MustCompile(`^security-pack-[a-z0-9-]{1,40}-[0-9]{4}-[0-9]{2}-[0-9]{2}$`)

func runSecurityPackBuild(ctx context.Context, e *Env, args []string) error {
	pos, err := mixedArgs("deedglass security-pack build", buildFlags, args)
	if err != nil {
		return err
	}
	if len(pos) != 0 {
		return usageErr(fmt.Errorf("deedglass security-pack build takes no arguments (got %q)", clean(pos[0])), "deedglass security-pack build [--period 90d] [--out dir] [--aliases]")
	}
	o := buildValues
	days, err := parsePeriod(o.period)
	if err != nil {
		return err
	}
	if o.noRecords && o.records {
		return usageErr(errors.New("--records and --no-records can't be used together"), "Pick one.")
	}
	records := !o.noRecords && (!o.aliases || o.records)
	if fi, err := os.Stat(o.out); err != nil || !fi.IsDir() {
		return failNext(e, ExitUsage, "usage_error", fmt.Sprintf("%s isn't a directory.", clean(o.out)), "Pass an existing directory to --out.")
	}
	c, err := apiClient(e)
	if err != nil {
		return err
	}
	c.HTTP = egress.FirstParty(egress.FirstPartyOptions{Timeout: packTimeout, Proxy: true})
	body := map[string]any{"period_days": days, "aliases": o.aliases, "records": records}
	rc, _, err := c.Stream(ctx, "POST", "/v1/security_pack", body)
	if err != nil {
		var de *deedglass.Error
		if errors.As(err, &de) && de.Code == "pro_required" && !e.Globals.JSON {
			fmt.Fprintln(e.Stderr, e.Style.FailLine(de.Message))
			fmt.Fprintln(e.Stderr, "  next  deedglass security-pack readiness (free), or see deedglass.com/pricing")
			return &ExitError{Code: ExitFailure, ErrCode: "pro_required", Err: errors.New(de.Message), printed: true}
		}
		return apiErr(e, c, err)
	}
	defer rc.Close()
	folder, err := unpackPack(rc, o.out)
	if err != nil {
		return failNext(e, ExitFailure, "pack_incomplete", "The pack didn't arrive whole: "+reason(err)+".",
			"Run deedglass security-pack build again; nothing was left behind.")
	}
	r := verifyPack(ctx, e, folder, nil)
	if e.Globals.JSON {
		return writeJSON(e, map[string]any{"object": "security_pack_build", "folder": folder, "verify": r})
	}
	s := e.Style
	recLine := "no records"
	if r.Folder.Pack != nil && r.Folder.Pack.Records != nil {
		recLine = deed.FormatCount(r.Folder.Pack.Records.Deeds) + " records"
	}
	answers := 0
	if r.Folder.Pack != nil {
		answers = len(r.Folder.Pack.Answers)
	}
	fmt.Fprintln(e.Stdout, s.OKLine(fmt.Sprintf("%s/ %s %d answers %s %s %s sealed (%d files)", clean(folder), ui.Sep, answers, ui.Sep, recLine, ui.Sep, len(r.Folder.Files))))
	printPackCheck(e, r)
	if o.aliases && !records {
		fmt.Fprintln(e.Stdout, s.Dim("  records.tar.gz left out: its sealed records carry the real names. Add --records to include it anyway."))
	}
	if o.aliases {
		fmt.Fprintln(e.Stdout, "  Send the folder except aliases.private.json. The reviewer checks it with: deedglass security-pack verify <folder>")
	} else {
		fmt.Fprintln(e.Stdout, "  Send the whole folder. The reviewer checks it with: deedglass security-pack verify <folder>")
	}
	if !r.OK {
		return &ExitError{Code: ExitFailure, ErrCode: "pack_invalid", Err: errors.New("the pack doesn't check"), printed: true}
	}
	return nil
}

// unpackPack writes the tar stream's one folder under dir and returns its path. It refuses
// anything but plain files with plain names in one folder whose name is a pack's, never
// overwrites, and removes what it wrote when the stream is cut short.
func unpackPack(r io.Reader, dir string) (string, error) {
	tr := tar.NewReader(r)
	var folder string
	var written []string
	fail := func(err error) (string, error) {
		for i := len(written) - 1; i >= 0; i-- {
			_ = os.Remove(written[i])
		}
		return "", err
	}
	for n := 0; ; n++ {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fail(err)
		}
		if n > maxPackEntries {
			return fail(errors.New("too many files"))
		}
		name := h.Name
		if n == 0 {
			top := strings.TrimSuffix(name, "/")
			if h.Typeflag != tar.TypeDir || !rePackFolder.MatchString(top) {
				return fail(fmt.Errorf("%q isn't a pack folder", clean(name)))
			}
			folder = filepath.Join(dir, top)
			if err := os.Mkdir(folder, 0o755); err != nil {
				if errors.Is(err, os.ErrExist) {
					return "", fmt.Errorf("%s already exists; move it or pass another --out", clean(folder))
				}
				return "", err
			}
			written = append(written, folder)
			continue
		}
		base := strings.TrimPrefix(name, filepath.Base(folder)+"/")
		if h.Typeflag != tar.TypeReg || base == name || strings.ContainsAny(base, `/\`) || base == "" || base[0] == '.' || h.Size < 0 || h.Size > maxPackFile {
			return fail(fmt.Errorf("%q isn't a pack file", clean(name)))
		}
		mode := os.FileMode(0o644)
		if base == securitypack.FileAliases {
			mode = 0o600
		}
		path := filepath.Join(folder, base)
		f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
		if err != nil {
			return fail(err)
		}
		written = append(written, path)
		_, err = io.Copy(f, io.LimitReader(tr, h.Size))
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return fail(err)
		}
	}
	if folder == "" {
		return "", errors.New("the answer held no folder")
	}
	return folder, nil
}

// --- verify ---

var packVerifyValues struct {
	keys string
}

func packVerifyFlags(fs *flag.FlagSet) {
	packVerifyValues.keys = ""
	fs.StringVar(&packVerifyValues.keys, "keys", "", "the org's public seal keys (keys.json file or https URL) to pin; default: the keys inside the pack")
}

// packCheck is security-pack verify's result.
type packCheck struct {
	Object  string                     `json:"object"` // "security_pack_check"
	OK      bool                       `json:"ok"`
	Pinned  bool                       `json:"keys_pinned"`
	Folder  *securitypack.FolderReport `json:"folder"`
	Records *deed.Report               `json:"records"`
	// RecordsProblem says why the records bundle doesn't check, in one sentence.
	RecordsProblem string `json:"records_problem,omitempty"`
}

// verifyPack checks a folder: every sealed file against its record, then records.tar.gz with
// deedglass verify's own code, keyed by the pack's keys (or the pinned ones), for the pack's org.
func verifyPack(ctx context.Context, e *Env, dir string, trusted *deed.KeySet) *packCheck {
	pc := &packCheck{Object: "security_pack_check", Pinned: trusted != nil}
	pc.Folder = securitypack.VerifyFolder(dir, trusted)
	pc.OK = pc.Folder.OK
	if pc.Folder.Records == "" {
		return pc
	}
	in, err := detect(e, pc.Folder.Records, archive.DefaultLimits())
	if err != nil || in.kind != inBundle {
		pc.OK, pc.RecordsProblem = false, "records.tar.gz isn't a deed bundle."
		return pc
	}
	defer func() {
		if in.closer != nil {
			in.closer.Close()
		}
	}()
	ks := pc.Folder.Keys
	if in.err == nil {
		// The records must come from the server that sealed the files: every key that sealed a file
		// is in the bundle's own keys.json.
		bks, err := parseKeys(in.bundle.Keys(), "records.tar.gz (keys.json)")
		if err != nil {
			pc.OK, pc.RecordsProblem = false, "records.tar.gz carries keys that can't be used."
			return pc
		}
		for _, id := range pc.Folder.SealKeys {
			if _, _, ok := bks.Lookup(id); !ok {
				pc.OK, pc.RecordsProblem = false, fmt.Sprintf("records.tar.gz wasn't sealed by the server that sealed the files (%s isn't in its keys).", shortID(id))
				return pc
			}
		}
	}
	org := ""
	if pc.Folder.Pack != nil {
		org = pc.Folder.Pack.Org
	}
	v := deed.NewVerifier(deed.StreamOptions{Keys: ks, KeysPinned: trusted != nil, Org: org})
	if err := verifyInput(ctx, v, in, ks); err != nil {
		pc.OK, pc.RecordsProblem = false, "records.tar.gz can't be read."
		return pc
	}
	rep := v.Report()
	pc.Records = rep
	if rep.Result != deed.ResultValid {
		pc.OK = false
		pc.RecordsProblem = "records.tar.gz doesn't check."
		if len(rep.Failures) > 0 {
			pc.RecordsProblem = "records.tar.gz doesn't check: " + strings.TrimSpace(rep.Failures[0].Message)
		}
	}
	return pc
}

func printPackCheck(e *Env, pc *packCheck) {
	s := e.Style
	f := pc.Folder
	if pc.OK {
		line := "Checked: every file matches its sealed record"
		if pc.Records != nil {
			line += fmt.Sprintf("; %s records, chain intact", deed.FormatCount(pc.Records.Intact))
		}
		fmt.Fprintln(e.Stdout, s.OKLine(line))
	} else {
		fmt.Fprintln(e.Stdout, s.FailLine("This pack doesn't check."))
		for _, p := range f.Problems {
			fmt.Fprintln(e.Stdout, "  "+clean(p))
		}
		if pc.RecordsProblem != "" {
			fmt.Fprintln(e.Stdout, "  "+clean(pc.RecordsProblem))
		}
	}
	for _, w := range f.Warnings {
		fmt.Fprintln(e.Stdout, s.Dim("  "+clean(w)))
	}
	if pc.OK && !pc.Pinned {
		fmt.Fprintln(e.Stdout, s.Dim("  Checked with the keys inside the pack. To be sure they are the org's, pin its keys: --keys keys.json"))
	}
}

func runSecurityPackVerify(ctx context.Context, e *Env, args []string) error {
	pos, err := mixedArgs("deedglass security-pack verify", packVerifyFlags, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usageErr(errors.New("deedglass security-pack verify takes one folder"), "deedglass security-pack verify security-pack-acme-2026-10-18")
	}
	dir := pos[0]
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		return inputErr("input_unreadable", fmt.Errorf("Can't read %s: not a folder", clean(dir)), "Pass the pack folder (the one with pack.seal.json in it).")
	}
	var trusted *deed.KeySet
	if packVerifyValues.keys != "" {
		src, err := loadKeyArg(ctx, packVerifyValues.keys)
		if err != nil {
			return err
		}
		trusted = src.ks
	}
	pc := verifyPack(ctx, e, dir, trusted)
	if e.Globals.JSON {
		if err := writeJSON(e, pc); err != nil {
			return err
		}
	} else {
		printPackCheck(e, pc)
	}
	if !pc.OK {
		return &ExitError{Code: ExitFailure, ErrCode: "pack_invalid", Err: errors.New("the pack doesn't check"), printed: true}
	}
	return nil
}
