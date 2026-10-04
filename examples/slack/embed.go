// Package slack holds the example Slack app manifest that `deedglass slack manifest` prints.
package slack

import _ "embed"

// Manifest is manifest.yaml, with the placeholder request URL https://YOUR-HOST/v1/slack/interact.
//
//go:embed manifest.yaml
var Manifest string

// Placeholder is the request URL the CLI replaces.
const Placeholder = "https://YOUR-HOST/v1/slack/interact"
