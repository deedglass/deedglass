package main

//deedglass:allow-exec opens the Google consent page in the person's browser (open or xdg-open) for connector gmail authorize

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/deedglass/deedglass/cmd/deedglass/internal/ui"
	"github.com/deedglass/deedglass/internal/connector"
	"github.com/deedglass/deedglass/internal/connector/egress"
	"github.com/deedglass/deedglass/internal/connector/gmail"
	"github.com/deedglass/deedglass/internal/connector/stripe"
	"github.com/deedglass/deedglass/internal/httpx"
	"github.com/deedglass/deedglass/internal/ids"
	"github.com/deedglass/deedglass/internal/money"
	"github.com/deedglass/deedglass/internal/vault"
)

// S30: deedglass connector types (the catalog), connector gmail authorize, dev stripe-fixtures,
// and the client-side Stripe key refusal in connector credentials set.

// --- connector types ---

var connectorTypesValues struct{ typ string }

func connectorTypesFlags(fs *flag.FlagSet) {
	connectorTypesValues.typ = ""
	fs.StringVar(&connectorTypesValues.typ, "type", "", "show one type: its tools, credentials, setup and limits")
}

// modeText is a type's MODE column.
func modeText(t connector.TypeInfo) string {
	var out []string
	for _, m := range t.Modes {
		out = append(out, strings.ReplaceAll(m, "_", " "))
	}
	return strings.Join(out, ", ")
}

// toolsText is a type's TOOLS column.
func toolsText(t connector.TypeInfo) string {
	switch {
	case t.Type == "postgres":
		return "built-in"
	case t.Type == "rest":
		return "from config"
	case t.ToolsDynamic:
		return "from server"
	}
	return strconv.Itoa(len(t.Tools))
}

func runConnectorTypes(ctx context.Context, e *Env, args []string) error {
	if len(args) > 0 {
		return usageErr(fmt.Errorf("deedglass connector types takes no arguments (got %q)", args[0]), "deedglass connector types [--type stripe] [--json]")
	}
	c, err := apiClient(e)
	if err != nil {
		return err
	}
	if t := connectorTypesValues.typ; t != "" {
		var one connector.TypeInfo
		if _, err := c.Do(ctx, "GET", "/v1/connector-types/"+url.PathEscape(t), nil, &one); err != nil {
			return apiErr(e, c, err)
		}
		if e.Globals.JSON {
			return writeJSON(e, one)
		}
		printConnectorType(e, one)
		return nil
	}
	var l httpx.List[connector.TypeInfo]
	if _, err := c.Do(ctx, "GET", "/v1/connector-types", nil, &l); err != nil {
		return apiErr(e, c, err)
	}
	if e.Globals.JSON {
		return writeJSON(e, l.Data)
	}
	rows := [][]string{{"TYPE", "TITLE", "LEVELS", "TOOLS", "MODE"}}
	for _, t := range l.Data {
		rows = append(rows, []string{t.Type, t.Title, strings.Join(t.Levels, " "), toolsText(t), modeText(t)})
	}
	printTable(e, rows, headerDim(e))
	return nil
}

func printConnectorType(e *Env, t connector.TypeInfo) {
	s := e.Style
	head := []string{s.Bold(t.Type), t.Title, strings.Join(t.Levels, " "), t.Status}
	if m := modeText(t); m != "" {
		head = append(head, m)
	}
	fmt.Fprintln(e.Stdout, strings.Join(head, " "+ui.Sep+" "))
	fmt.Fprintln(e.Stdout, "  "+t.Summary)
	if len(t.Tools) > 0 {
		fmt.Fprintln(e.Stdout)
		rows := [][]string{{"TOOL", "SIDE EFFECT", "LEVELS", "READ-BACK"}}
		for _, tl := range t.Tools {
			rows = append(rows, []string{tl.Name, string(tl.SideEffect), strings.Join(tl.Levels, " "), tl.ReadBack})
		}
		printTable(e, rows, headerDim(e))
	}
	if len(t.Setup) > 0 {
		fmt.Fprintln(e.Stdout, "\nSetup")
		for i, st := range t.Setup {
			fmt.Fprintf(e.Stdout, "  %d. %s\n", i+1, st.Title)
			if st.Command != "" {
				fmt.Fprintf(e.Stdout, "     %s\n", st.Command)
			}
		}
	}
	if len(t.Limits) > 0 {
		fmt.Fprintln(e.Stdout, "\nWhat it doesn't do")
		for _, l := range t.Limits {
			fmt.Fprintf(e.Stdout, "  %s %s\n", ui.Sep, l)
		}
	}
	fmt.Fprintln(e.Stdout, "\n"+s.Dim(t.DocsURL))
}

