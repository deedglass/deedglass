package connector_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/deedglass/deedglass/internal/connector"
	"github.com/deedglass/deedglass/internal/ids"
	"github.com/deedglass/deedglass/internal/store"
	"github.com/deedglass/deedglass/internal/vault"
)

// readerStub is a type that reads back: it echoes a credential-like value in its body, or panics.
type readerStub struct {
	typeStub
	panics bool
}

func (s readerStub) ReadBack(ctx context.Context, _ *connector.Instance, call connector.Call, _ connector.Result) (connector.Observation, error) {
	if s.panics {
		panic("boom")
	}
	vault.RedactorFrom(ctx).Add("s3cr3t-value-of-sixteen")
	return connector.Observation{Body: json.RawMessage(`{"echo":"s3cr3t-value-of-sixteen","args":` + string(call.Args) + `}`), Status: 200}, nil
}

func boundFor(t *testing.T, c connector.Connector) connector.ReadBacking {
	t.Helper()
	org := ids.New[ids.OrgKind]()
	row := store.Connector{ID: ids.New[ids.ConnectorKind]().String(), OrgID: org.String(), Name: "rows", Type: c.Type(), Status: "active",
		Config: json.RawMessage(`{}`), ConfigHash: "sha256:" + strings.Repeat("1", 64)}
	reg := connector.New(connector.Options{Types: []connector.Connector{c}, Loader: rowsLoader{rows: []store.Connector{row}}})
	ex, _, _, err := reg.Resolve(context.Background(), org, "rows.query")
	if err != nil {
		t.Fatal(err)
	}
	return ex.(connector.ReadBacking)
}

func TestBoundReadBack(t *testing.T) {
	spec := connector.ToolSpec{Name: "query", SideEffect: connector.SideEffectRead,
		InputSchema: json.RawMessage(`{"type":"object","additionalProperties":false,"properties":{"id":{"type":"string","pattern":"^[0-9]+$"}}}`)}
	// A type that can't read back says so; nothing runs.
	plain := boundFor(t, typeStub{typ: "plain"})
	var skip *connector.ErrReadBackSkipped
	if plain.CanReadBack() {
		t.Fatal("typeStub can't read back")
	}
	if _, err := plain.ReadBack(context.Background(), connector.Call{Tool: spec, Args: json.RawMessage(`{}`)}, connector.Result{}); !errors.As(err, &skip) {
		t.Fatalf("%v", err)
	}
	rb := boundFor(t, readerStub{typeStub: typeStub{typ: "reader"}})
	if !rb.CanReadBack() {
		t.Fatal("readerStub reads back")
	}
	// The read tool's arguments are validated again: a template can't smuggle what the schema refuses.
	if _, err := rb.ReadBack(context.Background(), connector.Call{Tool: spec, Args: json.RawMessage(`{"id":"../admin"}`)}, connector.Result{}); connector.AsError(err) == nil {
		t.Fatalf("validation: %v", err)
	}
	// The body is redacted with the call's redactor.
	obs, err := rb.ReadBack(context.Background(), connector.Call{Tool: spec, Args: json.RawMessage(`{"id":"42"}`)}, connector.Result{})
	if err != nil || strings.Contains(string(obs.Body), "s3cr3t-value-of-sixteen") || !strings.Contains(string(obs.Body), vault.RedactedText) {
		t.Fatalf("%s %v", obs.Body, err)
	}
	// A panic is recovered as an upstream error.
	p := boundFor(t, readerStub{typeStub: typeStub{typ: "panicky"}, panics: true})
	if _, err := p.ReadBack(context.Background(), connector.Call{Tool: spec, Args: json.RawMessage(`{}`)}, connector.Result{}); connector.AsError(err) == nil {
		t.Fatalf("panic: %v", err)
	}
}
