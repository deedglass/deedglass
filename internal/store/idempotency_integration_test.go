//go:build integration

package store_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/deedglass/deedglass/internal/ids"
	"github.com/deedglass/deedglass/internal/store"
	"github.com/deedglass/deedglass/internal/store/storetest"
)

func newOrg(t *testing.T, db *store.DB, slug string) ids.OrgID {
	t.Helper()
	o, _, _, err := db.CreateOrg(context.Background(), store.NewOrg{Name: slug, Slug: slug, OwnerEmail: slug + "@example.com"})
	if err != nil {
		t.Fatal(err)
	}
	return o.ID
}

const fpA = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
const fpB = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

func TestIdempotencyStore(t *testing.T) {
	e := storetest.New(t, storetest.Options{})
	ctx := context.Background()
	db := e.DB
	org := newOrg(t, db, "idem-a")
	org2 := newOrg(t, db, "idem-b")
	agent := ids.New[ids.AgentKind]().String()
	k := store.IdemKey{OrgID: org, Principal: agent, Key: "K1", Method: "POST", Route: "/v1/agents"}

	out, _, err := db.IdemBegin(ctx, k, fpA, 35*time.Second)
	if err != nil || out != store.IdemStarted {
		t.Fatalf("first begin: %v %v", out, err)
	}
	if out, _, _ := db.IdemBegin(ctx, k, fpA, 35*time.Second); out != store.IdemInProgress {
		t.Fatalf("second begin while running: %v", out)
	}
	if out, _, _ := db.IdemBegin(ctx, k, fpB, 35*time.Second); out != store.IdemMismatch {
		t.Fatalf("different fingerprint: %v", out)
	}
	resp := store.IdemResponse{Status: 201, Headers: map[string]string{"Content-Type": "application/json; charset=utf-8"}, Body: []byte(`{"object":"agent"}`)}
	if err := db.IdemComplete(ctx, k, resp); err != nil {
		t.Fatal(err)
	}
	out, got, err := db.IdemBegin(ctx, k, fpA, 35*time.Second)
	if err != nil || out != store.IdemReplay || got.Status != 201 || string(got.Body) != string(resp.Body) || got.Headers["Content-Type"] != resp.Headers["Content-Type"] {
		t.Fatalf("replay: %v %+v %v", out, got, err)
	}

	// Another principal, and another org, with the same key string are independent.
	k2 := k
	k2.Principal = ids.New[ids.AgentKind]().String()
	if out, _, _ := db.IdemBegin(ctx, k2, fpB, time.Second); out != store.IdemStarted {
		t.Fatalf("other principal: %v", out)
	}
	k3 := k
	k3.OrgID = org2
	if out, _, _ := db.IdemBegin(ctx, k3, fpB, time.Second); out != store.IdemStarted {
		t.Fatalf("other org: %v", out)
	}

	// Release forgets an in-progress key; it never deletes a completed one.
	if err := db.IdemRelease(ctx, k2); err != nil {
		t.Fatal(err)
	}
	if out, _, _ := db.IdemBegin(ctx, k2, fpA, time.Second); out != store.IdemStarted {
		t.Fatalf("after release: %v", out)
	}
	_ = db.IdemRelease(ctx, k)
	if out, _, _ := db.IdemBegin(ctx, k, fpA, time.Second); out != store.IdemReplay {
		t.Fatal("release removed a completed key")
	}

	// An expired lock is taken over; an expired key is new.
	admin := e.Admin(t)
	kl := store.IdemKey{OrgID: org, Principal: agent, Key: "LOCK", Method: "POST", Route: "/v1/agents"}
	db.IdemBegin(ctx, kl, fpA, 35*time.Second)
	if _, err := admin.Exec(ctx, `UPDATE deedglass.idempotency_keys SET locked_until = now() - interval '1 second' WHERE key = 'LOCK'`); err != nil {
		t.Fatal(err)
	}
	if out, _, _ := db.IdemBegin(ctx, kl, fpA, 35*time.Second); out != store.IdemStarted {
		t.Fatalf("takeover: %v", out)
	}
	if _, err := admin.Exec(ctx, `UPDATE deedglass.idempotency_keys SET created_at = now() - interval '25 hours', expires_at = now() - interval '1 hour' WHERE key = 'K1' AND principal = $1`, agent); err != nil {
		t.Fatal(err)
	}
	if out, _, _ := db.IdemBegin(ctx, k, fpB, time.Second); out != store.IdemStarted {
		t.Fatalf("25 h old key must be new: %v", out)
	}

	// Prune deletes only expired keys, across orgs.
	if _, err := admin.Exec(ctx, `UPDATE deedglass.idempotency_keys SET expires_at = now() - interval '1 minute' WHERE org_id = $1`, org2.String()); err != nil {
		t.Fatal(err)
	}
	n, err := db.PruneIdempotencyKeys(ctx)
	if err != nil || n != 1 {
		t.Fatalf("prune: %d %v", n, err)
	}
	var left int
	admin.QueryRow(ctx, `SELECT count(*) FROM deedglass.idempotency_keys`).Scan(&left)
	if left != 3 {
		t.Fatalf("%d keys left, want 3", left)
	}

	// RLS: a key is invisible from another org's transaction.
	if out, _, _ := db.IdemBegin(ctx, store.IdemKey{OrgID: org2, Principal: agent, Key: "K1", Method: "POST", Route: "/v1/agents"}, fpA, time.Second); out != store.IdemStarted {
		t.Fatalf("org2 must not see org1's key: %v", out)
	}
}

func TestIdempotencyStoreConcurrent(t *testing.T) {
	e := storetest.New(t, storetest.Options{MaxConns: 12})
	ctx := context.Background()
	org := newOrg(t, e.DB, "idem-c")
	k := store.IdemKey{OrgID: org, Principal: ids.New[ids.AgentKind]().String(), Key: "C", Method: "POST", Route: "/v1/agents"}
	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		started int
		inProg  int
	)
	for range 10 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			out, _, err := e.DB.IdemBegin(ctx, k, fpA, 35*time.Second)
			if err != nil {
				t.Error(err)
				return
			}
			mu.Lock()
			defer mu.Unlock()
			switch out {
			case store.IdemStarted:
				started++
			case store.IdemInProgress:
				inProg++
			}
		}()
	}
	wg.Wait()
	if started != 1 || inProg != 9 {
		t.Fatalf("started %d, in progress %d; want 1 and 9", started, inProg)
	}
}
