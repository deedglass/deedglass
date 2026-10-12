package main

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/deedglass/deedglass/cmd/deedglass/internal/ui"
	"github.com/deedglass/deedglass/ee/scanfull"
	"github.com/deedglass/deedglass/internal/connector/egress"
	"github.com/deedglass/deedglass/internal/deed"
	"github.com/deedglass/deedglass/internal/ids"
	"github.com/deedglass/deedglass/internal/ledger"
	"github.com/deedglass/deedglass/internal/licence"
	"github.com/deedglass/deedglass/internal/scan"
)

// deedglass licence activate|show|refresh|deactivate and scan --full (S48, DL-039). This file and
// ee/ are the only places outside internal/licence that read a licence (TestLicenceReaders):
// nothing free ever opens it.

// --- seams for tests ---

var (
	licenceNow      = time.Now
	licenceHostname = os.Hostname
	licenceKeys     = licence.Embedded
	licenceHTTP     = func() *http.Client {
		return egress.FirstParty(egress.FirstPartyOptions{Timeout: licence.RequestTimeout, Proxy: true, Redirects: egress.RedirectSameHost})
	}
)

// licDodoOverride replaces Dodo's host in tests only (never an environment variable: a licence
// must be checked against Dodo itself).
var licDodoOverride string

// Copy (brand §4, §13.3; S48 §5.2). Prices and plans are words, never typed numbers beyond these.
const (
	licProURL       = "deedglass.com/pro"
	licBillingLine  = "Billing and cancel: deedglass.com/licence#billing"
	licCheckInLine  = "The licence checks in at most once a day, only when a Pro feature runs, and keeps working 14 days offline."
	licFullNeedsPro = "The full report is part of Pro or the $29 full scan report. Get one at deedglass.com/pro. Everything in deedglass scan stays free."
	licDateLayout   = "2 Jan 2006"
)

func planName(plan string) string {
	switch plan {
	case licence.PlanReport:
		return "Full scan report"
	case licence.PlanFounding:
		return "Founding partner"
	}
	return "Pro"
}

func licDate(t time.Time) string { return t.UTC().Format(licDateLayout) }

// licenceManager builds the manager from the client-side settings.
func licenceManager(e *Env) (*licence.Manager, error) {
	ks, err := licenceKeys()
	if err != nil {
		return nil, &ExitError{Code: ExitFailure, ErrCode: "licence_keys_invalid", Err: err, Next: "Reinstall deedglass; this build's licence keys can't be read."}
	}
	path := e.Getenv("DEEDGLASS_LICENCE_FILE")
	if path == "" {
		path = filepath.Join(e.LoadOptions().Dir, licence.DefaultTokenFile)
	}
	mode := strings.ToLower(firstNonEmpty(e.Getenv("DEEDGLASS_LICENCE_MODE"), "live"))
	if mode != "live" && mode != "test" {
		return nil, usageErr(fmt.Errorf("DEEDGLASS_LICENCE_MODE is %q; it takes live or test", clean(mode)), "Unset it, or set it to test for Dodo test-mode keys.")
	}
	dodo := licence.DodoLiveBase
	if mode == "test" {
		dodo = licence.DodoTestBase
	}
	if licDodoOverride != "" {
		dodo = licDodoOverride
	}
	return &licence.Manager{
		Path: path, Keys: ks, Now: licenceNow, Live: mode == "live",
		Client: &licence.Client{HTTP: licenceHTTP(), DodoBase: dodo,
			Server:    firstNonEmpty(e.Getenv("DEEDGLASS_LICENCE_SERVER"), licence.DefaultServer),
			NoNetwork: truthy(e.Getenv("DEEDGLASS_NO_NETWORK"))},
	}, nil
}

func truthy(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "1", "true":
		return true
	}
	return false
}

