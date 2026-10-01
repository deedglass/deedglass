package egress

import (
	"context"
	"net"
	"net/netip"
	"strings"
	"sync"
)

// Static is a Resolver that answers from fixed data, for tests (S16, S18's attack harness, S34).
// A host can carry a sequence of answers that changes between calls, which is how DNS rebinding
// looks from the inside: the first lookup sees a public address, the next one loopback.
type Static struct {
	mu    sync.Mutex
	fixed map[string][]netip.Addr
	seq   map[string][][]netip.Addr
	calls map[string]int
}

// StaticResolver answers each host in m with its addresses. Unknown hosts are "no such host".
func StaticResolver(m map[string][]netip.Addr) *Static {
	s := &Static{fixed: map[string][]netip.Addr{}, seq: map[string][][]netip.Addr{}, calls: map[string]int{}}
	for h, as := range m {
		s.fixed[normHost(h)] = as
	}
	return s
}

// Sequence makes host answer answers[0] on the first lookup, answers[1] on the second, and the
// last one from then on.
func (s *Static) Sequence(host string, answers ...[]netip.Addr) *Static {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seq[normHost(host)] = answers
	return s
}

// Calls is how many lookups host has had.
func (s *Static) Calls(host string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls[normHost(host)]
}

func normHost(h string) string { return strings.TrimSuffix(strings.ToLower(h), ".") }

// LookupNetIP implements Resolver. network "ip4" and "ip6" filter the answer.
func (s *Static) LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	h := normHost(host)
	s.mu.Lock()
	n := s.calls[h]
	s.calls[h] = n + 1
	var as []netip.Addr
	if sq, ok := s.seq[h]; ok && len(sq) > 0 {
		as = sq[min(n, len(sq)-1)]
	} else if f, ok := s.fixed[h]; ok {
		as = f
	} else {
		s.mu.Unlock()
		return nil, &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
	}
	s.mu.Unlock()
	out := make([]netip.Addr, 0, len(as))
	for _, a := range as {
		switch {
		case network == "ip4" && !a.Unmap().Is4(), network == "ip6" && a.Is4():
			continue
		}
		out = append(out, a)
	}
	return out, nil
}
