package connector

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"runtime/debug"
	"sort"
	"sync"

	"github.com/jackc/pgx/v5"

	"github.com/deedglass/deedglass/internal/ids"
	"github.com/deedglass/deedglass/internal/store"
	"github.com/deedglass/deedglass/internal/vault"
)

// Executor runs a resolved tool. It is gate.Executor's shape.
type Executor interface {
	Execute(ctx context.Context, call Call) (Result, error)
}

// ArgsEnricher is an Executor that adds policy facts for a call before the decision (the REST
// connector's request.http). The Gate asks it with the call's arguments.
type ArgsEnricher interface {
	EnrichArgs(ctx context.Context, tool ToolSpec, args json.RawMessage) (map[string]any, error)
}

// Loader reads connector rows for the registry.
type Loader interface {
	// Head returns the live connector named name in org, or store.ErrNotFound.
	Head(ctx context.Context, org ids.OrgID, name string) (store.ConnectorHead, error)
	// Load returns the live connector with this id, or store.ErrNotFound.
	Load(ctx context.Context, org ids.OrgID, id string) (store.Connector, error)
}

// DBLoader is the Loader over Postgres: one short org read transaction per call.
type DBLoader struct{ DB *store.DB }

// Head implements Loader.
func (l DBLoader) Head(ctx context.Context, org ids.OrgID, name string) (store.ConnectorHead, error) {
	var h store.ConnectorHead
	err := l.DB.OrgReadTx(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		h, err = store.ConnectorHeadByName(ctx, tx, name)
		return err
	})
	return h, err
}

// Load implements Loader.
func (l DBLoader) Load(ctx context.Context, org ids.OrgID, id string) (store.Connector, error) {
	var c store.Connector
	err := l.DB.OrgReadTx(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		c, err = store.GetConnector(ctx, tx, id, false)
		return err
	})
	return c, err
}

// Registry holds the connector types, the built-ins (fake in dev) and a cache of per-org
// instances keyed by (connector id, config hash), so a config change takes effect on the next
// call while an unchanged connector keeps its parsed config and its egress client.
type Registry struct {
	mu       sync.RWMutex
	builtins map[string]Static
	types    map[string]Connector
	loader   Loader
	opener   vault.Opener
	env      Env
	log      *slog.Logger
	cache    sync.Map // instKey → *Instance
}

type instKey struct{ id, hash string }

// Options configures New.
type Options struct {
	Builtins []Connector  // connectors with one fixed instance named after the type (fake)
	Types    []Connector  // types whose instances are connectors rows (rest, …)
	Loader   Loader       // nil: only built-ins resolve
	Opener   vault.Opener // the execute path's decrypt; nil: every credential is missing
	Env      Env
	Logger   *slog.Logger
}

// New returns a registry.
func New(o Options) *Registry {
	r := &Registry{builtins: map[string]Static{}, types: map[string]Connector{}, loader: o.Loader, opener: o.Opener, env: o.Env, log: o.Logger}
	if r.log == nil {
		r.log = slog.Default()
	}
	for _, c := range o.Builtins {
		r.Register(c)
	}
	for _, c := range o.Types {
		r.RegisterType(c)
	}
	return r
}

// NewRegistry returns a registry of built-ins only (S13's shape, kept for tests and the MCP
// native source).
func NewRegistry(cs ...Connector) *Registry { return New(Options{Builtins: cs}) }

// Register adds a built-in. It panics on a bad or duplicate name: a misconfigured server must not
// start.
func (r *Registry) Register(c Connector) {
	s := Builtin(c)
	name := s.Name()
	if !ReConnector.MatchString(name) {
		panic(fmt.Sprintf("connector: %q is not a connector name", name))
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, dup := r.builtins[name]; dup {
		panic("connector: " + name + " is registered twice")
	}
	r.builtins[name] = s
}

// RegisterType adds a connector type whose instances are connectors rows.
func (r *Registry) RegisterType(c Connector) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, dup := r.types[c.Type()]; dup {
		panic("connector: type " + c.Type() + " is registered twice")
	}
	r.types[c.Type()] = c
}

// Names lists the built-in connector names, sorted.
func (r *Registry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.builtins))
	for n := range r.builtins {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// Builtins lists the built-ins, sorted by name.
func (r *Registry) Builtins() []Static {
	names := r.Names()
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Static, 0, len(names))
	for _, n := range names {
		out = append(out, r.builtins[n])
	}
	return out
}

// IsBuiltin reports whether name is a built-in connector (no row can take its name).
func (r *Registry) IsBuiltin(name string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	_, ok := r.builtins[name]
	return ok
}

// Type returns a registered connector type.
func (r *Registry) Type(t string) (Connector, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	c, ok := r.types[t]
	return c, ok
}

// Types lists the registered types, sorted.
func (r *Registry) Types() []Connector {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Connector, 0, len(r.types))
	for _, c := range r.types {
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Type() < out[j].Type() })
	return out
}

// Env is the registry's env.
func (r *Registry) Env() Env { return r.env }

// Instance returns the cached instance of row, building it on a miss.
func (r *Registry) Instance(org ids.OrgID, row store.Connector) (*Instance, error) {
	k := instKey{row.ID, row.ConfigHash}
	if v, ok := r.cache.Load(k); ok {
		return v.(*Instance), nil
	}
	id, err := ids.Parse[ids.ConnectorKind](row.ID)
	if err != nil {
		return nil, err
	}
	inst := NewInstance(id, org, row.Name, row.Type, row.Config, row.ConfigHash, r.env, r.opener)
	v, _ := r.cache.LoadOrStore(k, inst)
	return v.(*Instance), nil
}