// licenceErr turns a licence error into the line a person reads (S48 §5.2) and an exit code.
func licenceErr(err error, offlineUntil time.Time) error {
	until := ""
	if !offlineUntil.IsZero() {
		until = licDate(offlineUntil)
	}
	var ue *licence.UnsafeFileError
	switch {
	case errors.Is(err, licence.ErrNoLicence):
		return &ExitError{Code: ExitFailure, ErrCode: "licence_missing", Plain: true,
			Err: errors.New("No licence on this machine. Everything free works without one"), Next: "Pro and the full scan report: " + licProURL}
	case errors.Is(err, licence.ErrKeyUnknown):
		return &ExitError{Code: ExitFailure, ErrCode: "licence_key_unknown",
			Err: errors.New("Dodo Payments doesn't know this key"), Next: "Check it against the email from Dodo, or ask at deedglass.com/support."}
	case errors.Is(err, licence.ErrKeyInactive):
		return &ExitError{Code: ExitFailure, ErrCode: "licence_key_inactive",
			Err: errors.New("This key is no longer active. It may have been refunded or cancelled"), Next: "See billing at deedglass.com/licence#billing, or ask at deedglass.com/support."}
	case errors.Is(err, licence.ErrKeyLimit):
		return &ExitError{Code: ExitFailure, ErrCode: "licence_key_limit",
			Err: errors.New("This key is active on 3 machines already"), Next: "Run deedglass licence deactivate on one of them first."}
	case errors.Is(err, licence.ErrSignature), errors.Is(err, licence.ErrBadTokenAnswer):
		return &ExitError{Code: ExitFailure, ErrCode: "licence_signature",
			Err: errors.New("The licence file was changed after it was signed, or wasn't signed by Deedglass"), Next: "Run deedglass licence refresh."}
	case errors.Is(err, licence.ErrMode):
		return &ExitError{Code: ExitFailure, ErrCode: "licence_mode",
			Err: errors.New("This licence is from Dodo's other mode (test or live)"), Next: "Set DEEDGLASS_LICENCE_MODE=test for test-mode keys, or activate a live key."}
	case errors.As(err, &ue):
		return &ExitError{Code: ExitFailure, ErrCode: "licence_file_unsafe", Err: fmt.Errorf("%s %s", clean(ue.Path), ue.Why),
			Next: "Make it a regular file with chmod 600 " + clean(ue.Path) + ", or run deedglass licence activate again."}
	case errors.Is(err, licence.ErrNetworkOff):
		e := &ExitError{Code: ExitFailure, ErrCode: "network_off", Plain: true, Err: errors.New("Network is off (DEEDGLASS_NO_NETWORK), so nothing was checked")}
		if until != "" {
			e.Code, e.Err = ExitOK, fmt.Errorf("Network is off (DEEDGLASS_NO_NETWORK). Pro keeps working until %s without a check", until)
		}
		return e
	case errors.Is(err, licence.ErrUnreachable), errors.Is(err, licence.ErrServerRefused):
		e := &ExitError{Code: ExitFailure, ErrCode: "licence_unreachable", Plain: true, Err: errors.New("Couldn't reach Dodo Payments or deedglass.com"), Next: "Try again in a minute."}
		if until != "" {
			e.Code, e.Err = ExitOK, fmt.Errorf("Couldn't reach Dodo Payments. Pro keeps working until %s", until)
			e.Next = "Try again later."
		}
		return e
	}
	return err
}

// --- licence activate ---

func runLicenceActivate(ctx context.Context, e *Env, args []string) error {
	if len(args) != 1 {
		return usageErr(errors.New("deedglass licence activate takes the key from Dodo's email"), "For example: deedglass licence activate DG-7Q2M-…")
	}
	key := strings.TrimSpace(args[0])
	m, err := licenceManager(e)
	if err != nil {
		return err
	}
	if !e.Globals.JSON {
		fmt.Fprintln(e.Stdout, e.Style.Violet(ui.Pending)+" Checking the key with Dodo Payments")
	}
	host, _ := licenceHostname()
	l, err := m.Activate(ctx, key, host)
	if err != nil {
		return licenceErr(err, time.Time{})
	}
	return printLicence(e, l, "activated")
}

func activeLine(l licence.Local) string {
	t := l.Token
	switch t.Plan {
	case licence.PlanReport:
		return fmt.Sprintf("Full scan report active %s Pro features for 30 days %s until %s %s %s", ui.Sep, ui.Sep, licDate(l.State.PeriodEnd), ui.Sep, t.ID)
	case licence.PlanFounding:
		return fmt.Sprintf("Founding partner place active %s Pro until %s %s %s", ui.Sep, licDate(l.State.PeriodEnd), ui.Sep, t.ID)
	}
	if !l.State.PeriodEnd.IsZero() {
		return fmt.Sprintf("Pro active %s renews %s %s %s", ui.Sep, licDate(l.State.PeriodEnd), ui.Sep, t.ID)
	}
	return fmt.Sprintf("Pro active %s %s", ui.Sep, t.ID)
}

