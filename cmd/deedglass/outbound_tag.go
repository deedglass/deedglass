//go:build outboundmail

package main

// The ops build (S40): go build -tags outboundmail -o bin/deedglass-ops ./cmd/deedglass, which
// make ops-build runs. Only this build registers the outbound_mail connector type, and only for the
// org ids in connectors.outbound_mail.orgs. Release builds, make build and the Docker image never
// contain this file.

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/deedglass/deedglass/internal/cli/ui"
	"github.com/deedglass/deedglass/internal/config"
	"github.com/deedglass/deedglass/internal/connector"
	"github.com/deedglass/deedglass/internal/connector/egress"
	"github.com/deedglass/deedglass/internal/connector/gmail"
	"github.com/deedglass/deedglass/internal/connector/googleauth"
	"github.com/deedglass/deedglass/internal/connector/outbound"
	"github.com/deedglass/deedglass/internal/connector/outbound/fakegmail"
	"github.com/deedglass/deedglass/internal/vault"
)

func init() {
	outboundTypes = func(cfg *config.Config, _ *slog.Logger) []connector.Connector {
		return []connector.Connector{outbound.New(outbound.Options{APIBase: cfg.Connectors.OutboundMail.APIBase})}
	}
	commands = append(commands,
		&Command{Path: p("connector", "outbound", "authorize"), Group: Connectors, Summary: "Authorize the outbound mail connector",
			Usage: "<name> --client-file ./client_secret.json [--no-browser] [--json]", Flags: outboundAuthorizeFlags, Run: runOutboundAuthorize, Hidden: true,
			Detail: "Ops build only. Signs in with Google (PKCE, loopback on 127.0.0.1, 5 minutes) for exactly gmail.send and gmail.readonly, refuses any account other than the connector's mailbox or on a refused domain, and stores the refresh token in the vault."},
		&Command{Path: p("dev", "fake-gmail"), Group: Dev, Summary: "Run a fake Gmail API for outbound mail tests",
			Usage: "--listen 127.0.0.1:18299 --email <mailbox>", Flags: fakeGmailFlags, Run: runFakeGmail, Hidden: true, RawArgs: false,
			Detail: "Ops build, dev only. A fake Gmail API and token endpoint on loopback: sent messages stay in memory; POST /_fake/mode?mode=… changes how sends behave, GET /_fake/log shows the request log. Nothing is sent anywhere."},
	)
}

var outboundAuthValues struct {
	clientFile string
	noBrowser  bool
}

func outboundAuthorizeFlags(fs *flag.FlagSet) {
	v := &outboundAuthValues
	*v = struct {
		clientFile string
		noBrowser  bool
	}{}
	fs.StringVar(&v.clientFile, "client-file", "", "the Internal Desktop-app OAuth client JSON from Google Cloud")
	fs.BoolVar(&v.noBrowser, "no-browser", false, "print the sign-in link instead of opening a browser")
}

func runOutboundAuthorize(ctx context.Context, e *Env, args []string) error {
	ref, err := oneArg("deedglass connector outbound authorize", "the outbound mail connector's name",
		"deedglass connector outbound authorize mail --client-file ./client_secret.json", args)
	if err != nil {
		return err
	}
	v := outboundAuthValues
	if v.clientFile == "" {
		return usageErr(errors.New("--client-file is required"), "Download the JSON of an Internal Desktop app OAuth client, then pass --client-file ./client_secret.json.")
	}
	b, err := os.ReadFile(v.clientFile)
	if err != nil {
		return usageErr(fmt.Errorf("Can't read %s: %s", v.clientFile, lastPart(err)), "Check the path.")
	}
	var cf googleauth.ClientFile
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
	if x.Type != outbound.Type {
		return usageErr(fmt.Errorf("%s is a %s connector, not outbound_mail", x.Name, x.Type), "deedglass connector outbound authorize <outbound_mail connector>")
	}
	var cfg outbound.Config
	_ = json.Unmarshal(x.Config, &cfg)
	pol := &egress.Policy{Env: egress.EnvProd}
	opts := outbound.AuthorizeOptions{ClientID: cf.Installed.ClientID, ClientSecret: cf.Installed.ClientSecret, Name: x.Name, Mailbox: cfg.Mailbox,
		Deny: cfg.DenyFromDomains, HTTP: pol.Client(egress.ClientOptions{Timeout: 30 * time.Second, Redirects: egress.RedirectNone})}
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
	res, err := outbound.Authorize(ctx, opts)
	if err != nil {
		var ae *googleauth.Error
		if errors.As(err, &ae) {
			return &ExitError{Code: ExitFailure, ErrCode: "gmail_authorize_failed", Err: fmt.Errorf("Not connected %s %s %s %s", ui.Sep, x.Name, ui.Sep, strings.TrimSuffix(ae.Message, ".")),
				Next: "Nothing was stored. Run deedglass connector outbound authorize " + x.Name + " --client-file " + v.clientFile + " again."}
		}
		return err
	}
	secret := map[string]string{outbound.FieldClientID: cf.Installed.ClientID, outbound.FieldClientSecret: cf.Installed.ClientSecret, outbound.FieldRefreshToken: res.RefreshToken}
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
	if e.Globals.JSON {
		return writeJSON(e, map[string]any{"object": "outbound_authorization", "connector": x.Name, "email": res.Email,
			"scopes": []string{"gmail.send", "gmail.readonly"}, "authorized_at": res.AuthorizedAt.Format(time.RFC3339)})
	}
	fmt.Fprintln(e.Stdout, s.OKLine(fmt.Sprintf("Connected %s %s %s %s %s scopes: gmail.send, gmail.readonly", ui.Sep, x.Name, ui.Sep, res.Email, ui.Sep)))
	fmt.Fprintln(e.Stdout, "  Every send still goes through the rules: the window, the caps and, in approve mode, your tap.")
	return nil
}

var fakeGmailValues struct{ listen, email string }

func fakeGmailFlags(fs *flag.FlagSet) {
	fs.StringVar(&fakeGmailValues.listen, "listen", "127.0.0.1:18299", "loopback address to listen on")
	fs.StringVar(&fakeGmailValues.email, "email", "", "the profile's mailbox address")
}

func runFakeGmail(ctx context.Context, e *Env, _ []string) error {
	if e.Getenv("DEEDGLASS_ENV") == "prod" {
		return usageErr(errors.New("dev fake-gmail is dev only"), "Run it with DEEDGLASS_ENV=dev.")
	}
	v := fakeGmailValues
	host, _, err := net.SplitHostPort(v.listen)
	if err != nil || (host != "127.0.0.1" && host != "::1" && host != "localhost") {
		return usageErr(errors.New("--listen must be a loopback address"), "For example --listen 127.0.0.1:18299.")
	}
	if v.email == "" {
		return usageErr(errors.New("--email is required"), "Pass the connector's mailbox, for example --email arjun@trydeedglass.example.")
	}
	srv := &http.Server{Addr: v.listen, Handler: fakegmail.New(strings.ToLower(v.email)), ReadHeaderTimeout: 5 * time.Second}
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(sctx)
	}()
	fmt.Fprintf(e.Stdout, "%s fake Gmail on http://%s %s profile %s %s nothing leaves this machine\n", e.Style.Violet(ui.Pending), v.listen, ui.Sep, v.email, ui.Sep)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

var _ = gmail.Type
