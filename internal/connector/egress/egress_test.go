package egress

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type corpusRow struct {
	Name    string              `json:"name"`
	URL     string              `json:"url"`
	Prod    string              `json:"prod"`
	Dev     string              `json:"dev"`
	Class   string              `json:"class"`
	Resolve map[string][]string `json:"resolve"`
	Dial    *struct {
		Answer []string `json:"answer"`
		Prod   string   `json:"prod"`
		Dev    string   `json:"dev"`
	} `json:"dial"`
	AllowCIDRs []string `json:"allow_cidrs"`
	Self       []string `json:"self"`
}

func loadCorpus(t *testing.T) []corpusRow {
	t.Helper()
	b, err := os.ReadFile("testdata/corpus.json")
	if err != nil {
		t.Fatal(err)
	}
	var c struct {
		Rows []corpusRow `json:"rows"`
	}
	if err := json.Unmarshal(b, &c); err != nil {
		t.Fatal(err)
	}
	return c.Rows
}

func addrs(t testing.TB, ss []string) []netip.Addr {
	out := make([]netip.Addr, len(ss))
	for i, s := range ss {
		out[i] = netip.MustParseAddr(s)
	}
	return out
}

// fakeDialer records dials and connects to nothing.
type fakeDialer struct{ dials atomic.Int64 }

func (f *fakeDialer) dial(context.Context, string, string) (net.Conn, error) {
	f.dials.Add(1)
	a, b := net.Pipe()
	go func() { _ = b.Close() }()
	return a, nil
}

func policyFor(t *testing.T, row corpusRow, mode string) (*Policy, *Static, *fakeDialer) {
	m := map[string][]netip.Addr{}
	for h, as := range row.Resolve {
		m[h] = addrs(t, as)
	}
	res := StaticResolver(m)
	if row.Dial != nil {
		host := strings.TrimSuffix(strings.TrimPrefix(row.URL, "https://"), "/")
		res.Sequence(host, addrs(t, row.Resolve[host]), addrs(t, row.Dial.Answer))
	}
	p := &Policy{Env: EnvProd, Resolver: res}
	if mode == "dev" {
		p.Env, p.AllowPrivate = EnvDev, true
	}
	for _, c := range row.AllowCIDRs {
		p.AllowCIDRs = append(p.AllowCIDRs, netip.MustParsePrefix(c))
	}
	for _, s := range row.Self {
		p.Self = append(p.Self, netip.MustParseAddrPort(s))
	}
	fd := &fakeDialer{}
	p.dialer = fd.dial
	return p, res, fd
}

// TestCorpus runs every row of testdata/corpus.json in prod and in dev with the override.
func TestCorpus(t *testing.T) {
	rows := loadCorpus(t)
	if len(rows) < 47 {
		t.Fatalf("the corpus has %d rows; S16 needs at least 47", len(rows))
	}
	for _, row := range rows {
		for _, mode := range []string{"prod", "dev"} {
			t.Run(row.Name+"/"+mode, func(t *testing.T) {
				want := row.Prod
				if mode == "dev" {
					want = row.Dev
				}
				p, _, fd := policyFor(t, row, mode)
				c, err := p.CheckURL(context.Background(), row.URL)
				got := "allow"
				var be *BlockedError
				var re *ResolveError
				switch {
				case errors.As(err, &be) && be.Class != "":
					got = "block"
					if row.Class != "" && string(be.Class) != row.Class {
						t.Errorf("class %s, want %s", be.Class, row.Class)
					}
				case errors.As(err, &be), errors.As(err, &re):
					got = "refuse"
				case err != nil:
					t.Fatalf("unexpected error type %T: %v", err, err)
				}
				if got != want {
					t.Fatalf("%s: %s, want %s (err: %v)", row.URL, got, want, err)
				}
				if got == "allow" && row.Class != "" && string(c.Class) != row.Class {
					t.Errorf("class %s, want %s", c.Class, row.Class)
				}
				if got != "allow" || row.Dial == nil {
					return
				}
				// The dial resolves again: a rebinding answer is caught there.
				dwant := row.Dial.Prod
				if mode == "dev" {
					dwant = row.Dial.Dev
				}
				conn, err := p.DialContext(context.Background(), "tcp", net.JoinHostPort(c.Host, fmt.Sprint(c.Port)))
				dgot := "allow"
				if err != nil {
					if _, ok := IsBlocked(err); !ok {
						t.Fatalf("dial: %v", err)
					}
					dgot = "block"
				} else {
					_ = conn.Close()
				}
				if dgot != dwant {
					t.Fatalf("dial: %s, want %s", dgot, dwant)
				}
				if dgot == "block" && fd.dials.Load() != 0 {
					t.Fatalf("a blocked destination was dialed %d times", fd.dials.Load())
				}
			})
		}
	}
}