func licenceJSON(l licence.Local, status string) map[string]any {
	st := l.State
	out := map[string]any{"object": "licence_status", "status": status, "id": l.Token.ID, "plan": l.Token.Plan,
		"features": l.Token.Features, "active": st.Active, "checked_days_ago": st.DaysSince,
		"offline_until": st.OfflineUntil.UTC().Format(licence.TimeLayout), "livemode": l.Token.Livemode, "period_end": nil}
	if !st.PeriodEnd.IsZero() {
		out["period_end"] = st.PeriodEnd.UTC().Format(licence.TimeLayout)
	}
	return out
}

func printLicence(e *Env, l licence.Local, status string) error {
	if e.Globals.JSON {
		return writeJSON(e, licenceJSON(l, status))
	}
	fmt.Fprintln(e.Stdout, e.Style.OKLine(activeLine(l)))
	fmt.Fprintln(e.Stdout, "  "+licCheckInLine)
	return nil
}

// --- licence show ---

func runLicenceShow(_ context.Context, e *Env, args []string) error {
	if len(args) > 0 {
		return usageErr(fmt.Errorf("deedglass licence show takes no arguments (got %q)", clean(args[0])), "Run deedglass licence show.")
	}
	m, err := licenceManager(e)
	if err != nil {
		return err
	}
	l, err := m.Load()
	if err != nil {
		return licenceErr(err, time.Time{})
	}
	st := l.State
	status := "active"
	if !st.Active {
		status = "paused"
		if st.Expired {
			status = "ended"
		}
	}
	if e.Globals.JSON {
		return writeJSON(e, licenceJSON(l, status))
	}
	switch status {
	case "active":
		fmt.Fprintln(e.Stdout, e.Style.OKLine(fmt.Sprintf("%s %s checked %d %s ago %s offline until %s", planName(l.Token.Plan), ui.Sep,
			st.DaysSince, plural(st.DaysSince, "day", "days"), ui.Sep, licDate(st.OfflineUntil))))
		fmt.Fprintln(e.Stdout, "  "+licBillingLine)
	case "ended":
		fmt.Fprintln(e.Stdout, e.Style.UnknownLine(fmt.Sprintf("Your %s plan ended on %s. Everything free still works. Pro: %s",
			planName(l.Token.Plan), licDate(st.PeriodEnd), licProURL)))
	default:
		fmt.Fprintln(e.Stdout, e.Style.UnknownLine(pausedLine(st.DaysSince)))
	}
	return nil
}

func pausedLine(days int) string {
	return fmt.Sprintf("Pro paused: your licence was last checked %d %s ago. Run deedglass licence refresh. Everything free still works.", days, plural(days, "day", "days"))
}

// --- licence refresh ---

func runLicenceRefresh(ctx context.Context, e *Env, args []string) error {
	if len(args) > 0 {
		return usageErr(fmt.Errorf("deedglass licence refresh takes no arguments (got %q)", clean(args[0])), "Run deedglass licence refresh.")
	}
	m, err := licenceManager(e)
	if err != nil {
		return err
	}
	old, lerr := m.Load()
	l, err := m.Refresh(ctx)
	if err != nil {
		until := time.Time{}
		if lerr == nil && old.State.Active {
			until = old.State.OfflineUntil
		}
		return licenceErr(err, until)
	}
	return printLicence(e, l, "refreshed")
}

// --- licence deactivate ---

func runLicenceDeactivate(ctx context.Context, e *Env, args []string) error {
	if len(args) > 0 {
		return usageErr(fmt.Errorf("deedglass licence deactivate takes no arguments (got %q)", clean(args[0])), "Run deedglass licence deactivate.")
	}
	m, err := licenceManager(e)
	if err != nil {
		return err
	}
	if err := m.Deactivate(ctx); err != nil {
		return licenceErr(err, time.Time{})
	}
	if e.Globals.JSON {
		return writeJSON(e, map[string]any{"object": "licence_status", "status": "deactivated"})
	}
	fmt.Fprintln(e.Stdout, e.Style.OKLine("Licence removed from this machine "+ui.Sep+" one activation freed"))
	fmt.Fprintln(e.Stdout, "  Activate it elsewhere with deedglass licence activate <key>.")
	return nil
}

