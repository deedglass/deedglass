// Command mock-orders is a small orders API for the REST connector demo (S16) and S19's lying-mock
// witness demo. Standard library only.
//
//	GET   /v1/health         {"ok":true}                      needs the bearer token
//	GET   /v1/orders/{id}    one of 50 seeded orders (1–50)  needs the bearer token
//	PATCH /v1/orders/{id}    {"status":"pending|shipped|cancelled"}
//
// MOCK_ORDERS_TOKEN is the bearer token it accepts (required, at least 16 characters).
// MOCK_ORDERS_MODE=lying makes PATCH answer 200 with the new status without saving it, so a
// read-back sees the old one (S19 turns that into an L1 MISMATCH). MOCK_ORDERS_ADDR defaults to
// 127.0.0.1:8090. A repeated Idempotency-Key replays the first answer.
package main

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"sync"
	"syscall"
	"time"
)

type order struct {
	ID         int    `json:"id"`
	Status     string `json:"status"`
	Customer   string `json:"customer"`
	TotalCents int    `json:"total_cents"`
	Currency   string `json:"currency"`
	UpdatedAt  string `json:"updated_at"`
}

// Server is the mock API.
type Server struct {
	token string
	lying bool

	mu     sync.Mutex
	orders map[int]order
	replay map[string][]byte
}

var statuses = []string{"pending", "shipped", "cancelled"}

// New seeds 50 orders deterministically.
func New(token string, lying bool) *Server {
	s := &Server{token: token, lying: lying, orders: map[int]order{}, replay: map[string][]byte{}}
	customers := []string{"acme-retail", "globex", "initech", "umbrella", "hooli"}
	for i := 1; i <= 50; i++ {
		s.orders[i] = order{ID: i, Status: statuses[(i*7)%3], Customer: customers[i%len(customers)],
			TotalCents: 1000 + (i*7919)%90000, Currency: "usd", UpdatedAt: "2026-10-05T09:00:00Z"}
	}
	return s
}

func (s *Server) authed(r *http.Request) bool {
	got := r.Header.Get("Authorization")
	want := "Bearer " + s.token
	return subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}

func writeJSON(w http.ResponseWriter, status int, v any) []byte {
	b, _ := json.Marshal(v)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(b)
	return b
}

func problem(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// ServeHTTP implements http.Handler.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !s.authed(r) {
		w.Header().Set("WWW-Authenticate", `Bearer realm="mock-orders"`)
		problem(w, http.StatusUnauthorized, "missing or wrong bearer token")
		return
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "lying": s.lying})
	})
	mux.HandleFunc("GET /v1/orders/{id}", s.get)
	mux.HandleFunc("PATCH /v1/orders/{id}", s.patch)
	mux.ServeHTTP(w, r)
}

func (s *Server) id(w http.ResponseWriter, r *http.Request) (int, bool) {
	id, err := strconv.Atoi(r.PathValue("id"))
	if err != nil || id < 1 {
		problem(w, http.StatusNotFound, "no such order")
		return 0, false
	}
	return id, true
}

func (s *Server) get(w http.ResponseWriter, r *http.Request) {
	id, ok := s.id(w, r)
	if !ok {
		return
	}
	s.mu.Lock()
	o, found := s.orders[id]
	s.mu.Unlock()
	if !found {
		problem(w, http.StatusNotFound, "no such order")
		return
	}
	writeJSON(w, http.StatusOK, o)
}

func (s *Server) patch(w http.ResponseWriter, r *http.Request) {
	id, ok := s.id(w, r)
	if !ok {
		return
	}
	key := r.Header.Get("Idempotency-Key")
	s.mu.Lock()
	defer s.mu.Unlock()
	if b, seen := s.replay[key]; key != "" && seen {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Idempotent-Replayed", "true")
		_, _ = w.Write(b)
		return
	}
	var in struct {
		Status string `json:"status"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&in); err != nil {
		problem(w, http.StatusBadRequest, "the body must be {\"status\": …}")
		return
	}
	valid := false
	for _, st := range statuses {
		valid = valid || st == in.Status
	}
	if !valid {
		problem(w, http.StatusUnprocessableEntity, "status must be pending, shipped or cancelled")
		return
	}
	o, found := s.orders[id]
	if !found {
		problem(w, http.StatusNotFound, "no such order")
		return
	}
	o.Status, o.UpdatedAt = in.Status, time.Now().UTC().Format(time.RFC3339)
	if !s.lying {
		s.orders[id] = o // a lying mock answers as if it saved, and doesn't
	}
	b := writeJSON(w, http.StatusOK, o)
	if key != "" {
		s.replay[key] = b
	}
}

func main() {
	token := os.Getenv("MOCK_ORDERS_TOKEN")
	if len(token) < 16 {
		fmt.Fprintln(os.Stderr, "✕ MOCK_ORDERS_TOKEN must be set to at least 16 characters.")
		fmt.Fprintln(os.Stderr, "  Next: export MOCK_ORDERS_TOKEN=$(openssl rand -hex 16), and store the same value with deedglass connector credentials set orders --from-env MOCK_ORDERS_TOKEN.")
		os.Exit(2)
	}
	addr := os.Getenv("MOCK_ORDERS_ADDR")
	if addr == "" {
		addr = "127.0.0.1:8090"
	}
	mode := os.Getenv("MOCK_ORDERS_MODE")
	if mode != "" && mode != "honest" && mode != "lying" {
		fmt.Fprintln(os.Stderr, "✕ MOCK_ORDERS_MODE must be honest or lying.")
		os.Exit(2)
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		log.Fatalf("mock-orders: %v", err)
	}
	srv := &http.Server{Handler: New(token, mode == "lying"), ReadHeaderTimeout: 5 * time.Second}
	fmt.Printf("✓ mock-orders on http://%s · 50 orders · %s mode\n", ln.Addr(), map[bool]string{true: "lying", false: "honest"}[mode == "lying"])
	go func() {
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
		<-sig
		_ = srv.Close()
	}()
	if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatalf("mock-orders: %v", err)
	}
}
