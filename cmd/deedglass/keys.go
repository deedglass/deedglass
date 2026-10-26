package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/deedglass/deedglass/internal/cli/ui"
	"github.com/deedglass/deedglass/internal/config"
	"github.com/deedglass/deedglass/internal/connector/egress"
	"github.com/deedglass/deedglass/internal/deed"
	"github.com/deedglass/deedglass/internal/ids"
	"github.com/deedglass/deedglass/internal/store"
)

// --- seams for tests ---

var (
	keysNow                = time.Now
	keysRand     io.Reader = rand.Reader
	newSealKeyID           = func() string { return ids.New[ids.SealKeyKind]().String() }
	// openKeyStore connects as the runtime role (DEEDGLASS_DATABASE_URL).
	openKeyStore = func(ctx context.Context, u config.Secret) (keyStore, error) {
		return store.Open(ctx, u, store.Options{MaxConns: 2})
	}
)

// keyStore is what keys rotate and keys list need from the database.
type keyStore interface {
	SealKeys(ctx context.Context) ([]store.SealKeyRow, error)
	RotateSealKey(ctx context.Context, id string, pub []byte, overlap time.Duration) (store.SealKeyRow, []store.SealKeyRow, error)
	AddPQSealKey(ctx context.Context, id string, pub []byte, overlap time.Duration) (store.SealKeyRow, []store.SealKeyRow, error)
	Close()
}

// --- flags ---

type keysOpts struct {
	dir     string
	overlap time.Duration
	alg     string
}

var keysFlagValues keysOpts

func keysGenFlags(fs *flag.FlagSet) {
	keysFlagValues = keysOpts{}
	fs.StringVar(&keysFlagValues.dir, "dir", "", "directory for the key file (default .deedglass/keys)")
	keysGenPurposeFlag(fs)
}

func keysRotateFlags(fs *flag.FlagSet) {
	keysFlagValues = keysOpts{}
	fs.StringVar(&keysFlagValues.dir, "dir", "", "directory for the new key file (default: next to DEEDGLASS_SEAL_KEY_FILE, else .deedglass/keys)")
	fs.DurationVar(&keysFlagValues.overlap, "overlap", 5*time.Minute, "how long the old key keeps sealing (1s to 168h)")
	fs.StringVar(&keysFlagValues.alg, "alg", deed.AlgEd25519, "Ed25519 (replace the seal key) or ML-DSA-65 (add a post-quantum key beside it)")
}

// keyDir resolves where key files go. shown is the path as the person should see and type it;
// real is where the file is written (relative paths resolve against the working directory).
func keyDir(e *Env, flagDir, sealKeyFile string) (shown, real string) {
	shown = deed.DefaultKeyDir
	switch {
	case flagDir != "":
		shown = flagDir
	case sealKeyFile != "":
		shown = filepath.Dir(sealKeyFile)
	}
	real = shown
	if !filepath.IsAbs(shown) {
		real = filepath.Join(e.LoadOptions().Dir, shown)
	}
	return shown, real
}

// --- keys gen ---

func runKeysGen(_ context.Context, e *Env, args []string) error {
	if len(args) > 0 {
		return usageErr(fmt.Errorf("deedglass keys gen takes no arguments (got %q)", args[0]), "Run deedglass help keys gen.")
	}
	switch keysGenPurpose {
	case "seal", "":
	case "licence":
		return runKeysGenLicence(e, keysFlagValues.dir)
	case "packs":
		return runKeysGenPacks(e, keysFlagValues.dir)
	default:
		return usageErr(fmt.Errorf("--purpose takes seal, licence or packs (got %q)", clean(keysGenPurpose)), "For a licence signing key: deedglass keys gen --purpose licence.")
	}
	shown, dir := keyDir(e, keysFlagValues.dir, "")
	path, kf, err := writeNewKey(dir, shown)
	if err != nil {
		return err
	}
	pub, _ := b64(kf.PublicKey)
	if e.Globals.JSON {
		return writeJSON(e, map[string]any{"object": "seal_key_file", "id": kf.ID, "file": path, "mode": "0600",
			"public_key": kf.PublicKey, "thumbprint": deed.Thumbprint(pub), "created_at": kf.CreatedAt})
	}
	s := e.Style
	fmt.Fprintln(e.Stdout, s.OKLine("Seal key created "+ui.Sep+" "+kf.ID))
	printRows(e, [][2]string{
		{"file", path + " (0600)"},
		{"thumbprint", shortHash(deed.Thumbprint(pub))},
		{"next", "export DEEDGLASS_SEAL_KEY_FILE=" + path},
	})
	return nil
}