// --- scan --full (Pro) ---

// scanFull renders the full report for a finished free scan. The free scan has already written
// its files; this only adds deedglass-full-report.html, then seals it on the person's own server
// when one is configured.
func scanFull(ctx context.Context, e *Env, r *scan.Report, outDir string, noSeal bool) error {
	m, err := licenceManager(e)
	if err != nil {
		return err
	}
	d := m.Require(ctx, licence.FeatureScanFull)
	if !d.Allowed {
		return proRefusal(d, licFullNeedsPro, "")
	}
	raw, err := json.Marshal(r)
	if err != nil {
		return err
	}
	html, err := scanfull.Render(raw, scanfull.Options{})
	if err != nil {
		return fmt.Errorf("the full report couldn't be rendered: %w", err)
	}
	path := filepath.Join(outDir, scanfull.FileName)
	if err := os.WriteFile(path, []byte(html), 0o644); err != nil {
		return fmt.Errorf("couldn't write %s: %w", path, err)
	}
	sum, size, err := hashFile(path)
	if err != nil {
		return err
	}
	res := map[string]any{"object": "full_report", "file": path, "sha256": sum, "bytes": size, "sealed": false}
	lines := []string{e.Style.OKLine(fmt.Sprintf("%s %s %s bytes %s %s", scanfull.FileName, ui.Sep, deed.FormatCount(size), ui.Sep, shortHash(sum))),
		"  The report stays on this machine."}
	server := firstNonEmpty(e.Globals.Server, e.Getenv("DEEDGLASS_SERVER"))
	key := firstNonEmpty(e.Globals.APIKey, e.Getenv("DEEDGLASS_API_KEY"))
	switch {
	case noSeal:
		lines = append(lines, "  Not sealed (--no-seal).")
	case server == "" || key == "":
		lines = append(lines, "  Not sealed: no Deedglass server is configured. Seal it on yours later: deedglass deeds seal-file "+scanfull.FileName+` --label "Full scan report"`)
	default:
		c, err := apiClient(e)
		if err != nil {
			return err
		}
		label := "Deedglass full report " + ui.Sep + " " + licenceNow().UTC().Format("2006-01-02")
		var sf ledger.SealedFile
		if _, err := c.Do(ctx, "POST", "/v1/deeds/files", ledger.SealFileBody{SHA256: sum, Bytes: &size, MediaType: "text/html", Label: label}, &sf); err != nil || sf.Deed == nil {
			lines = append(lines, e.Style.UnknownLine("Not sealed: your server refused the seal. Try deedglass deeds seal-file "+scanfull.FileName+" later."))
			break
		}
		envRaw, err := sf.Deed.MarshalCanonical()
		if err == nil {
			err = os.WriteFile(path+".deed.json", append(envRaw, '\n'), 0o644)
		}
		if err == nil {
			res["sealed"], res["deed"] = true, sf.Deed.ID
			lines = append(lines, e.Style.OKLine(fmt.Sprintf("Sealed %s %s %s", ui.Sep, sf.Deed.ID, ui.Sep+" "+shortHash(sum))),
				"  Verify any time: deedglass verify "+scanfull.FileName+".deed.json --file "+scanfull.FileName)
		}
	}
	if e.Globals.JSON {
		return nil // the scan's own JSON is the output; the report file is on disk
	}
	for _, l := range lines {
		fmt.Fprintln(e.Stdout, l)
	}
	_ = res
	return nil
}

// proRefusal is the line a person reads when a Pro feature can't run: an ended, paused or broken
// licence says so; otherwise needsPro. tail is added to each sentence (" Nothing was written.").
func proRefusal(d licence.Decision, needsPro, tail string) error {
	var e *ExitError
	switch {
	case errors.Is(d.Err, licence.ErrKeyInactive), errors.Is(d.Err, licence.ErrSignature), errors.Is(d.Err, licence.ErrMode):
		return licenceErr(d.Err, time.Time{})
	case d.Err == nil && d.Local.State.Expired:
		e = &ExitError{Code: ExitFailure, ErrCode: "licence_ended", Plain: true,
			Err: fmt.Errorf("Your %s plan ended on %s. Everything free still works", planName(d.Local.Token.Plan), licDate(d.Local.State.PeriodEnd)), Next: "Pro: " + licProURL}
	case d.Local.Token.ID != "" && !d.Local.State.Active && !errors.Is(d.Err, licence.ErrFeature):
		e = &ExitError{Code: ExitFailure, ErrCode: "licence_paused", Plain: true, Err: errors.New(strings.TrimSuffix(pausedLine(d.Local.State.DaysSince), "."))}
	default:
		e = &ExitError{Code: ExitFailure, ErrCode: "pro_required", Plain: true, Err: errors.New(strings.TrimSuffix(needsPro, "."))}
	}
	if tail != "" {
		e.Err = fmt.Errorf("%s.%s", e.Err.Error(), strings.TrimSuffix(tail, "."))
	}
	return e
}

