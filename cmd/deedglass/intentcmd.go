package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/deedglass/deedglass/cmd/deedglass/internal/ui"
	"github.com/deedglass/deedglass/internal/httpx"
	"github.com/deedglass/deedglass/internal/identity"
	"github.com/deedglass/deedglass/internal/intent"
	"github.com/deedglass/deedglass/pkg/deedglass"
)

// Signed-intent commands (S09, docs/INTENTS.md). keygen and dev sign-intent run locally: the
// private key never leaves the machine that runs the agent. add, list, remove and mode call the API
// with an org admin key, like the other agent commands.

// newIntentKey makes a key pair (tests swap in the RFC 8032 TEST 1 key).
var newIntentKey = deedglass.GenerateIntentKey

// intentKeyDir is where keygen writes private keys, relative to the working directory.
const intentKeyDir = ".deedglass/intent"

var intentKeygenValues struct {
	dir   string
	force bool
}

func intentKeygenFlags(fs *flag.FlagSet) {
	v := &intentKeygenValues
	v.dir, v.force = intentKeyDir, false
	fs.StringVar(&v.dir, "dir", intentKeyDir, "where to write <agent>.pem (created with mode 0700)")
	fs.BoolVar(&v.force, "force", false, "replace an existing key file")
}

func runIntentKeygen(ctx context.Context, e *Env, args []string) error {
	pos, err := mixedArgs("deedglass agent intent keygen", intentKeygenFlags, args)
	if err != nil {
		return err
	}
	name, err := oneArg("deedglass agent intent keygen", "an agent name", "deedglass agent intent keygen support-bot", pos)
	if err != nil {
		return err
	}
	if !identity.ValidAgentName(name) && !strings.HasPrefix(name, "agt_") {
		return usageErr(fmt.Errorf("%q isn't an agent name or id", name), "Agent names use lowercase letters, digits and hyphens, like support-bot.")
	}
	v := intentKeygenValues
	path := filepath.Join(v.dir, name+".pem")
	if _, err := os.Stat(path); err == nil && !v.force {
		return &ExitError{Code: ExitFailure, ErrCode: "key_exists", Err: fmt.Errorf("%s already exists", path),
			Next: "Keep using it, or pass --force to replace it (register the new key before you remove the old one)."}
	}
	priv, pub, kid, err := newIntentKey()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(v.dir, 0o700); err != nil {
		return &ExitError{Code: ExitFailure, ErrCode: "write_failed", Err: fmt.Errorf("Can't create %s: %s", v.dir, lastPart(err))}
	}
	if err := writePrivate(path, priv); err != nil {
		return &ExitError{Code: ExitFailure, ErrCode: "write_failed", Err: fmt.Errorf("Can't write %s: %s", path, lastPart(err))}
	}
	if e.Globals.JSON {
		return writeJSON(e, map[string]any{"object": "intent_keypair", "agent": name, "kid": kid, "public_key": pub, "private_key_path": path})
	}
	s := e.Style
	fmt.Fprintln(e.Stdout, s.OKLine(fmt.Sprintf("Intent key created %s %s %s kid %s", ui.Sep, name, ui.Sep, kid)))
	fmt.Fprintf(e.Stdout, "  Private key  %s (mode 0600). Keep it with the agent; it never goes to Deedglass.\n", path)
	fmt.Fprintf(e.Stdout, "  Public key   %s\n", pub)
	fmt.Fprintf(e.Stdout, "  Next: deedglass agent intent add %s --public-key %s\n", name, pub)
	return nil
}

// writePrivate writes b to path with mode 0600, refusing to follow a symlink there.
func writePrivate(path string, b []byte) error {
	if fi, err := os.Lstat(path); err == nil && fi.Mode()&os.ModeSymlink != 0 {
		return errors.New("it is a symbolic link")
	}
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(b); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
}

var intentAddKey string

func intentAddFlags(fs *flag.FlagSet) {
	intentAddKey = ""
	fs.StringVar(&intentAddKey, "public-key", "", `the public key keygen printed, "ed25519:…"`)
}

