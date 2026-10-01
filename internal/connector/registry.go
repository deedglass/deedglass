package connector

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"

	"github.com/deedglass/deedglass/internal/ids"
)

// Connector is one in-process connector type with its tools. S16's framework adds credentials,
// per-org instances (con_ ids) and the egress door; this is the subset the Gate needs today.
type Connector interface {
	// Name is the connector slug that prefixes its tools: "fake" in "fake.echo".
	Name() string
	// Type is the connector type from S10's enum (postgres, rest, github, …, fake).
	Type() string
	// Tools lists the tools it serves.
	Tools() []ToolSpec
	// Execute runs one call. It must respect ctx and return a *Error for typed failures.
	Execute(ctx context.Context, call Call) (Result, error)
}

// Executor runs a resolved tool. It is gate.Executor's shape.
type Executor interface {
	Execute(ctx context.Context, call Call) (Result, error)
}

// Registry is the small in-process registry of S13. S16's Registry.Resolve(ctx, org, qualified)
// replaces it behind the same Resolve signature, so the Gate doesn't change.
type Registry struct {
	mu    sync.RWMutex
	conns map[string]Connector
}

// NewRegistry returns a registry with cs registered.
func NewRegistry(cs ...Connector) *Registry {
	r := &Registry{conns: map[string]Connector{}}
	for _, c := range cs {
		r.Register(c)
	}
	return r
}

// reConnector is the connector-slug grammar (S16's naming rules).
var reConnector = regexp.MustCompile(`^[a-z][a-z0-9-]{1,31}$`)

// Register adds c. It panics on a bad or duplicate slug: a misconfigured server must not start.
func (r *Registry) Register(c Connector) {
	name := c.Name()
	if !reConnector.MatchString(name) {
		panic(fmt.Sprintf("connector: %q is not a connector name", name))
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, dup := r.conns[name]; dup {
		panic("connector: " + name + " is registered twice")
	}
	r.conns[name] = c
}

// Names lists the registered connector slugs, sorted.
func (r *Registry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.conns))
	for n := range r.conns {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// Resolve finds the tool qualified ("fake.echo") for org. It returns ErrToolUnknown when no
// connector or tool matches. The org is unused in S13 (one in-process registry for every org).
func (r *Registry) Resolve(_ context.Context, _ ids.OrgID, qualified string) (Executor, ToolSpec, string, error) {
	conn, tool, ok := strings.Cut(qualified, ".")
	if !ok {
		return nil, ToolSpec{}, "", ErrToolUnknown
	}
	r.mu.RLock()
	c, found := r.conns[conn]
	r.mu.RUnlock()
	if !found {
		return nil, ToolSpec{}, "", ErrToolUnknown
	}
	for _, t := range c.Tools() {
		if t.Name == tool {
			return c, t, c.Type(), nil
		}
	}
	return nil, ToolSpec{}, "", ErrToolUnknown
}