// --- policy install: Pro packs (S47 §3.8) ---

// licPacksNeedPro is the refusal for a Pro pack without the feature.
const licPacksNeedPro = "Pro packs need a Pro licence: " + licProURL + "."

// packsProCheck answers whether this machine may install a Pro pack. It is called only for a pack
// whose tier is pro, so free packs never open the licence. An installed pack never asks again:
// enforcement never reads the licence.
func packsProCheck(ctx context.Context, e *Env) error {
	m, err := licenceManager(e)
	if err != nil {
		return err
	}
	if d := m.Require(ctx, licence.FeaturePacksPro); !d.Allowed {
		return proRefusal(d, licPacksNeedPro, " Nothing was written.")
	}
	return nil
}

// --- keys gen --purpose licence ---

// runKeysGenLicence writes a new lk_ key: the private JWK (0600) for the site's Pages secret and
// licence-keys.json, the public set to commit (internal/licence/licence-keys.json) and serve.
func runKeysGenLicence(e *Env, flagDir string) error {
	shown, dir := keyDir(e, flagDir, "")
	seed, err := deed.NewSeed(keysRand)
	if err != nil {
		return err
	}
	priv := ed25519.NewKeyFromSeed(seed)
	pub := priv.Public().(ed25519.PublicKey)
	id := "lk_" + strings.TrimPrefix(ids.New[ids.SealKeyKind]().String(), "sk_")
	enc := base64.RawURLEncoding
	jwk, _ := json.MarshalIndent(map[string]string{"kty": "OKP", "crv": "Ed25519", "kid": id, "d": enc.EncodeToString(seed), "x": enc.EncodeToString(pub)}, "", "  ")
	set, err := licence.KeySetJSON([]licence.KeyRecord{licence.Record(id, pub)})
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	privPath := filepath.Join(dir, id+".jwk.json")
	f, err := os.OpenFile(privPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return &ExitError{Code: ExitFailure, ErrCode: "key_file_write_failed", Err: fmt.Errorf("Can't write %s: %s", filepath.Join(shown, id+".jwk.json"), reason(err)),
			Next: "Check the directory's permissions, or choose another with --dir."}
	}
	_, werr := f.Write(append(jwk, '\n'))
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		return werr
	}
	pubPath := filepath.Join(dir, id+".licence-keys.json")
	if err := os.WriteFile(pubPath, set, 0o644); err != nil {
		return err
	}
	if e.Globals.JSON {
		return writeJSON(e, map[string]any{"object": "licence_key_file", "id": id, "private_jwk": filepath.Join(shown, id+".jwk.json"), "mode": "0600",
			"public_set": filepath.Join(shown, id+".licence-keys.json"), "thumbprint": deed.Thumbprint(pub)})
	}
	fmt.Fprintln(e.Stdout, e.Style.OKLine("Licence key created "+ui.Sep+" "+id))
	printRows(e, [][2]string{
		{"private", filepath.Join(shown, id+".jwk.json") + " (0600; the site's LICENCE_SIGNING_JWK secret, never in git)"},
		{"public", filepath.Join(shown, id+".licence-keys.json") + " (commit as internal/licence/licence-keys.json)"},
		{"thumbprint", shortHash(deed.Thumbprint(pub))},
	})
	return nil
}

// keysGenPurpose is keys gen's --purpose flag value.
var keysGenPurpose string

func keysGenPurposeFlag(fs *flag.FlagSet) {
	fs.StringVar(&keysGenPurpose, "purpose", "seal", "seal (a deed seal key), licence (an lk_ key that signs licence tokens, never deeds) or packs (the key that signs the policy-packs index)")
}
