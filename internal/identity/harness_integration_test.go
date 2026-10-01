//go:build integration

package identity_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/deedglass/deedglass/internal/deed"
	"github.com/deedglass/deedglass/internal/httpx"
	"github.com/deedglass/deedglass/internal/identity"
	"github.com/deedglass/deedglass/internal/ids"
	"github.com/deedglass/deedglass/internal/ledger"
	"github.com/deedglass/deedglass/internal/mailer"
	"github.com/deedglass/deedglass/internal/store/storetest"
)

var ctx = context.Background()

// clock is the service's settable clock.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) Add(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// inbox is a mailer that keeps every message, and a queue that runs each job at once with it.
type inbox struct {
	mu   sync.Mutex
	msgs []mailer.Message
}

func (b *inbox) Send(_ context.Context, m mailer.Message) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.msgs = append(b.msgs, m)
	return nil
}

func (b *inbox) Submit(_, _, _ string, j mailer.Job) bool {
	if err := j(ctx, b); err != nil {
		panic("mail job: " + err.Error())
	}
	return true
}

var (
	linkRE = regexp.MustCompile(`/auth/confirm#t=([A-Za-z0-9_-]{43})`)
	codeRE = regexp.MustCompile(`(?m)\b(\d{3}) (\d{3})\b`)
)

// last returns the newest message to addr, its token and its code ("" for invites).
func (b *inbox) last(t testing.TB, addr string) (mailer.Message, string, string) {
	t.Helper()
	b.mu.Lock()
	defer b.mu.Unlock()
	for i := len(b.msgs) - 1; i >= 0; i-- {
		m := b.msgs[i]
		if m.To != addr {
			continue
		}
		tok := linkRE.FindStringSubmatch(m.Text)
		if tok == nil {
			t.Fatalf("no link in the email:\n%s", m.Text)
		}
		code := ""
		if c := codeRE.FindStringSubmatch(m.Text); c != nil {
			code = c[1] + c[2]
		}
		return m, tok[1], code
	}
	t.Fatalf("no email to %s", addr)
	return mailer.Message{}, "", ""
}

func (b *inbox) count(addr string) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := 0
	for _, m := range b.msgs {
		if m.To == addr {
			n++
		}
	}
	return n
}

// world is a fresh database, a seal key, a Ledger, the identity service and an HTTP server.
type world struct {
	e     *storetest.Env
	led   *ledger.Ledger
	svc   *identity.Service
	keys  *identity.KeyAuth // S08: API keys and the Stop guard, with its listener running
	srv   *httpx.Server
	ts    *httptest.Server
	clock *clock
	mail  *inbox
	logs  *bytes.Buffer
	logMu sync.Mutex
}

type syncWriter struct {
	mu *sync.Mutex
	b  *bytes.Buffer
}

func (w syncWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.b.Write(p)
}

