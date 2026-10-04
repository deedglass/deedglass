package identity

import (
	"bytes"
	"encoding/json"
	"flag"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/deedglass/deedglass/internal/httpx"
)

var updateAgentGolden = flag.Bool("update-agent-golden", false, "rewrite testdata/agent_object.golden.json")

func fixedAgent() Agent {
	at := httpx.Time(time.Date(2026, 10, 3, 10, 2, 11, 0, time.UTC))
	reason, by, owner := "drill", "usr_01k6c9kb2zcjsyn7b8g2k1vqvp", "usr_01k6c9kb2zcjsyn7b8g2k1vqvp"
	perMin, perDay := int64(60), int64(5000)
	return Agent{Object: "agent", ID: "agt_01k633rrmh3bcz2p39gg4vp46q", Name: "support-bot", Description: "Answers refund tickets",
		Labels:  map[string]string{"env": "prod", "team": "support"},
		Budgets: Budgets{CallsPerMinute: &perMin, CallsPerDay: &perDay, SpendPerDay: &httpx.Money{Amount: 5000, Currency: "usd"}},
		Status:  AgentSuspended, StatusReason: &reason, StatusChangedAt: &at, StatusChangedBy: &by, Owner: &owner, ActiveKeys: 2,
		IntentMode: IntentOptional, LastUsedAt: &at, CreatedAt: at, UpdatedAt: at}
}

// TestAgentObjectGolden pins the agent object's bytes: without ?expand[]=usage the payload is
// exactly what it was before S25 (testdata/agent_object.golden.json; -update-agent-golden).
func TestAgentObjectGolden(t *testing.T) {
	got, err := json.MarshalIndent(fixedAgent(), "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	got = append(got, '\n')
	path := filepath.Join("testdata", "agent_object.golden.json")
	if *updateAgentGolden {
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("the agent object changed:\n%s\nwant:\n%s", got, want)
	}
	for _, k := range []string{`"usage"`, `"last_action_at"`, `"calls_today"`} {
		if bytes.Contains(got, []byte(k)) {
			t.Errorf("the plain agent object carries %s", k)
		}
	}
	// The expansion is the same object plus usage and last_action_at, at the end.
	ex, err := json.MarshalIndent(AgentWithUsage{Agent: fixedAgent(), Usage: AgentUsage{CallsMinute: 1, CallsHour: 2, CallsToday: 3}}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	prefix := bytes.TrimSuffix(bytes.TrimSpace(want), []byte("\n}"))
	if !bytes.HasPrefix(ex, prefix) {
		t.Errorf("the expansion reorders or changes the agent's fields:\n%s", ex)
	}
	var m map[string]any
	_ = json.Unmarshal(ex, &m)
	u, _ := m["usage"].(map[string]any)
	if u == nil || u["calls_minute"] != 1.0 || u["calls_hour"] != 2.0 || u["calls_today"] != 3.0 || u["spend_today"] != nil {
		t.Errorf("usage %v", m["usage"])
	}
	if v, ok := m["last_action_at"]; !ok || v != nil {
		t.Errorf("last_action_at must be present and null: %v", v)
	}
}

// TestParseExpandUsage: expand[]=usage or expand=usage; anything else is a validation error on
// the parameter sent.
func TestParseExpandUsage(t *testing.T) {
	for _, c := range []struct {
		q     string
		usage bool
		param string
	}{
		{"", false, ""},
		{"?expand[]=usage", true, ""},
		{"?expand=usage", true, ""},
		{"?expand[]=usage&expand[]=usage", true, ""},
		{"?expand[]=keys", false, "expand[]"},
		{"?expand=usage&expand=witness.detail", false, "expand"},
		{"?expand[]=", false, "expand[]"},
	} {
		r := httptest.NewRequest("GET", "/v1/agents"+c.q, nil)
		usage, err := parseExpandUsage(r)
		var he *httpx.Error
		switch {
		case c.param == "" && (err != nil || usage != c.usage):
			t.Errorf("%s: %v %v", c.q, usage, err)
		case c.param != "" && (err == nil || !asHTTP(err, &he) || he.Code != httpx.CodeValidationFailed || he.Param != c.param):
			t.Errorf("%s: %v, want validation_failed on %s", c.q, err, c.param)
		}
	}
}

func asHTTP(err error, he **httpx.Error) bool {
	e, ok := err.(*httpx.Error)
	*he = e
	return ok
}
