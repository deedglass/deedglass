package main

import (
	"log/slog"

	"github.com/deedglass/deedglass/internal/config"
	"github.com/deedglass/deedglass/internal/connector"
)

// outboundTypes is set only in the ops build (outbound_tag.go, -tags outboundmail, make
// ops-build): it returns the outbound_mail connector type. In every other build it stays nil, the
// release binary carries no sending code (TestNoOutboundMailInRelease), and serve refuses to start
// when deedglass.yaml names connectors.outbound_mail.
var outboundTypes func(cfg *config.Config, log *slog.Logger) []connector.Connector

// MsgNoOutboundBuild is serve's refusal in a plain build with connectors.outbound_mail set.
const MsgNoOutboundBuild = "This build has no outbound_mail connector. Build the ops binary with make ops-build."
