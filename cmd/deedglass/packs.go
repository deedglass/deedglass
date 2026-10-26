package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/deedglass/deedglass/internal/cli/ui"
	"github.com/deedglass/deedglass/internal/connector/egress"
	"github.com/deedglass/deedglass/internal/deed"
	"github.com/deedglass/deedglass/internal/ids"
	"github.com/deedglass/deedglass/internal/packs"
)

// deedglass policy install and policy packs list|verify|sign, keys gen --purpose packs (S47 §3.8).
// A pack is checked against its signed index, compiled and tested before anything is written, and
// the person sees the files and the diff first ([y/N]). Nothing here pushes or activates a policy.
// A Pro pack asks packsProCheck (licence.go), the one door to the licence.

// --- seams for tests ---

var (
	packKeys      = packs.EmbeddedKeys
	packEmbedded  = packs.Embedded
	packLatestURL = packs.LatestURL
	packsHTTP     = func() *http.Client {
		return egress.FirstParty(egress.FirstPartyOptions{Timeout: packs.FetchTimeout, Proxy: true, Redirects: egress.RedirectNone})
	}
	packsNow = time.Now
)

type packSourceOpts struct {
	from   string
	latest bool
	json   bool
}

func (o *packSourceOpts) bind(fs *flag.FlagSet) {
	fs.StringVar(&o.from, "from", o.from, "a local copy of the policy-packs repo: a directory or a .tar.gz")
	fs.BoolVar(&o.latest, "latest", o.latest, "download the newest policy-packs release over HTTPS (the only network path)")
	fs.BoolVar(&o.json, "json", o.json, "print JSON")
}

// loadPacks reads and checks the source the flags name. The copy built into the binary is checked
// by its hashes (and its seal once it has one); any other copy must be sealed by a known key.
func loadPacks(ctx context.Context, e *Env, o packSourceOpts, hint string) (*packs.Checked, error) {
	var (
		src *packs.Source
		err error
	)
	switch {
	case o.from != "" && o.latest:
		return nil, usageErr(errors.New("--from and --latest each name a source; use one"), "deedglass policy packs list --latest")
	case o.latest:
		src, err = packs.Fetch(ctx, packsHTTP(), packLatestURL)
		if err != nil {
			return nil, failNext(e, ExitFailure, "packs_unreachable", "Couldn't download the newest policy packs. Nothing was written.",
				"Try again later, or leave out --latest to use the copy built into deedglass.")
		}
	case o.from != "":
		src, err = readPackSource(o.from)
		if err != nil {
			return nil, packCheckErr(e, err, hint)
		}
	default:
		src = packEmbedded()
	}
	ks, err := packKeys()
	if err != nil {
		return nil, &ExitError{Code: ExitFailure, ErrCode: "pack_keys_invalid", Err: err, Next: "Reinstall deedglass; this build's pack keys can't be read."}
	}
	c, err := packs.Verify(src, ks, src.Origin != "embedded")
	if err != nil {
		if errors.Is(err, packs.ErrUnsigned) || errors.Is(err, packs.ErrKeyUnknown) {
			next := "Check where this copy came from, or use the copy built in: " + hint
			if ks.Len() == 0 {
				next = "This build doesn't have the pack signing key yet. Use the copy built in: " + hint
			}
			return nil, failNext(e, ExitFailure, "pack_unsigned", "This copy of the policy packs isn't signed by a key this build knows. Nothing was written.", next)
		}
		return nil, packCheckErr(e, err, hint)
	}
	return c, nil
}

func readPackSource(from string) (*packs.Source, error) {
	fi, err := os.Stat(from)
	if err != nil {
		return nil, &packs.FormatError{File: from, Why: "can't be read"}
	}
	if fi.IsDir() {
		return packs.FromDir(from)
	}
	f, err := os.Open(from)
	if err != nil {
		return nil, &packs.FormatError{File: from, Why: "can't be read"}
	}
	defer f.Close()
	return packs.FromTarGz(f, from)
}