// writeNewKey generates a seed and writes dir/<id>.json; it returns the path as shown.
func writeNewKey(dir, shown string) (string, deed.KeyFile, error) {
	seed, err := deed.NewSeed(keysRand)
	if err != nil {
		return "", deed.KeyFile{}, err
	}
	kf, _, err := deed.NewKeyFile(newSealKeyID(), seed, keysNow())
	if err != nil {
		return "", deed.KeyFile{}, err
	}
	if _, err := deed.WriteKeyFile(dir, kf); err != nil {
		return "", deed.KeyFile{}, &ExitError{Code: ExitFailure, ErrCode: "key_file_write_failed",
			Err:  fmt.Errorf("Can't write the seal key file in %s: %s", shown, lastPart(errors.Unwrap(err))),
			Next: "Check the directory's permissions, or choose another with --dir."}
	}
	return filepath.Join(shown, kf.ID+".json"), kf, nil
}

func b64(s string) ([]byte, error) { return base64URL.DecodeString(s) }

// --- keys rotate ---

func runKeysRotate(ctx context.Context, e *Env, args []string) error {
	if len(args) > 0 {
		return usageErr(fmt.Errorf("deedglass keys rotate takes no arguments (got %q)", args[0]), "Run deedglass help keys rotate.")
	}
	overlap := keysFlagValues.overlap
	if overlap < time.Second || overlap > 168*time.Hour {
		return usageErr(fmt.Errorf("--overlap must be from 1s to 168h (got %s)", overlap), "For example --overlap 5m.")
	}
	switch keysFlagValues.alg {
	case deed.AlgEd25519, "":
	case deed.AlgMLDSA65:
		return runKeysAddPQ(ctx, e, overlap)
	default:
		return usageErr(fmt.Errorf("--alg takes Ed25519 or ML-DSA-65 (got %q)", clean(keysFlagValues.alg)), "For a post-quantum key beside Ed25519: deedglass keys rotate --alg ML-DSA-65.")
	}
	cfg, err := loadFor(e, config.NeedDatabase)
	if err != nil {
		return err
	}
	ks, err := openKeys(ctx, cfg)
	if err != nil {
		return err
	}
	defer ks.Close()
	shown, dir := keyDir(e, keysFlagValues.dir, cfg.SealKeyFile)
	s := e.Style
	if !e.Globals.JSON {
		fmt.Fprintln(e.Stdout, s.Violet(ui.Pending)+" Rotating the seal key "+ui.Sep+" overlap "+humanDur(overlap))
	}
	path, kf, err := writeNewKey(dir, shown)
	if err != nil {
		return err
	}
	pub, _ := b64(kf.PublicKey)
	newRow, retired, err := ks.RotateSealKey(ctx, kf.ID, pub, overlap)
	if err != nil {
		_ = os.Remove(filepath.Join(dir, kf.ID+".json")) // never leave a key file that isn't registered
		return &ExitError{Code: ExitFailure, ErrCode: "seal_key_rotation_failed",
			Err:  fmt.Errorf("The seal key was not rotated: %s", lastPart(err)),
			Next: "Nothing changed. Check the database connection and run deedglass keys rotate again."}
	}
	if e.Globals.JSON {
		out := map[string]any{"object": "seal_key_rotation", "file": path, "overlap_ms": overlap.Milliseconds(),
			"new_key": deed.NewSealKey(newRow.Record()), "retired": []deed.SealKey{}}
		var rs []deed.SealKey
		for _, r := range retired {
			rs = append(rs, deed.NewSealKey(r.Record()))
		}
		if rs != nil {
			out["retired"] = rs
		}
		return writeJSON(e, out)
	}
	fmt.Fprintln(e.Stdout, s.OKLine("New seal key "+newRow.ID+" is active"))
	retireBy := ""
	for _, r := range retired {
		fmt.Fprintln(e.Stdout, s.OKLine(fmt.Sprintf("%s retires at %s UTC (in %s)", r.ID, r.RetiredAt.UTC().Format("15:04:05"), humanDur(overlap))))
		retireBy = r.RetiredAt.UTC().Format("15:04") + " UTC"
	}
	next := "Point DEEDGLASS_SEAL_KEY_FILE at " + path
	if retireBy == "" {
		printRows(e, [][2]string{{"next", next + " and restart every instance."}})
		return nil
	}
	printRows(e, [][2]string{{"next", next}, {"", "and restart every instance before " + retireBy + ". Records sealed with either key keep verifying."}})
	return nil
}

