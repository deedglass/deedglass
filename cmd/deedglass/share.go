package main

// S47: `deeds share <id>` and `mcp install-links`.
//
// deeds share prints https://deedglass.com/verify#d=<b64url(JCS envelope)>&k=<b64url(JCS seal_key_set)>.
// The fragment never reaches a server; /verify checks the deed offline against the key in the link
// and says so. The link holds one deed (ids, agent name, tool, decision, verdict and digests; never
// arguments or results) and asks first, [y/N].

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"strings"

	"github.com/gowebpki/jcs"

	"github.com/deedglass/deedglass/internal/deed"
	"github.com/deedglass/deedglass/internal/installlinks"
	"github.com/deedglass/deedglass/internal/ledger"
)

// MaxDeedShareURL is the longest deed link deeds share prints (S47 §3.4).
const MaxDeedShareURL = 6144

var deedsShareYes bool

func deedsShareFlags(fs *flag.FlagSet) {
	deedsShareYes = false
	fs.BoolVar(&deedsShareYes, "yes", false, "make the link without asking")
}

// DeedShareLink builds the /verify link from a stored envelope and its key set.
func DeedShareLink(envelope []byte, ks *deed.KeySet) (string, error) {
	env, err := jcs.Transform(envelope)
	if err != nil {
		return "", err
	}
	kb, err := json.Marshal(ks)
	if err != nil {
		return "", err
	}
	keys, err := jcs.Transform(kb)
	if err != nil {
		return "", err
	}
	u := "https://deedglass.com/verify#d=" + base64.RawURLEncoding.EncodeToString(env) + "&k=" + base64.RawURLEncoding.EncodeToString(keys)
	if len(u) > MaxDeedShareURL {
		return "", fmt.Errorf("the link would be %d characters (the limit is %d)", len(u), MaxDeedShareURL)
	}
	return u, nil
}

func runDeedsShare(ctx context.Context, e *Env, args []string) error {
	if len(args) != 1 {
		return usageErr(errors.New("deedglass deeds share needs one deed id or seq"), "deedglass deeds share deed_…")
	}
	s, err := openLedger(ctx, e, false)
	if err != nil {
		return err
	}
	defer s.Close()
	ref := strings.ToLower(strings.TrimSpace(args[0]))
	d, err := s.led.GetDeed(ctx, s.org.ID, ref)
	if errors.Is(err, ledger.ErrNotFound) {
		return failNext(e, ExitFailure, "not_found", fmt.Sprintf("No deed %s in %s.", clean(ref), s.org.Slug), "deedglass deeds list")
	}
	if err != nil {
		return err
	}
	env, err := deed.ParseEnvelope(d.Envelope)
	if err != nil {
		return failNext(e, ExitFailure, "deed_unreadable", "This record does not parse, so it can't be shared.", "deedglass deeds show "+d.ID)
	}
	ks, err := s.led.SealKeySet(ctx, env.SealKeyID)
	if err != nil {
		return err
	}
	link, err := DeedShareLink(d.Envelope, ks)
	if err != nil {
		return failNext(e, ExitFailure, "share_too_long", "This record is too long for a link: "+err.Error()+".",
			"deedglass deeds export --from "+fmt.Sprint(d.Seq)+" --to "+fmt.Sprint(d.Seq)+" and share the bundle")
	}
	if !deedsShareYes && !e.Globals.JSON {
		fmt.Fprint(e.Stderr, "This link holds one deed: org id, agent name, tool, decision, verdict and digests. No arguments or results. Make the link? [y/N] ")
		ans, _ := bufio.NewReader(e.stdin()).ReadString('\n')
		if a := strings.ToLower(strings.TrimSpace(ans)); a != "y" && a != "yes" {
			fmt.Fprintln(e.Stderr, "No link made.")
			return nil
		}
	}
	if e.Globals.JSON {
		return writeJSON(e, map[string]any{"object": "deed_share", "deed": d.ID, "url": link})
	}
	fmt.Fprintln(e.Stdout, e.Style.OKLine(link))
	fmt.Fprintln(e.Stdout, "The part after # stays in the browser. /verify checks it against the key in the link; compare that key with your server's /.well-known/deedglass/keys.json.")
	return nil
}

func runMCPInstallLinks(_ context.Context, e *Env, args []string) error {
	if len(args) > 0 {
		return usageErr(fmt.Errorf("deedglass mcp install-links takes no arguments (got %q)", args[0]), "Run deedglass mcp install-links [--json].")
	}
	if e.Globals.JSON {
		return writeJSON(e, map[string]any{"object": "install_links", "version": installlinks.Version, "server": installlinks.Server(), "links": installlinks.Links()})
	}
	for _, l := range installlinks.Links() {
		v := l.URL
		if v == "" {
			v = l.Command
		}
		fmt.Fprintf(e.Stdout, "%-38s %s\n", l.Client, v)
	}
	fmt.Fprintln(e.Stdout, "No link holds a key. Each client asks Deedglass for one when it starts: run npx deedglass init first.")
	return nil
}
