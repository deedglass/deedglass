//go:build integration

package vault_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/deedglass/deedglass/internal/deed"
	"github.com/deedglass/deedglass/internal/ids"
	"github.com/deedglass/deedglass/internal/ledger"
	"github.com/deedglass/deedglass/internal/store"
	"github.com/deedglass/deedglass/internal/store/storetest"
	"github.com/deedglass/deedglass/internal/vault"
)

const sentinel = "dgSENTINEL7Q2vK9xT4mB8nR3"

var ctx = context.Background()

func key(b byte) string { return base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{b}, 32)) }

type world struct {
	e   *storetest.Env
	led *ledger.Ledger
	log *bytes.Buffer
}

func newWorld(t *testing.T) *world {
	t.Helper()
	e := storetest.New(t, storetest.Options{MaxConns: 16})
	seed, err := deed.NewSeed(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := deed.NewFileSigner(ids.New[ids.SealKeyKind]().String(), seed)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.DB.RegisterSealKey(ctx, signer.KeyID(), signer.PublicKey()); err != nil {
		t.Fatal(err)
	}
	return &world{e: e, led: ledger.New(e.DB, deed.NewSealer(signer), signer, ledger.WithDev(true)), log: &bytes.Buffer{}}
}

func (w *world) store(t *testing.T, ring string) *vault.Store {
	t.Helper()
	r, err := vault.NewRing(ring)
	if err != nil {
		t.Fatal(err)
	}
	return vault.NewStore(w.e.DB, r, vault.StoreOptions{Ledger: w.led, Logger: slog.New(vault.NewRedactingHandler(slog.NewTextHandler(w.log, nil)))})
}

// org creates an org without the seed rows (whose credential uses an unknown key on purpose).
func (w *world) org(t *testing.T, slug string) ids.OrgID {
	t.Helper()
	o, _, _, err := w.e.DB.CreateOrg(ctx, store.NewOrg{Name: "Org " + slug, Slug: slug, OwnerEmail: "owner@" + slug + ".example.test"})
	if err != nil {
		t.Fatal(err)
	}
	return o.ID
}

func (w *world) connector(t *testing.T, org ids.OrgID, name string) ids.ConnectorID {
	t.Helper()
	id := ids.New[ids.ConnectorKind]()
	if err := w.e.DB.OrgTx(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
		return store.InsertConnector(ctx, tx, store.Connector{ID: id.String(), OrgID: org.String(), Type: "rest", Name: name,
			Config: []byte(`{}`), ConfigHash: "sha256:" + strings.Repeat("0", 64), Status: "active", CreatedBy: "operator"})
	}); err != nil {
		t.Fatal(err)
	}
	return id
}

var actor = vault.Actor{Ref: deed.Ref{Type: "system", ID: "operator"}, By: "operator"}