func runIntentAdd(ctx context.Context, e *Env, args []string) error {
	pos, err := mixedArgs("deedglass agent intent add", intentAddFlags, args)
	if err != nil {
		return err
	}
	ref, err := oneArg("deedglass agent intent add", "an agent name or id", "deedglass agent intent add support-bot --public-key ed25519:…", pos)
	if err != nil {
		return err
	}
	if intentAddKey == "" {
		return usageErr(errors.New("deedglass agent intent add needs --public-key"), "Generate a key with deedglass agent intent keygen "+ref+", then pass the public key it prints.")
	}
	c, err := apiClient(e)
	if err != nil {
		return err
	}
	a, err := resolveAgent(ctx, e, c, ref)
	if err != nil {
		return err
	}
	var res identity.IntentKeyObject
	if _, err := c.Do(ctx, "POST", "/v1/agents/"+a.ID+"/intent-keys", map[string]string{"public_key": intentAddKey}, &res); err != nil {
		return apiErr(e, c, err)
	}
	if e.Globals.JSON {
		return writeJSON(e, res)
	}
	s := e.Style
	line := fmt.Sprintf("Intent key registered %s %s %s kid %s", ui.Sep, a.Name, ui.Sep, identity.ShortKID(res.KID))
	if res.Deed != nil {
		line += fmt.Sprintf(" %s %s sealed", ui.Sep, *res.Deed)
	}
	fmt.Fprintln(e.Stdout, s.OKLine(line))
	switch a.IntentMode {
	case identity.IntentRequired:
		fmt.Fprintf(e.Stdout, "  %s already requires signed intents; calls signed with this key are accepted now.\n", a.Name)
	case identity.IntentOptional:
		fmt.Fprintf(e.Stdout, "  Signed intents stay optional for %s until you require them:\n", a.Name)
		fmt.Fprintf(e.Stdout, "  deedglass agent intent mode %s required\n", a.Name)
	default:
		fmt.Fprintf(e.Stdout, "  %s doesn't check signed intents yet. Check them when sent, or require them:\n", a.Name)
		fmt.Fprintf(e.Stdout, "  deedglass agent intent mode %s optional|required\n", a.Name)
	}
	return nil
}

func runIntentList(ctx context.Context, e *Env, args []string) error {
	ref, err := oneArg("deedglass agent intent list", "an agent name or id", "deedglass agent intent list support-bot", args)
	if err != nil {
		return err
	}
	c, err := apiClient(e)
	if err != nil {
		return err
	}
	a, err := resolveAgent(ctx, e, c, ref)
	if err != nil {
		return err
	}
	var l httpx.List[identity.IntentKeyObject]
	if _, err := c.Do(ctx, "GET", "/v1/agents/"+a.ID+"/intent-keys", nil, &l); err != nil {
		return apiErr(e, c, err)
	}
	if e.Globals.JSON {
		return writeJSON(e, l)
	}
	fmt.Fprintf(e.Stdout, "%s %s intent mode %s\n", a.Name, ui.Sep, a.IntentMode)
	if len(l.Data) == 0 {
		fmt.Fprintf(e.Stdout, "No intent keys yet. Generate one on the machine that runs the agent: deedglass agent intent keygen %s\n", a.Name)
		return nil
	}
	now := time.Now()
	rows := [][]string{{"KID", "PUBLIC KEY", "ADDED", "BY"}}
	for _, k := range l.Data {
		t := httpx.Time(k.CreatedAt)
		rows = append(rows, []string{k.KID, k.PublicKey, ago(now, &t), k.CreatedBy})
	}
	printTable(e, rows, nil)
	return nil
}

func runIntentRemove(ctx context.Context, e *Env, args []string) error {
	if len(args) != 2 {
		return usageErr(errors.New("deedglass agent intent remove needs an agent and a kid"), "deedglass agent intent remove support-bot 21fe31dfa154a261626bf854046fd227")
	}
	c, err := apiClient(e)
	if err != nil {
		return err
	}
	a, err := resolveAgent(ctx, e, c, args[0])
	if err != nil {
		return err
	}
	var res identity.IntentKeyDeleted
	if _, err := c.Do(ctx, "DELETE", "/v1/agents/"+a.ID+"/intent-keys/"+url.PathEscape(args[1]), nil, &res); err != nil {
		return apiErr(e, c, err)
	}
	if e.Globals.JSON {
		return writeJSON(e, res)
	}
	line := fmt.Sprintf("Intent key removed %s %s %s kid %s", ui.Sep, a.Name, ui.Sep, identity.ShortKID(res.KID))
	if res.Deed != nil {
		line += fmt.Sprintf(" %s %s sealed", ui.Sep, *res.Deed)
	}
	fmt.Fprintln(e.Stdout, e.Style.OKLine(line))
	fmt.Fprintln(e.Stdout, "  Every Deedglass instance refuses intents signed with it within 2 seconds.")
	return nil
}