func packCheckErr(e *Env, err error, hint string) error {
	var mm *packs.MismatchError
	var fe *packs.FormatError
	switch {
	case errors.As(err, &mm):
		return failNext(e, ExitFailure, "pack_mismatch", "This pack doesn't match its signed index ("+clean(mm.File)+"). Nothing was written.",
			"Get a fresh copy, or use the copy built in: "+hint)
	case errors.Is(err, packs.ErrSeal):
		return failNext(e, ExitFailure, "pack_mismatch", "This copy's index doesn't match its seal. Nothing was written.",
			"Get a fresh copy, or use the copy built in: "+hint)
	case errors.Is(err, packs.ErrNoIndex):
		return failNext(e, ExitFailure, "pack_unreadable", "This copy has no index.json, so it isn't a policy-packs copy. Nothing was written.",
			"Point --from at the repo's root, or at its release .tar.gz.")
	case errors.As(err, &fe):
		return failNext(e, ExitFailure, "pack_unreadable", clean(fe.File)+" "+fe.Why+". Nothing was written.",
			"Get a fresh copy, or use the copy built in: "+hint)
	}
	return err
}

// sourceLine says where the packs came from and how they were checked.
func sourceLine(c *packs.Checked) string {
	where := "the copy built into deedglass"
	if c.Source.Origin != "embedded" {
		where = clean(c.Source.Origin)
	}
	if c.Sealed {
		return where + " " + ui.Sep + " index sealed by " + c.KeyID
	}
	return where + " " + ui.Sep + " checked against its index (not sealed yet)"
}

func tierWord(t string) string {
	if t == packs.TierPro {
		return "Pro"
	}
	return "free"
}

// --- policy packs list ---

func runPolicyPacksList(ctx context.Context, e *Env, args []string) error {
	var o packSourceOpts
	rest, err := parseInterleaved("deedglass policy packs list", args, o.bind)
	if err != nil {
		return err
	}
	if len(rest) > 0 {
		return usageErr(fmt.Errorf("deedglass policy packs list takes no arguments (got %q)", clean(rest[0])), "deedglass policy packs list [--from <dir|tar.gz>] [--latest] [--json]")
	}
	c, err := loadPacks(ctx, e, o, "deedglass policy packs list")
	if err != nil {
		return err
	}
	if o.json || e.Globals.JSON {
		return writeJSON(e, map[string]any{"object": "pack_list", "source": c.Source.Origin, "sealed": c.Sealed, "key_id": c.KeyID, "packs": c.Index.Packs})
	}
	for _, p := range c.Index.Packs {
		fmt.Fprintf(e.Stdout, "%-16s %-7s %-5s %3d cases  %s\n", p.Name, p.Version, tierWord(p.Tier), p.Cases, p.Summary)
	}
	fmt.Fprintln(e.Stdout, "From "+sourceLine(c)+".")
	fmt.Fprintln(e.Stdout, "Install one: deedglass policy install <name>")
	return nil
}

// --- policy packs verify ---

func runPolicyPacksVerify(ctx context.Context, e *Env, args []string) error {
	var o packSourceOpts
	rest, err := parseInterleaved("deedglass policy packs verify", args, o.bind)
	if err != nil {
		return err
	}
	if len(rest) > 0 {
		return usageErr(fmt.Errorf("deedglass policy packs verify takes no arguments (got %q)", clean(rest[0])), "deedglass policy packs verify [--from <dir|tar.gz>] [--latest] [--json]")
	}
	c, err := loadPacks(ctx, e, o, "deedglass policy packs verify")
	if err != nil {
		return err
	}
	failed := []string{}
	for _, p := range c.Index.Packs {
		if r := packs.Check(ctx, p, c.PackFiles(p)); !r.OK() {
			failed = append(failed, p.Name)
		}
	}
	if o.json || e.Globals.JSON {
		if err := writeJSON(e, map[string]any{"object": "pack_verify", "source": c.Source.Origin, "sealed": c.Sealed, "key_id": c.KeyID,
			"packs": len(c.Index.Packs), "failed": failed, "ok": len(failed) == 0}); err != nil {
			return err
		}
		if len(failed) > 0 {
			return &ExitError{Code: ExitFailure, printed: true}
		}
		return nil
	}
	if len(failed) > 0 {
		return failNext(e, ExitFailure, "pack_cases_failed", "These packs don't pass their own cases: "+strings.Join(failed, ", ")+".",
			"Run deedglass policy test on the pack's policy to see which case.")
	}
	fmt.Fprintln(e.Stdout, e.Style.OKLine(fmt.Sprintf("%d %s %s every file matches %s every case passes", len(c.Index.Packs), plural(len(c.Index.Packs), "pack", "packs"), ui.Sep, ui.Sep)))
	fmt.Fprintln(e.Stdout, "From "+sourceLine(c)+".")
	return nil
}

