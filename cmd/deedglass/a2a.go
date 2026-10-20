package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os/signal"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/deedglass/deedglass/internal/a2a"
	"github.com/deedglass/deedglass/internal/connector/egress"
	"github.com/deedglass/deedglass/internal/mcp/gateclient"
	"github.com/deedglass/deedglass/internal/mcp/wrap"
)

// deedglass a2a proxy (TY-07, ADR-113): a loopback A2A JSON-RPC endpoint in front of one remote
// agent. Calls that ask the peer to act go through the Gate first.

type a2aProxyOpts struct {
	connector, peer, cardURL, listen, onBehalfOf, resultMode string
	peerHeaders                                              headerEnvs
	callTimeout, approvalWait                                time.Duration
	insecureHTTP                                             bool
}

var a2aProxyValues a2aProxyOpts

// headerEnvs is a repeatable --peer-header-env Header=VAR.
type headerEnvs []string

func (h *headerEnvs) String() string     { return strings.Join(*h, ",") }
func (h *headerEnvs) Set(v string) error { *h = append(*h, v); return nil }

const a2aProxyHelp = `
What it does
  Listens on a loopback address and forwards A2A JSON-RPC requests to one remote agent. A request
  that asks the agent to act (SendMessage, SendStreamingMessage, CancelTask, push config writes)
  goes to the Gate first and is forwarded only if it is allowed; the peer's answer is recorded as
  the peer reported it. Reads pass through. Point your agent's A2A client at this address.

Connection
  --connector <name>        the name calls are matched as in policies: <connector>.SendMessage
  --peer <url>              the remote agent's JSON-RPC endpoint (A2A 1.0)
  --card-url <url>          its Agent Card (default <peer origin>/.well-known/agent-card.json)
  --listen <addr>           a loopback address (default 127.0.0.1:7420)
  --peer-header-env H=VAR   send header H with the value of VAR to the peer, e.g. Authorization=PEER_AUTH
  --server <url>            the Deedglass server (default DEEDGLASS_SERVER, then http://127.0.0.1:8080)
  --api-key <key>           an agent's key (default DEEDGLASS_API_KEY); never sent to the peer
  --on-behalf-of <ref>      the person, when a request's delegation chain names none
Limits
  --approval-wait <d>       how long a call waits for a person's decision (default 10s)
  --call-timeout <d>        give up on the peer after d (default 5m)
  --result-mode <m>         full (answers up to 256 KiB) or digest

Private and loopback peers need DEEDGLASS_ENV=dev and DEEDGLASS_ALLOW_PRIVATE_NETWORKS=1, or a
range in DEEDGLASS_EGRESS_ALLOW_CIDRS.

Example
  deedglass a2a proxy --connector researcher --peer https://research.example.com/a2a/v1 --on-behalf-of u_5780

Learn more: https://docs.deedglass.com/reference/a2a
`

func a2aProxyFlags(fs *flag.FlagSet) {
	o := &a2aProxyValues
	*o = a2aProxyOpts{}
	fs.StringVar(&o.connector, "connector", "", "the connector name calls are matched as (<connector>.<Method>), for example researcher")
	fs.StringVar(&o.peer, "peer", "", "the remote agent's A2A JSON-RPC endpoint")
	fs.StringVar(&o.cardURL, "card-url", "", "the remote agent's Agent Card (default <peer origin>/.well-known/agent-card.json)")
	fs.StringVar(&o.listen, "listen", "127.0.0.1:7420", "the loopback address to listen on")
	fs.Var(&o.peerHeaders, "peer-header-env", "Header=VAR: send this header to the peer with VAR's value (repeatable)")
	fs.StringVar(&o.onBehalfOf, "on-behalf-of", "", "the person the agent acts for, when a request's delegation chain names none")
	fs.DurationVar(&o.approvalWait, "approval-wait", 10*time.Second, "how long a call waits for a person's decision")
	fs.DurationVar(&o.callTimeout, "call-timeout", 5*time.Minute, "give up on the peer after this long")
	fs.StringVar(&o.resultMode, "result-mode", "full", "report answers in full (up to 256 KiB) or as a digest only")
	fs.BoolVar(&o.insecureHTTP, "insecure-http", false, "allow http to a Deedglass server that isn't loopback (DEEDGLASS_ENV=dev only)")
}

