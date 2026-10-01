package snippet

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// The Acme demo's client config (S15 §3.8): four entries in one file.
//
//	orders         crystaldba/postgres-mcp in Docker, through deedglass mcp wrap (proxy mode,
//	               --connector-type postgres so the SQL classifier reads args.sql)
//	orders-direct  the same server without Deedglass: the contrast shot only, never a recommendation
//	scratch        the filesystem server on /tmp/deedglass-demo, through deedglass mcp wrap
//	deedglass      the Gate's native tools over the stdio bridge (deedglass mcp serve)

// Pinned demo images and packages (S15 report: versions verified 2026-10-01).
const (
	PostgresMCPImage = "crystaldba/postgres-mcp:0.3.0@sha256:dbbd346860d29f1543e991f30f3284bf4ab5f096d049ecc3426528f20b1b6e6b"
	FilesystemPkg    = "@modelcontextprotocol/server-filesystem@2026.8.31"
	DemoFolder       = "/tmp/deedglass-demo"
	// PlaceholderOwnerDSN is the orders DSN with the password left for the founder to fill.
	PlaceholderOwnerDSN = "postgresql://acme_owner:ACME_OWNER_PASSWORD@localhost:55434/acme"
)

// DemoOptions are the paths and secrets of one demo config. Empty secrets become placeholders.
type DemoOptions struct {
	Client    string
	Deedglass string // absolute path of bin/deedglass
	Docker    string // absolute path of docker
	Npx       string // absolute path of npx
	Server    string // http://127.0.0.1:8080
	APIKey    string // the agent's dg_test_ key, or "" for the placeholder
	OwnerDSN  string // postgresql://acme_owner:…@localhost:55434/acme, or "" for the placeholder
	PATH      string // the PATH every entry gets
}

type demoEntry struct {
	name    string
	command string
	args    []string
	env     map[string]string
}

func (o DemoOptions) entries() []demoEntry {
	key, dsn := o.APIKey, o.OwnerDSN
	if key == "" {
		key = PlaceholderKey
	}
	if dsn == "" {
		dsn = PlaceholderOwnerDSN
	}
	dg := map[string]string{"DEEDGLASS_API_KEY": key, "DEEDGLASS_SERVER": o.Server, "PATH": o.PATH}
	// --init: the server runs as PID 1 and ignores SIGTERM without it, so the container would
	// outlive the wrapper's shutdown (stdin closed, then SIGTERM to the docker CLI).
	docker := []string{o.Docker, "run", "-i", "--rm", "--init", "-e", "DATABASE_URI", PostgresMCPImage, "--access-mode=unrestricted"}
	withDSN := func(m map[string]string) map[string]string {
		out := map[string]string{"DATABASE_URI": dsn}
		for k, v := range m {
			out[k] = v
		}
		return out
	}
	return []demoEntry{
		{name: "orders", command: o.Deedglass, args: append([]string{"mcp", "wrap", "--connector", "orders", "--connector-type", "postgres", "--"}, docker...), env: withDSN(dg)},
		{name: "orders-direct", command: o.Docker, args: docker[1:], env: withDSN(map[string]string{"PATH": o.PATH})},
		{name: "scratch", command: o.Deedglass, args: []string{"mcp", "wrap", "--connector", "scratch", "--", o.Npx, "-y", FilesystemPkg, DemoFolder}, env: dg},
		{name: "deedglass", command: o.Deedglass, args: []string{"mcp", "serve", "--connector", "native"},
			env: map[string]string{"DEEDGLASS_API_KEY": key, "DEEDGLASS_SERVER": o.Server}},
	}
}

// BuildDemo returns the demo config for one client.
func BuildDemo(o DemoOptions) ([]byte, error) {
	if !Known(o.Client) {
		return nil, fmt.Errorf("unknown client %q: use one of %s", o.Client, strings.Join(Clients, ", "))
	}
	if o.Deedglass == "" || o.Docker == "" || o.Npx == "" || o.Server == "" {
		return nil, fmt.Errorf("the demo snippet needs the deedglass, docker and npx paths and the server")
	}
	es := o.entries()
	if o.Client == "claude-code" {
		var b strings.Builder
		b.WriteString("# The Acme demo for Claude Code (make demo-snippet CLIENT=claude-code). orders-direct bypasses Deedglass on purpose: contrast only.\n")
		for _, e := range es {
			keys := make([]string, 0, len(e.env))
			for k := range e.env {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			b.WriteString("claude mcp add")
			for _, k := range keys {
				b.WriteString(" --env " + shQuote(k+"="+e.env[k]))
			}
			b.WriteString(" --transport stdio " + shQuote(e.name) + " -- " + shQuote(e.command))
			for _, a := range e.args {
				b.WriteString(" " + shQuote(a))
			}
			b.WriteString("\n")
		}
		return []byte(b.String()), nil
	}
	type server struct {
		Type    string            `json:"type,omitempty"`
		Command string            `json:"command"`
		Args    []string          `json:"args"`
		Env     map[string]string `json:"env"`
	}
	m := map[string]server{}
	for _, e := range es {
		s := server{Command: e.command, Args: e.args, Env: e.env}
		if o.Client == "vscode" {
			s.Type = "stdio"
		}
		m[e.name] = s
	}
	var v any = map[string]any{"mcpServers": m}
	if o.Client == "vscode" {
		v = map[string]any{"servers": m}
	}
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}
