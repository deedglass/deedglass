package connector

import (
	"regexp"
	"slices"
	"strings"
)

// Naming rules (S16 §3.3).
var (
	// ReConnector is a connector name: unique per org among live connectors.
	ReConnector = regexp.MustCompile(`^[a-z][a-z0-9-]{1,31}$`)
	// ReNativeTool is a tool name of a native connector type (rest, postgres, github, …, fake).
	ReNativeTool = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)
	// ReMCPTool is a tool name from an MCP server or an SDK edge: the ASCII MCP charset.
	ReMCPTool = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}$`)
)

// MaxQualified is the longest qualified tool name, in bytes.
const MaxQualified = 200

// ReservedNames are connector names nobody can register: deedglass, mcp and native everywhere,
// and fake outside dev.
var ReservedNames = []string{"deedglass", "mcp", "native"}

// NativeTypes are the connector types whose tools use the native charset.
var NativeTypes = []string{"rest", "postgres", "github", "slack", "stripe", "gmail", "notion", "fake"}

// Reserved reports whether name is reserved in this env.
func Reserved(name string, dev bool) bool {
	return slices.Contains(ReservedNames, name) || name == "fake" && !dev
}

// ValidToolName reports whether tool is a well-formed tool name for a connector of type typ. MCP
// sources (mcp, mcp_http) and SDK edges (sdk) use the MCP charset; every native type the strict one.
func ValidToolName(typ, tool string) bool {
	if slices.Contains(NativeTypes, typ) {
		return ReNativeTool.MatchString(tool)
	}
	return ReMCPTool.MatchString(tool)
}

// Split cuts a qualified name at its first dot: "orders.update_status" → ("orders", "update_status").
func Split(qualified string) (conn, tool string, ok bool) {
	if len(qualified) > MaxQualified {
		return "", "", false
	}
	conn, tool, ok = strings.Cut(qualified, ".")
	if !ok || !ReConnector.MatchString(conn) || tool == "" {
		return "", "", false
	}
	return conn, tool, true
}
