package connector_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/deedglass/deedglass/internal/connector"
	"github.com/deedglass/deedglass/internal/ids"
	"github.com/deedglass/deedglass/internal/store"
)

// rowsLoader is an in-memory Loader and Lister.
type rowsLoader struct{ rows []store.Connector }

func (l rowsLoader) Head(_ context.Context, _ ids.OrgID, name string) (store.ConnectorHead, error) {
	for _, r := range l.rows {
		if r.Name == name {
			return store.ConnectorHead{ID: r.ID, Type: r.Type, Status: r.Status, ConfigHash: r.ConfigHash}, nil
		}
	}
	return store.ConnectorHead{}, store.ErrNotFound
}

func (l rowsLoader) Load(_ context.Context, _ ids.OrgID, id string) (store.Connector, error) {
	for _, r := range l.rows {
		if r.ID == id {
			return r, nil
		}
	}
	return store.Connector{}, store.ErrNotFound
}

func (l rowsLoader) List(_ context.Context, _ ids.OrgID, max int) ([]store.Connector, error) {
	if len(l.rows) > max {
		return l.rows[:max], nil
	}
	return l.rows, nil
}

// typeStub is a connector type with fixed tools; failing makes Tools fail.
type typeStub struct {
	typ     string
	failing bool
}

func (s typeStub) Type() string             { return s.typ }
func (s typeStub) Info() connector.TypeInfo { return connector.TypeInfo{Type: s.typ} }
func (s typeStub) Validate(context.Context, connector.Env, json.RawMessage) (json.RawMessage, []connector.FieldError) {
	return nil, nil
}
func (s typeStub) Tools(context.Context, *connector.Instance) ([]connector.ToolSpec, error) {
	if s.failing {
		return nil, errors.New("down")
	}
	return []connector.ToolSpec{{Name: "query", SideEffect: connector.SideEffectRead}}, nil
}
func (s typeStub) Execute(context.Context, *connector.Instance, connector.Call) (connector.Result, error) {
	return connector.Result{}, nil
}
func (s typeStub) Test(context.Context, *connector.Instance) (connector.TestReport, error) {
	return connector.TestReport{}, nil
}

func row(name, typ, status string) store.Connector {
	return store.Connector{ID: ids.New[ids.ConnectorKind]().String(), Name: name, Type: typ, Status: status, Config: json.RawMessage(`{}`), ConfigHash: "sha256:x"}
}

// TestNativeRowsAndNamed (S18): /mcp/native lists only active rows of native types this build
// serves; Named finds a row by name without resolving a tool.
func TestNativeRowsAndNamed(t *testing.T) {
	rows := []store.Connector{
		row("postgres", "postgres", "active"),
		row("orders", "rest", "active"),
		row("old", "rest", "disabled"),
		row("fs", "mcp_http", "active"),
		row("broken", "github", "active"), // a native type whose tools fail to list
		row("future", "slack", "active"),  // a native type this build doesn't serve
	}
	reg := connector.New(connector.Options{Loader: rowsLoader{rows},
		Types: []connector.Connector{typeStub{typ: "postgres"}, typeStub{typ: "rest"}, typeStub{typ: "mcp_http"}, typeStub{typ: "github", failing: true}}})
	org := ids.New[ids.OrgKind]()
	got, err := reg.NativeRows(context.Background(), org)
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, r := range got {
		names[r.Name] = true
		if len(r.Tools) != 1 {
			t.Errorf("%s: %d tools", r.Name, len(r.Tools))
		}
	}
	if len(got) != 2 || !names["postgres"] || !names["orders"] {
		t.Fatalf("native rows: %v", names)
	}
	inst, impl, err := reg.Named(context.Background(), org, "fs")
	if err != nil || impl.Type() != "mcp_http" || inst.Name != "fs" {
		t.Fatalf("named fs: %v", err)
	}
	if _, _, err := reg.Named(context.Background(), org, "nobody"); !errors.Is(err, connector.ErrToolUnknown) {
		t.Fatalf("unknown: %v", err)
	}
	if _, _, err := reg.Named(context.Background(), org, "old"); !errors.Is(err, connector.ErrConnectorUnavailable) {
		t.Fatalf("disabled: %v", err)
	}
	if _, _, err := reg.Named(context.Background(), org, "future"); !errors.Is(err, connector.ErrConnectorUnavailable) {
		t.Fatalf("unserved type: %v", err)
	}
	if _, _, err := reg.Named(context.Background(), org, "Bad Name"); !errors.Is(err, connector.ErrToolUnknown) {
		t.Fatalf("bad name: %v", err)
	}
	// A registry without a Lister lists nothing.
	if rs, err := connector.New(connector.Options{}).NativeRows(context.Background(), org); err != nil || rs != nil {
		t.Fatalf("no loader: %v %v", rs, err)
	}
}
