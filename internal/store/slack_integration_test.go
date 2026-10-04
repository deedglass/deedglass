//go:build integration

package store_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/deedglass/deedglass/internal/ids"
	"github.com/deedglass/deedglass/internal/store"
	"github.com/deedglass/deedglass/internal/store/storetest"
)

// TestSlackApprovalsStore: routing (S29), the replay table, the card rows and their version rule,
// and row-level security on both new tables.
func TestSlackApprovalsStore(t *testing.T) {
	e := storetest.New(t, storetest.Options{})
	a := storetest.NewOrg(t, e.DB, "slack-a")
	b := storetest.NewOrg(t, e.DB, "slack-b")
	slack := func(org storetest.Org, team, app string, enabled bool) (string, error) {
		id := ids.New[ids.ConnectorKind]().String()
		cfg, _ := json.Marshal(map[string]any{"team_id": team, "app_id": app, "approvals": map[string]any{"enabled": enabled, "channel": "#deedglass-approvals"}})
		err := e.DB.OrgTx(ctx, org.ID, func(ctx context.Context, tx pgx.Tx) error {
			return store.InsertConnector(ctx, tx, store.Connector{ID: id, OrgID: org.ID.String(), Type: "slack", Name: "slack-" + id[len(id)-6:],
				Config: cfg, ConfigHash: storetest.Digest([]byte(id)), Status: "active", CreatedBy: "test"})
		})
		return id, err
	}
	conA, err := slack(a, "T0AAAAAAA", "A0AAAAAAA", true)
	if err != nil {
		t.Fatal(err)
	}
	pre := e.DB.PreAuth()
	org, con, ok, err := pre.SlackRoute(ctx, "T0AAAAAAA", "A0AAAAAAA")
	if err != nil || !ok || org != a.ID || con != conA {
		t.Fatalf("route: %v %v %v %v", org, con, ok, err)
	}
	for _, c := range [][2]string{{"T0AAAAAAA", "A0ZZZZZZZ"}, {"t0aaaaaaa", "A0AAAAAAA"}, {"T0AAAAAAA' OR '1'='1", "A0AAAAAAA"}, {"", ""}} {
		if _, _, ok, err := pre.SlackRoute(ctx, c[0], c[1]); ok || err != nil {
			t.Errorf("route %q: ok %v err %v", c, ok, err)
		}
	}
	// One live connector per Slack app (any org), and one per org.
	if _, err := slack(b, "T0AAAAAAA", "A0AAAAAAA", true); !errors.Is(err, store.ErrSlackAppTaken) {
		t.Fatalf("second org, same app: %v", err)
	}
	if _, err := slack(a, "T0BBBBBBB", "A0BBBBBBB", true); !errors.Is(err, store.ErrSlackApprovalsTaken) {
		t.Fatalf("second approvals connector in one org: %v", err)
	}
	if _, err := slack(b, "T0AAAAAAA", "A0AAAAAAA", false); err != nil {
		t.Fatalf("approvals off is free to share the app: %v", err)
	}
	// A disabled connector doesn't route.
	if err := e.DB.OrgTx(ctx, a.ID, func(ctx context.Context, tx pgx.Tx) error {
		c, err := store.GetConnector(ctx, tx, conA, true)
		if err != nil {
			return err
		}
		c.Status = "disabled"
		return store.UpdateConnector(ctx, tx, c)
	}); err != nil {
		t.Fatal(err)
	}
	if _, _, ok, _ := pre.SlackRoute(ctx, "T0AAAAAAA", "A0AAAAAAA"); ok {
		t.Fatal("a disabled connector routes")
	}

	// Replay: one digest once per org; another org has its own space; expired ones are pruned.
	dg := storetest.Digest([]byte("v0=abc"))
	exp := time.Now().Add(-time.Minute)
	for i, want := range []bool{true, false} {
		var fresh bool
		if err := e.DB.OrgTx(ctx, a.ID, func(ctx context.Context, tx pgx.Tx) error {
			var err error
			fresh, err = store.RecordInbound(ctx, tx, a.ID, "slack", dg, exp)
			return err
		}); err != nil || fresh != want {
			t.Fatalf("record %d: fresh %v err %v", i, fresh, err)
		}
	}
	if err := e.DB.OrgTx(ctx, b.ID, func(ctx context.Context, tx pgx.Tx) error {
		fresh, err := store.RecordInbound(ctx, tx, b.ID, "slack", dg, time.Now().Add(time.Hour))
		if err == nil && !fresh {
			t.Error("org B saw org A's digest")
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if n, err := pre.PruneInboundReplay(ctx, 1000); err != nil || n < 1 {
		t.Fatalf("prune: %d %v", n, err)
	}

	// Cards: insert once, the version only moves up, failure is recorded, org B sees nothing.
	m := store.NotificationMessage{Org: a.ID, ApprovalID: a.Approval.String(), ActionID: a.Action.String(), ConnectorID: conA,
		ChannelID: "C0APPROVALS", TS: "1760069640.000100", Version: 1}
	err = e.DB.OrgTx(ctx, a.ID, func(ctx context.Context, tx pgx.Tx) error {
		if ok, err := store.InsertNotificationMessage(ctx, tx, m); err != nil || !ok {
			t.Fatalf("insert: %v %v", ok, err)
		}
		if ok, _ := store.InsertNotificationMessage(ctx, tx, m); ok {
			t.Fatal("a second card for the same approval and channel")
		}
		all, err := store.NotificationMessagesFor(ctx, tx, a.ID, a.Approval.String())
		var ms []store.NotificationMessage
		for _, x := range all {
			if x.ChannelID == "C0APPROVALS" {
				ms = append(ms, x)
			}
		}
		if err != nil || len(ms) != 1 || ms[0].ConnectorID != conA || ms[0].State != "posted" {
			t.Fatalf("list: %v %v", all, err)
		}
		if ok, _ := store.MarkNotificationShown(ctx, tx, a.ID, ms[0].ID, 4); !ok {
			t.Fatal("up refused")
		}
		if ok, _ := store.MarkNotificationShown(ctx, tx, a.ID, ms[0].ID, 3); ok {
			t.Fatal("down accepted")
		}
		if err := store.MarkNotificationFailed(ctx, tx, a.ID, ms[0].ID, "upstream_error: Slack failed."); err != nil {
			t.Fatal(err)
		}
		has, err := store.HasNotificationMessage(ctx, tx, a.ID, a.Action.String())
		if err != nil || !has {
			t.Fatalf("has: %v %v", has, err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := e.DB.OrgReadTx(ctx, b.ID, func(ctx context.Context, tx pgx.Tx) error {
		ms, err := store.NotificationMessagesFor(ctx, tx, a.ID, a.Approval.String())
		if err == nil && len(ms) != 0 {
			t.Errorf("org B read org A's cards: %v", ms)
		}
		has, _ := store.HasNotificationMessage(ctx, tx, a.ID, a.Action.String())
		if has {
			t.Error("org B sees org A's card")
		}
		if _, err := store.ApprovalCard(ctx, tx, a.ID, a.Approval.String()); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("org B read org A's approval: %v", err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	// The card read and the member lookup.
	if err := e.DB.OrgReadTx(ctx, a.ID, func(ctx context.Context, tx pgx.Tx) error {
		r, err := store.ApprovalCard(ctx, tx, a.ID, a.Approval.String())
		if err != nil || r.Status != "pending" {
			t.Fatalf("card: %+v %v", r, err)
		}
		if id, err := store.ApprovalOfAction(ctx, tx, a.ID, r.ActionID); err != nil || id != a.Approval.String() {
			t.Fatalf("approval of action: %v %v", id, err)
		}
		if u, err := store.MemberByEmail(ctx, tx, a.ID, " OWNER@slack-a.example.test "); err != nil || u != a.Owner {
			t.Fatalf("member: %v %v", u, err)
		}
		if _, err := store.MemberByEmail(ctx, tx, a.ID, "owner@slack-b.example.test"); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("another org's member: %v", err)
		}
		if _, err := store.SlackApprovalsConnector(ctx, tx, a.ID); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("the disabled connector still receives approvals: %v", err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