// --- credentials set: the Stripe refusal before anything is sent ---

// stripeKeyRefusal is the CLI's refusal of a key that isn't a Stripe sandbox key. The server
// refuses it too; refusing here means a live key never leaves the founder's machine.
func stripeKeyRefusal(name, key string) error {
	code, msg := stripe.CheckKey(key)
	if code == "" {
		return nil
	}
	what := "this isn't a Stripe sandbox key"
	switch {
	case code == connector.CodeStripeLiveKeyRefused:
		what = "this is a live-mode key"
	case strings.HasPrefix(strings.TrimSpace(key), "sk_org_"):
		what = "this is an organization key"
	case strings.HasPrefix(strings.TrimSpace(key), "pk_"):
		what = "this is a publishable key"
	case strings.HasPrefix(strings.TrimSpace(key), "whsec_"):
		what = "this is a webhook secret"
	}
	detail := msg
	if code == connector.CodeStripeLiveKeyRefused {
		detail = "Deedglass connects to Stripe sandboxes only. Create a restricted key that starts with rk_test_ in your sandbox:"
	} else {
		detail = strings.TrimSuffix(msg, ".") + " in your sandbox:"
	}
	if code == connector.CodeValidationFailed {
		code = "validation_failed"
	}
	return &ExitError{Code: ExitFailure, ErrCode: code,
		Err:    fmt.Errorf("Refused %s %s %s %s (%s)", ui.Sep, name, ui.Sep, what, stripe.Prefix(key)),
		Detail: []string{detail}, Next: stripe.KeysURL}
}

// --- connector gmail authorize ---

var gmailAuthValues struct {
	clientFile string
	readonly   bool
	noBrowser  bool
}

func gmailAuthorizeFlags(fs *flag.FlagSet) {
	v := &gmailAuthValues
	*v = struct {
		clientFile string
		readonly   bool
		noBrowser  bool
	}{}
	fs.StringVar(&v.clientFile, "client-file", "", "the Desktop-app OAuth client JSON from Google Cloud")
	fs.BoolVar(&v.readonly, "readonly", false, "also ask for gmail.readonly, so agents can read threads (gmail.get_thread)")
	fs.BoolVar(&v.noBrowser, "no-browser", false, "print the sign-in link instead of opening a browser")
}