// Forget drops a connector's cached instances (after an update or delete; the hash key already
// makes stale entries unreachable, this frees them).
func (r *Registry) Forget(id string) {
	r.cache.Range(func(k, _ any) bool {
		if k.(instKey).id == id {
			r.cache.Delete(k)
		}
		return true
	})
}

// Resolve finds the tool qualified ("orders.get") for org: a built-in, or one of the org's
// connectors. It returns ErrToolUnknown when nothing serves it (or it is Internal), and
// ErrConnectorUnavailable when the connector is disabled or its tools can't be listed.
func (r *Registry) Resolve(ctx context.Context, org ids.OrgID, qualified string) (Executor, ToolSpec, string, error) {
	conn, tool, ok := Split(qualified)
	if !ok {
		return nil, ToolSpec{}, "", ErrToolUnknown
	}
	r.mu.RLock()
	b, isBuiltin := r.builtins[conn]
	r.mu.RUnlock()
	if isBuiltin {
		for _, t := range b.Tools() {
			if t.Name == tool && !t.Internal {
				return b, t, b.Type(), nil
			}
		}
		return nil, ToolSpec{}, "", ErrToolUnknown
	}
	if r.loader == nil {
		return nil, ToolSpec{}, "", ErrToolUnknown
	}
	head, err := r.loader.Head(ctx, org, conn)
	if errors.Is(err, store.ErrNotFound) {
		return nil, ToolSpec{}, "", ErrToolUnknown
	}
	if err != nil {
		return nil, ToolSpec{}, "", err
	}
	impl, ok := r.Type(head.Type)
	if !ok || !ValidToolName(head.Type, tool) {
		if !ok {
			return nil, ToolSpec{}, "", fmt.Errorf("%w: type %s isn't served by this build", ErrConnectorUnavailable, head.Type)
		}
		return nil, ToolSpec{}, "", ErrToolUnknown
	}
	if head.Status != "active" {
		return nil, ToolSpec{}, "", fmt.Errorf("%w: the connector is %s", ErrConnectorUnavailable, head.Status)
	}
	inst, err := r.instanceFor(ctx, org, head)
	if err != nil {
		return nil, ToolSpec{}, "", err
	}
	tools, err := impl.Tools(ctx, inst)
	if err != nil {
		return nil, ToolSpec{}, "", fmt.Errorf("%w: %v", ErrConnectorUnavailable, err)
	}
	for _, t := range tools {
		if t.Name == tool && !t.Internal {
			return &bound{r: r, impl: impl, inst: inst}, t, head.Type, nil
		}
	}
	return nil, ToolSpec{}, "", ErrToolUnknown
}

func (r *Registry) instanceFor(ctx context.Context, org ids.OrgID, head store.ConnectorHead) (*Instance, error) {
	if v, ok := r.cache.Load(instKey{head.ID, head.ConfigHash}); ok {
		return v.(*Instance), nil
	}
	row, err := r.loader.Load(ctx, org, head.ID)
	if errors.Is(err, store.ErrNotFound) {
		return nil, ErrToolUnknown
	}
	if err != nil {
		return nil, err
	}
	return r.Instance(org, row)
}

// bound is one org's connector instance as an Executor.
type bound struct {
	r    *Registry
	impl Connector
	inst *Instance
}

// Instance returns the bound instance (tests and the Console's test path).
func (b *bound) Instance() *Instance { return b.inst }

// EnrichArgs implements ArgsEnricher when the type is an Enricher.
func (b *bound) EnrichArgs(ctx context.Context, t ToolSpec, args json.RawMessage) (map[string]any, error) {
	en, ok := b.impl.(Enricher)
	if !ok {
		return nil, nil
	}
	return en.Enrich(ctx, b.inst, t, args)
}

// Execute validates the arguments, runs the call with the per-call redactor in its context,
// recovers a panic (a stack in the log, upstream_error to the caller), and redacts and caps the
// output and every message.
func (b *bound) Execute(ctx context.Context, call Call) (res Result, err error) {
	if err := ValidateArgs(call.Tool.InputSchema, call.Args); err != nil {
		return Result{}, err
	}
	if !call.Deadline.IsZero() {
		var cancel context.CancelFunc
		ctx, cancel = context.WithDeadline(ctx, call.Deadline)
		defer cancel()
	}
	red := vault.NewRedactor()
	ctx = vault.WithRedactor(ctx, red)
	red = vault.RedactorFrom(ctx)
	defer func() {
		if p := recover(); p != nil {
			b.r.log.ErrorContext(ctx, "connector: a call panicked", "connector", b.inst.Name, "type", b.impl.Type(),
				"action_id", call.ActionID.String(), "panic", fmt.Sprint(p), "stack", string(debug.Stack()))
			res, err = Result{}, &Error{Kind: KindUpstream, Code: CodeUpstreamError,
				AgentMessage: "The connector stopped unexpectedly while it ran this tool. It may have run."}
		}
		res, err = scrubResult(red, res, err)
	}()
	return b.impl.Execute(ctx, b.inst, call)
}

// scrubResult redacts the output and the error's messages, and caps the output.
func scrubResult(red *vault.Redactor, res Result, err error) (Result, error) {
	if len(res.Output) > 0 {
		out := red.RedactBytes(res.Output)
		out = []byte(vault.Scrub(string(out)))
		if !json.Valid(out) {
			out = json.RawMessage(`{"redacted":"The tool's output held a credential and couldn't be kept."}`)
		}
		if len(out) > MaxOutput {
			return Result{}, ErrResponseTooLarge(MaxOutput)
		}
		res.Output = out
	}
	if ce := AsError(err); ce != nil {
		ce.AgentMessage = vault.Scrub(red.Redact(ce.AgentMessage))
		ce.AdminDetail = vault.Scrub(red.Redact(ce.AdminDetail))
	}
	return res, err
}
