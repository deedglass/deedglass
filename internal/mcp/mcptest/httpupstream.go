package mcptest

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// HTTPUpstream is a remote MCP server for the S15 gateway's tests: the official SDK's Streamable
// HTTP handler, stateless (2026-07-28) or stateful (2025-11-25 sessions), with counters, the
// request headers it saw, and tools whose behaviour tests control.
//
//	echo        returns "Echo: <message>"
//	hang        blocks until the request is cancelled (records that it was)
//	fail        returns isError: true
//	approve_me  returns "approved: <message>" (policies make it wait for a person)
//	delete_all  returns "deleted" (policies deny it, so it must never run)
type HTTPUpstream struct {
	Server    *httptest.Server
	Calls     sync.Map // tool name → *atomic.Int64
	Cancelled atomic.Int64
	Started   chan string // receives a tool name when a call starts (buffered)

	mu      sync.Mutex
	headers []http.Header

	cancel context.CancelFunc
	done   <-chan struct{}
}

type msgIn struct {
	Message string `json:"message,omitempty"`
}

// NewHTTPUpstream starts an upstream. stateless serves 2026-07-28 statelessly; otherwise the
// SDK's stateful handler with only 2025-11-25 (a legacy-era server).
func NewHTTPUpstream(stateless bool) *HTTPUpstream {
	ctx, cancel := context.WithCancel(context.Background())
	u := &HTTPUpstream{Started: make(chan string, 256), cancel: cancel, done: ctx.Done()}
	opts := &mcp.ServerOptions{Instructions: "An upstream for Deedglass tests."}
	if !stateless {
		opts.SupportedProtocolVersions = []string{"2025-11-25"}
	}
	s := mcp.NewServer(&mcp.Implementation{Name: "upstream-fake", Version: "1"}, opts)
	count := func(name string) {
		v, _ := u.Calls.LoadOrStore(name, new(atomic.Int64))
		v.(*atomic.Int64).Add(1)
		select {
		case u.Started <- name:
		default:
		}
	}
	text := func(t string) *mcp.CallToolResult {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: t}}}
	}
	mcp.AddTool(s, &mcp.Tool{Name: "echo", Description: "Echo a message."}, func(_ context.Context, _ *mcp.CallToolRequest, in msgIn) (*mcp.CallToolResult, any, error) {
		count("echo")
		return text("Echo: " + in.Message), nil, nil
	})
	mcp.AddTool(s, &mcp.Tool{Name: "hang", Description: "Wait until cancelled."}, func(ctx context.Context, _ *mcp.CallToolRequest, _ msgIn) (*mcp.CallToolResult, any, error) {
		count("hang")
		select {
		case <-ctx.Done():
			u.Cancelled.Add(1)
			return nil, nil, ctx.Err()
		case <-u.done:
			u.Cancelled.Add(1)
			return nil, nil, context.Canceled
		case <-time.After(10 * time.Second):
		}
		return text("done"), nil, nil
	})
	mcp.AddTool(s, &mcp.Tool{Name: "fail", Description: "Always reports an error."}, func(_ context.Context, _ *mcp.CallToolRequest, _ msgIn) (*mcp.CallToolResult, any, error) {
		count("fail")
		r := text("the tool failed")
		r.IsError = true
		return r, nil, nil
	})
	mcp.AddTool(s, &mcp.Tool{Name: "approve_me", Description: "Runs after a person approves."}, func(_ context.Context, _ *mcp.CallToolRequest, in msgIn) (*mcp.CallToolResult, any, error) {
		count("approve_me")
		return text("approved: " + in.Message), nil, nil
	})
	mcp.AddTool(s, &mcp.Tool{Name: "delete_all", Description: "Deletes everything."}, func(_ context.Context, _ *mcp.CallToolRequest, _ msgIn) (*mcp.CallToolResult, any, error) {
		count("delete_all")
		return text("deleted"), nil, nil
	})
	h := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return s },
		&mcp.StreamableHTTPOptions{Stateless: stateless, JSONResponse: true})
	u.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u.mu.Lock()
		u.headers = append(u.headers, r.Header.Clone())
		u.mu.Unlock()
		h.ServeHTTP(w, r)
	}))
	return u
}

// URL is the MCP endpoint.
func (u *HTTPUpstream) URL() string { return u.Server.URL + "/mcp" }

// Count is how many times a tool ran.
func (u *HTTPUpstream) Count(name string) int64 {
	if v, ok := u.Calls.Load(name); ok {
		return v.(*atomic.Int64).Load()
	}
	return 0
}

// Headers returns every request header set the upstream saw.
func (u *HTTPUpstream) Headers() []http.Header {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]http.Header(nil), u.headers...)
}

// SawHeader reports whether any request carried a header whose name starts with prefix
// (case-insensitive) or whose value contains needle.
func (u *HTTPUpstream) SawHeader(prefix, needle string) bool {
	for _, h := range u.Headers() {
		for k, vs := range h {
			if prefix != "" && strings.HasPrefix(strings.ToLower(k), strings.ToLower(prefix)) {
				return true
			}
			for _, v := range vs {
				if needle != "" && strings.Contains(v, needle) {
					return true
				}
			}
		}
	}
	return false
}

// Close stops the server.
func (u *HTTPUpstream) Close() {
	if u.cancel != nil {
		u.cancel()
	}
	u.Server.CloseClientConnections()
	u.Server.Close()
}