func runIntentMode(ctx context.Context, e *Env, args []string) error {
	if len(args) != 2 {
		return usageErr(errors.New("deedglass agent intent mode needs an agent and a mode"), "deedglass agent intent mode support-bot off|optional|required")
	}
	mode := identity.IntentMode(args[1])
	if !mode.Valid() {
		return usageErr(fmt.Errorf("%q isn't a mode", args[1]), "Use off, optional or required.")
	}
	c, err := apiClient(e)
	if err != nil {
		return err
	}
	a, err := resolveAgent(ctx, e, c, args[0])
	if err != nil {
		return err
	}
	var res identity.AgentResult
	if _, err := c.Do(ctx, "PATCH", "/v1/agents/"+a.ID, map[string]string{"intent_mode": string(mode)}, &res); err != nil {
		return apiErr(e, c, err)
	}
	if e.Globals.JSON {
		return writeJSON(e, res)
	}
	sealed := " " + ui.Sep + " no change, no new record"
	if res.Deed != nil {
		sealed = fmt.Sprintf(" %s %s sealed", ui.Sep, *res.Deed)
	}
	s := e.Style
	switch mode {
	case identity.IntentRequired:
		fmt.Fprintln(e.Stdout, s.OKLine(a.Name+" now requires a signed intent on every call"+sealed))
		fmt.Fprintln(e.Stdout, "  Calls without a valid Deedglass-Intent header are refused with intent_required.")
		fmt.Fprintf(e.Stdout, "  MCP clients can't sign intents yet, so %s can act only through the SDKs or the REST API.\n", a.Name)
	case identity.IntentOptional:
		fmt.Fprintln(e.Stdout, s.OKLine(a.Name+" now checks signed intents when they are sent"+sealed))
		fmt.Fprintln(e.Stdout, "  Calls without a Deedglass-Intent header are still accepted; an invalid one is refused.")
	default:
		fmt.Fprintln(e.Stdout, s.OKLine(a.Name+" no longer checks signed intents"+sealed))
		fmt.Fprintf(e.Stdout, "  Deedglass-Intent headers from %s are ignored, and its records hold intent_signed false.\n", a.Name)
	}
	return nil
}

// --- dev sign-intent ---

var signIntentValues struct {
	key, tool, args, obo, session, agent, now, nonce string
	ttl                                              time.Duration
	explain                                          bool
}

func signIntentFlags(fs *flag.FlagSet) {
	v := &signIntentValues
	*v = struct {
		key, tool, args, obo, session, agent, now, nonce string
		ttl                                              time.Duration
		explain                                          bool
	}{ttl: deedglass.DefaultIntentTTL}
	fs.StringVar(&v.key, "key", "", "the PKCS#8 PEM private key keygen wrote")
	fs.StringVar(&v.tool, "tool", "", "the connector.tool name, like stripe.refund")
	fs.StringVar(&v.args, "args", "", `the exact JSON arguments, like '{"amount":1200}'`)
	fs.StringVar(&v.obo, "obo", "", "sign Deedglass-On-Behalf-Of: the person the agent serves")
	fs.StringVar(&v.session, "session", "", "sign Deedglass-Session: the conversation or run")
	fs.StringVar(&v.agent, "agent", "", "the agent's id (agt_…); from whoami when an API key is set")
	fs.DurationVar(&v.ttl, "ttl", deedglass.DefaultIntentTTL, "how long the intent is valid, at most 5m")
	fs.StringVar(&v.now, "now", "", "sign as of this RFC 3339 time (tests and vectors)")
	fs.StringVar(&v.nonce, "nonce", "", "use this nonce, 32 lowercase hex (tests and vectors)")
	fs.BoolVar(&v.explain, "explain", false, "show every step: args, digest, intent, hash, header")
}

var nonceRE = regexp.MustCompile(`^[0-9a-f]{32}$`)

