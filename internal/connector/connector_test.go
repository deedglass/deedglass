package connector_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/deedglass/deedglass/internal/connector"
	"github.com/deedglass/deedglass/internal/connector/egress"
	"github.com/deedglass/deedglass/internal/connector/fake"
	"github.com/deedglass/deedglass/internal/ids"
	"github.com/deedglass/deedglass/internal/vault"
)

func TestNaming(t *testing.T) {
	for _, q := range []string{"orders.get", "orders.update_status", "fake.echo", "a1.x.y"} {
		if _, _, ok := connector.Split(q); !ok {
			t.Errorf("%s refused", q)
		}
	}
	for _, q := range []string{"orders", ".get", "orders.", "Orders.get", "o.get", strings.Repeat("a", 201)} {
		if _, _, ok := connector.Split(q); ok {
			t.Errorf("%q accepted", q)
		}
	}
	if !connector.ValidToolName("rest", "update_status") || connector.ValidToolName("rest", "readTextFile") || !connector.ValidToolName("mcp_http", "readTextFile") {
		t.Error("tool names")
	}
	if !connector.Reserved("fake", false) || connector.Reserved("fake", true) || !connector.Reserved("native", true) {
		t.Error("reserved")
	}
}

func TestRegistryBuiltins(t *testing.T) {
	reg := connector.NewRegistry(fake.New())
	if got := reg.Names(); len(got) != 1 || got[0] != "fake" || !reg.IsBuiltin("fake") || len(reg.Builtins()) != 1 {
		t.Fatalf("names %v", got)
	}
	ex, spec, typ, err := reg.Resolve(context.Background(), ids.OrgID{}, "fake.echo")
	if err != nil || typ != "fake" || spec.Name != "echo" {
		t.Fatal(err)
	}
	if _, err := ex.Execute(context.Background(), connector.Call{Tool: spec, Args: json.RawMessage(`{"message":"hi"}`)}); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{"fake.nope", "nobody.echo", "bad name"} {
		if _, _, _, err := reg.Resolve(context.Background(), ids.OrgID{}, q); !errors.Is(err, connector.ErrToolUnknown) {
			t.Errorf("%s: %v", q, err)
		}
	}
	defer func() {
		if recover() == nil {
			t.Error("a duplicate built-in registered")
		}
	}()
	reg.Register(fake.New())
}

func TestStatusMapping(t *testing.T) {
	cases := map[int]struct {
		kind connector.Kind
		code string
	}{
		401: {connector.KindAuth, "upstream_auth_failed"}, 403: {connector.KindAuth, "upstream_auth_failed"},
		404: {connector.KindNotFound, "upstream_not_found"}, 429: {connector.KindRateLimited, "upstream_rate_limited"},
		422: {connector.KindInvalidRequest, "upstream_error"}, 500: {connector.KindUpstream, "upstream_error"},
		503: {connector.KindUpstream, "upstream_error"},
	}
	for st, want := range cases {
		e := connector.FromStatus(st, 2*time.Second)
		if e.Kind != want.kind || e.Code != want.code || e.HTTPStatus != st || e.AgentMessage == "" {
			t.Errorf("%d: %+v", st, e)
		}
	}
	if m := connector.FromStatus(429, 2*time.Second).AgentMessage; !strings.Contains(m, "Wait 2s") {
		t.Error(m)
	}
	if connector.Bytes(4<<20) != "4 MiB" || connector.Bytes(512<<10) != "512 KiB" || connector.Bytes(900) != "900 bytes" {
		t.Error("bytes")
	}
	be := &egress.BlockedError{Host: "x", Addr: "169.254.169.254", Class: egress.ClassMetadata, Reason: "No."}
	if e := connector.ErrDestinationBlocked(be); strings.Contains(e.AgentMessage, "169.254") || !strings.Contains(e.AdminDetail, "169.254") {
		t.Error("destination messages")
	}
	for cause, want := range map[error]string{
		&vault.UnknownKEKError{KEKID: "kek:000000000000", Rows: 2}: "2 credentials use kek:000000000000",
		vault.ErrIntegrity: "vault_integrity",
		vault.ErrNoVault:   "DEEDGLASS_VAULT_KEY isn't set",
	} {
		if e := connector.ErrVaultUnavailable(cause); e.AgentMessage != connector.VaultAgentMessage || !strings.Contains(e.AdminDetail, want) {
			t.Errorf("%v: %+v", cause, e)
		}
	}
}

func TestTestReportSettle(t *testing.T) {
	r := connector.TestReport{Checks: []connector.Check{{Status: connector.CheckPass}, {Status: connector.CheckSkip}}}
	if r.Settle(); r.Status != connector.CheckPass {
		t.Error(r.Status)
	}
	r.Checks = append(r.Checks, connector.Check{Status: connector.CheckWarn})
	if r.Settle(); r.Status != connector.CheckWarn {
		t.Error(r.Status)
	}
	r.Checks = append(r.Checks, connector.Check{Status: connector.CheckFail})
	if r.Settle(); r.Status != connector.CheckFail {
		t.Error(r.Status)
	}
	r.Checks = nil
	if r.Settle(); r.Status != connector.CheckSkip {
		t.Error(r.Status)
	}
}
