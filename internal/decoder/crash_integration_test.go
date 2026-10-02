//go:build integration

package decoder_test

import (
	"fmt"
	"runtime"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/deedglass/deedglass/internal/decoder"
	"github.com/deedglass/deedglass/internal/deed"
	"github.com/deedglass/deedglass/internal/ids"
)

// The tests that crash Postgres or change settings that need a restart run on a private cluster
// the test starts itself (Homebrew binaries; skipped where there are none, as in CI's Docker
// mode). The control plane stays on the shared throwaway cluster.

// E02: the slot advances, then Postgres crashes before its next checkpoint, so the slot's position
// rewinds (the position is written out at checkpoint). The same transaction is delivered again and
// stored once.
func TestRewindAfterCrash(t *testing.T) {
	c := newCluster(t)
	r := newRig(t, rigOpts{adminURL: c.admin, dbName: "deedglass_it_dec_rewind"})
	r.waitRunning()
	id := r.act(`UPDATE public.orders SET status = 'shipped' WHERE id = 60`)
	r.waitFrame(id, "bound")
	before := r.confirmedFlush()
	c.crash(t)
	after := r.confirmedFlush()
	t.Logf("confirmed_flush_lsn before the crash %s, after %s", before, after)
	id2 := r.act(`UPDATE public.orders SET status = 'shipped' WHERE id = 61`)
	r.waitFrame(id2, "bound")
	if n := len(r.framesFor(id)); n != 1 {
		t.Fatalf("%d frames for the action delivered twice", n)
	}
	if s := r.slot(); s.Frames["bound"] != 2 {
		t.Fatalf("counters %v", s.Frames)
	}
}

func (r *rig) confirmedFlush() string {
	r.t.Helper()
	var l string
	eventually(r.t, 30*time.Second, func() string {
		c, err := pgx.Connect(bg, r.cust.AdminURL)
		if err != nil {
			return err.Error()
		}
		defer c.Close(bg)
		if err := c.QueryRow(bg, `SELECT confirmed_flush_lsn::text FROM pg_catalog.pg_replication_slots WHERE slot_name = $1`,
			r.slot().SlotName).Scan(&l); err != nil {
			return err.Error()
		}
		return ""
	})
	return l
}

// E05: Postgres is killed before COMMIT: nothing is stored, and reconcile finds no ledger row.
func TestKilledBeforeCommit(t *testing.T) {
	c := newCluster(t)
	r := newRig(t, rigOpts{adminURL: c.admin, dbName: "deedglass_it_dec_e05"})
	r.waitRunning()
	conn, err := pgx.Connect(bg, r.cust.AgentDSN)
	if err != nil {
		t.Fatal(err)
	}
	id := ids.New[ids.ActionKind]().String()
	tx, _ := conn.Begin(bg)
	if _, err := tx.Exec(bg, `SELECT * FROM deedglass.begin_action($1, $2, $3, NULL, $4)`, id, r.org.String(), ids.New[ids.AgentKind]().String(),
		digest0); err != nil {
		t.Fatal(err)
	}
	_, _ = tx.Exec(bg, `UPDATE public.orders SET status = 'shipped' WHERE id = 62`)
	c.crash(t)
	_ = conn.Close(bg)
	next := r.act(`UPDATE public.orders SET status = 'shipped' WHERE id = 63`)
	r.waitFrame(next, "bound")
	r.sup.Runner(r.con.String()).ReconcileNow()
	time.Sleep(time.Second)
	if n := len(r.framesFor(id)); n != 0 {
		t.Fatalf("%d frames for a transaction that never committed", n)
	}
}

const digest0 = "sha256:0000000000000000000000000000000000000000000000000000000000000000"

