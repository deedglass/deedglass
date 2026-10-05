//go:build integration

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/deedglass/deedglass/internal/connector/postgres/pgtest"
	"github.com/deedglass/deedglass/internal/decoder"
	"github.com/deedglass/deedglass/internal/store/storetest"
	"github.com/deedglass/deedglass/internal/witness"
	"github.com/deedglass/deedglass/internal/witness/l2"
)

// TestL2Budget is S21 §3.12's benchmark table (make bench-l2): Derive, Compare on a frame with
// 1,000 images and on a 200,000-row frame (counts only), the diff render, and, through a real
// deedglass serve, the real Gate, the Postgres connector and the decoder, 100 writes/s for 60 s:
// commit → verdict, from the frame's commit_time to the deed's issued_at (one cluster, one clock).
// The contract is p50 < 1 s; S21 aims for p50 < 300 ms and p99 < 1.5 s. Raw numbers go to
// DEEDGLASS_L2_BENCH_OUT.
func TestL2Budget(t *testing.T) {
	if os.Getenv("DEEDGLASS_IT_BENCH") != "1" {
		t.Skip("set DEEDGLASS_IT_BENCH=1 (make bench-l2)")
	}
	out := map[string]any{"date": time.Now().UTC().Format(time.RFC3339), "go": runtime.Version(), "goos": runtime.GOOS, "goarch": runtime.GOARCH,
		"cpus": runtime.NumCPU()}
	pure(t, out)

	admin := pgtest.AdminURL(t)
	unlock, err := pgtest.Lock(context.Background(), admin)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	cust := pgtest.NewDB(t, admin, "deedglass_it_l2bench")
	e := storetest.New(t, storetest.Options{NoMigrate: true})
	keyFile, _ := newKeyFile(t, t.TempDir(), 22)
	env := map[string]string{"DEEDGLASS_DATABASE_URL": e.AppURL, "DEEDGLASS_MIGRATE_DATABASE_URL": e.MigratorURL,
		"DEEDGLASS_LISTEN": "127.0.0.1:0", "DEEDGLASS_LOG_LEVEL": "warn", "DEEDGLASS_SEAL_KEY_FILE": keyFile,
		"DEEDGLASS_API_KEY_PEPPER": key32, "DEEDGLASS_VAULT_KEY": key32, "DEEDGLASS_ALLOW_PRIVATE_NETWORKS": "true"}
	s := startServe(t, env, "--migrate")
	var base string
	select {
	case a := <-s.addr:
		base = "http://" + a.String()
	case c := <-s.code:
		t.Fatalf("exited %d\n%s%s", c, s.out, s.err)
	case <-time.After(30 * time.Second):
		t.Fatal("serve never listened")
	}
	defer func() {
		s.sigs <- syscall.SIGTERM
		s.wait(t)
		if c, err := pgx.Connect(context.Background(), cust.AdminURL); err == nil {
			_, _ = c.Exec(context.Background(), `SELECT pg_catalog.pg_drop_replication_slot(slot_name) FROM pg_catalog.pg_replication_slots
			    WHERE database = pg_catalog.current_database() AND NOT active`)
			_ = c.Close(context.Background())
		}
	}()
	env["DEEDGLASS_BASE_URL"], env["DEEDGLASS_SERVER"] = base, base
	run := func(args ...string) result {
		t.Helper()
		r := cli(t, env, false, args...)
		if r.code != 0 {
			t.Fatalf("deedglass %s: %+v", strings.Join(args, " "), r)
		}
		return r
	}
	var created struct {
		AdminKey struct{ Secret string } `json:"admin_key"`
	}
	r := run("org", "create", "--name", "Acme Robotics", "--slug", "acme", "--owner-email", "priya@acme.test", "--admin-key", "--json")
	if json.Unmarshal([]byte(r.stdout), &created) != nil {
		t.Fatal(r.stdout)
	}
	env["DEEDGLASS_API_KEY"] = created.AdminKey.Secret
	pf := filepath.Join(t.TempDir(), "postgres-l2.yaml")
	_ = os.WriteFile(pf, []byte(l2Policy), 0o600)
	run("policy", "push", pf)
	run("policy", "activate", "postgres-l2", "--version", "1", "--mode", "enforce", "--reason", "S21 benchmark", "--yes")
	client := &http.Client{Timeout: 60 * time.Second, Transport: &http.Transport{MaxIdleConnsPerHost: 256}}
	call := func(key, method, path, body string) (int, string) {
		req, _ := http.NewRequestWithContext(context.Background(), method, base+path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+key)
		req.Header.Set("Content-Type", "application/json")
		res, err := client.Do(req)
		if err != nil {
			return 0, err.Error()
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return res.StatusCode, string(b)
	}
	code, body := call(created.AdminKey.Secret, "POST", "/v1/agents", `{"name":"bench-bot"}`)
	var agent struct{ ID string }
	if code != 201 || json.Unmarshal([]byte(body), &agent) != nil {
		t.Fatalf("%d %s", code, body)
	}
	code, body = call(created.AdminKey.Secret, "POST", "/v1/agents/"+agent.ID+"/keys", `{}`)
	var ak struct{ Secret string }
	if code != 201 || json.Unmarshal([]byte(body), &ak) != nil {
		t.Fatalf("%d %s", code, body)
	}
	cf := filepath.Join(t.TempDir(), "postgres.json")
	// 100 writes/s, each with its statement and its row read-back on the connector's pool: the pool
	// gets 16 connections (the connector's maximum) instead of the default 4.
	cfg := strings.Replace(pgtest.ConfigJSON, `{"allowed_schemas"`, `{"max_connections":16,"allowed_schemas"`, 1)
	_ = os.WriteFile(cf, []byte(`{"type":"postgres","name":"postgres","description":"Acme (bench)","config":`+cfg+`}`), 0o600)
	run("connector", "create", "--config", cf)
	env["DEEDGLASS_INIT_DSN"] = cust.AdminURL
	run("connector", "postgres", "init", "--connector", "postgres", "--plan-dir", t.TempDir(), "--apply", "--rotate-passwords")
	delete(env, "DEEDGLASS_INIT_DSN")
	deadline := time.Now().Add(60 * time.Second)
	for {
		var v decoder.View
		_ = json.Unmarshal([]byte(cli(t, env, false, "decoder", "status", "--connector", "postgres", "--json").stdout), &v)
		if v.Status == decoder.StatusRunning {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the decoder never ran")
		}
		time.Sleep(200 * time.Millisecond)
	}

	// 100 writes/s for 60 s: single-row updates of public.bulk (1,204 rows), sent on schedule,
	// without waiting on the answer (wait 0): the measurement is in the database.
	rate := 100
	if n, err := strconv.Atoi(os.Getenv("DEEDGLASS_L2_BENCH_RATE")); err == nil && n > 0 {
		rate = n // S35's witness suite runs 10, 100 and 500 writes/s; the contract target is at 100
	}
	seconds := 60
	if n, err := strconv.Atoi(os.Getenv("DEEDGLASS_L2_BENCH_SECONDS")); err == nil && n > 0 {
		seconds = n // a shorter run while tuning; the recorded benchmark is 60 s
	}
	start := time.Now()
	var wg sync.WaitGroup
	var mu sync.Mutex
	var fails []string
	for i := 0; i < rate*seconds; i++ {
		at := start.Add(time.Duration(i) * time.Second / time.Duration(rate))
		time.Sleep(time.Until(at))
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			flag := (i/1204)%2 == 0
			sql := fmt.Sprintf("UPDATE public.bulk SET flag = %t WHERE id = %d", flag, 1+i%1204)
			code, body := call(ak.Secret, "POST", "/v1/actions", `{"tool":"postgres.execute","args":{"sql":"`+sql+`"},"wait":0}`)
			if code != 200 && code != 202 {
				mu.Lock()
				fails = append(fails, fmt.Sprintf("%d %s", code, clipOut(body)))
				mu.Unlock()
			}
		}(i)
	}
	wg.Wait()
	sendFor := time.Since(start)
	if len(fails) > 0 {
		t.Fatalf("%d sends failed, first: %s", len(fails), fails[0])
	}
	ctl := e.Admin(t)
	deadline = time.Now().Add(3 * time.Minute)
	var sealed int
	for {
		_ = ctl.QueryRow(context.Background(), `SELECT count(*) FROM deedglass.actions WHERE tool = 'postgres.execute' AND state = 'sealed'`).Scan(&sealed)
		if sealed >= rate*seconds || time.Now().After(deadline) {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if sealed < rate*seconds {
		if rate <= 100 {
			t.Fatalf("only %d of %d actions sealed", sealed, rate*seconds)
		}
		t.Errorf("only %d of %d actions sealed in 3 minutes at %d/s; the numbers below cover the sealed ones", sealed, rate*seconds, rate) // S35: 500/s is reported with its achieved rate
	}
	rows, err := ctl.Query(context.Background(), `SELECT a.witness_level, a.witness_verdict,
	    EXTRACT(EPOCH FROM (d.issued_at - f.commit_time)) * 1000,
	    EXTRACT(EPOCH FROM (f.created_at - f.commit_time)) * 1000,
	    EXTRACT(EPOCH FROM (d.issued_at - f.created_at)) * 1000
	  FROM deedglass.actions AS a
	  JOIN deedglass.deeds AS d ON d.org_id = a.org_id AND d.action_id = a.id AND d.kind = 'action'
	  JOIN deedglass.decoded_txs AS f ON f.action_id = a.id AND f.class = 'bound'
	 WHERE a.tool = 'postgres.execute'`)
	if err != nil {
		t.Fatal(err)
	}
	var lat, decode, after []float64
	verdicts := map[string]int{}
	for rows.Next() {
		var level, verdict string
		var ms, dms, ams float64
		if err := rows.Scan(&level, &verdict, &ms, &dms, &ams); err != nil {
			t.Fatal(err)
		}
		verdicts[level+" "+verdict]++
		lat, decode, after = append(lat, ms), append(decode, dms), append(after, ams)
	}
	rows.Close()
	sort.Float64s(lat)
	sort.Float64s(decode)
	sort.Float64s(after)
	q := func(xs []float64, p float64) float64 { return xs[min(len(xs)-1, int(p*float64(len(xs))))] }
	t.Logf("PERF commit → frame persisted: p50 %.1f ms, p99 %.1f ms; frame persisted → deed: p50 %.1f ms, p99 %.1f ms",
		q(decode, 0.5), q(decode, 0.99), q(after, 0.5), q(after, 0.99))
	out["segments_ms"] = map[string]float64{"decode_p50": q(decode, 0.5), "decode_p99": q(decode, 0.99), "witness_p50": q(after, 0.5), "witness_p99": q(after, 0.99)}
	pct := func(p float64) float64 { return lat[min(len(lat)-1, int(p*float64(len(lat))))] }
	if len(lat) == 0 {
		t.Fatal("no measurements")
	}
	p50, p90, p99 := pct(0.50), pct(0.90), pct(0.99)
	over := 0
	for _, x := range lat {
		if x > 1500 {
			over++
		}
	}
	out["l2"] = map[string]any{"rate_per_s": rate, "seconds": seconds, "sent_in_s": sendFor.Seconds(), "actions": len(lat), "over_1500_ms": over,
		"verdicts": verdicts, "commit_to_verdict_ms": map[string]float64{"p50": p50, "p90": p90, "p99": p99, "max": lat[len(lat)-1]}}
	t.Logf("PERF L2 commit → verdict at %d writes/s for %d s (%d actions): p50 %.1f ms, p90 %.1f ms, p99 %.1f ms, max %.1f ms (contract p50 < 1,000; aim p50 < 300, p99 < 1,500)",
		rate, seconds, len(lat), p50, p90, p99, lat[len(lat)-1])
	t.Logf("PERF verdicts %v", verdicts)
	warns := map[string]int{}
	for _, l := range strings.Split(s.err.String(), "\n") {
		if i := strings.Index(l, `"msg":"`); i >= 0 {
			m := l[i+7:]
			if j := strings.Index(m, `"`); j >= 0 {
				warns[m[:j]]++
			}
		} else if i := strings.Index(l, "msg="); i >= 0 {
			m := l[i+4:]
			if j := strings.Index(m, "\" "); j > 0 {
				m = m[:j]
			}
			warns[clip(m, 100)]++
		}
	}
	t.Logf("PERF server warnings: %v", warns)
	shown := 0
	for _, l := range strings.Split(s.err.String(), "\n") {
		if strings.Contains(l, "decoder:") && shown < 12 {
			t.Logf("serve: %s", clip(l, 400))
			shown++
		}
	}
	if p50 >= 1000 && rate <= 100 {
		t.Errorf("L2 p50 %.1f ms ≥ 1 s (contract §12)", p50)
	}
	if verdicts["L2 VERIFIED"] != len(lat) {
		t.Errorf("not every write was L2 · VERIFIED: %v", verdicts)
	}
	if path := os.Getenv("DEEDGLASS_L2_BENCH_OUT"); path != "" {
		b, _ := json.MarshalIndent(out, "", "  ")
		if err := os.WriteFile(path, append(b, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// pure is the in-process half of the table: Derive, Compare and the render.
func pure(t *testing.T, out map[string]any) {
	sp := func(s string) *string { return &s }
	info := l2.TableInfo{RelID: 16400, PK: []string{"id"}, Identity: "f", Columns: []string{"id", "status", "total_cents"}, TypeOIDs: []uint32{23, 25, 23}}
	mk := func(images, total int) *l2.Frame {
		f := &l2.Frame{Class: l2.ClassBound, SystemID: "1", Timeline: 1, CommitLSN: "0/1", CommitTime: time.Now(), Tables: map[string]l2.TableInfo{"public.orders": info},
			Counts: map[string]l2.OpCounts{"public.orders": {Update: total}}, Total: total}
		for i := 0; i < images; i++ {
			id := fmt.Sprint(i + 1)
			f.Changes = append(f.Changes, l2.Change{Seq: i + 1, Op: "U", Table: "public.orders", Key: map[string]*string{"id": sp(id)},
				Old: map[string]l2.Value{"id": {T: sp(id)}, "status": {T: sp("paid")}, "total_cents": {T: sp("100")}},
				New: map[string]l2.Value{"id": {T: sp(id)}, "status": {T: sp("shipped")}, "total_cents": {T: sp("100")}}, Changed: []string{"status"}})
		}
		if images < total {
			f.Flags = []string{"images_truncated"}
		}
		return f
	}
	one := int64(1)
	in := l2.DeriveInput{Tool: l2.ToolExecute, SQL: "UPDATE public.orders SET status = 'shipped' WHERE id = 42", Reported: &one,
		PK: map[string][]string{"public.orders": {"id"}}}
	fp, _ := l2.Derive(in)
	f1000, f200k := mk(1000, 1000), mk(0, 200000)
	res := map[string]testing.BenchmarkResult{
		"derive": testing.Benchmark(func(b *testing.B) {
			for b.Loop() {
				l2.Derive(in)
			}
		}),
		"compare_1000": testing.Benchmark(func(b *testing.B) {
			for b.Loop() {
				l2.Compare(fp, f1000, l2.Config{})
			}
		}),
		"compare_200k": testing.Benchmark(func(b *testing.B) {
			for b.Loop() {
				l2.Compare(fp, f200k, l2.Config{})
			}
		}),
		"diff_build_1000": testing.Benchmark(func(b *testing.B) {
			for b.Loop() {
				l2.BuildDiff(fp, f1000, l2.Config{})
			}
		}),
	}
	ev := l2.BuildDiff(fp, f1000, l2.Config{})
	d := witness.Detail{Levels: witness.LevelsDetail{L2: &witness.LevelDetail{Verdict: witness.Mismatch, Method: l2.Method, L2Evidence: ev}}}
	res["render"] = testing.Benchmark(func(b *testing.B) {
		for b.Loop() {
			l2.Render(d, l2.RenderOptions{Width: 120})
		}
	})
	targets := map[string]float64{"derive": 200_000, "compare_1000": 5_000_000, "compare_200k": 1_000_000, "render": 2_000_000, "diff_build_1000": 5_000_000}
	m := map[string]any{}
	for k, r := range res {
		ns := float64(r.NsPerOp())
		m[k+"_ns"] = ns
		t.Logf("PERF %s %.0f ns/op (target < %.0f)", k, ns, targets[k])
		if ns >= targets[k] {
			t.Errorf("%s: %.0f ns ≥ %.0f", k, ns, targets[k])
		}
	}
	out["pure"] = m
}