func runGmailAuthorize(ctx context.Context, e *Env, args []string) error {
	ref, err := oneArg("deedglass connector gmail authorize", "the Gmail connector's name",
		"deedglass connector gmail authorize gmail --client-file ./client_secret.json", args)
	if err != nil {
		return err
	}
	v := gmailAuthValues
	if v.clientFile == "" {
		return usageErr(errors.New("--client-file is required"), "Download the JSON of a Desktop app OAuth client from Google Cloud › Credentials, then pass --client-file ./client_secret.json.")
	}
	b, err := os.ReadFile(v.clientFile)
	if err != nil {
		return usageErr(fmt.Errorf("Can't read %s: %s", v.clientFile, lastPart(err)), "Check the path.")
	}
	if fi, err := os.Stat(v.clientFile); err == nil && fi.Mode().Perm()&0o077 != 0 {
		fmt.Fprintf(e.Stderr, "%s %s can be read by other users (mode %04o). Run chmod 600 %s.\n",
			e.Style.Amber(ui.Unknown), v.clientFile, fi.Mode().Perm(), v.clientFile)
	}
	var cf gmail.ClientFile
	if err := json.Unmarshal(b, &cf); err != nil || cf.Installed.ClientID == "" {
		return &ExitError{Code: ExitFailure, ErrCode: "validation_failed", Err: errors.New("The client file isn't a Desktop app OAuth client"),
			Next: "Create one in Google Cloud › Credentials › Create credentials › OAuth client ID › Desktop app, then download its JSON."}
	}
	c, err := apiClient(e)
	if err != nil {
		return err
	}
	x, err := getConnector(ctx, e, c, ref)
	if err != nil {
		return err
	}
	if x.Type != gmail.Type {
		return usageErr(fmt.Errorf("%s is a %s connector, not gmail", x.Name, x.Type), "deedglass connector gmail authorize <gmail connector>")
	}
	pol := &egress.Policy{Env: egress.EnvProd}
	opts := gmail.AuthorizeOptions{ClientID: cf.Installed.ClientID, ClientSecret: cf.Installed.ClientSecret, Readonly: v.readonly, Name: x.Name,
		HTTP: pol.Client(egress.ClientOptions{Timeout: 30 * time.Second, Redirects: egress.RedirectNone})}
	// A dev server may point the connector at a fake Google (tests); prod is Google only.
	var cfg gmail.Config
	_ = json.Unmarshal(x.Config, &cfg)
	if e.Getenv("DEEDGLASS_DEV_GOOGLE") != "" && e.Getenv("DEEDGLASS_ENV") != "prod" {
		g := strings.TrimSuffix(e.Getenv("DEEDGLASS_DEV_GOOGLE"), "/")
		opts.AuthURL, opts.TokenURL, opts.APIBaseURL = g+"/auth", g+"/token", g
		dev := &egress.Policy{Env: egress.EnvDev, AllowPrivate: true}
		opts.HTTP = dev.Client(egress.ClientOptions{Timeout: 30 * time.Second, Redirects: egress.RedirectNone})
	}
	s := e.Style
	opts.OnURL = func(consent, callback string) {
		where := "a browser tab is open"
		if v.noBrowser || !openBrowser(consent) {
			where = "open this link to sign in"
			fmt.Fprintln(e.Stdout, consent)
		}
		fmt.Fprintf(e.Stdout, "%s Waiting for Google %s %s (%s %s 5 min)\n", s.Violet(ui.Pending), ui.Sep, where, callback, ui.Sep)
	}
	res, err := gmail.Authorize(ctx, opts)
	if err != nil {
		var ae *gmail.AuthorizeError
		if errors.As(err, &ae) {
			return &ExitError{Code: ExitFailure, ErrCode: "gmail_authorize_failed", Err: fmt.Errorf("Not connected %s %s %s %s", ui.Sep, x.Name, ui.Sep, strings.TrimSuffix(ae.Message, ".")),
				Next: "Nothing was stored. Run deedglass connector gmail authorize " + x.Name + " --client-file " + v.clientFile + " again."}
		}
		return err
	}
	secret := map[string]string{gmail.FieldClientID: cf.Installed.ClientID, gmail.FieldClientSecret: cf.Installed.ClientSecret, gmail.FieldRefreshToken: res.RefreshToken}
	defer func() {
		for k := range secret {
			secret[k] = ""
		}
		res.RefreshToken = ""
	}()
	var m vault.Meta
	if _, err := c.Do(ctx, "PUT", "/v1/connectors/"+url.PathEscape(x.ID)+"/credentials", map[string]any{"kind": vault.KindFields, "secret": secret}, &m); err != nil {
		return apiErr(e, c, err)
	}
	var cm map[string]any
	_ = json.Unmarshal(x.Config, &cm)
	if cm == nil {
		cm = map[string]any{}
	}
	cm["email"], cm["scopes"], cm["authorized_at"] = res.Email, res.Scopes, res.AuthorizedAt.Format(time.RFC3339)
	nc, _ := json.Marshal(cm)
	if _, err := c.Do(ctx, "PATCH", "/v1/connectors/"+url.PathEscape(x.ID), map[string]any{"config": json.RawMessage(nc)}, nil); err != nil {
		return apiErr(e, c, err)
	}
	short := make([]string, 0, len(res.Scopes))
	for _, sc := range res.Scopes {
		short = append(short, strings.TrimPrefix(sc, "https://www.googleapis.com/auth/"))
	}
	if e.Globals.JSON {
		return writeJSON(e, map[string]any{"object": "gmail_authorization", "connector": x.Name, "email": res.Email, "scopes": short,
			"authorized_at": res.AuthorizedAt.Format(time.RFC3339), "reauthorize_by": res.AuthorizedAt.Add(gmail.TestingTokenLife).Format(time.RFC3339)})
	}
	fmt.Fprintln(e.Stdout, s.OKLine(fmt.Sprintf("Connected %s %s %s %s %s scopes: %s", ui.Sep, x.Name, ui.Sep, res.Email, ui.Sep, strings.Join(short, ", "))))
	fmt.Fprintln(e.Stdout, "  Deedglass writes drafts only. People send them.")
	fmt.Fprintf(e.Stdout, "  %s %s\n", s.Amber(ui.Unknown), gmail.ReauthNote(res.AuthorizedAt))
	return nil
}

// openBrowser opens url on macOS (open) and Linux (xdg-open); false when it can't.
func openBrowser(u string) bool {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", u)
	case "linux":
		cmd = exec.Command("xdg-open", u)
	default:
		return false
	}
	return cmd.Start() == nil
}

// --- dev stripe-fixtures ---

var stripeFixturesValues struct {
	fromEnv, out, connectorName string
}

