package egress

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

// Checked is a URL that passed CheckURL: its normalized form, and what its host resolved to.
type Checked struct {
	URL   *url.URL         // scheme and host lowercased, one trailing dot stripped
	Host  string           // the host without port or brackets
	Port  uint16           // the explicit port, or the scheme's default
	Addrs []netip.AddrPort // the resolved addresses, each allowed
	Class Class            // the most restricted class among Addrs (public for a clean answer)
}

// ResolveError is a DNS failure: the destination couldn't be checked yet. Creating a connector
// tolerates it (the test step reports it); a dial never does.
type ResolveError struct {
	Host string
	Err  error
}

func (e *ResolveError) Error() string {
	return fmt.Sprintf("%s doesn't resolve right now (%s). Check the name, or try again when its DNS is ready.", e.Host, lastWords(e.Err))
}

func (e *ResolveError) Unwrap() error { return e.Err }

func lastWords(err error) string {
	var de *net.DNSError
	if errors.As(err, &de) {
		if de.IsNotFound {
			return "no such host"
		}
		if de.IsTimeout {
			return "DNS timed out"
		}
		return de.Err
	}
	s := err.Error()
	if i := strings.LastIndex(s, ": "); i >= 0 {
		s = s[i+2:]
	}
	return s
}

var (
	// reNumericLast is a last label a WHATWG URL parser reads as a number: decimal, or 0x hex.
	reNumericLast = regexp.MustCompile(`^(0x[0-9a-f]*|[0-9]+)$`)
	reHostLabel   = regexp.MustCompile(`^[a-z0-9_]([a-z0-9_-]{0,61}[a-z0-9_])?$`)
)

func shapeErr(raw, reason string) *BlockedError {
	return &BlockedError{Host: clip(raw), Reason: reason}
}

func clip(s string) string {
	if len(s) > 200 {
		return s[:200] + "…"
	}
	return s
}

// ParseURL applies the URL rules (S16 §3.2) without DNS: https (http in dev only), no userinfo,
// an ASCII host lowercased with one trailing dot stripped, no non-canonical numeric host, no zone
// id. A host that is an address, a metadata name or localhost is classified here too.
func (p *Policy) ParseURL(raw string) (*Checked, error) {
	if raw == "" || len(raw) > 2048 {
		return nil, shapeErr(raw, "The URL must be 1 to 2,048 characters.")
	}
	for _, r := range raw {
		if r > 0x7e || r < 0x21 {
			return nil, shapeErr(raw, "The URL must be plain ASCII without spaces. Write an international domain name in its punycode form (xn--…).")
		}
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, shapeErr(raw, "The URL can't be parsed. Use the form https://api.example.com/v1.")
	}
	u.Scheme = strings.ToLower(u.Scheme)
	switch u.Scheme {
	case "https":
	case "http":
		if !p.dev() {
			return nil, shapeErr(raw, "Deedglass connects with https outside dev. Use the service's https URL.")
		}
	default:
		return nil, shapeErr(raw, fmt.Sprintf("The scheme %q isn't allowed. Use https.", clip(u.Scheme)))
	}
	if u.Opaque != "" || u.Host == "" {
		return nil, shapeErr(raw, "The URL needs a host, like https://api.example.com.")
	}
	if u.User != nil || strings.Contains(u.Host, "@") {
		return nil, shapeErr(raw, "The URL can't carry a user name or password. Store credentials with deedglass connector credentials set.")
	}
	host, port := u.Hostname(), u.Port()
	if strings.Contains(host, "%") {
		return nil, shapeErr(raw, "IPv6 zone ids aren't allowed in a destination.")
	}
	host = strings.ToLower(host)
	host = strings.TrimSuffix(host, ".")
	if host == "" {
		return nil, shapeErr(raw, "The URL needs a host, like https://api.example.com.")
	}
	c := &Checked{Host: host}
	if port != "" {
		n, err := strconv.ParseUint(port, 10, 16)
		if err != nil || n == 0 || strconv.FormatUint(n, 10) != port {
			return nil, shapeErr(raw, "The port must be a number from 1 to 65535.")
		}
		c.Port = uint16(n)
	} else if u.Scheme == "https" {
		c.Port = 443
	} else {
		c.Port = 80
	}
	if a, err := netip.ParseAddr(host); err == nil {
		if a.Zone() != "" {
			return nil, shapeErr(raw, "IPv6 zone ids aren't allowed in a destination.")
		}
		if a.Is4() && strings.Contains(u.Hostname(), ":") {
			return nil, shapeErr(raw, "Write an IPv4 address in dotted form, like 93.184.216.34.")
		}
	} else {
		labels := strings.Split(host, ".")
		if reNumericLast.MatchString(labels[len(labels)-1]) {
			return nil, shapeErr(raw, "The host is a number in a non-standard form, which Deedglass doesn't accept. Write the address as four decimal parts, like 93.184.216.34, or use the host's name.")
		}
		if len(host) > 253 {
			return nil, shapeErr(raw, "The host name is longer than 253 characters.")
		}
		for _, l := range labels {
			if !reHostLabel.MatchString(l) {
				return nil, shapeErr(raw, fmt.Sprintf("The host %q isn't a valid DNS name. Write an international domain name in its punycode form (xn--…).", clip(host)))
			}
		}
	}
	nu := *u
	nu.Host = host
	if strings.Contains(host, ":") {
		nu.Host = "[" + host + "]"
	}
	if port != "" {
		nu.Host += ":" + port
	}
	c.URL = &nu
	return c, nil
}

