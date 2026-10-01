package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/deedglass/deedglass/cmd/deedglass/internal/ui"
	"github.com/deedglass/deedglass/internal/config"
	"github.com/deedglass/deedglass/internal/deed"
	"github.com/deedglass/deedglass/internal/httpx"
	"github.com/deedglass/deedglass/internal/store"
)

var base64URL = base64.RawURLEncoding.Strict()

// Seal key messages at serve start (S04 §5): what happened, then the next step.
const (
	msgNoSealKey     = "No seal key. DEEDGLASS_SEAL_KEY_FILE is not set."
	nextNoSealKey    = "deedglass keys gen (in dev, deedglass serve creates one for you)."
	msgTestKey       = "This is a published test key. It must never seal real deeds."
	nextGen          = "deedglass keys gen"
	nextRetired      = "deedglass keys list, then point DEEDGLASS_SEAL_KEY_FILE at the active key."
	nextMismatch     = "Check DEEDGLASS_SEAL_KEY_FILE. If the file was replaced, generate a new key: deedglass keys gen"
	nextUnreadable   = "Check DEEDGLASS_SEAL_KEY_FILE, or generate a new key: deedglass keys gen"
	nextDamaged      = "Restore the file from your backup, or generate a new key: deedglass keys gen"
	nextKeyConflict  = "Use the key file that matches that id, or generate a new key: deedglass keys gen"
	devCreatedFormat = "No seal key set. Created %s for this dev instance."
	devUsingFormat   = "No seal key set. Using %s (dev)."
)

// keyErr prints a refusal in the §5 shape (✕ message, then "  next  step") and returns exit 1;
// with --json it returns the usual error object instead.
func keyErr(e *Env, code, msg, next string) error {
	if e.Globals.JSON {
		return &ExitError{Code: ExitFailure, ErrCode: code, Err: errors.New(strings.TrimSuffix(msg, ".")), Next: next}
	}
	fmt.Fprintln(e.Stderr, e.Style.FailLine(msg))
	fmt.Fprintln(e.Stderr, "  next  "+next)
	return &ExitError{Code: ExitFailure, ErrCode: code, Err: errors.New(msg), printed: true}
}

// loadSealKey loads, checks and registers the seal key (S04 §3.7). It fails closed: serve never
// starts with a key it would refuse to seal with.
func loadSealKey(ctx context.Context, e *Env, cfg *config.Config, db *store.DB, log *slog.Logger) (*deed.FileSigner, error) {
	return loadSealKeyQuiet(ctx, e, cfg, db, log, false)
}