// runKeysAddPQ is keys rotate --alg ML-DSA-65 (ADR-109): it writes an ML-DSA-65 key file and
// registers the key beside the Ed25519 key. The first such key activates after the overlap, so every
// instance can be restarted with DEEDGLASS_SEAL_PQ_KEY_FILE first; a later one replaces the
// previous ML-DSA-65 key with the overlap, as an Ed25519 rotation does.
func runKeysAddPQ(ctx context.Context, e *Env, overlap time.Duration) error {
	cfg, err := loadFor(e, config.NeedDatabase)
	if err != nil {
		return err
	}
	ks, err := openKeys(ctx, cfg)
	if err != nil {
		return err
	}
	defer ks.Close()
	base := cfg.SealPQKeyFile
	if base == "" {
		base = cfg.SealKeyFile
	}
	shown, dir := keyDir(e, keysFlagValues.dir, base)
	s := e.Style
	if !e.Globals.JSON {
		fmt.Fprintln(e.Stdout, s.Violet(ui.Pending)+" Adding an ML-DSA-65 seal key "+ui.Sep+" overlap "+humanDur(overlap))
	}
	seed, err := deed.NewSeed(keysRand)
	if err != nil {
		return err
	}
	kf, signer, err := deed.NewMLDSAKeyFile(newSealKeyID(), seed, keysNow())
	if err != nil {
		return err
	}
	if _, err := deed.WriteKeyFile(dir, kf); err != nil {
		return &ExitError{Code: ExitFailure, ErrCode: "key_file_write_failed",
			Err:  fmt.Errorf("Can't write the seal key file in %s: %s", shown, lastPart(errors.Unwrap(err))),
			Next: "Check the directory's permissions, or choose another with --dir."}
	}
	path := filepath.Join(shown, kf.ID+".json")
	newRow, retired, err := ks.AddPQSealKey(ctx, kf.ID, signer.PublicKeyBytes(), overlap)
	if err != nil {
		_ = os.Remove(filepath.Join(dir, kf.ID+".json")) // never leave a key file that isn't registered
		return &ExitError{Code: ExitFailure, ErrCode: "seal_key_rotation_failed",
			Err:  fmt.Errorf("The ML-DSA-65 seal key was not added: %s", lastPart(err)),
			Next: "Nothing changed. Check the database connection and run deedglass keys rotate --alg ML-DSA-65 again."}
	}
	if e.Globals.JSON {
		out := map[string]any{"object": "seal_key_rotation", "file": path, "overlap_ms": overlap.Milliseconds(),
			"new_key": deed.NewSealKey(newRow.Record()), "retired": []deed.SealKey{}}
		var rs []deed.SealKey
		for _, r := range retired {
			rs = append(rs, deed.NewSealKey(r.Record()))
		}
		if rs != nil {
			out["retired"] = rs
		}
		return writeJSON(e, out)
	}
	at := newRow.ActivatedAt.UTC().Format("15:04:05")
	fmt.Fprintln(e.Stdout, s.OKLine(fmt.Sprintf("ML-DSA-65 seal key %s seals beside Ed25519 from %s UTC", newRow.ID, at)))
	for _, r := range retired {
		fmt.Fprintln(e.Stdout, s.OKLine(fmt.Sprintf("%s retires at %s UTC (in %s)", r.ID, r.RetiredAt.UTC().Format("15:04:05"), humanDur(overlap))))
	}
	by := newRow.ActivatedAt
	if len(retired) > 0 && retired[0].RetiredAt != nil {
		by = *retired[0].RetiredAt
	}
	printRows(e, [][2]string{{"next", "Point DEEDGLASS_SEAL_PQ_KEY_FILE at " + path},
		{"", "and restart every instance before " + by.UTC().Format("15:04") + " UTC. From then on every deed carries both seals."}})
	return nil
}