// CheckURL is ParseURL plus resolution: every address the host resolves to must be allowed. A
// DNS failure is a *ResolveError; a refused destination is a *BlockedError.
func (p *Policy) CheckURL(ctx context.Context, raw string) (*Checked, error) {
	c, err := p.ParseURL(raw)
	if err != nil {
		return nil, err
	}
	aps, cls, err := p.resolveChecked(ctx, c.Host, c.Port)
	c.Addrs, c.Class = aps, cls
	if err != nil {
		return c, err
	}
	return c, nil
}

// resolveChecked resolves host and checks every answer. Any blocked answer refuses the whole
// destination (fail closed on mixed answers).
func (p *Policy) resolveChecked(ctx context.Context, host string, port uint16) ([]netip.AddrPort, Class, error) {
	if hc, ok := hostClass(host); ok {
		if hc == ClassMetadata {
			return nil, hc, &BlockedError{Host: host, Class: hc, Reason: reasonFor(hc, p.dev())}
		}
		// localhost and *.localhost are loopback whatever DNS says (RFC 6761 §6.3).
		if !(p.dev() && p.AllowPrivate) {
			return nil, hc, &BlockedError{Host: host, Class: hc, Reason: reasonFor(hc, p.dev())}
		}
	}
	var addrs []netip.Addr
	if a, err := netip.ParseAddr(host); err == nil {
		addrs = []netip.Addr{a}
	} else if hc, ok := hostClass(host); ok && hc == ClassLoopback {
		addrs = []netip.Addr{netip.MustParseAddr("127.0.0.1"), loop6}
	} else {
		as, err := p.resolver().LookupNetIP(ctx, "ip", host)
		if err != nil {
			return nil, "", &ResolveError{Host: host, Err: err}
		}
		if len(as) == 0 {
			return nil, "", &ResolveError{Host: host, Err: errors.New("no addresses")}
		}
		addrs = as
	}
	out := make([]netip.AddrPort, 0, len(addrs))
	worst := ClassPublic
	var firstErr error
	for _, a := range addrs {
		ap := netip.AddrPortFrom(a.WithZone(""), port)
		c, err := p.Check(host, ap)
		if rank(c) > rank(worst) {
			worst = c
		}
		if err != nil && firstErr == nil {
			firstErr = err
		}
		out = append(out, ap)
	}
	if firstErr != nil {
		return out, worst, firstErr
	}
	return out, worst, nil
}