func TestClassify(t *testing.T) {
	cases := map[string]Class{
		"169.254.169.254": ClassMetadata, "169.254.1.1": ClassMetadata, "fe80::abcd": ClassMetadata,
		"100.100.100.200": ClassMetadata, "100.100.100.201": ClassPrivate, "::a9fe:a9fe": ClassReserved,
		"::1": ClassLoopback, "::": ClassReserved, "::ffff:127.0.0.1": ClassLoopback, "::ffff:10.0.0.1": ClassPrivate,
		"64:ff9b:1:a9fe:a9:fe00::": ClassMetadata, "2002:0a00:0001::": ClassPrivate, "8.8.8.8": ClassPublic,
		"2606:4700::1111": ClassPublic, "ff02::1": ClassReserved, "240.0.0.1": ClassReserved, "fd12::1": ClassPrivate,
	}
	for s, want := range cases {
		if got := Classify(netip.MustParseAddr(s)); got != want {
			t.Errorf("Classify(%s) = %s, want %s", s, got, want)
		}
	}
}

func TestCheckAllowCIDR(t *testing.T) {
	for _, ok := range []string{"10.20.0.0/16", "192.168.4.0/24", "fd12:3456::/48", "203.0.114.0/24", "10.1.2.3/32"} {
		if err := CheckAllowCIDR(netip.MustParsePrefix(ok)); err != nil {
			t.Errorf("%s: %v", ok, err)
		}
	}
	for _, bad := range []string{"0.0.0.0/0", "169.254.0.0/16", "169.0.0.0/8", "127.0.0.0/8", "::/0", "fe80::/64", "224.0.0.0/8", "::ffff:0:0/96", "100.100.100.200/32", "2002::/16"} {
		if err := CheckAllowCIDR(netip.MustParsePrefix(bad)); err == nil {
			t.Errorf("%s was accepted", bad)
		}
	}
}

func TestBlockedErrorMessages(t *testing.T) {
	p := &Policy{Env: EnvProd, Resolver: StaticResolver(map[string][]netip.Addr{"metadata.attack.test": {netip.MustParseAddr("169.254.169.254")}})}
	_, err := p.CheckURL(context.Background(), "https://metadata.attack.test/")
	be, ok := IsBlocked(err)
	if !ok {
		t.Fatalf("err = %v", err)
	}
	want := "metadata.attack.test resolves to 169.254.169.254 (cloud metadata). Deedglass never connects to cloud metadata addresses, even in dev. Point base_url at the service's public address."
	if be.Error() != want {
		t.Errorf("Error() = %q", be.Error())
	}
	if strings.Contains(be.AgentMessage(), "169.254") || strings.Contains(be.AgentMessage(), "metadata") {
		t.Errorf("the agent message names the network: %q", be.AgentMessage())
	}
	_, err = p.CheckURL(context.Background(), "https://169.254.169.254/")
	if be, _ := IsBlocked(err); be == nil || be.Error() != "169.254.169.254 is a cloud metadata address. Deedglass never connects to cloud metadata addresses, even in dev. Point base_url at the service's public address." {
		t.Errorf("literal: %v", err)
	}
}

// loopbackPolicy reaches httptest servers (dev with the override).
func loopbackPolicy() *Policy { return &Policy{Env: EnvDev, AllowPrivate: true} }

func TestDialRebindingRealSocket(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "ok") }))
	defer srv.Close()
	_, port, _ := net.SplitHostPort(srv.Listener.Addr().String())
	res := StaticResolver(nil).Sequence("rebind.attack.test", []netip.Addr{netip.MustParseAddr("93.184.216.34")}, []netip.Addr{netip.MustParseAddr("127.0.0.1")})
	p := &Policy{Env: EnvProd, Resolver: res}
	if _, err := p.CheckURL(context.Background(), "https://rebind.attack.test:"+port+"/"); err != nil {
		t.Fatalf("check: %v", err)
	}
	_, err := p.DialContext(context.Background(), "tcp", "rebind.attack.test:"+port)
	if _, ok := IsBlocked(err); !ok {
		t.Fatalf("the rebound loopback answer was dialed: %v", err)
	}
	// ControlContext: even an address that skipped resolution is checked at connect time.
	d := &Policy{Env: EnvProd}
	if _, err := d.dialOne(context.Background(), "x", netip.MustParseAddrPort("127.0.0.1:"+port)); err == nil {
		t.Fatal("dialOne connected to loopback in prod")
	} else if _, ok := IsBlocked(err); !ok {
		t.Fatalf("dialOne: %v", err)
	}
}

