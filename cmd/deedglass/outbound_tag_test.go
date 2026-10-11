//go:build outboundmail

package main

import (
	"context"
	"log/slog"
	"testing"

	"github.com/deedglass/deedglass/internal/config"
)

// TestOutboundOpsBuild (S40): the ops build registers outbound_mail for the listed orgs only, and
// its two commands stay out of help.
func TestOutboundOpsBuild(t *testing.T) {
	if outboundTypes == nil {
		t.Fatal("the ops build has no outbound_mail type")
	}
	listed, other := "org_01k6c9kb2zcjsyn7b8g2k1vqvp", "org_01k6c9kb2zcjsyn7b8g2k1vqvq"
	cfg := &config.Config{Env: config.Dev}
	cfg.Connectors.OutboundMail = config.OutboundMailSettings{Set: true, Orgs: []string{listed}, APIBase: config.DefaultOutboundMailAPIBase}
	c, err := newConnectors(context.Background(), cfg, nil, nil, nil, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := c.reg.TypeFor(listed, "outbound_mail"); !ok {
		t.Fatal("the listed org can't use outbound_mail")
	}
	if _, ok := c.reg.TypeFor(other, "outbound_mail"); ok {
		t.Fatal("an unlisted org can use outbound_mail")
	}
	// No setting at all: the type is registered but no org may use it.
	cfg.Connectors.OutboundMail = config.OutboundMailSettings{}
	c, err = newConnectors(context.Background(), cfg, nil, nil, nil, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := c.reg.TypeFor(listed, "outbound_mail"); ok {
		t.Fatal("outbound_mail is usable with no orgs listed")
	}
	seen := 0
	for _, cmd := range commands {
		if len(cmd.Path) == 3 && cmd.Path[0] == "connector" && cmd.Path[1] == "outbound" || len(cmd.Path) == 2 && cmd.Path[1] == "fake-gmail" {
			seen++
			if !cmd.Hidden || cmd.Run == nil {
				t.Errorf("%v should be built and hidden", cmd.Path)
			}
		}
	}
	if seen != 2 {
		t.Fatalf("%d outbound commands", seen)
	}
}