// --- policy install ---

type policyInstallOpts struct {
	packSourceOpts
	dir string
	yes bool
}

func runPolicyInstall(ctx context.Context, e *Env, args []string) error {
	o := policyInstallOpts{dir: "policies"}
	rest, err := parseInterleaved("deedglass policy install", args, func(fs *flag.FlagSet) {
		o.bind(fs)
		fs.StringVar(&o.dir, "dir", o.dir, "where to write the policy and its cases")
		fs.BoolVar(&o.yes, "yes", o.yes, "write without asking")
	})
	if err != nil {
		return err
	}
	if len(rest) != 1 {
		return usageErr(errors.New("deedglass policy install needs one pack name"), "deedglass policy install postgres-safe · deedglass policy packs list")
	}
	name, version, _ := strings.Cut(strings.TrimSpace(rest[0]), "@")
	hint := "deedglass policy install " + clean(name)
	c, err := loadPacks(ctx, e, o.packSourceOpts, hint)
	if err != nil {
		return err
	}
	p, ok := c.Index.Find(name, version)
	if !ok {
		msg := "No pack named " + clean(name) + "."
		if q, any := c.Index.Find(name, ""); any {
			msg = "No version " + clean(version) + " of " + clean(name) + " here (this copy has " + q.Version + ")."
		}
		return failNext(e, ExitFailure, "pack_not_found", msg, "deedglass policy packs list")
	}
	if p.Tier == packs.TierPro {
		if err := packsProCheck(ctx, e); err != nil {
			return err
		}
	}
	files := c.PackFiles(p)
	r := packs.Check(ctx, p, files)
	if !r.OK() {
		why := "its policy doesn't compile"
		if r.Test != nil {
			why = fmt.Sprintf("%d of its own cases fail", r.Test.Failed)
		}
		return failNext(e, ExitFailure, "pack_cases_failed", "The pack "+p.Name+" didn't pass its checks: "+why+". Nothing was written.",
			"Get a fresh copy, or use the copy built in: "+hint)
	}
	plan := packs.PlanFiles(p, files, filepath.ToSlash(o.dir), readExisting)
	for _, f := range plan {
		if fi, err := os.Lstat(filepath.FromSlash(f.Path)); err == nil && !fi.Mode().IsRegular() {
			return failNext(e, ExitFailure, "pack_target_unsafe", clean(f.Path)+" isn't a regular file, so it won't be replaced. Nothing was written.",
				"Move it away, or choose another directory with --dir.")
		}
	}
	toWrite := 0
	for _, f := range plan {
		if !f.Same {
			toWrite++
		}
	}
	jsonOut := o.json || e.Globals.JSON
	head := fmt.Sprintf("%s %s %s %s %s %d cases passed %s every rule covered", p.Name, p.Version, ui.Sep, tierWord(p.Tier), ui.Sep, r.Test.Passed, ui.Sep)
	if !jsonOut {
		fmt.Fprintln(e.Stdout, e.Style.OKLine(head))
		fmt.Fprintln(e.Stdout, "  from  "+sourceLine(c))
	}
	if toWrite == 0 {
		if jsonOut {
			return writeJSON(e, installJSON(p, plan, false))
		}
		fmt.Fprintln(e.Stdout, e.Style.OKLine("Both files already match "+p.Name+" "+p.Version+". Nothing to write."))
		return nil
	}
	if !jsonOut {
		for _, f := range plan {
			switch {
			case f.Same:
				fmt.Fprintf(e.Stdout, "  same    %s\n", clean(f.Path))
			case !f.Exists:
				fmt.Fprintf(e.Stdout, "  new     %s (%d %s)\n", clean(f.Path), f.Diff.Added, plural(f.Diff.Added, "line", "lines"))
			default:
				fmt.Fprintf(e.Stdout, "  change  %s +%d, −%d\n", clean(f.Path), f.Diff.Added, f.Diff.Removed)
				printPolicyDiff(e.Stdout, e.Style, f.Diff)
			}
		}
	}
	if !o.yes {
		if jsonOut {
			return writeJSON(e, installJSON(p, plan, false))
		}
		fmt.Fprintf(e.Stderr, "Write %d %s? [y/N] ", toWrite, plural(toWrite, "file", "files"))
		ans, _ := bufio.NewReader(e.stdin()).ReadString('\n')
		if a := strings.ToLower(strings.TrimSpace(ans)); a != "y" && a != "yes" {
			fmt.Fprintln(e.Stderr, "Nothing was written.")
			return nil
		}
	}
	if err := os.MkdirAll(filepath.FromSlash(o.dir), 0o755); err != nil {
		return failNext(e, ExitFailure, "pack_write_failed", "Can't create "+clean(o.dir)+": "+reason(err)+". Nothing was written.", "Check the directory's permissions, or choose another with --dir.")
	}
	for _, f := range plan {
		if f.Same {
			continue
		}
		if err := writeFileAtomic(filepath.FromSlash(f.Path), f.Content); err != nil {
			return failNext(e, ExitFailure, "pack_write_failed", "Can't write "+clean(f.Path)+": "+reason(err)+".", "Check the directory's permissions, or choose another with --dir.")
		}
	}
	if jsonOut {
		return writeJSON(e, installJSON(p, plan, true))
	}
	pol := filepath.ToSlash(filepath.Join(o.dir, p.Policy()))
	fmt.Fprintln(e.Stdout, e.Style.OKLine(fmt.Sprintf("Wrote %d %s. Nothing was pushed or activated.", toWrite, plural(toWrite, "file", "files"))))
	fmt.Fprintln(e.Stdout, "  next  deedglass policy test "+pol+", then deedglass policy push "+pol)
	return nil
}

