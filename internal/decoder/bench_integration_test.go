//go:build integration

package decoder_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pglogrepl"
	"github.com/jackc/pgx/v5"

	"github.com/deedglass/deedglass/internal/decoder"
	"github.com/deedglass/deedglass/internal/ids"
)

// TestDecoderBudget is S20 §6's benchmark table (make bench-decoder): SafeParse's overhead over
// Parse, the assembler's rate, and on a real throwaway PG 18, 100 writes/s for 60 s: the poll round
// and commit → frame persisted. Raw numbers go to DEEDGLASS_DECODER_BENCH_OUT.
func TestDecoderBudget(t *testing.T) {
	if os.Getenv("DEEDGLASS_IT_BENCH") != "1" {
		t.Skip("set DEEDGLASS_IT_BENCH=1 (make bench-decoder)")
	}
	out := map[string]any{"date": time.Now().UTC().Format(time.RFC3339), "go": runtime.Version(), "goos": runtime.GOOS, "goarch": runtime.GOARCH,
		"cpus": runtime.NumCPU()}
	// 1. SafeParse vs Parse over the captured corpus.
	var corpus [][]byte
	for _, f := range mustGlob(t, filepath.Join("..", "..", "testdata", "decoder", "pgoutput", "*.bin")) {
		b, _ := os.ReadFile(f)
		corpus = append(corpus, b)
	}
	safe := testing.Benchmark(func(b *testing.B) {
		for i := 0; b.Loop(); i++ {
			_, _ = decoder.SafeParse(corpus[i%len(corpus)])
		}
	})
	raw := testing.Benchmark(func(b *testing.B) {
		for i := 0; b.Loop(); i++ {
			_, _ = pglogrepl.Parse(corpus[i%len(corpus)])
		}
	})
	over := float64(safe.NsPerOp() - raw.NsPerOp())
	out["safeparse_ns_per_msg"], out["parse_ns_per_msg"], out["safeparse_overhead_ns"] = safe.NsPerOp(), raw.NsPerOp(), over
	t.Logf("PERF SafeParse %d ns/msg, Parse %d ns/msg, overhead %.0f ns (target < 1,000)", safe.NsPerOp(), raw.NsPerOp(), over)
	if over > 1000 {
		t.Errorf("SafeParse overhead %.0f ns", over)
	}

	// 2. A real decoder at 100 writes/s for 60 s.
	r := newRig(t, rigOpts{dbName: "deedglass_it_dec_bench"})
	r.waitRunning()
	const rate, seconds = 100, 60
	type sent struct {
		id string
		at time.Time
	}
	var mu sync.Mutex
	var all []sent
	conns := make([]*pgx.Conn, 4)
	for i := range conns {
		c, err := pgx.Connect(bg, r.cust.AgentDSN)
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close(bg)
		conns[i] = c
	}
	jobs := make(chan int, rate*seconds)
	var wg sync.WaitGroup
	for w := range conns {
		wg.Go(func() {
			c := conns[w]
			for i := range jobs {
				id := ids.New[ids.ActionKind]().String()
				tx, err := c.Begin(bg)
				if err != nil {
					t.Error(err)
					return
				}
				if _, err := tx.Exec(bg, `SELECT * FROM deedglass.begin_action($1, $2, $3, NULL, $4)`, id, r.org.String(),
					ids.New[ids.AgentKind]().String(), digest0); err != nil {
					t.Error(err)
					return
				}
				if _, err := tx.Exec(bg, `UPDATE public.bulk SET flag = NOT flag WHERE id = $1`, 1+i%1204); err != nil {
					t.Error(err)
					return
				}
				if err := tx.Commit(bg); err != nil {
					t.Error(err)
					return
				}
				mu.Lock()
				all = append(all, sent{id, time.Now()})
				mu.Unlock()
			}
		})
	}
	tick := time.NewTicker(time.Second / rate)
	for i := range rate * seconds {
		<-tick.C
		jobs <- i
	}
	tick.Stop()
	close(jobs)
	wg.Wait()
	eventually(t, 60*time.Second, func() string {
		if n := r.count(`class = 'bound'`); n != len(all) {
			return fmt.Sprintf("%d of %d bound", n, len(all))
		}
		return ""
	})
	// Commit (client side, after COMMIT returned) → frame persisted (decoded_txs.created_at, the
	// control plane's clock on the same host).
	c := r.e.Admin(t)
	rows, err := c.Query(bg, `SELECT action_id, created_at FROM deedglass.decoded_txs WHERE connector_id = $1 AND class = 'bound'`, r.con.String())
	if err != nil {
		t.Fatal(err)
	}
	stored := map[string]time.Time{}
	for rows.Next() {
		var id string
		var at time.Time
		_ = rows.Scan(&id, &at)
		stored[id] = at
	}
	rows.Close()
	var lat []time.Duration
	for _, s := range all {
		if at, ok := stored[s.id]; ok {
			lat = append(lat, max(at.Sub(s.at), 0))
		}
	}
	p50, p99 := pct(lat, 50), pct(lat, 99)
	polls := r.sup.Runner(r.con.String()).View().Polls
	pp50, pp99 := pct(polls, 50), pct(polls, 99)
	out["writes"], out["rate_per_s"], out["seconds"] = len(all), rate, seconds
	out["commit_to_frame_ms"] = map[string]float64{"p50": ms(p50), "p90": ms(pct(lat, 90)), "p99": ms(p99), "max": ms(pct(lat, 100))}
	out["poll_round_ms"] = map[string]float64{"p50": ms(pp50), "p99": ms(pp99), "n": float64(len(polls))}
	t.Logf("PERF %d writes at %d/s: commit → frame persisted p50 %.1f ms, p99 %.1f ms (targets 250, 1,000); poll round p50 %.1f ms, p99 %.1f ms (target p50 < 20)",
		len(all), rate, ms(p50), ms(p99), ms(pp50), ms(pp99))
	if p50 > 250*time.Millisecond || p99 > time.Second {
		t.Errorf("commit → frame persisted p50 %v, p99 %v", p50, p99)
	}
	if pp50 > 20*time.Millisecond {
		t.Errorf("poll round p50 %v", pp50)
	}
	if path := os.Getenv("DEEDGLASS_DECODER_BENCH_OUT"); path != "" {
		b, _ := json.MarshalIndent(out, "", "  ")
		if err := os.WriteFile(path, append(b, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func mustGlob(t *testing.T, pattern string) []string {
	t.Helper()
	m, err := filepath.Glob(pattern)
	if err != nil || len(m) == 0 {
		t.Fatalf("no files at %s", pattern)
	}
	return m
}

func pct(d []time.Duration, p int) time.Duration {
	if len(d) == 0 {
		return 0
	}
	s := append([]time.Duration{}, d...)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	i := (len(s) - 1) * p / 100
	return s[i]
}

func ms(d time.Duration) float64 { return float64(d.Microseconds()) / 1000 }

var _ = strings.Contains
