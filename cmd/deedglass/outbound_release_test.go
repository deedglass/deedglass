//go:build !outboundmail

package main

import (
	"context"
	"log/slog"
	"os/exec"
	"strings"
	"testing"

	"github.com/deedglass/deedglass/internal/config"
)

// TestNoOutboundMailInRelease (S40): a build without -tags outboundmail has no outbound_mail type,
// no import of internal/connector/outbound, and refuses to serve with connectors.outbound_mail set.
func TestNoOutboundMailInRelease(t *testing.T) {
	if outboundTypes != nil {
		t.Fatal("a default build registers the outbound_mail type")
	}
	out, err := exec.Command("go", "list", "-deps", ".").Output()
	if err != nil {
		t.Skipf("go list unavailable: %v", err)
	}
	for _, l := range strings.Split(string(out), "\n") {
		if strings.Contains(l, "connector/outbound") || strings.Contains(l, "fakegmail") {
			t.Fatalf("the release build imports %s", l)
		}
	}
	tags, err := exec.Command("go", "list", "-deps", "-tags", "outboundmail", ".").Output()
	if err == nil && !strings.Contains(string(tags), "internal/connector/outbound") {
		t.Fatal("the ops build should import internal/connector/outbound (planted-import check)")
	}
}

func TestPlainBuildRefusesOutboundSetting(t *testing.T) {
	cfg := &config.Config{Env: config.Dev}
	cfg.Connectors.OutboundMail = config.OutboundMailSettings{Set: true, Orgs: []string{"org_01k6c9kb2zcjsyn7b8g2k1vqvp"}, APIBase: config.DefaultOutboundMailAPIBase}
	_, err := newConnectors(context.Background(), cfg, nil, nil, nil, slog.New(slog.DiscardHandler))
	if err == nil || err.Error() != MsgNoOutboundBuild {
		t.Fatalf("got %v", err)
	}
	cfg.Connectors.OutboundMail = config.OutboundMailSettings{}
	if _, err := newConnectors(context.Background(), cfg, nil, nil, nil, slog.New(slog.DiscardHandler)); err != nil {
		t.Fatalf("without the setting: %v", err)
	}
}
