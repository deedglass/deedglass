package wrap

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/deedglass/deedglass/internal/gate"
)

// Interop (S14 §4 step 13a): the official Go SDK's client ⇄ deedglass mcp wrap ⇄ the SDK's server,
// over real stdio, in every era combination: list, an allowed call, a denied call, and a
// cancellation. The SDK is a test-only dependency here (ADR-018).

type echoIn struct {
	Message string `json:"message"`
}

type pathIn struct {
	Path    string `json:"path"`
	Content string `json:"content,omitempty"`
}

// runSDKServer is the child: an SDK server over stdio. sdk-legacy speaks 2025-11-25 only.
func runSDKServer(mode string) {
	opts := &mcp.ServerOptions{Instructions: "SDK fake."}
	if mode == "sdk-legacy" {
		opts.SupportedProtocolVersions = []string{"2025-11-25"}
	}
	s := mcp.NewServer(&mcp.Implementation{Name: "sdk-fake", Version: "1"}, opts)
	logf := func(line string) {
		if p := os.Getenv("FAKE_MCP_LOG"); p != "" {
			if f, err := os.OpenFile(p, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600); err == nil {
				f.WriteString(line + "\n")
				f.Close()
			}
		}
	}
	mcp.AddTool(s, &mcp.Tool{Name: "echo", Description: "Echo a message."}, func(ctx context.Context, _ *mcp.CallToolRequest, in echoIn) (*mcp.CallToolResult, any, error) {
		logf("call echo")
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "echo: " + in.Message}}}, nil, nil
	})
	mcp.AddTool(s, &mcp.Tool{Name: "write_file", Description: "Write a file."}, func(ctx context.Context, _ *mcp.CallToolRequest, in pathIn) (*mcp.CallToolResult, any, error) {
		logf("call write_file")
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "wrote " + in.Path}}}, nil, nil
	})
	mcp.AddTool(s, &mcp.Tool{Name: "move_file", Description: "Move a file."}, func(ctx context.Context, _ *mcp.CallToolRequest, in pathIn) (*mcp.CallToolResult, any, error) {
		logf("call move_file")
		return &mcp.CallToolResult{}, nil, nil
	})
	mcp.AddTool(s, &mcp.Tool{Name: "slow", Description: "Wait until cancelled."}, func(ctx context.Context, _ *mcp.CallToolRequest, in echoIn) (*mcp.CallToolResult, any, error) {
		logf("call slow")
		select {
		case <-ctx.Done():
			logf("slow cancelled")
			return nil, nil, ctx.Err()
		case <-time.After(20 * time.Second):
		}
		return &mcp.CallToolResult{}, nil, nil
	})
	_ = s.Run(context.Background(), &mcp.StdioTransport{})
}

func TestSDKInterop(t *testing.T) {
	for _, client := range []string{"2026-07-28", "2025-11-25"} {
		for _, server := range []string{"sdk-modern", "sdk-legacy"} {
			t.Run(client+"×"+server, func(t *testing.T) { sdkRound(t, client, server) })
		}
	}
}

func sdkRound(t *testing.T, clientVersion, serverMode string) {
	g := newFakeGate(t)
	dir := t.TempDir()
	calls := filepath.Join(dir, "calls.log")
	u, _ := url.Parse(g.srv.URL)
	cfg := Config{Connector: "scratch", Server: u, APIKey: "dg_test_wrapkey", HideDenied: true, Version: "test",
		Upstream: []string{os.Args[0]}, Env: append(os.Environ(), "FAKE_MCP_MODE="+serverMode, "FAKE_MCP_LOG="+calls),
		RestartBase: 20 * time.Millisecond, ShutdownWait: 500 * time.Millisecond, KillWait: 200 * time.Millisecond}
	stderr := &syncBuf{}
	w, err := New(cfg, stderr)
	if err != nil {
		t.Fatal(err)
	}
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	done := make(chan error, 1)
	go func() {
		done <- w.Run(context.Background(), inR, outW)
		outW.Close()
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	c := mcp.NewClient(&mcp.Implementation{Name: "sdk-client", Version: "1"}, nil)
	cs, err := c.Connect(ctx, &mcp.IOTransport{Reader: outR, Writer: inW}, &mcp.ClientSessionOptions{ProtocolVersion: clientVersion})
	if err != nil {
		t.Fatalf("connect: %v\n%s", err, stderr)
	}
	defer func() {
		cs.Close()
		select {
		case <-done:
		case <-time.After(15 * time.Second):
			t.Error("wrapper didn't exit")
		}
	}()
	if ir := cs.InitializeResult(); ir == nil || !strings.Contains(ir.Instructions, gate.MCPInstructions) {
		t.Fatalf("instructions: %+v", ir)
	}
	lt, err := cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	var names []string
	for _, tl := range lt.Tools {
		names = append(names, tl.Name)
	}
	if !slices.Contains(names, "deedglass_check_approval") || slices.Contains(names, "move_file") || !slices.Contains(names, "echo") {
		t.Fatalf("tools %v", names)
	}
	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "echo", Arguments: map[string]any{"message": "hi"}})
	if err != nil || res.IsError || res.Content[0].(*mcp.TextContent).Text != "echo: hi" {
		t.Fatalf("echo: %v %+v", err, res)
	}
	if res.Meta["com.deedglass/action"] == nil {
		t.Fatalf("meta %v", res.Meta)
	}
	res, err = cs.CallTool(ctx, &mcp.CallToolParams{Name: "write_file", Arguments: map[string]any{"path": "/tmp/deedglass-demo/outside.txt", "content": "x"}})
	if err != nil || !res.IsError || !strings.HasPrefix(res.Content[0].(*mcp.TextContent).Text, `Blocked by policy "scratch-writes-only-in-allowed"`) {
		t.Fatalf("deny: %v %+v", err, res)
	}
	// Cancel after forwarding: the server's handler sees its context end.
	cctx, ccancel := context.WithCancel(ctx)
	errc := make(chan error, 1)
	go func() {
		_, err := cs.CallTool(cctx, &mcp.CallToolParams{Name: "slow", Arguments: map[string]any{"message": "x"}})
		errc <- err
	}()
	deadline := time.Now().Add(10 * time.Second)
	for !strings.Contains(readFile(calls), "call slow") && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	ccancel()
	<-errc
	for !strings.Contains(readFile(calls), "slow cancelled") && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if got := readFile(calls); strings.Count(got, "call ") != 2 || !strings.Contains(got, "slow cancelled") {
		t.Fatalf("server saw:\n%s\nstderr:\n%s", got, stderr)
	}
	if strings.Contains(readFile(calls), "call write_file") {
		t.Fatal("denied call reached the server")
	}
	fmt.Fprint(io.Discard, stderr.String())
}

func readFile(p string) string {
	b, _ := os.ReadFile(p)
	return string(b)
}