func TestRedirects(t *testing.T) {
	var targetHits atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { targetHits.Add(1) }))
	defer target.Close()
	hops := 0
	var self *httptest.Server
	self = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/to-metadata":
			http.Redirect(w, r, "http://169.254.169.254/latest/meta-data/", http.StatusFound)
		case "/cross-host":
			http.Redirect(w, r, target.URL+"/x", http.StatusFound)
		case "/to-http":
			http.Redirect(w, r, "http://"+r.Host+"/ok", http.StatusFound)
		case "/loop":
			hops++
			http.Redirect(w, r, "/loop", http.StatusFound)
		case "/two":
			http.Redirect(w, r, "/one", http.StatusFound)
		case "/one":
			http.Redirect(w, r, "/ok", http.StatusFound)
		case "/ok":
			_, _ = io.WriteString(w, "ok")
		}
	}))
	defer self.Close()
	p := loopbackPolicy()
	same := p.Client(ClientOptions{Timeout: 5 * time.Second, Redirects: RedirectSameHost, MaxResponseBytes: 1 << 20})
	none := p.Client(ClientOptions{Timeout: 5 * time.Second, Redirects: RedirectNone})
	get := func(c *http.Client, path string) (*http.Response, error) {
		res, err := c.Get(self.URL + path)
		if err == nil {
			_ = res.Body.Close()
		}
		return res, err
	}
	for _, path := range []string{"/to-metadata", "/cross-host", "/loop"} {
		if _, err := get(same, path); err == nil {
			t.Errorf("same_host followed %s", path)
		}
	}
	// S18 (K02): a redirect to a blocked destination is named as one; a cross-host redirect to an
	// allowed one stays a refused redirect.
	if _, err := get(same, "/to-metadata"); func() bool { _, ok := IsBlocked(err); return !ok }() {
		t.Errorf("a redirect to metadata wasn't a blocked destination: %v", err)
	}
	var re *RedirectError
	if _, err := get(same, "/cross-host"); !errors.As(err, &re) {
		t.Errorf("cross-host: %v", err)
	}
	if hops != MaxRedirects+1 {
		t.Errorf("the redirect loop made %d requests, want %d", hops, MaxRedirects+1)
	}
	if res, err := get(same, "/two"); err != nil || res.StatusCode != 200 {
		t.Errorf("two same-host hops: %v", err)
	}
	if res, err := get(none, "/two"); err != nil || res.StatusCode != http.StatusFound {
		t.Errorf("none followed a redirect: %v %v", res, err)
	}
	if targetHits.Load() != 0 {
		t.Errorf("the redirect target was hit %d times", targetHits.Load())
	}
	// https → http is refused even to the same host.
	tls := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://"+r.Host+"/ok", http.StatusFound)
	}))
	defer tls.Close()
	c := Wrap(tls.Client().Transport, ClientOptions{Redirects: RedirectSameHost})
	if _, err := c.Get(tls.URL + "/"); err == nil || !strings.Contains(err.Error(), "https to http") {
		t.Errorf("https → http: %v", err)
	}
}

func TestBodyCap(t *testing.T) {
	big := bytes.Repeat([]byte("a"), 5<<20)
	var bomb bytes.Buffer
	gz := gzip.NewWriter(&bomb)
	zeros := make([]byte, 1<<20)
	for range 100 {
		_, _ = gz.Write(zeros)
	}
	_ = gz.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/big":
			_, _ = w.Write(big)
		case "/bomb":
			w.Header().Set("Content-Encoding", "gzip")
			_, _ = w.Write(bomb.Bytes())
		case "/fits":
			_, _ = w.Write(big[:1<<20])
		}
	}))
	defer srv.Close()
	const max = 4 << 20
	c := loopbackPolicy().Client(ClientOptions{Timeout: 10 * time.Second, MaxResponseBytes: max})
	for _, path := range []string{"/big", "/bomb"} {
		res, err := c.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		n, err := io.Copy(io.Discard, res.Body)
		_ = res.Body.Close()
		if !errors.Is(err, ErrResponseTooLarge) {
			t.Errorf("%s: err = %v after %d bytes", path, err, n)
		}
		if n > max {
			t.Errorf("%s: read %d bytes, more than the cap", path, n)
		}
	}
	res, err := c.Get(srv.URL + "/fits")
	if err != nil {
		t.Fatal(err)
	}
	b, err := ReadBody(res.Body, max)
	_ = res.Body.Close()
	if err != nil || len(b) != 1<<20 {
		t.Errorf("fits: %d bytes, %v", len(b), err)
	}
	if _, err := ReadBody(strings.NewReader("12345"), 4); !errors.Is(err, ErrResponseTooLarge) {
		t.Errorf("ReadBody over the cap: %v", err)
	}
}