func installJSON(p packs.Entry, plan []packs.Planned, written bool) map[string]any {
	return map[string]any{"object": "pack_install", "pack": p.Name, "version": p.Version, "tier": p.Tier, "id": p.ID, "files": plan, "written": written}
}

func readExisting(path string) ([]byte, bool) {
	b, err := os.ReadFile(filepath.FromSlash(path))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, false
	}
	return b, true
}

// writeFileAtomic writes b to path (0644) through a temporary file in the same directory.
func writeFileAtomic(path string, b []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".deedglass-pack-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	_, werr := tmp.Write(b)
	if cerr := tmp.Close(); werr == nil {
		werr = cerr
	}
	if werr == nil {
		werr = os.Chmod(name, 0o644)
	}
	if werr == nil {
		werr = os.Rename(name, path)
	}
	if werr != nil {
		os.Remove(name)
	}
	return werr
}

// --- policy packs sign (the policy-packs release workflow) ---

func runPolicyPacksSign(_ context.Context, e *Env, args []string) error {
	var key, dir string
	rest, err := parseInterleaved("deedglass policy packs sign", args, func(fs *flag.FlagSet) {
		fs.StringVar(&key, "key", "", "the pack key file (deedglass keys gen --purpose packs)")
		fs.StringVar(&dir, "dir", ".", "the policy-packs directory to sign")
	})
	if err != nil {
		return err
	}
	if len(rest) > 0 || key == "" {
		return usageErr(errors.New("deedglass policy packs sign needs --key and no arguments"), "deedglass policy packs sign --key pack-key.json --dir .")
	}
	s, err := deed.LoadKeyFile(key, deed.LoadOptions{Prod: true})
	if err != nil {
		return failNext(e, ExitFailure, "pack_key_unusable", "Can't use the pack key "+clean(key)+": "+deed.UnsafeReason(err)+".",
			"Make it a regular file with chmod 600, or generate one with deedglass keys gen --purpose packs.")
	}
	src, err := packs.FromDir(dir)
	if err != nil {
		return packCheckErr(e, err, "deedglass policy packs sign --dir <repo>")
	}
	ix, err := packs.BuildIndex(context.Background(), src)
	if err != nil {
		return failNext(e, ExitFailure, "pack_cases_failed", "The packs can't be signed: "+err.Error()+".", "Fix the pack, then sign again.")
	}
	index, seal, err := packs.Sign(ix, s, packsNow())
	if err != nil {
		return err
	}
	if err := writeFileAtomic(filepath.Join(dir, packs.IndexFile), index); err != nil {
		return err
	}
	if err := writeFileAtomic(filepath.Join(dir, packs.SealFile), seal); err != nil {
		return err
	}
	fmt.Fprintln(e.Stdout, e.Style.OKLine(fmt.Sprintf("Signed %d %s %s %s", len(ix.Packs), plural(len(ix.Packs), "pack", "packs"), ui.Sep, s.KeyID())))
	printRows(e, [][2]string{{"index", filepath.Join(dir, packs.IndexFile)}, {"seal", filepath.Join(dir, packs.SealFile)}, {"thumbprint", shortHash(deed.Thumbprint(s.PublicKey()))}})
	return nil
}