// --- keys list ---

func runKeysList(ctx context.Context, e *Env, args []string) error {
	if len(args) > 0 {
		return usageErr(fmt.Errorf("deedglass keys list takes no arguments (got %q)", args[0]), "Run deedglass help keys list.")
	}
	var (
		ks  *deed.KeySet
		url string
	)
	if e.Globals.Server != "" {
		var err error
		if ks, url, err = fetchKeySet(ctx, e.Globals.Server); err != nil {
			return err
		}
	} else {
		cfg, err := loadFor(e, config.NeedDatabase)
		if err != nil {
			return err
		}
		st, err := openKeys(ctx, cfg)
		if err != nil {
			return err
		}
		defer st.Close()
		rows, err := st.SealKeys(ctx)
		if err != nil {
			return fmt.Errorf("reading seal keys: %w", err)
		}
		recs := make([]deed.KeyRecord, 0, len(rows))
		for _, r := range rows {
			recs = append(recs, r.Record())
		}
		base := strings.TrimSuffix(cfg.BaseURL.String(), "/")
		if ks, err = deed.NewKeySet(base, recs); err != nil {
			return &ExitError{Code: ExitFailure, ErrCode: "seal_key_rejected", Err: errors.New("The database holds a seal key Deedglass refuses to publish"),
				Next: "Run deedglass keys list --json to see it, and contact support."}
		}
		url = base + keysPath
	}
	if e.Globals.JSON {
		return writeJSON(e, ks)
	}
	printKeyTable(e, ks, url)
	return nil
}

const keysPath = "/.well-known/deedglass/keys.json"

func printKeyTable(e *Env, ks *deed.KeySet, url string) {
	s := e.Style
	head := []string{"ID", "STATUS", "ACTIVATED", "RETIRED", "THUMBPRINT"}
	var rows [][]string
	active := 0
	for i := len(ks.Keys) - 1; i >= 0; i-- { // newest first
		k := ks.Keys[i]
		status := k.Status
		switch k.Status {
		case deed.KeyActive:
			active++
			status = s.Status(ui.OK, "active")
		case deed.KeyRevoked:
			status = s.Status(ui.Fail, "revoked")
		}
		end := ui.None
		if k.RevokedAt != nil {
			end = minuteUTC(*k.RevokedAt)
		} else if k.RetiredAt != nil {
			end = minuteUTC(*k.RetiredAt)
		}
		id := k.ID
		if e.IsTTY && e.Globals.Verbose == 0 {
			id = ui.ShortID(id) // CLI_STYLE.md › Ids: -v, --json and a pipe carry the full id
		}
		if k.Alg != deed.AlgEd25519 {
			id += " " + k.Alg // ADR-109: a key of another algorithm says which
		}
		rows = append(rows, []string{id, status, minuteUTC(k.ActivatedAt), end, shortHash(k.Thumbprint)})
	}
	ui.Lines(e.Stdout, s.Table(head, rows, nil)...)
	fmt.Fprintln(e.Stdout, s.Dim(fmt.Sprintf("%d %s, %d active", len(ks.Keys), plural(len(ks.Keys), "seal key", "seal keys"), active)))
	ui.Lines(e.Stdout, s.KV("", e.Width, ui.Row{Key: "published", Value: url})...)
}