func stripeFixturesFlags(fs *flag.FlagSet) {
	v := &stripeFixturesValues
	v.fromEnv, v.out, v.connectorName = "", "", ""
	fs.StringVar(&v.connectorName, "connector", "stripe", "the Stripe connector's name (for messages)")
	fs.StringVar(&v.fromEnv, "from-env", "STRIPE_TEST_KEY", "the environment variable holding a sandbox key (rk_test_ or sk_test_)")
	fs.StringVar(&v.out, "out", filepath.Join("examples", "demo", ".stripe-fixtures.json"), "where to write the ids (mode 0600)")
}

func runStripeFixtures(ctx context.Context, e *Env, args []string) error {
	if len(args) > 0 {
		return usageErr(fmt.Errorf("deedglass dev stripe-fixtures takes no arguments (got %q)", args[0]), "deedglass dev stripe-fixtures --from-env STRIPE_TEST_KEY")
	}
	if env := e.Getenv("DEEDGLASS_ENV"); env == "prod" {
		return &ExitError{Code: ExitFailure, ErrCode: "dev_only", Err: errors.New("dev stripe-fixtures runs only in dev"), Next: "Unset DEEDGLASS_ENV=prod, or run it on your own machine."}
	}
	v := stripeFixturesValues
	key := e.Getenv(v.fromEnv)
	if strings.TrimSpace(key) == "" {
		return &ExitError{Code: ExitUsage, ErrCode: "validation_failed", Err: errors.New(strings.TrimSuffix(emptyEnvMessage(v.fromEnv), "."))}
	}
	if err := stripeKeyRefusal(v.connectorName, key); err != nil {
		return err
	}
	pol := &egress.Policy{Env: egress.EnvProd}
	base := "https://api.stripe.com"
	if g := e.Getenv("DEEDGLASS_DEV_STRIPE"); g != "" && e.Getenv("DEEDGLASS_ENV") != "prod" { // a fake Stripe for tests
		base, pol = g, &egress.Policy{Env: egress.EnvDev, AllowPrivate: true}
	}
	cfg, errs := stripe.New().Validate(ctx, connector.Env{Name: pol.Env}, json.RawMessage(`{"api_base_url":`+strconv.Quote(base)+`}`))
	if len(errs) > 0 {
		return fmt.Errorf("stripe config: %v", errs)
	}
	inst := connector.NewInstance(ids.MustParse[ids.ConnectorKind]("con_00000000000000000000000000"), ids.OrgID{}, v.connectorName, stripe.Type, cfg,
		"sha256:fixtures", connector.Env{Name: pol.Env, Egress: pol}, keyOpener{key: key})
	day := time.Now()
	out, err := stripe.Fixtures(ctx, inst, day)
	if err != nil {
		if ce := connector.AsError(err); ce != nil {
			return &ExitError{Code: ExitFailure, ErrCode: ce.Code, Err: errors.New(strings.TrimSuffix(ce.AgentMessage, ".")), Detail: nonEmptyLines(ce.AdminDetail)}
		}
		return err
	}
	b, _ := json.MarshalIndent(out, "", "  ")
	if err := os.MkdirAll(filepath.Dir(v.out), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(v.out, append(b, '\n'), 0o600); err != nil {
		return err
	}
	if e.Globals.JSON {
		return writeJSON(e, out)
	}
	s := e.Style
	fmt.Fprintln(e.Stdout, s.OKLine(fmt.Sprintf("Stripe sandbox fixtures %s %s %s written to %s", ui.Sep, out.Date, ui.Sep, v.out)))
	fmt.Fprintf(e.Stdout, "  customer      %s (Acme Demo Customer)\n", out.Customer)
	for _, p := range stripe.FixturePayments {
		k := fmt.Sprintf("%s_%d", p.Currency, p.Amount)
		fmt.Fprintf(e.Stdout, "  charge        %s %s %s\n", out.Charges[k], ui.Sep, moneyText(p))
	}
	fmt.Fprintf(e.Stdout, "  subscription  %s %s $29.00 / month\n", out.Subscription, ui.Sep)
	fmt.Fprintln(e.Stdout, "  A second run today returns the same objects.")
	return nil
}

func moneyText(m connector.Money) string { return stripeMoney(m.Amount, m.Currency) }

func nonEmptyLines(s string) []string {
	if s == "" {
		return nil
	}
	return []string{s}
}

// keyOpener hands the fixtures command's key to the connector as its credential.
type keyOpener struct{ key string }

func (k keyOpener) Open(context.Context, vault.Ref) (*vault.Secret, error) {
	return vault.NewSecretForTest(vault.KindBearer, map[string]string{stripe.FieldToken: k.key}), nil
}

func stripeMoney(amount int64, cur string) string { return money.Format(amount, cur) }