// --- keys gen --purpose packs ---

// runKeysGenPacks writes a new pack key: the key file (0600, the policy-packs repo's environment
// secret, never in git) and the public pack-keys.json to commit as internal/packs/pack-keys.json.
func runKeysGenPacks(e *Env, flagDir string) error {
	shown := flagDir
	if shown == "" {
		shown = filepath.Join(filepath.Dir(deed.DefaultKeyDir), "pack-keys")
	}
	dir := shown
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(e.LoadOptions().Dir, dir)
	}
	seed, err := deed.NewSeed(keysRand)
	if err != nil {
		return err
	}
	kf, s, err := deed.NewKeyFile(ids.New[ids.SealKeyKind]().String(), seed, keysNow())
	if err != nil {
		return err
	}
	if _, err := deed.WriteKeyFile(dir, kf); err != nil {
		return &ExitError{Code: ExitFailure, ErrCode: "key_file_write_failed", Err: fmt.Errorf("Can't write the pack key in %s", shown),
			Next: "Check the directory's permissions, or choose another with --dir."}
	}
	set, err := packs.KeySetJSON([]packs.KeyRecord{packs.Record(kf.ID, s.PublicKey())})
	if err != nil {
		return err
	}
	pubPath := filepath.Join(dir, kf.ID+".pack-keys.json")
	if err := os.WriteFile(pubPath, set, 0o644); err != nil {
		return err
	}
	if e.Globals.JSON {
		return writeJSON(e, map[string]any{"object": "pack_key_file", "id": kf.ID, "key_file": filepath.Join(shown, kf.ID+".json"), "mode": "0600",
			"public_set": filepath.Join(shown, kf.ID+".pack-keys.json"), "thumbprint": deed.Thumbprint(s.PublicKey())})
	}
	fmt.Fprintln(e.Stdout, e.Style.OKLine("Pack key created "+ui.Sep+" "+kf.ID))
	printRows(e, [][2]string{
		{"private", filepath.Join(shown, kf.ID+".json") + " (0600; the policy-packs repo's packs environment secret, never in git)"},
		{"public", filepath.Join(shown, kf.ID+".pack-keys.json") + " (commit as internal/packs/pack-keys.json)"},
		{"thumbprint", shortHash(deed.Thumbprint(s.PublicKey()))},
	})
	return nil
}
