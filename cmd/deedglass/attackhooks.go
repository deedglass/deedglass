//go:build attackhooks

package main

// The attack harness's DNS (S18). This file is compiled only with -tags attackhooks, which only
// make attack uses; release builds, make build and the Docker image never contain it, and
// TestNoAttackHooks checks that a default build has no hook. Even here it works only in dev.

import (
	"context"
	"encoding/json"
	"log/slog"
	"net"
	"net/netip"
	"os"
	"strings"

	"github.com/deedglass/deedglass/internal/config"
	"github.com/deedglass/deedglass/internal/connector/egress"
)

func init() { egressResolverHook = attackResolver }

// attackResolver answers the hosts listed in the JSON file named by DEEDGLASS_DEV_ATTACK_HOSTS
// ({"metadata.attack.test": ["169.254.169.254"]}), re-read on every lookup so the harness can
// change an answer mid-run (DNS rebinding); other hosts use the system resolver.
func attackResolver(cfg *config.Config, log *slog.Logger) egress.Resolver {
	path := os.Getenv("DEEDGLASS_DEV_ATTACK_HOSTS")
	if path == "" || cfg.Env != config.Dev {
		return nil
	}
	log.Warn("attack harness: host answers come from a file (an attackhooks build, dev only)", "file", path)
	return fileResolver(path)
}

type fileResolver string

func (f fileResolver) LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error) {
	b, err := os.ReadFile(string(f))
	if err != nil {
		return nil, err
	}
	var m map[string][]string
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	as, ok := m[strings.TrimSuffix(strings.ToLower(host), ".")]
	if !ok {
		return net.DefaultResolver.LookupNetIP(ctx, network, host)
	}
	out := make([]netip.Addr, 0, len(as))
	for _, s := range as {
		a, err := netip.ParseAddr(s)
		if err != nil {
			return nil, err
		}
		switch {
		case network == "ip4" && !a.Unmap().Is4(), network == "ip6" && a.Unmap().Is4():
			continue
		}
		out = append(out, a)
	}
	if len(out) == 0 {
		return nil, &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
	}
	return out, nil
}
