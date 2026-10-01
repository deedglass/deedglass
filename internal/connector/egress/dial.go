package egress

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"syscall"
	"time"
)

// DialTimeout bounds one TCP connect.
const DialTimeout = 5 * time.Second

// DialContext resolves addr's host, refuses it if any answer is blocked, then dials the checked
// addresses in order. The dialer's ControlContext checks the address again at connect time, so a
// path that skips resolution (or a resolver that lies twice) still can't reach a blocked address.
// Its signature fits http.Transport.DialContext and pgx's DialFunc.
func (p *Policy) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	switch network {
	case "tcp", "tcp4", "tcp6":
	default:
		return nil, fmt.Errorf("egress: network %q isn't allowed", network)
	}
	host, portS, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, fmt.Errorf("egress: %w", err)
	}
	pn, err := strconv.ParseUint(portS, 10, 16)
	if err != nil || pn == 0 {
		return nil, fmt.Errorf("egress: bad port in %q", addr)
	}
	aps, _, err := p.resolveChecked(ctx, host, uint16(pn))
	if err != nil {
		return nil, err
	}
	var last error
	for _, ap := range aps {
		if network == "tcp4" && !ap.Addr().Unmap().Is4() || network == "tcp6" && ap.Addr().Is4() {
			continue
		}
		c, err := p.dialOne(ctx, host, ap)
		if err == nil {
			return c, nil
		}
		if _, blocked := IsBlocked(err); blocked {
			return nil, err
		}
		last = err
		if ctx.Err() != nil {
			break
		}
	}
	if last == nil {
		last = fmt.Errorf("egress: no address of %s to dial", host)
	}
	return nil, last
}

// dialOne connects to one checked address, re-checking it in ControlContext.
func (p *Policy) dialOne(ctx context.Context, host string, ap netip.AddrPort) (net.Conn, error) {
	if p != nil && p.dialer != nil {
		if _, err := p.Check(host, ap); err != nil {
			return nil, err
		}
		return p.dialer(ctx, "tcp", ap.String())
	}
	var blocked error
	d := &net.Dialer{Timeout: DialTimeout, KeepAlive: 30 * time.Second,
		ControlContext: func(_ context.Context, _, address string, _ syscall.RawConn) error {
			got, err := netip.ParseAddrPort(address)
			if err != nil {
				blocked = &BlockedError{Host: host, Addr: address, Class: ClassReserved, Reason: "Deedglass couldn't read the address it was about to connect to, so it didn't."}
				return blocked
			}
			if _, err := p.Check(host, got); err != nil {
				blocked = err
				return err
			}
			return nil
		}}
	c, err := d.DialContext(ctx, "tcp", ap.String())
	if blocked != nil {
		if c != nil {
			_ = c.Close()
		}
		return nil, blocked
	}
	if err != nil {
		var be *BlockedError
		if errors.As(err, &be) {
			return nil, be
		}
	}
	return c, err
}

// netDialer is FirstParty's dialer.
var netDialer = net.Dialer{Timeout: DialTimeout, KeepAlive: 30 * time.Second}
