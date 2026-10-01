// Package egress is Deedglass's one door to the network (S16 §3.2). Every connection to a
// destination someone configured (a REST connector's base_url, an MCP upstream, a webhook in S22,
// the SaaS connectors in S29–S30, the Postgres connector's dial in S17, deedglass verify --keys)
// goes through a Policy: it checks the URL before DNS, resolves the host itself, refuses the
// destination when any answer is blocked, dials the checked address, and checks the address again
// at connect time, which defeats DNS rebinding. TestSingleEgressDoor keeps every other package
// from building an http.Client.
//
// Classification, first match wins:
//
//	1 metadata  169.254.169.254, 169.254.170.2, 100.100.100.200, fd00:ec2::254, fd20:ce::254, 169.254/16,
//	            fe80::/10, metadata.google.internal                         never, even in dev
//	2 reserved  0/8, ::/128, 224/4, ff00::/8, 240/4, 192.0.0/24, documentation and
//	            benchmarking ranges, 100::/64, 100:0:0:1::/64, 2001::/23 (Teredo),
//	            2001:db8::/32, 3fff::/20, 5f00::/16, fec0::/10              never
//	3 embedded  ::ffff:0:0/96, 64:ff9b::/96, 64:ff9b:1::/48, 2002::/16     the embedded IPv4 decides
//	4 self      the resolved ip:port of DEEDGLASS_DATABASE_URL              prod: only if allow-listed
//	5 loopback  127/8, ::1, localhost, *.localhost                          dev override only
//	6 private   10/8, 172.16/12, 192.168/16, 100.64/10, fc00::/7            dev override or allow-listed
//	7 public    everything else                                            yes
package egress

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"strings"
)

// Class is what an address is to the egress policy.
type Class string

const (
	ClassMetadata Class = "metadata"
	ClassReserved Class = "reserved"
	ClassSelf     Class = "self"
	ClassLoopback Class = "loopback"
	ClassPrivate  Class = "private"
	ClassPublic   Class = "public"
)

// Label is the class as an admin reads it: "cloud metadata", "private", …
func (c Class) Label() string {
	switch c {
	case ClassMetadata:
		return "cloud metadata"
	case ClassReserved:
		return "reserved"
	case ClassSelf:
		return "Deedglass's own database"
	case ClassLoopback:
		return "loopback"
	case ClassPrivate:
		return "private"
	}
	return "public"
}

// Env values the policy knows. Anything but "dev" is treated as prod.
const (
	EnvDev  = "dev"
	EnvProd = "prod"
)

// Resolver looks up a host's addresses. *net.Resolver satisfies it.
type Resolver interface {
	LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error)
}

// Policy is the egress policy of one process. The zero value is prod with the system resolver:
// public destinations only.
type Policy struct {
	Env          string           // "dev" or "prod" (default prod)
	AllowPrivate bool             // DEEDGLASS_ALLOW_PRIVATE_NETWORKS: dev only; never reaches classes 1–3
	AllowCIDRs   []netip.Prefix   // DEEDGLASS_EGRESS_ALLOW_CIDRS: private networks (and self) reachable in prod
	Self         []netip.AddrPort // the resolved ip:port of Deedglass's own database
	Resolver     Resolver         // nil: net.DefaultResolver
	Log          *slog.Logger     // warnings (the dev override reaching the own database)
	// dialer is replaced in tests that need a fake network; nil uses net.Dialer.
	dialer func(ctx context.Context, network, addr string) (net.Conn, error)
}

func (p *Policy) dev() bool { return p != nil && p.Env == EnvDev }

func (p *Policy) resolver() Resolver {
	if p != nil && p.Resolver != nil {
		return p.Resolver
	}
	return net.DefaultResolver
}

// BlockedError says a destination is refused, and why. Error() is for admins (it names the address
// and its class); AgentMessage() is terse, so an agent can't map the network with it.
type BlockedError struct {
	Host   string // the host as written in the URL (or the dial address)
	Addr   string // the address that was refused ("" when refused before DNS)
	Class  Class  // the class that refused it ("" for a URL-shape refusal)
	Reason string // one sentence for the admin, ending with a period
}

func (e *BlockedError) Error() string {
	switch {
	case e.Addr != "" && e.Addr != e.Host:
		return fmt.Sprintf("%s resolves to %s (%s). %s", e.Host, e.Addr, e.Class.Label(), e.Reason)
	case e.Class != "":
		return fmt.Sprintf("%s is %s. %s", e.Host, article(e.Class.Label()), e.Reason)
	}
	return e.Reason
}

func article(label string) string {
	switch label {
	case "Deedglass's own database":
		return label
	case "cloud metadata":
		return "a cloud metadata address"
	case "reserved", "loopback", "private":
		return "a " + label + " address"
	}
	return label
}

// About names the refused destination for an admin, as a config field: "base_url resolves to
// 169.254.169.254, a cloud metadata address. Deedglass never connects to …".
func (e *BlockedError) About(param string) string {
	switch {
	case e.Addr != "" && e.Addr != e.Host:
		return fmt.Sprintf("%s resolves to %s, %s. %s", param, e.Addr, article(e.Class.Label()), e.Reason)
	case e.Class != "":
		return fmt.Sprintf("%s is %s. %s", param, article(e.Class.Label()), e.Reason)
	}
	return e.Reason
}