var (
	reA2AConnector = regexp.MustCompile(`^[a-z][a-z0-9-]{1,31}$`)
	reHeaderName   = regexp.MustCompile(`^[A-Za-z0-9-]{1,64}$`)
	reEnvName      = regexp.MustCompile(`^[A-Z_][A-Z0-9_]{0,63}$`)
)

const a2aProxyExample = "deedglass a2a proxy --connector researcher --peer https://research.example.com/a2a/v1"

func runA2AProxy(ctx context.Context, e *Env, args []string) error {
	o := a2aProxyValues
	next := "For example: " + a2aProxyExample
	switch {
	case len(args) > 0:
		return usageErr(fmt.Errorf("deedglass a2a proxy takes no arguments (got %q)", args[0]), "Run deedglass help a2a proxy.")
	case !reA2AConnector.MatchString(o.connector):
		return usageErr(fmt.Errorf("--connector %q isn't a connector name: use 2 to 32 lowercase letters, digits or hyphens, starting with a letter", o.connector), next)
	case o.connector == "deedglass" || o.connector == "native" || o.connector == "mcp":
		return usageErr(fmt.Errorf("--connector %q is reserved", o.connector), "Pick another name, such as researcher.")
	case o.peer == "":
		return usageErr(errors.New("deedglass a2a proxy needs --peer"), next)
	case o.resultMode != "full" && o.resultMode != "digest":
		return usageErr(fmt.Errorf("--result-mode must be full or digest (got %q)", o.resultMode), next)
	case o.callTimeout <= 0 || o.approvalWait < 0 || o.approvalWait > 25*time.Second:
		return usageErr(errors.New("--call-timeout must be positive and --approval-wait between 0 and 25s"), next)
	}
	dev := e.Getenv("DEEDGLASS_ENV") == "dev"
	pol := &egress.Policy{Env: egress.EnvProd}
	if dev {
		pol.Env = egress.EnvDev
		pol.AllowPrivate = e.Getenv("DEEDGLASS_ALLOW_PRIVATE_NETWORKS") == "1" || e.Getenv("DEEDGLASS_ALLOW_PRIVATE_NETWORKS") == "true"
	}
	for _, c := range strings.Split(e.Getenv("DEEDGLASS_EGRESS_ALLOW_CIDRS"), ",") {
		if c = strings.TrimSpace(c); c == "" {
			continue
		}
		n, err := netip.ParsePrefix(c)
		if err == nil {
			err = egress.CheckAllowCIDR(n)
		}
		if err != nil {
			return usageErr(fmt.Errorf("DEEDGLASS_EGRESS_ALLOW_CIDRS: %q: %v", c, err), "List private ranges such as 10.20.0.0/16.")
		}
		pol.AllowCIDRs = append(pol.AllowCIDRs, n)
	}
	peer, err := url.Parse(o.peer)
	if err != nil || peer.Host == "" || (peer.Scheme != "https" && peer.Scheme != "http") || peer.User != nil {
		return usageErr(fmt.Errorf("--peer %q isn't an http(s) URL", o.peer), next)
	}
	if peer.Scheme == "http" && !pol.AllowPrivate {
		return usageErr(errors.New("--peer must use https (http needs DEEDGLASS_ENV=dev and DEEDGLASS_ALLOW_PRIVATE_NETWORKS=1)"), next)
	}
	var card *url.URL
	if o.cardURL != "" {
		if card, err = url.Parse(o.cardURL); err != nil || card.Host == "" || (card.Scheme != "https" && card.Scheme != peer.Scheme) {
			return usageErr(fmt.Errorf("--card-url %q isn't a URL the proxy can use", o.cardURL), next)
		}
	}
	cctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	_, err = pol.CheckURL(cctx, peer.String())
	cancel()
	if _, blocked := egress.IsBlocked(err); blocked {
		return usageErr(fmt.Errorf("--peer: %v", err), "Private and loopback peers need DEEDGLASS_ENV=dev with DEEDGLASS_ALLOW_PRIVATE_NETWORKS=1, or DEEDGLASS_EGRESS_ALLOW_CIDRS.")
	}
	hdr := http.Header{}
	for _, kv := range o.peerHeaders {
		h, v, ok := strings.Cut(kv, "=")
		if !ok || !reHeaderName.MatchString(h) || !reEnvName.MatchString(v) {
			return usageErr(fmt.Errorf("--peer-header-env %q isn't Header=VAR", kv), "For example: --peer-header-env Authorization=PEER_AUTH")
		}
		val := e.Getenv(v)
		if val == "" {
			return usageErr(fmt.Errorf("--peer-header-env %s reads %s, which is empty", h, v), "Set "+v+" in this environment.")
		}
		hdr.Set(h, val)
	}
	host, _, err := net.SplitHostPort(o.listen)
	if err != nil || !wrap.IsLoopback(host) {
		return usageErr(fmt.Errorf("--listen %q must be a loopback address: the proxy has no sign-in of its own", o.listen), "Use 127.0.0.1:7420.")
	}
	server := firstNonEmpty(e.Globals.Server, e.Getenv("DEEDGLASS_SERVER"), e.Getenv("DEEDGLASS_BASE_URL"), defaultServer)
	su, err := url.Parse(strings.TrimSuffix(server, "/"))
	if err != nil || su.Host == "" || (su.Scheme != "http" && su.Scheme != "https") {
		return usageErr(fmt.Errorf("the server %q isn't a URL", server), "Use --server http://127.0.0.1:8080 or set DEEDGLASS_SERVER.")
	}
	if su.Scheme == "http" && !wrap.IsLoopback(su.Hostname()) && !(dev && o.insecureHTTP) {
		return usageErr(fmt.Errorf("the server %s uses http, which would send the API key in clear", su.Redacted()), "Use https, or a loopback address.")
	}
	key := firstNonEmpty(e.Globals.APIKey, e.Getenv("DEEDGLASS_API_KEY"))
	if key == "" {
		return usageErr(errors.New("deedglass a2a proxy needs an agent's key"), "Set DEEDGLASS_API_KEY, or pass --api-key.")
	}
	gc := gateclient.New(gateclient.Options{Server: su.String(), APIKey: key, UserAgent: "deedglass-a2a/" + buildInfo().Version})
	ln, err := net.Listen("tcp", o.listen)
	if err != nil {
		return usageErr(fmt.Errorf("can't listen on %s: %v", o.listen, err), "Pick a free port with --listen 127.0.0.1:<port>.")
	}
	public := "http://" + ln.Addr().String() + "/"
	log := slog.New(slog.NewTextHandler(e.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	hc := pol.Client(egress.ClientOptions{HeaderTimeout: o.callTimeout, MaxResponseBytes: a2a.MaxAnswerBytes + 1, UserAgent: "deedglass/" + buildInfo().Version})
	hc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	p, err := a2a.New(a2a.Config{Connector: o.connector, Peer: peer, CardURL: card, PublicURL: public, PeerHeaders: hdr,
		OnBehalfOf: o.onBehalfOf, ResultMode: o.resultMode, CallTimeout: o.callTimeout, ApprovalWait: o.approvalWait,
		HTTP: hc, Version: buildInfo().Version}, gc, log)
	if err != nil {
		_ = ln.Close()
		return usageErr(err, next)
	}
	if err := p.LoadCard(ctx); err != nil {
		fmt.Fprintf(e.Stderr, "! the peer's Agent Card can't be read (%v); the chain names it by URL\n", err)
	}
	fmt.Fprintf(e.Stderr, "✓ A2A proxy for %s on %s → %s\n  Send A2A-Version: %s. Calls that ask the peer to act go to %s first.\n",
		o.connector, public, peer.Redacted(), a2a.SpecVersion, su.Redacted())
	srv := &http.Server{Handler: p, ReadHeaderTimeout: 10 * time.Second}
	ctx, stop := signal.NotifyContext(ctx, syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	done := make(chan error, 1)
	go func() { done <- srv.Serve(ln) }()
	select {
	case <-ctx.Done():
	case err := <-done:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	}
	sctx, scancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer scancel()
	_ = srv.Shutdown(sctx)
	p.Wait()
	return nil
}