// loadSealKeyQuiet is loadSealKey for commands that seal as a side effect (deeds export, ledger
// prune, dev fake-action): with quiet set, success prints nothing; refusals still do.
func loadSealKeyQuiet(ctx context.Context, e *Env, cfg *config.Config, db *store.DB, log *slog.Logger, quiet bool) (*deed.FileSigner, error) {
	prod := cfg.Env == config.Prod
	path, shown := cfg.SealKeyFile, cfg.SealKeyFile
	if path == "" {
		if prod {
			return nil, keyErr(e, "seal_key_missing", msgNoSealKey, nextNoSealKey)
		}
		var created bool
		var err error
		if path, shown, created, err = devSealKey(e); err != nil {
			return nil, err
		}
		format := devUsingFormat
		if created {
			format = devCreatedFormat
		}
		switch {
		case !quiet:
			fmt.Fprintln(e.Stdout, e.Style.UnknownLine(fmt.Sprintf(format, shown)))
		case created:
			fmt.Fprintln(e.Stderr, e.Style.UnknownLine(fmt.Sprintf(format, shown)))
		}
	}
	s, err := deed.LoadKeyFile(path, deed.LoadOptions{Prod: prod})
	var kfe *deed.KeyFileError
	if errors.As(err, &kfe) {
		switch kfe.Problem {
		case deed.KeyFileTestKey:
			return nil, keyErr(e, "seal_key_test_key", msgTestKey, nextGen)
		case deed.KeyFileTooOpen:
			return nil, keyErr(e, "seal_key_file_mode", fmt.Sprintf("Seal key file %s can be read by other users (mode %04o).", shown, kfe.Mode.Perm()), "chmod 600 "+shown)
		case deed.KeyFileUnsafe:
			return nil, keyErr(e, "seal_key_rejected", fmt.Sprintf("Seal key %s is not a safe Ed25519 key (%s). It was not registered.", kfe.ID, kfe.Detail), nextGen)
		case deed.KeyFileMismatch:
			return nil, keyErr(e, "seal_key_file_damaged", fmt.Sprintf("Seal key file %s is damaged: its seed does not match its public key.", shown), nextDamaged)
		default:
			return nil, keyErr(e, "seal_key_file_unreadable", fmt.Sprintf("Seal key file %s can't be used: %s.", shown, kfe.Detail), nextUnreadable)
		}
	}
	if err != nil {
		return nil, err
	}
	row, created, err := db.RegisterSealKey(ctx, s.KeyID(), s.PublicKey())
	if errors.Is(err, store.ErrSealKeyConflict) {
		return nil, keyErr(e, "seal_key_conflict", fmt.Sprintf("Seal key %s's public key is already registered under another id.", s.KeyID()), nextKeyConflict)
	}
	if err != nil {
		return nil, fmt.Errorf("registering the seal key: %w", err)
	}
	if !bytes.Equal(row.PublicKey, s.PublicKey()) {
		return nil, keyErr(e, "seal_key_mismatch", fmt.Sprintf("Seal key %s in the file does not match the registered public key.", s.KeyID()), nextMismatch)
	}
	switch {
	case row.Status == deed.KeyRevoked || row.RevokedAt != nil:
		return nil, keyErr(e, "seal_key_revoked", fmt.Sprintf("Seal key %s was revoked at %s. Deedglass will not seal deeds with it.", s.KeyID(), minuteTime(row.RevokedAt)), nextRetired)
	case row.Status == deed.KeyRetired && row.RetiredAt != nil && row.RetiredAt.After(time.Now()):
		return nil, keyErr(e, "seal_key_retired", fmt.Sprintf("Seal key %s was replaced and retires at %s. Deedglass will not start with it.", s.KeyID(), minuteTime(row.RetiredAt)), nextRetired)
	case row.Status != deed.KeyActive:
		return nil, keyErr(e, "seal_key_retired", fmt.Sprintf("Seal key %s was retired at %s. Deedglass will not seal deeds with it.", s.KeyID(), minuteTime(row.RetiredAt)), nextRetired)
	}
	tp := deed.Thumbprint(s.PublicKey())
	log.Info("seal key loaded", "seal_key_id", s.KeyID(), "thumbprint", tp, "registered", created)
	if !quiet {
		fmt.Fprintln(e.Stdout, e.Style.OKLine("Seal key "+ui.Sep+" "+s.KeyID()+" "+ui.Sep+" "+shortHash(tp)))
	}
	return s, nil
}

// devSealKey picks the newest key file in .deedglass/keys, or creates one (dev only).
func devSealKey(e *Env) (path, shown string, created bool, err error) {
	shown, dir := keyDir(e, "", "")
	matches, _ := filepath.Glob(filepath.Join(dir, "sk_*.json"))
	if len(matches) > 0 {
		sort.Strings(matches) // ULIDs sort by time: the last is the newest
		p := matches[len(matches)-1]
		return p, filepath.Join(shown, filepath.Base(p)), false, nil
	}
	shownPath, kf, err := writeNewKey(dir, shown)
	if err != nil {
		return "", "", false, err
	}
	return filepath.Join(dir, kf.ID+".json"), shownPath, true, nil
}

func minuteTime(t *time.Time) string {
	if t == nil {
		return ui.None
	}
	return t.UTC().Format("2006-01-02 15:04") + " UTC"
}

// sealKeyCheck is the health check for the loaded key: ok, degraded inside a rotation overlap,
// down when it can no longer seal.
func sealKeyCheck(db *store.DB, id string) httpx.SealKeyCheck {
	return func(ctx context.Context) string {
		row, err := db.SealKey(ctx, id)
		switch {
		case err != nil, row.Status == deed.KeyRevoked, row.RetiredAt != nil && time.Until(*row.RetiredAt) < deed.RetireMargin:
			return httpx.StatusDown
		case row.Status == deed.KeyRetired:
			return httpx.StatusDegraded
		}
		return httpx.StatusOK
	}
}

// keySetSource builds keys.json from the store.
func keySetSource(db *store.DB, issuer string) httpx.KeySetSource {
	return func(ctx context.Context) (*deed.KeySet, error) {
		if db == nil {
			return nil, errors.New("no database")
		}
		rows, err := db.SealKeys(ctx)
		if err != nil {
			return nil, err
		}
		recs := make([]deed.KeyRecord, 0, len(rows))
		for _, r := range rows {
			recs = append(recs, r.Record())
		}
		return deed.NewKeySet(issuer, recs)
	}
}
