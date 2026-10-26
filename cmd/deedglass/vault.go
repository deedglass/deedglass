package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/deedglass/deedglass/internal/cli/ui"
	"github.com/deedglass/deedglass/internal/config"
	"github.com/deedglass/deedglass/internal/httpx"
	"github.com/deedglass/deedglass/internal/store"
	"github.com/deedglass/deedglass/internal/vault"
)

// deedglass vault status|rotate (S16) run on the server host, like migrate: they read
// DEEDGLASS_DATABASE_URL and DEEDGLASS_VAULT_KEY, walk every org one transaction at a time, and
// never print a key or a credential, only key ids and counts.

type vaultSession struct {
	cfg *config.Config
	db  *store.DB
	vs  *vault.Store
}

func (s *vaultSession) Close() { s.db.Close() }

func openVault(ctx context.Context, e *Env, sign bool) (*vaultSession, error) {
	cfg, warnings, err := config.Load(cliLoadOptions(e))
	var ps *config.Problems
	if errors.As(err, &ps) {
		return nil, configProblems(e, ps, warnings)
	}
	if err != nil {
		return nil, err
	}
	if err := cfg.Require(config.NeedDatabase | config.NeedVault); errors.As(err, &ps) {
		return nil, configProblems(e, ps, warnings)
	}
	ring, err := vault.NewRing(cfg.VaultKey.Reveal())
	if err != nil {
		return nil, failNext(e, ExitUsage, "config_invalid", "DEEDGLASS_VAULT_KEY can't be read.", "Run deedglass config print for the problem.")
	}
	db, err := store.Open(ctx, cfg.DatabaseURL, store.Options{MaxConns: 4})
	if err != nil {
		return nil, failNext(e, ExitUsage, "database_unreachable", "Can't reach the database in DEEDGLASS_DATABASE_URL.",
			"Check that Postgres is running and that migrations ran (deedglass migrate up).")
	}
	log := slog.New(slog.DiscardHandler)
	if e.Globals.Verbose > 0 {
		log = slog.New(vault.NewRedactingHandler(httpx.NewLogger(e.Stderr, false, cfg.LogLevel).Handler()))
	}
	o := vault.StoreOptions{Logger: log, Prod: cfg.Env == config.Prod}
	if sign {
		signer, err := loadSealKeyQuiet(ctx, e, cfg, db, log, true)
		if err != nil {
			db.Close()
			return nil, err
		}
		o.Ledger = newLedger(db, signer, cfg, log)
	}
	return &vaultSession{cfg: cfg, db: db, vs: vault.NewStore(db, ring, o)}, nil
}

func runVaultStatus(ctx context.Context, e *Env, args []string) error {
	if len(args) > 0 {
		return usageErr(fmt.Errorf("deedglass vault status takes no arguments (got %q)", args[0]), "deedglass vault status [--json]")
	}
	s, err := openVault(ctx, e, false)
	if err != nil {
		return err
	}
	defer s.Close()
	st, err := s.vs.Status(ctx)
	if err != nil {
		return err
	}
	if e.Globals.JSON {
		return writeJSON(e, st)
	}
	sty := e.Style
	head := fmt.Sprintf("Vault %s %s %s %s %s active %s", ui.Sep, countWord(st.Total, "credential"), ui.Sep, vault.Alg, ui.Sep, st.Active)
	if len(st.Unknown) > 0 {
		fmt.Fprintln(e.Stdout, sty.FailLine(head))
	} else if st.Pending > 0 {
		fmt.Fprintln(e.Stdout, sty.UnknownLine(head))
	} else {
		fmt.Fprintln(e.Stdout, sty.OKLine(head))
	}
	for i, id := range st.Ring {
		note := ""
		if i == 0 {
			note = " (active)"
		}
		fmt.Fprintf(e.Stdout, "  %s %s %s%s\n", id, ui.Sep, countWord(st.ByKEK[id], "credential"), note)
	}
	for _, id := range sortedKeys(st.Unknown) {
		fmt.Fprintf(e.Stdout, "  %s %s %s %s %s\n", sty.Red(ui.Fail), id, ui.Sep, countWord(st.Unknown[id], "credential"), "not in DEEDGLASS_VAULT_KEY")
	}
	switch {
	case len(st.Unknown) > 0:
		for _, id := range sortedKeys(st.Unknown) {
			fmt.Fprintf(e.Stdout, "  Next: add %s back as the second key in DEEDGLASS_VAULT_KEY, restart, then run deedglass vault rotate.\n", id)
			break
		}
		return &ExitError{Code: ExitFailure, ErrCode: "vault_unavailable", Err: errors.New("unknown key"), printed: true}
	case st.Pending > 0:
		fmt.Fprintf(e.Stdout, "  Next: deedglass vault rotate moves %s to %s.\n", countWord(st.Pending, "credential"), st.Active)
	}
	return nil
}

func countWord(n int, word string) string { return fmt.Sprintf("%d %s", n, plural(n, word, word+"s")) }

func runVaultRotate(ctx context.Context, e *Env, args []string) error {
	if len(args) > 0 {
		return usageErr(fmt.Errorf("deedglass vault rotate takes no arguments (got %q)", args[0]), "deedglass vault rotate [--json]")
	}
	s, err := openVault(ctx, e, true)
	if err != nil {
		return err
	}
	defer s.Close()
	sty := e.Style
	progress := func(done, total int) {
		if e.Globals.JSON || total == 0 {
			return
		}
		line := fmt.Sprintf("%s Rewrapping data keys %s %d of %d", sty.Violet(ui.Pending), ui.Sep, done, total)
		if e.IsTTY {
			fmt.Fprintf(e.Stdout, "\r%s", line)
			if done == total {
				fmt.Fprintln(e.Stdout)
			}
		} else if done == total {
			fmt.Fprintln(e.Stdout, line)
		}
	}
	res, err := s.vs.Rotate(ctx, progress)
	var uk *vault.UnknownKEKError
	if errors.As(err, &uk) {
		return &ExitError{Code: ExitFailure, ErrCode: "vault_unavailable", Err: errors.New(strings.TrimSuffix(uk.Error(), "."))}
	}
	if err != nil {
		return err
	}
	if e.Globals.JSON {
		return writeJSON(e, res)
	}
	msg := fmt.Sprintf("All %s %s %s.", countWord(res.Total, "credential"), plural(res.Total, "uses", "use"), res.Active)
	if res.Total == 0 {
		msg = "No credentials are stored yet. New ones use " + res.Active + "."
	}
	if len(res.Unused) > 0 {
		msg += fmt.Sprintf(" You can remove %s from DEEDGLASS_VAULT_KEY.", strings.Join(res.Unused, " and "))
	}
	fmt.Fprintln(e.Stdout, sty.OKLine(msg))
	if len(res.Deeds) > 0 {
		fmt.Fprintf(e.Stdout, "  %s rewrapped %s %s sealed (vault.rotated)\n", countWord(res.Rewrapped, "data key"), ui.Sep, countWord(len(res.Deeds), "deed"))
	}
	return nil
}