// fetchKeySet reads a server's public keys.json (no credentials) and checks every key.
func fetchKeySet(ctx context.Context, server string) (*deed.KeySet, string, error) {
	u, err := url.Parse(strings.TrimSuffix(server, "/"))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil {
		return nil, "", usageErr(fmt.Errorf("--server must be an http or https URL (got %q)", server), "For example --server http://127.0.0.1:8080.")
	}
	full := u.String() + keysPath
	unreachable := func(reason string) error {
		return &ExitError{Code: ExitFailure, ErrCode: "server_unreachable", Err: fmt.Errorf("Can't read the seal keys at %s: %s", full, reason),
			Next: "Check the URL, and that deedglass serve is running."}
	}
	rctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(rctx, http.MethodGet, full, nil)
	req.Header.Set("User-Agent", "deedglass/"+buildInfo().Version)
	client := egress.FirstParty(egress.FirstPartyOptions{Timeout: 5 * time.Second})
	res, err := client.Do(req)
	if err != nil {
		return nil, "", unreachable(lastPart(err))
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode != http.StatusOK {
		return nil, "", unreachable(fmt.Sprintf("it answered %d", res.StatusCode))
	}
	ks, err := deed.ParseKeySet(bytes.TrimSpace(body))
	if err != nil {
		var ve *deed.VerifyError
		if errors.As(err, &ve) {
			msg, next := deed.Message(ve)
			return nil, "", &ExitError{Code: ExitFailure, ErrCode: string(ve.Code), Err: errors.New(strings.TrimSuffix(msg, ".")), Next: next}
		}
		return nil, "", unreachable("the answer is not a seal key set")
	}
	return ks, full, nil
}

// --- shared ---

func loadFor(e *Env, need config.Need) (*config.Config, error) {
	cfg, warnings, err := config.Load(cliLoadOptions(e))
	var ps *config.Problems
	if errors.As(err, &ps) {
		return nil, configProblems(e, ps, warnings)
	}
	if err != nil {
		return nil, err
	}
	if err := cfg.Require(need); errors.As(err, &ps) {
		return nil, configProblems(e, ps, warnings)
	}
	printWarnings(e, warnings)
	return cfg, nil
}

func openKeys(ctx context.Context, cfg *config.Config) (keyStore, error) {
	octx, cancel := context.WithTimeout(ctx, openBudget)
	defer cancel()
	st, err := openKeyStore(octx, cfg.DatabaseURL)
	if err != nil {
		t := targetOf(cfg.DatabaseURL)
		cause := err
		if u := errors.Unwrap(err); u != nil {
			cause = u
		}
		return nil, &ExitError{Code: ExitFailure, ErrCode: "database_unreachable",
			Err:  fmt.Errorf("Can't connect to %s as %s: %s", t.addr(), t.User, connectReason(cause, cfg.DatabaseURL.Reveal())),
			Next: "Check that Postgres is running, or run deedglass init for a local stack."}
	}
	return st, nil
}

// printRows prints aligned "  label  value" lines (brand §13.3: the next step on its own line).
func printRows(e *Env, rows [][2]string) {
	w := 0
	for _, r := range rows {
		w = max(w, ui.Width(r[0]))
	}
	for _, r := range rows {
		fmt.Fprintf(e.Stdout, "  %s  %s\n", ui.Pad(r[0], w), r[1])
	}
}

// shortHash shows a digest as sha256:3f9a…c21e (brand §12).
func shortHash(d string) string {
	h := strings.TrimPrefix(d, deed.DigestPrefix)
	if len(h) < 8 {
		return d
	}
	return deed.DigestPrefix + h[:4] + "…" + h[len(h)-4:]
}

// minuteUTC shows a deed time as "2026-10-02 09:17 UTC".
func minuteUTC(s string) string {
	t, err := deed.ParseTime(s)
	if err != nil {
		return s
	}
	return t.Format("2006-01-02 15:04") + " UTC"
}

// humanDur prints 5m0s as 5m and 1h0m0s as 1h.
func humanDur(d time.Duration) string {
	s := d.String()
	if strings.HasSuffix(s, "m0s") {
		s = strings.TrimSuffix(s, "0s")
	}
	if strings.HasSuffix(s, "h0m") {
		s = strings.TrimSuffix(s, "0m")
	}
	return s
}