func runSignIntent(ctx context.Context, e *Env, args []string) error {
	pos, err := mixedArgs("deedglass dev sign-intent", signIntentFlags, args)
	if err != nil {
		return err
	}
	if len(pos) > 0 {
		return usageErr(fmt.Errorf("deedglass dev sign-intent takes flags only (got %q)", pos[0]), signIntentExample)
	}
	v := signIntentValues
	switch {
	case v.key == "":
		return usageErr(errors.New("deedglass dev sign-intent needs --key"), signIntentExample)
	case v.tool == "":
		return usageErr(errors.New("deedglass dev sign-intent needs --tool"), signIntentExample)
	case v.args == "":
		return usageErr(errors.New("deedglass dev sign-intent needs --args (use '{}' for none)"), signIntentExample)
	case v.nonce != "" && !nonceRE.MatchString(v.nonce):
		return usageErr(errors.New("--nonce must be 32 lowercase hex characters"), "Leave it out to get 128 fresh random bits.")
	case v.ttl <= 0 || v.ttl > deedglass.MaxIntentTTL || v.ttl%time.Millisecond != 0:
		return usageErr(errors.New("--ttl must be more than 0 and at most 5m"), "Sign one intent per call; the default is 60s.")
	case v.obo != "" && !intent.ValidRef(v.obo):
		return usageErr(errors.New("--obo must be 1 to 256 characters: letters, digits and . _ ~ : @ / + = -"), "Use an opaque user id such as usr_ext_4821, not a name or email.")
	case v.session != "" && !intent.ValidRef(v.session):
		return usageErr(errors.New("--session must be 1 to 256 characters: letters, digits and . _ ~ : @ / + = -"), "Use an opaque id such as conv_9f2c.")
	}
	now := time.Now
	if v.now != "" {
		t, err := time.Parse(time.RFC3339Nano, v.now)
		if err != nil {
			return usageErr(fmt.Errorf("--now %q isn't an RFC 3339 time", v.now), "Use a time like 2026-10-03T09:30:00Z.")
		}
		now = func() time.Time { return t }
	}
	pemBytes, err := os.ReadFile(v.key)
	if err != nil {
		return &ExitError{Code: ExitFailure, ErrCode: "key_unreadable", Err: fmt.Errorf("Can't read %s: %s", v.key, lastPart(err)),
			Next: "Generate a key with deedglass agent intent keygen <agent>."}
	}
	org, agent := e.Globals.Org, v.agent
	if org == "" || agent == "" {
		if firstNonEmpty(e.Globals.APIKey, e.Getenv("DEEDGLASS_API_KEY")) == "" {
			return usageErr(errors.New("deedglass dev sign-intent needs --org and --agent, or the agent's API key"),
				"Pass --org org_… --agent agt_…, or set DEEDGLASS_API_KEY to the agent's key so whoami can name them.")
		}
		c, err := apiClient(e)
		if err != nil {
			return err
		}
		w, err := whoami(ctx, e, c)
		if err != nil {
			return err
		}
		if w.Agent == nil {
			return usageErr(errors.New("this API key is an org admin key, and intents are signed for one agent"), "Pass --agent agt_…, or use the agent's own key.")
		}
		org, agent = firstNonEmpty(org, w.Org.ID), firstNonEmpty(agent, w.Agent.ID)
	}
	signer, err := deedglass.LoadIntentSigner(pemBytes, org, agent)
	if err != nil {
		return &ExitError{Code: ExitFailure, ErrCode: "key_invalid", Err: errors.New(strings.TrimPrefix(err.Error(), "deedglass: ")),
			Next: "Generate a key with deedglass agent intent keygen <agent>."}
	}
	sg, err := signer.SignDetailed(v.tool, json.RawMessage(v.args), deedglass.SignOptions{OnBehalfOf: v.obo, Session: v.session, TTL: v.ttl, Now: now, Nonce: v.nonce})
	if err != nil {
		msg, _, _ := strings.Cut(strings.TrimPrefix(err.Error(), "deedglass: "), ": intent:")
		return &ExitError{Code: ExitFailure, ErrCode: "sign_failed", Err: errors.New(upperFirst(msg)),
			Next: "Check --org, --agent, --tool and --args against docs/INTENTS.md."}
	}
	if e.Globals.JSON {
		return writeJSON(e, map[string]any{"object": "signed_intent", "header": sg.Header, "intent_hash": sg.IntentHash, "intent": string(sg.Intent),
			"args_digest": sg.ArgsDigest, "nonce": sg.Nonce, "kid": signer.KID(), "expires_at": sg.ExpiresAt.Format(time.RFC3339)})
	}
	if !v.explain {
		fmt.Fprintln(e.Stdout, sg.Header)
		return nil
	}
	row := func(k, val string) { fmt.Fprintf(e.Stdout, "%-13s %s\n", k, val) }
	row("args", string(sg.Args))
	row("args_digest", sg.ArgsDigest)
	row("intent", fmt.Sprintf("%s   %d bytes", sg.Intent, len(sg.Intent)))
	row("intent_hash", sg.IntentHash)
	row("signed bytes", `"deedglass/intent/v1\n" + intent_hash`)
	row("kid", signer.KID())
	row("header", sg.Header)
	fmt.Fprintln(e.Stdout, e.Style.OKLine(fmt.Sprintf("Valid for %s %s nonce %s %s works once", ttlText(v.ttl), ui.Sep, shortHex(sg.Nonce), ui.Sep)))
	return nil
}

const signIntentExample = `deedglass dev sign-intent --key .deedglass/intent/support-bot.pem --tool stripe.refund --args '{"charge":"ch_3QxZ8f2L","amount":1200}' --org org_… --agent agt_…`

func ttlText(d time.Duration) string {
	if d%time.Minute == 0 {
		if d == time.Minute {
			return "60 s"
		}
		return fmt.Sprintf("%d min", int(d/time.Minute))
	}
	return fmt.Sprintf("%g s", d.Seconds())
}

func shortHex(s string) string {
	if len(s) <= 8 {
		return s
	}
	return s[:4] + "…" + s[len(s)-4:]
}
