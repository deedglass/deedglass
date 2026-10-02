package console

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http/httptest"
	"os"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/deedglass/deedglass/internal/httpx"
	"github.com/deedglass/deedglass/internal/ids"
	"github.com/deedglass/deedglass/internal/ledger"
)

// TestHubBudget is S24 §3.12's hub budget (make bench-events): 200 real SSE connections of one
// org on a loopback listener, 1,000 events/s for 5 s through the hub, p99 enqueue→write < 50 ms;
// and the memory each open stream costs (< 64 KiB), measured as the process's heap and stack growth
// across opening the 200 streams, divided by 200. Both ends of each connection live in this
// process, so the figure includes the client side too: an upper bound for the server. It writes
// JSON to $DEEDGLASS_EVENTS_BENCH_OUT and skips without it.
func TestHubBudget(t *testing.T) {
	out := os.Getenv("DEEDGLASS_EVENTS_BENCH_OUT")
	if out == "" {
		t.Skip("set DEEDGLASS_EVENTS_BENCH_OUT (make bench-events)")
	}
	const (
		subs     = 200
		rate     = 1000
		duration = 5 * time.Second
	)
	a := &evAuth{org: ids.New[ids.OrgKind](), agent: ids.New[ids.AgentKind](), user: ids.New[ids.UserKind]()}
	hub := NewHub(HubOptions{Instance: "bench1", MaxPerUser: 1000, MaxPerOrg: 1000})
	in := make(chan ledger.BusMsg, BusBuffer)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = hub.Run(ctx, in) }()
	var mu sync.Mutex
	var lat []time.Duration
	api := httpx.New(httpx.Options{Logger: slog.New(slog.DiscardHandler), Keys: a, Sessions: a})
	api.Mount(EventRoutes(EventsOptions{Hub: hub, observe: func(q time.Time) {
		d := time.Since(q)
		mu.Lock()
		lat = append(lat, d)
		mu.Unlock()
	}})...)
	srv := httptest.NewServer(api)
	defer srv.Close()

	memNow := func() uint64 {
		runtime.GC()
		var ms runtime.MemStats
		runtime.ReadMemStats(&ms)
		return ms.HeapInuse + ms.StackInuse
	}
	// Each client is one raw TCP connection and one goroutine reading into a small buffer, so its
	// own cost is small and measured separately (against a listener that only holds connections),
	// then subtracted.
	open := func(addr string, request bool) (net.Conn, error) {
		c, err := net.Dial("tcp", addr)
		if err != nil {
			return nil, err
		}
		if request {
			_, err = fmt.Fprintf(c, "GET /v1/events HTTP/1.1\r\nHost: x\r\nCookie: deedglass_session=%s\r\nAccept: text/event-stream\r\n\r\n", evCookie)
		}
		return c, err
	}
	var wg sync.WaitGroup
	clients := func(addr string, request bool) ([]net.Conn, chan struct{}) {
		ready := make(chan struct{}, subs)
		conns := make([]net.Conn, 0, subs)
		for range subs {
			c, err := open(addr, request)
			if err != nil {
				t.Fatal(err)
			}
			conns = append(conns, c)
			wg.Go(func() {
				buf := make([]byte, 512)
				seen := ""
				signalled := !request
				if signalled {
					ready <- struct{}{}
				}
				for {
					n, err := c.Read(buf)
					if err != nil {
						if !signalled {
							ready <- struct{}{}
						}
						return
					}
					if !signalled {
						seen += string(buf[:n])
						if strings.Contains(seen, "event: hello") {
							signalled, seen = true, ""
							ready <- struct{}{}
						}
					}
				}
			})
		}
		for range subs {
			<-ready
		}
		return conns, ready
	}
	// The clients' own cost: the same connections to a listener that only holds them.
	hold, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var held []net.Conn
	var heldMu sync.Mutex
	go func() {
		for {
			c, err := hold.Accept()
			if err != nil {
				return
			}
			heldMu.Lock()
			held = append(held, c)
			heldMu.Unlock()
		}
	}()
	base := memNow()
	cs, _ := clients(hold.Addr().String(), false)
	time.Sleep(100 * time.Millisecond)
	clientPer := float64(memNow()-base) / subs
	for _, c := range cs {
		c.Close()
	}
	hold.Close()
	heldMu.Lock()
	for _, c := range held {
		c.Close()
	}
	heldMu.Unlock()
	wg.Wait()

	before := memNow()
	conns, _ := clients(strings.TrimPrefix(srv.URL, "http://"), true)
	totalPer := float64(memNow()-before) / subs
	perSub := totalPer - clientPer
	if st := hub.Stats(); st.Streams != subs {
		t.Fatalf("%d streams open", st.Streams)
	}

	org := a.org.String()
	tick := time.NewTicker(time.Second / rate)
	start := time.Now()
	n := 0
	for time.Since(start) < duration {
		<-tick.C
		act := ids.New[ids.ActionKind]().String()
		in <- ledger.BusMsg{Event: &ledger.Event{V: 1, Event: ids.New[ids.ActionEventKind]().String(), Type: ledger.EventActionCreated,
			Org: org, ID: act, Action: &act, At: "2026-10-02T10:00:00.000Z"}}
		n++
	}
	tick.Stop()
	elapsed := time.Since(start)
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		mu.Lock()
		got := len(lat)
		mu.Unlock()
		if got >= n*subs {
			break
		}
	}
	for _, c := range conns {
		c.Close()
	}
	wg.Wait()
	mu.Lock()
	defer mu.Unlock()
	slices.Sort(lat)
	q := func(p float64) time.Duration {
		if len(lat) == 0 {
			return 0
		}
		return lat[min(len(lat)-1, int(p*float64(len(lat))))]
	}
	st := hub.Stats()
	res := map[string]any{
		"bench": "S24 hub fan-out (make bench-events)", "date": time.Now().UTC().Format(time.RFC3339), "go": runtime.Version(),
		"os": runtime.GOOS, "arch": runtime.GOARCH, "cpus": runtime.NumCPU(),
		"subscribers": subs, "events_sent": n, "events_per_s": float64(n) / elapsed.Seconds(), "writes": len(lat),
		"enqueue_to_write_ms": map[string]float64{"p50": ms(q(0.50)), "p90": ms(q(0.90)), "p99": ms(q(0.99)), "max": ms(q(1))},
		"lagged":              st.Lagged, "bytes_per_subscriber": perSub, "kib_per_subscriber": perSub / 1024,
		"client_side_bytes_per_connection": clientPer, "both_ends_bytes_per_connection": totalPer,
		"budget": map[string]any{"p99_ms": 50, "kib_per_subscriber": 64},
		"note": "200 loopback SSE connections in one process; the server's cost per stream is the heap and stack growth " +
			"with them open, minus the same clients' cost against a listener that only holds the connections",
	}
	b, _ := json.MarshalIndent(res, "", "  ")
	if err := os.WriteFile(out, append(b, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Logf("PERF %s", b)
	if len(lat) != n*subs || st.Lagged != 0 {
		t.Errorf("%d of %d writes, %d lagged", len(lat), n*subs, st.Lagged)
	}
	if q(0.99) >= 50*time.Millisecond {
		t.Errorf("p99 enqueue→write %v, budget 50 ms", q(0.99))
	}
	if perSub >= 64<<10 {
		t.Errorf("%.0f bytes per subscriber, budget 64 KiB", perSub)
	}
}

func ms(d time.Duration) float64 { return float64(d.Microseconds()) / 1000 }