func TestProxyIgnored(t *testing.T) {
	t.Setenv("HTTPS_PROXY", "http://10.9.9.9:3128")
	t.Setenv("HTTP_PROXY", "http://10.9.9.9:3128")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()
	c := loopbackPolicy().Client(ClientOptions{Timeout: 3 * time.Second})
	res, err := c.Get(srv.URL)
	if err != nil {
		t.Fatalf("the client used the environment's proxy: %v", err)
	}
	_ = res.Body.Close()
	if ua := UserAgent(); !strings.HasPrefix(ua, "deedglass/") || !strings.Contains(ua, "docs.deedglass.com/connectors") {
		t.Errorf("User-Agent %q", ua)
	}
}

func TestTLSFloor(t *testing.T) {
	c := (&Policy{}).Client(ClientOptions{})
	tr := c.Transport.(*transport).next.(*http.Transport)
	if tr.TLSClientConfig.MinVersion < 0x0303 || tr.TLSClientConfig.InsecureSkipVerify || tr.Proxy != nil {
		t.Fatalf("transport: min %x, skip %v, proxy %v", tr.TLSClientConfig.MinVersion, tr.TLSClientConfig.InsecureSkipVerify, tr.Proxy != nil)
	}
}

// FuzzCheckURL: an accepted URL only ever resolves to allowed classes, and parsing is stable.
func FuzzCheckURL(f *testing.F) {
	for _, row := range []string{"https://example.com/", "https://169.254.169.254/", "https://[::ffff:a9fe:a9fe]/",
		"https://0x7f.1/", "https://127.1/", "http://localhost:80/", "https://a.b.c:443/x?y=z", "https://[fe80::1%25x]/",
		"https://exa%6dple.com/", "https://user@x.com/", "https://1.2.3.4.5/", "https://[64:ff9b::7f00:1]/"} {
		f.Add(row)
	}
	pub := []netip.Addr{netip.MustParseAddr("93.184.216.34")}
	f.Fuzz(func(t *testing.T, raw string) {
		for _, p := range []*Policy{{Env: EnvProd}, {Env: EnvDev, AllowPrivate: true}} {
			p.Resolver = fuzzResolver{pub}
			c, err := p.CheckURL(context.Background(), raw)
			if err != nil {
				continue
			}
			for _, ap := range c.Addrs {
				cls := p.ClassifyAddrPort(ap)
				if cls == ClassMetadata || cls == ClassReserved || p.Env == EnvProd && (cls == ClassLoopback || cls == ClassPrivate) {
					t.Fatalf("%q accepted with %s (%s) in %s", raw, ap, cls, p.Env)
				}
			}
			if c.URL.User != nil {
				t.Fatalf("%q accepted with userinfo", raw)
			}
			again, err := p.CheckURL(context.Background(), c.URL.String())
			if err != nil || again.URL.String() != c.URL.String() {
				t.Fatalf("not stable: %q → %q → %v", raw, c.URL, err)
			}
			if _, err := url.Parse(c.URL.String()); err != nil {
				t.Fatal(err)
			}
		}
	})
}

// fuzzResolver answers every name with a public address, except names that spell an address.
type fuzzResolver struct{ pub []netip.Addr }

func (r fuzzResolver) LookupNetIP(_ context.Context, _, host string) ([]netip.Addr, error) {
	if a, err := netip.ParseAddr(host); err == nil {
		return []netip.Addr{a}, nil
	}
	return r.pub, nil
}

func BenchmarkClassify(b *testing.B) {
	as := []netip.Addr{netip.MustParseAddr("93.184.216.34"), netip.MustParseAddr("10.1.2.3"), netip.MustParseAddr("::ffff:169.254.169.254"),
		netip.MustParseAddr("2606:4700::1111")}
	b.ReportAllocs()
	for i := 0; b.Loop(); i++ {
		_ = Classify(as[i%len(as)])
	}
}