// TestStoreLifecycle: put → open → replace → delete, with RLS between orgs and no value anywhere
// in the row but its ciphertext.
func TestStoreLifecycle(t *testing.T) {
	w := newWorld(t)
	vs := w.store(t, key(1))
	a, b := w.org(t, "vault-a"), w.org(t, "vault-b")
	con := w.connector(t, a, "orders")
	ref := vault.Ref{Org: a, Connector: con}
	if _, err := vs.Open(ctx, ref); !errors.Is(err, vault.ErrNotFound) {
		t.Fatalf("open before put: %v", err)
	}
	m, err := vs.Put(ctx, ref, vault.KindBearer, map[string][]byte{"token": []byte(sentinel)}, actor)
	if err != nil {
		t.Fatal(err)
	}
	if !m.Set || m.Version != 1 || m.Hint != "…"+sentinel[len(sentinel)-4:] || m.Deed == nil || !strings.HasPrefix(m.KEKID, "kek:") {
		t.Fatalf("meta %+v", m)
	}
	s, err := vs.Open(ctx, ref)
	if err != nil || s.Field("token") != sentinel {
		t.Fatalf("open: %v", err)
	}
	s.Destroy()
	// Replace: version 2; the old data key is gone (the row holds only the new one).
	m2, err := vs.Put(ctx, ref, vault.KindBearer, map[string][]byte{"token": []byte(sentinel + "-v2")}, actor)
	if err != nil || m2.Version != 2 {
		t.Fatalf("replace %+v %v", m2, err)
	}
	if s, err := vs.Open(ctx, ref); err != nil || s.Field("token") != sentinel+"-v2" {
		t.Fatalf("open v2: %v", err)
	}
	// RLS: org B can't see org A's credential, even with A's connector id.
	if _, err := vs.Open(ctx, vault.Ref{Org: b, Connector: con}); !errors.Is(err, vault.ErrNotFound) {
		t.Fatalf("org B opened org A's credential: %v", err)
	}
	// Nothing but ciphertext anywhere: the row, admin_events and deeds hold no value.
	admin := w.e.Admin(t)
	for _, q := range []string{
		`SELECT count(*) FROM deedglass.credentials WHERE position($1::bytea in ciphertext) > 0 OR position($1::bytea in wrapped_dek) > 0 OR hint LIKE '%' || $2 || '%'`,
		`SELECT count(*) FROM deedglass.admin_events WHERE $1::bytea IS NOT NULL AND data::text LIKE '%' || $2 || '%'`,
		`SELECT count(*) FROM deedglass.deeds WHERE $2::text IS NOT NULL AND position($1::bytea in envelope) > 0`,
	} {
		var n int
		if err := admin.QueryRow(ctx, q, []byte(sentinel), sentinel).Scan(&n); err != nil || n != 0 {
			t.Fatalf("%d rows hold the sentinel (%v): %s", n, err, q)
		}
	}
	// Delete crypto-shreds it and seals a deed.
	d, err := vs.Delete(ctx, ref, actor)
	if err != nil || !d.Deleted || d.Deed == nil {
		t.Fatalf("delete %+v %v", d, err)
	}
	if _, err := vs.Open(ctx, ref); !errors.Is(err, vault.ErrNotFound) {
		t.Fatalf("open after delete: %v", err)
	}
	if strings.Contains(w.log.String(), sentinel) {
		t.Fatal("the log holds the sentinel")
	}
}

// TestRowMovedFailsLoudly: a row copied into another org's connector fails to open with the
// vault_integrity alert and no plaintext.
func TestRowMovedFailsLoudly(t *testing.T) {
	w := newWorld(t)
	vs := w.store(t, key(1))
	a, b := w.org(t, "move-a"), w.org(t, "move-b")
	ca, cb := w.connector(t, a, "orders"), w.connector(t, b, "orders")
	if _, err := vs.Put(ctx, vault.Ref{Org: a, Connector: ca}, vault.KindBearer, map[string][]byte{"token": []byte(sentinel)}, actor); err != nil {
		t.Fatal(err)
	}
	admin := w.e.Admin(t)
	if _, err := admin.Exec(ctx, `INSERT INTO deedglass.credentials (org_id, connector_id, version, kind, field_names, hint, kek_id, wrapped_dek, ciphertext, updated_by)
	    SELECT $1, $2, version, kind, field_names, hint, kek_id, wrapped_dek, ciphertext, updated_by FROM deedglass.credentials WHERE connector_id = $3`,
		b.String(), cb.String(), ca.String()); err != nil {
		t.Fatal(err)
	}
	s, err := vs.Open(ctx, vault.Ref{Org: b, Connector: cb})
	if !errors.Is(err, vault.ErrIntegrity) || s != nil {
		t.Fatalf("a moved row opened: %v", err)
	}
	if !strings.Contains(w.log.String(), "alert=vault_integrity") {
		t.Fatalf("no alert:\n%s", w.log)
	}
	// An edited version fails the same way.
	if _, err := admin.Exec(ctx, `UPDATE deedglass.credentials SET version = version + 1 WHERE connector_id = $1`, ca.String()); err != nil {
		t.Fatal(err)
	}
	if _, err := vs.Open(ctx, vault.Ref{Org: a, Connector: ca}); !errors.Is(err, vault.ErrIntegrity) {
		t.Fatalf("an edited version opened: %v", err)
	}
}