// AgentMessage is what an agent reads: it says nothing about the network.
func (e *BlockedError) AgentMessage() string { return AgentBlockedMessage }

// AgentBlockedMessage is destination_blocked's message to an agent (S16 §5).
const AgentBlockedMessage = "The connector's destination is blocked by Deedglass's network rules. An admin can see why in Console › Connectors."

// IsBlocked reports whether err is (or wraps) a *BlockedError, and returns it.
func IsBlocked(err error) (*BlockedError, bool) {
	var b *BlockedError
	if errors.As(err, &b) {
		return b, true
	}
	return nil, false
}

// reasons, one per class: what Deedglass does, and the next step.
func reasonFor(c Class, dev bool) string {
	switch c {
	case ClassMetadata:
		return "Deedglass never connects to cloud metadata addresses, even in dev. Point base_url at the service's public address."
	case ClassReserved:
		return "Deedglass never connects to reserved, multicast or documentation addresses. Use the service's real address."
	case ClassSelf:
		return "Deedglass doesn't connect to its own database through a connector. To allow it on purpose, list that exact address in DEEDGLASS_EGRESS_ALLOW_CIDRS."
	case ClassLoopback:
		if dev {
			return "Loopback is reachable in dev only with DEEDGLASS_ALLOW_PRIVATE_NETWORKS=1."
		}
		return "Deedglass doesn't connect to loopback addresses outside dev."
	case ClassPrivate:
		if dev {
			return "Private networks are reachable in dev with DEEDGLASS_ALLOW_PRIVATE_NETWORKS=1, or in any env when listed in DEEDGLASS_EGRESS_ALLOW_CIDRS."
		}
		return "Private networks are reachable only when listed in DEEDGLASS_EGRESS_ALLOW_CIDRS, for example 10.20.0.0/16."
	}
	return ""
}

func mustPrefixes(ss ...string) []netip.Prefix {
	out := make([]netip.Prefix, len(ss))
	for i, s := range ss {
		out[i] = netip.MustParsePrefix(s)
	}
	return out
}