func newWorld(t testing.TB, mods ...func(*identity.Options)) *world {
	t.Helper()
	e := storetest.New(t, storetest.Options{MaxConns: 16})
	seed, err := deed.NewSeed(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := deed.NewFileSigner(ids.New[ids.SealKeyKind]().String(), seed)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.DB.RegisterSealKey(ctx, signer.KeyID(), signer.PublicKey()); err != nil {
		t.Fatal(err)
	}
	w := &world{e: e, clock: &clock{t: time.Now().UTC().Truncate(time.Second)}, mail: &inbox{}, logs: &bytes.Buffer{}}
	log := slog.New(slog.NewJSONHandler(syncWriter{&w.logMu, w.logs}, &slog.HandlerOptions{Level: slog.LevelDebug}))
	w.led = ledger.New(e.DB, deed.NewSealer(signer), signer, ledger.WithDev(true), ledger.WithLogger(log))

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	base, _ := url.Parse("http://" + ln.Addr().String())
	o := identity.Options{DB: e.DB, Ledger: w.led, Mail: w.mail, Pepper: bytes.Repeat([]byte{9}, 32), BaseURL: base,
		Logger: log, Now: w.clock.Now}
	for _, m := range mods {
		m(&o)
	}
	if w.svc, err = identity.New(o); err != nil {
		t.Fatal(err)
	}
	if w.keys, err = identity.NewKeyAuth(identity.KeyAuthOptions{DB: e.DB, Pepper: o.Pepper, Logger: log, Now: w.clock.Now}); err != nil {
		t.Fatal(err)
	}
	kctx, stopKeys := context.WithCancel(ctx)
	keysDone := make(chan struct{})
	go func() { defer close(keysDone); w.keys.Run(kctx) }()
	t.Cleanup(func() { stopKeys(); <-keysDone })
	// The loopback peer is a trusted proxy, so tests can vary the client address with X-Forwarded-For.
	w.srv = httpx.New(httpx.Options{Logger: slog.New(httpx.NewRedactingHandler(log.Handler())), Sessions: w.svc, Keys: w.keys, BaseURL: base,
		TrustedProxies: []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")}})
	w.srv.Mount(w.svc.Routes()...)
	w.srv.Mount(w.svc.AgentRoutes()...)
	w.srv.Mount(ledger.Routes(w.led)...)
	w.ts = &httptest.Server{Listener: ln, Config: &http.Server{Handler: w.srv}}
	w.ts.Start()
	t.Cleanup(w.ts.Close)
	t.Cleanup(func() {
		if t.Failed() {
			l := w.logText()
			if len(l) > 6000 {
				l = l[len(l)-6000:]
			}
			t.Logf("server log (tail):\n%s", l)
		}
	})
	return w
}

// browser is one person's cookie jar against the world's server.
type browser struct {
	t    testing.TB
	w    *world
	c    *http.Client
	csrf string
}

func (w *world) browser(t testing.TB) *browser {
	jar, _ := cookiejar.New(nil)
	return &browser{t: t, w: w, c: &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}

type resp struct {
	status int
	header http.Header
	body   []byte
}

func (r resp) json(t testing.TB, v any) {
	t.Helper()
	if err := json.Unmarshal(r.body, v); err != nil {
		t.Fatalf("%v: %s", err, r.body)
	}
}

func (r resp) code() string {
	var e struct {
		Error struct{ Code, Message string } `json:"error"`
	}
	_ = json.Unmarshal(r.body, &e)
	return e.Error.Code
}

func (r resp) message() string {
	var e struct {
		Error struct{ Code, Message string } `json:"error"`
	}
	_ = json.Unmarshal(r.body, &e)
	return e.Error.Message
}

// do sends a request; unsafe methods carry the browser's CSRF token unless hdr overrides it.
func (b *browser) do(method, path, body string, hdr ...string) resp {
	b.t.Helper()
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req, _ := http.NewRequest(method, b.w.ts.URL+path, rd)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if method != "GET" && b.csrf != "" {
		req.Header.Set(identity.CSRFHeader, b.csrf)
	}
	for i := 0; i+1 < len(hdr); i += 2 {
		if hdr[i+1] == "" {
			req.Header.Del(hdr[i])
			continue
		}
		req.Header.Set(hdr[i], hdr[i+1])
	}
	r, err := b.c.Do(req)
	if err != nil {
		b.t.Fatal(err)
	}
	defer r.Body.Close()
	out, _ := io.ReadAll(r.Body)
	return resp{status: r.StatusCode, header: r.Header, body: out}
}

// signInWithToken previews and redeems a link token, and keeps the CSRF token.
func (b *browser) signInWithToken(tok string) identity.Me {
	b.t.Helper()
	if r := b.do("POST", "/v1/auth/preview", `{"token":"`+tok+`"}`); r.status != 200 {
		b.t.Fatalf("preview: %d %s", r.status, r.body)
	}
	r := b.do("POST", "/v1/auth/callback", `{"token":"`+tok+`"}`)
	if r.status != 200 {
		b.t.Fatalf("callback: %d %s", r.status, r.body)
	}
	var me identity.Me
	r.json(b.t, &me)
	if me.CSRFToken == nil {
		b.t.Fatal("no csrf token")
	}
	b.csrf = *me.CSRFToken
	return me
}

// org creates an org whose owner is signed in through an operator link.
func (w *world) org(t testing.TB, name, owner string) (identity.CreatedOrg, *browser) {
	t.Helper()
	c, err := identity.CreateOrg(ctx, w.e.DB, w.led, name, "", owner)
	if err != nil {
		t.Fatal(err)
	}
	l, err := w.svc.IssueLink(ctx, owner, c.Org.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	b := w.browser(t)
	b.signInWithToken(tokenOf(l.Link))
	return c, b
}

// invite has inviter invite email as role, and signs the invitee in through the invite email.
func (w *world) invite(t testing.TB, inviter *browser, email, role string) (identity.Member, *browser) {
	t.Helper()
	r := inviter.do("POST", "/v1/org/members", `{"email":"`+email+`","role":"`+role+`"}`)
	if r.status != 201 {
		t.Fatalf("invite %s: %d %s", email, r.status, r.body)
	}
	var m identity.Member
	r.json(t, &m)
	_, tok, _ := w.mail.last(t, email)
	b := w.browser(t)
	b.signInWithToken(tok)
	return m, b
}

func (w *world) logText() string { w.logMu.Lock(); defer w.logMu.Unlock(); return w.logs.String() }

// tokenOf is the token in a confirm link.
func tokenOf(link string) string { return link[strings.LastIndex(link, "#t=")+3:] }