// TestRotation: 2 orgs × 50 credentials, interrupted after 30 and re-run, end on the active key,
// each still opening; two rotators at once never double-wrap; an unknown key stops it, named.
func TestRotation(t *testing.T) {
	w := newWorld(t)
	old := w.store(t, key(1))
	var refs []vault.Ref
	for _, slug := range []string{"rot-a", "rot-b"} {
		org := w.org(t, slug)
		for i := range 50 {
			con := w.connector(t, org, fmt.Sprintf("api-%02d", i))
			ref := vault.Ref{Org: org, Connector: con}
			if _, err := old.Put(ctx, ref, vault.KindBearer, map[string][]byte{"token": []byte(fmt.Sprintf("%s-%s-%02d", sentinel, slug, i))}, actor); err != nil {
				t.Fatal(err)
			}
			refs = append(refs, ref)
		}
	}
	both := w.store(t, key(2)+","+key(1))
	st, err := both.Status(ctx)
	if err != nil || st.Total != 100 || st.Pending != 100 || st.ByKEK[st.Ring[1]] != 100 {
		t.Fatalf("status %+v %v", st, err)
	}
	// Interrupt after 30.
	cctx, cancel := context.WithCancel(ctx)
	_, err = both.Rotate(cctx, func(done, total int) {
		if done >= 30 {
			cancel()
		}
	})
	cancel()
	if err == nil {
		t.Log("the first run finished before the interruption took effect")
	}
	mid, _ := both.Status(ctx)
	if mid.Pending == 0 || mid.Pending == 100 {
		t.Logf("after the interruption %d are pending", mid.Pending)
	}
	// Two rotators at once finish the rest.
	var wg sync.WaitGroup
	results := make([]vault.RotateResult, 2)
	errs := make([]error, 2)
	for i := range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i], errs[i] = w.store(t, key(2)+","+key(1)).Rotate(ctx, nil)
		}()
	}
	wg.Wait()
	for _, e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	end, _ := both.Status(ctx)
	if end.Pending != 0 || end.ByKEK[end.Active] != 100 {
		t.Fatalf("after rotation %+v", end)
	}
	// Every credential still opens with the new key alone; nothing was double-wrapped.
	newOnly := w.store(t, key(2))
	for _, ref := range refs {
		if s, err := newOnly.Open(ctx, ref); err != nil || !strings.HasPrefix(s.Field("token"), sentinel) {
			t.Fatalf("%v: %v", ref, err)
		}
	}
	if results[0].Rewrapped+results[1].Rewrapped != mid.Pending {
		t.Errorf("rewrapped %d + %d, %d were pending", results[0].Rewrapped, results[1].Rewrapped, mid.Pending)
	}
	r, err := both.Rotate(ctx, nil)
	if err != nil || r.Rewrapped != 0 || len(r.Unused) != 1 {
		t.Fatalf("a no-op rotation: %+v %v", r, err)
	}
	// An unknown key stops the rotation and names it and its row count.
	_, err = w.store(t, key(3)).Rotate(ctx, nil)
	var uk *vault.UnknownKEKError
	if !errors.As(err, &uk) || uk.Rows != 100 || !strings.Contains(uk.Error(), "100 credentials use "+end.Active+", which isn't in DEEDGLASS_VAULT_KEY") {
		t.Fatalf("unknown key: %v", err)
	}
	if strings.Contains(w.log.String(), sentinel) {
		t.Fatal("the log holds the sentinel")
	}
}