var (
	metadataAddrs = []netip.Addr{
		netip.MustParseAddr("169.254.169.254"), netip.MustParseAddr("169.254.170.2"),
		netip.MustParseAddr("100.100.100.200"), netip.MustParseAddr("fd00:ec2::254"),
		netip.MustParseAddr("fd20:ce::254"), // GCP's IPv6 metadata address (IPv6-only instances)
	}
	metadataNets = mustPrefixes("169.254.0.0/16", "fe80::/10")
	reservedNets = mustPrefixes(
		"0.0.0.0/8", "::/128", "224.0.0.0/4", "ff00::/8", "240.0.0.0/4", "192.0.0.0/24", "192.0.2.0/24",
		"198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "100::/64", "100:0:0:1::/64", "2001::/23",
		"2001:db8::/32", "3fff::/20", "5f00::/16", "fec0::/10", "::ffff:0:0:0/96")
	// ipv4Compat is the deprecated IPv4-compatible range ::/96 (RFC 4291 §2.5.5.1); only :: and ::1
	// are meaningful there, so the rest is reserved.
	ipv4Compat = netip.MustParsePrefix("::/96")
	loop6      = netip.MustParseAddr("::1")
	nat64      = netip.MustParsePrefix("64:ff9b::/96")
	nat64L     = netip.MustParsePrefix("64:ff9b:1::/48")
	sixToFour  = netip.MustParsePrefix("2002::/16")
	embedNets  = []netip.Prefix{netip.MustParsePrefix("::ffff:0:0/96"), nat64, nat64L, sixToFour}
	loopNets   = mustPrefixes("127.0.0.0/8", "::1/128")
	privNets   = mustPrefixes("10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "100.64.0.0/10", "fc00::/7")
)

// metadataHosts are names that reach cloud metadata without a metadata IP in the URL.
var metadataHosts = map[string]bool{"metadata.google.internal": true, "metadata": true, "instance-data": true}

// Classify returns an address's class, without the self check (which needs a port).
func Classify(a netip.Addr) Class {
	a = a.WithZone("")
	for _, m := range metadataAddrs {
		if a == m {
			return ClassMetadata
		}
	}
	for _, n := range metadataNets {
		if n.Contains(a) {
			return ClassMetadata
		}
	}
	for _, n := range reservedNets {
		if n.Contains(a) {
			return ClassReserved
		}
	}
	if ipv4Compat.Contains(a) && a != loop6 {
		return ClassReserved
	}
	if v4, ok := embedded(a); ok {
		return Classify(v4)
	}
	for _, n := range loopNets {
		if n.Contains(a) {
			return ClassLoopback
		}
	}
	for _, n := range privNets {
		if n.Contains(a) {
			return ClassPrivate
		}
	}
	return ClassPublic
}

// embedded returns the IPv4 address an IPv6 address carries (class 3). For 64:ff9b:1::/48, whose
// layout depends on the operator's prefix length, it returns the more restrictive of the /48 and
// /96 readings (RFC 6052 §2.2), so an ambiguous address fails closed.
func embedded(a netip.Addr) (netip.Addr, bool) {
	if !a.Is6() {
		return netip.Addr{}, false
	}
	b := a.As16()
	switch {
	case a.Is4In6():
		return a.Unmap(), true
	case nat64.Contains(a):
		return netip.AddrFrom4([4]byte{b[12], b[13], b[14], b[15]}), true
	case nat64L.Contains(a):
		r48 := netip.AddrFrom4([4]byte{b[6], b[7], b[9], b[10]})
		r96 := netip.AddrFrom4([4]byte{b[12], b[13], b[14], b[15]})
		if rank(Classify(r48)) >= rank(Classify(r96)) {
			return r48, true
		}
		return r96, true
	case sixToFour.Contains(a):
		return netip.AddrFrom4([4]byte{b[2], b[3], b[4], b[5]}), true
	}
	return netip.Addr{}, false
}

// rank orders classes from least to most restricted.
func rank(c Class) int {
	switch c {
	case ClassMetadata:
		return 6
	case ClassReserved:
		return 5
	case ClassSelf:
		return 4
	case ClassLoopback:
		return 3
	case ClassPrivate:
		return 2
	}
	return 1
}

// ClassifyAddrPort adds the self check: Deedglass's own database at its exact ip:port.
func (p *Policy) ClassifyAddrPort(ap netip.AddrPort) Class {
	c := Classify(ap.Addr())
	if c == ClassMetadata || c == ClassReserved {
		return c
	}
	if p != nil {
		want := netip.AddrPortFrom(ap.Addr().WithZone("").Unmap(), ap.Port())
		for _, s := range p.Self {
			if netip.AddrPortFrom(s.Addr().Unmap(), s.Port()) == want {
				return ClassSelf
			}
		}
	}
	return c
}

// allowListed reports whether a is inside DEEDGLASS_EGRESS_ALLOW_CIDRS.
func (p *Policy) allowListed(a netip.Addr) bool {
	if p == nil {
		return false
	}
	a = a.WithZone("").Unmap()
	for _, n := range p.AllowCIDRs {
		if n.Contains(a) {
			return true
		}
	}
	return false
}

// allowed decides one classified address.
func (p *Policy) allowed(a netip.Addr, c Class) bool {
	switch c {
	case ClassPublic:
		return true
	case ClassPrivate:
		return p.dev() && p.AllowPrivate || p.allowListed(a)
	case ClassLoopback:
		return p.dev() && p.AllowPrivate
	case ClassSelf:
		if p.dev() && p.AllowPrivate {
			if p.Log != nil {
				p.Log.Warn("egress: a connector is reaching Deedglass's own database (dev override)", "addr", a.String())
			}
			return true
		}
		// Prod: only if that exact address is listed.
		if p == nil {
			return false
		}
		for _, n := range p.AllowCIDRs {
			if n.IsSingleIP() && n.Addr() == a.WithZone("").Unmap() {
				return true
			}
		}
		return false
	}
	return false
}

// Check decides one ip:port. It returns the class, and a *BlockedError when refused.
func (p *Policy) Check(host string, ap netip.AddrPort) (Class, error) {
	c := p.ClassifyAddrPort(ap)
	if p.allowed(ap.Addr(), c) {
		return c, nil
	}
	return c, &BlockedError{Host: host, Addr: ap.Addr().WithZone("").String(), Class: c, Reason: reasonFor(c, p.dev())}
}

// hostClass classifies a host name before DNS: metadata names and localhost.
func hostClass(host string) (Class, bool) {
	h := strings.TrimSuffix(strings.ToLower(host), ".")
	switch {
	case metadataHosts[h]:
		return ClassMetadata, true
	case h == "localhost" || strings.HasSuffix(h, ".localhost"):
		return ClassLoopback, true
	}
	return "", false
}

// CheckAllowCIDR refuses an allow-list entry that overlaps a class that is never reachable
// (metadata, reserved, embedded IPv4) or loopback (S16 §3.2). config uses it.
func CheckAllowCIDR(n netip.Prefix) error {
	n = n.Masked()
	check := func(what string, ps ...netip.Prefix) error {
		for _, q := range ps {
			if n.Overlaps(q) {
				return fmt.Errorf("%s overlaps %s (%s), which Deedglass never connects to through the allow list", n, q, what)
			}
		}
		return nil
	}
	var meta []netip.Prefix
	for _, a := range metadataAddrs {
		meta = append(meta, netip.PrefixFrom(a, a.BitLen()))
	}
	meta = append(meta, metadataNets...)
	if err := check("cloud metadata", meta...); err != nil {
		return err
	}
	if err := check("reserved", reservedNets...); err != nil {
		return err
	}
	if err := check("an IPv6 range that embeds IPv4", embedNets...); err != nil {
		return err
	}
	return check("loopback", loopNets...)
}