// E07: a prepared transaction is decoded at COMMIT PREPARED (one bound frame); one rolled back
// with ROLLBACK PREPARED leaves nothing.
func TestPreparedTransactions(t *testing.T) {
	c := newCluster(t, "max_prepared_transactions = 4")
	r := newRig(t, rigOpts{adminURL: c.admin, dbName: "deedglass_it_dec_e07"})
	r.waitRunning()
	prepare := func(gid string, order int) string {
		conn, err := pgx.Connect(bg, r.cust.AgentDSN)
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close(bg)
		id := ids.New[ids.ActionKind]().String()
		for _, q := range []string{"BEGIN"} {
			if _, err := conn.Exec(bg, q); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := conn.Exec(bg, `SELECT * FROM deedglass.begin_action($1, $2, $3, NULL, $4)`, id, r.org.String(),
			ids.New[ids.AgentKind]().String(), digest0); err != nil {
			t.Fatal(err)
		}
		if _, err := conn.Exec(bg, fmt.Sprintf(`UPDATE public.orders SET status = 'shipped' WHERE id = %d`, order)); err != nil {
			t.Fatal(err)
		}
		if _, err := conn.Exec(bg, fmt.Sprintf(`PREPARE TRANSACTION '%s'`, gid)); err != nil {
			t.Fatal(err)
		}
		return id
	}
	kept := prepare("dg_it_keep", 64)
	dropped := prepare("dg_it_drop", 65)
	time.Sleep(300 * time.Millisecond)
	if n := len(r.framesFor(kept)); n != 0 {
		t.Fatalf("a prepared, uncommitted transaction was stored (%d frames)", n)
	}
	r.admin(`COMMIT PREPARED 'dg_it_keep'`)
	r.admin(`ROLLBACK PREPARED 'dg_it_drop'`)
	r.waitFrame(kept, "bound")
	sentinel := r.act(`UPDATE public.orders SET status = 'shipped' WHERE id = 66`)
	r.waitFrame(sentinel, "bound")
	if n := len(r.framesFor(dropped)); n != 0 {
		t.Fatalf("%d frames for the rolled-back prepared transaction", n)
	}
}

// E03: the decoder is stopped, the slot falls behind max_slot_wal_keep_size and Postgres
// invalidates it: the runner halts (slot_invalidated) and signals SlotLost; after a drop and a new
// slot, reconcile records the actions in the gap as missing.
func TestSlotInvalidated(t *testing.T) {
	c := newCluster(t, "max_slot_wal_keep_size = 16MB", "max_wal_size = 32MB", "min_wal_size = 32MB")
	r := newRig(t, rigOpts{adminURL: c.admin, dbName: "deedglass_it_dec_e03"})
	r.waitRunning()
	first := r.act(`UPDATE public.orders SET status = 'shipped' WHERE id = 70`)
	r.waitFrame(first, "bound")
	r.stop()
	var gap []string
	for i := range 2 {
		gap = append(gap, r.act(`UPDATE public.orders SET status = 'shipped' WHERE id = $1`, 71+i))
	}
	r.admin(`CREATE TABLE public.noise (id int, pad text)`)
	eventually(t, 120*time.Second, func() string {
		r.admin(`INSERT INTO public.noise SELECT g, pg_catalog.repeat('x', 1000) FROM generate_series(1, 20000) AS g`)
		r.admin(`SELECT pg_catalog.pg_switch_wal()`)
		r.admin(`CHECKPOINT`)
		conn, err := pgx.Connect(bg, r.cust.AdminURL)
		if err != nil {
			return err.Error()
		}
		defer conn.Close(bg)
		var ws string
		_ = conn.QueryRow(bg, `SELECT wal_status FROM pg_catalog.pg_replication_slots WHERE slot_name = $1`, r.slot().SlotName).Scan(&ws)
		if ws != "lost" {
			return "wal_status " + ws
		}
		return ""
	})
	r.start()
	eventually(t, 30*time.Second, func() string {
		if s := r.slot(); s.Status != decoder.StatusHalted || deref(s.StatusReason) != decoder.ReasonSlotInvalidated {
			return "status " + s.Status + "/" + deref(s.StatusReason)
		}
		return ""
	})
	if _, _, lost := r.hooks.snapshot(); !slices.Contains(lost, decoder.ReasonSlotInvalidated) {
		t.Fatalf("SlotLost %v", lost)
	}
	if _, err := r.sup.Drop(bg, r.org, r.con, deed.Ref{Type: "system", ID: "operator"}, ""); err != nil {
		t.Fatal(err)
	}
	r.waitRunning()
	after := r.act(`UPDATE public.orders SET status = 'shipped' WHERE id = 79`)
	r.waitFrame(after, "bound")
	r.sup.Runner(r.con.String()).ReconcileNow()
	for _, g := range gap {
		r.waitFrame(g, "missing")
	}
}

// E08: an unattributed 200,000-row UPDATE decoded with logical_decoding_work_mem = 64kB (Postgres
// spills it to disk): one frame, exact counts, 1,000 images, images_truncated, and the decoder's
// heap grows by less than 64 MB.
func TestHugeTransaction(t *testing.T) {
	r := newRig(t, rigOpts{noStart: true})
	r.admin(`INSERT INTO public.bulk (id) SELECT g FROM generate_series(200001, 398796) AS g`) // 198,796 + 1,204 = 200,000 rows
	r.admin(fmt.Sprintf(`ALTER DATABASE %s SET logical_decoding_work_mem = '64kB'`, r.cust.Name))
	r.start()
	r.waitRunning()
	var peak atomic.Uint64
	var base runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&base)
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Go(func() {
		for {
			var m runtime.MemStats
			runtime.ReadMemStats(&m)
			if m.HeapInuse > peak.Load() {
				peak.Store(m.HeapInuse)
			}
			select {
			case <-stop:
				return
			case <-time.After(20 * time.Millisecond):
			}
		}
	})
	r.admin(`UPDATE public.bulk SET flag = NOT flag`)
	var total int
	eventually(t, 120*time.Second, func() string {
		for _, f := range r.frames() {
			if f.Class == "unattributed" {
				total = f.ChangeCount
				if f.ChangeCount == 200000 && slices.Contains(f.Flags, "images_truncated") {
					return ""
				}
			}
		}
		return fmt.Sprintf("no 200,000-change frame yet (%d)", total)
	})
	close(stop)
	wg.Wait()
	growth := int64(peak.Load()) - int64(base.HeapInuse)
	t.Logf("heap in use: %d MB before, peak %d MB (growth %d MB)", base.HeapInuse>>20, peak.Load()>>20, growth>>20)
	if growth > 64<<20 {
		t.Fatalf("the decoder's heap grew by %d MB", growth>>20)
	}
	if n := r.count(`class = 'unattributed'`); n != 1 {
		t.Fatalf("%d frames", n)
	}
}
