// Package toolname maps a tool's qualified name ("postgres.query") to the name MCP clients see
// ("postgres_query") and back.
//
// Model APIs limit tool names to letters, digits, "_" and "-" (Anthropic's Messages API:
// ^[a-zA-Z0-9_-]{1,128}$; OpenAI's function names: a-z, A-Z, 0-9, "_" and "-", at most 64), while
// the MCP spec (2025-11-25, "Tool Names") also allows a dot. A dotted name breaks agents that pass
// an MCP server's tools/list straight to the model. A connector name never holds
// "_" or "." (^[a-z][a-z0-9-]{1,31}$), so swapping the first dot for an underscore is reversible:
// the first underscore of a safe name always ends the connector.
//
// The qualified, dotted name stays canonical everywhere else: actions, deeds, policies, logs.
package toolname

import (
	"regexp"
	"strings"
)

// Style is how an MCP server spells its tools in tools/list.
type Style string

const (
	// Safe spells "postgres.query" as "postgres_query", accepted by every model API (default).
	Safe Style = "safe"
	// Dotted is the listing in 0.7 and earlier: "postgres.query". Deprecated; kept for clients that
	// pinned the old names.
	Dotted Style = "dotted"
)

// ParseStyle reads --tool-names and mcp.tool_names: "" is Safe.
func ParseStyle(s string) (Style, bool) {
	switch Style(s) {
	case "", Safe:
		return Safe, true
	case Dotted:
		return Dotted, true
	}
	return "", false
}

// reConnector is connector.ReConnector, repeated here so this package imports nothing.
var reConnector = regexp.MustCompile(`^[a-z][a-z0-9-]{1,31}$`)

// SafeName is the API-safe spelling of a qualified name: the dot after the connector becomes an
// underscore ("postgres.query" → "postgres_query"). A name that isn't "<connector>.<tool>" comes
// back unchanged.
func SafeName(qualified string) string {
	conn, tool, ok := strings.Cut(qualified, ".")
	if !ok || tool == "" || !reConnector.MatchString(conn) {
		return qualified
	}
	return conn + "_" + tool
}

// Canonical is the qualified name for either spelling: a name holding a dot comes back unchanged,
// and "postgres_query" becomes "postgres.query" when the part before the first underscore is a
// connector name. Anything else comes back unchanged (the caller reports it as it does today).
func Canonical(name string) string {
	if strings.IndexByte(name, '.') >= 0 {
		return name
	}
	conn, tool, ok := strings.Cut(name, "_")
	if !ok || tool == "" || !reConnector.MatchString(conn) {
		return name
	}
	return conn + "." + tool
}

// Wire is the name tools/list shows for a qualified name in style s.
func Wire(qualified string, s Style) string {
	if s == Dotted {
		return qualified
	}
	return SafeName(qualified)
}

// IsDotted reports whether a name a client called is the deprecated dotted spelling of a
// Deedglass-qualified name.
func IsDotted(name string) bool {
	conn, tool, ok := strings.Cut(name, ".")
	return ok && tool != "" && reConnector.MatchString(conn)
}
