package httpx

import (
	"context"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/deedglass/deedglass/internal/buildinfo"
)

// HealthSource is what /v1/health asks the database. cmd/deedglass/serve.go implements it with
// internal/store.
type HealthSource interface {
	// PingDatabase checks that a connection can be used.
	PingDatabase(ctx context.Context) error
	// Migrations compares the applied migrations with this build's.
	Migrations(ctx context.Context) (MigrationState, error)
}

// MigrationState is the schema as health reports it.
type MigrationState struct {
	Applied, Expected int
	Changed           bool // an applied migration differs from this build's file, or is unknown to it
}

// Check statuses.
const (
	StatusOK            = "ok"
	StatusDegraded      = "degraded"
	StatusDown          = "down"
	StatusDraining      = "draining"
	StatusNotConfigured = "not_configured"
	StatusPending       = "pending"
	StatusMismatch      = "mismatch"
	StatusUnknown       = "unknown"
)

// HealthBody is GET /v1/health. It shows statuses only: error text and hosts go to the log.
type HealthBody struct {
	Object     string       `json:"object"`
	Status     string       `json:"status"`
	Version    string       `json:"version"`
	APIVersion string       `json:"api_version"`
	Checks     HealthChecks `json:"checks"`
	Time       Time         `json:"time"`
}

// HealthChecks are the individual checks.
type HealthChecks struct {
	Database   DatabaseCheck  `json:"database"`
	Migrations MigrationCheck `json:"migrations"`
	SealKey    SimpleCheck    `json:"seal_key"` // required since S04 (SystemDeps.SealKey)
	Decoder    SimpleCheck    `json:"decoder"`  // S20 makes it required
	Policies   SimpleCheck    `json:"policies"` // S12: ok, or degraded when an org's policy failed its load checks
}

// DatabaseCheck reports the database round trip.
type DatabaseCheck struct {
	Status    string `json:"status"`
	LatencyMS *int64 `json:"latency_ms"`
}

// MigrationCheck reports the schema version.
type MigrationCheck struct {
	Status   string `json:"status"`
	Applied  *int   `json:"applied"`
	Expected *int   `json:"expected"`
}

// SimpleCheck is a check with a status only.
type SimpleCheck struct {
	Status string `json:"status"`
}

// VersionBody is GET /v1/version.
type VersionBody struct {
	Object            string   `json:"object"`
	Version           string   `json:"version"`
	Commit            string   `json:"commit"`
	BuiltAt           *string  `json:"built_at"`
	Go                string   `json:"go"`
	APIVersions       []string `json:"api_versions"`
	DefaultAPIVersion string   `json:"default_api_version"`
}

// SealKeyCheck reports the seal key this server seals with: StatusOK, StatusDegraded (it retires
// soon: restart with the new key), StatusDown (retired, revoked or unreadable), or
// StatusNotConfigured.
type SealKeyCheck func(ctx context.Context) string

// PolicyCheck reports the policy registry (S12): StatusOK, or StatusDegraded when an org's
// enforced policy failed its load checks and every action there is refused.
type PolicyCheck func() string

// SystemDeps are what the system routes need.
type SystemDeps struct {
	Health   HealthSource // nil: the database check reports not_configured
	SealKey  SealKeyCheck // nil: the seal key check reports not_configured
	Policies PolicyCheck  // nil: the policies check reports not_configured
	Spec     []byte       // api/openapi.yaml, embedded (api.Spec)
	Now      func() time.Time
}

// healthCacheFor is how long one set of checks answers every probe.
const healthCacheFor = time.Second

type health struct {
	src      HealthSource
	sealKey  SealKeyCheck
	policies PolicyCheck
	now      func() time.Time
	mu       sync.Mutex
	cur      HealthBody
	at       time.Time
	wait     chan struct{}
	s        *Server
}

// SystemRoutes returns GET /v1/health, GET /v1/version and GET /api/openapi.yaml.
func (s *Server) SystemRoutes(d SystemDeps) []Route {
	if d.Now == nil {
		d.Now = time.Now
	}
	h := &health{src: d.Health, sealKey: d.SealKey, policies: d.Policies, now: d.Now, s: s}
	return []Route{
		{Method: "GET", Pattern: "/v1/health", OperationID: "getHealth", Auth: Public, Timeout: 5 * time.Second, Since: "2026-10-01", Handler: h.serve},
		{Method: "GET", Pattern: "/v1/version", OperationID: "getVersion", Auth: Public, Timeout: 5 * time.Second, Since: "2026-10-01", Handler: serveVersion},
		{Method: "GET", Pattern: "/api/openapi.yaml", OperationID: "getOpenAPISpec", Auth: Public, Timeout: 5 * time.Second, Since: "2026-10-01",
			Handler: func(w http.ResponseWriter, _ *http.Request) error {
				w.Header().Set("Content-Type", ContentTypeYAML)
				w.Header().Set("Cache-Control", "public, max-age=300")
				w.Header().Set("Content-Length", strconv.Itoa(len(d.Spec)))
				w.WriteHeader(http.StatusOK)
				_, err := w.Write(d.Spec)
				return err
			}},
	}
}

// Version returns the version body for this build.
func Version() VersionBody {
	bi := buildinfo.Get()
	v := VersionBody{
		Object: "version", Version: bi.Version, Commit: bi.Commit, Go: bi.Go,
		APIVersions: bi.APIVersions, DefaultAPIVersion: buildinfo.LatestAPIVersion,
	}
	if bi.Date != "" {
		d := bi.Date
		v.BuiltAt = &d
	}
	return v
}

// versionBody is GET /v1/version, encoded once: the build never changes while the process runs.
var versionBody = sync.OnceValues(func() ([]byte, error) { return MarshalJSON(Version()) })

func serveVersion(w http.ResponseWriter, _ *http.Request) error {
	b, err := versionBody()
	if err != nil {
		return err
	}
	h := w.Header()
	h["Content-Type"] = hvJSON
	h.Set("Content-Length", strconv.Itoa(len(b)))
	w.WriteHeader(http.StatusOK)
	_, err = w.Write(b)
	return err
}

func (h *health) serve(w http.ResponseWriter, r *http.Request) error {
	body := h.get(r.Context())
	body.APIVersion = APIVersion(r.Context())
	body.Time = Time(h.now())
	status := http.StatusOK
	if h.s != nil && h.s.Draining() {
		body.Status = StatusDraining
	}
	if body.Status == StatusDown || body.Status == StatusDraining {
		status = http.StatusServiceUnavailable
	}
	return WriteJSON(w, status, body)
}

// get returns cached checks, computing them at most once per second however many probes arrive
// (single flight).
func (h *health) get(ctx context.Context) HealthBody {
	h.mu.Lock()
	if !h.at.IsZero() && h.now().Sub(h.at) < healthCacheFor {
		b := h.cur
		h.mu.Unlock()
		return b
	}
	if ch := h.wait; ch != nil {
		h.mu.Unlock()
		select {
		case <-ch:
		case <-ctx.Done():
		}
		h.mu.Lock()
		b := h.cur
		h.mu.Unlock()
		if b.Object == "" {
			return h.compute(ctx)
		}
		return b
	}
	ch := make(chan struct{})
	h.wait = ch
	h.mu.Unlock()

	// Detached from the probe's context, so one impatient probe can't fail the shared result.
	cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	b := h.compute(cctx)
	cancel()

	h.mu.Lock()
	h.cur, h.at, h.wait = b, h.now(), nil
	h.mu.Unlock()
	close(ch)
	return b
}

func (h *health) compute(ctx context.Context) HealthBody {
	b := HealthBody{
		Object:  "health",
		Status:  StatusOK,
		Version: buildinfo.Get().Version,
		Checks: HealthChecks{
			Database:   DatabaseCheck{Status: StatusNotConfigured},
			Migrations: MigrationCheck{Status: StatusNotConfigured},
			SealKey:    SimpleCheck{Status: StatusNotConfigured},
			Decoder:    SimpleCheck{Status: StatusNotConfigured},
			Policies:   SimpleCheck{Status: StatusNotConfigured},
		},
	}
	if h.policies != nil {
		b.Checks.Policies.Status = h.policies()
		if b.Checks.Policies.Status != StatusOK {
			b.Status = StatusDegraded
		}
	}
	if h.src == nil {
		return b
	}
	log := h.s.log
	start := h.now()
	pctx, cancel := context.WithTimeout(ctx, time.Second)
	err := h.src.PingDatabase(pctx)
	cancel()
	if err != nil {
		log.WarnContext(ctx, "health: database check failed", "error", err.Error())
		b.Status = StatusDown
		b.Checks.Database.Status = StatusDown
		b.Checks.Migrations.Status = StatusUnknown
		return b
	}
	ms := h.now().Sub(start).Milliseconds()
	b.Checks.Database = DatabaseCheck{Status: StatusOK, LatencyMS: &ms}

	st, err := h.src.Migrations(ctx)
	if err != nil {
		log.WarnContext(ctx, "health: migration check failed", "error", err.Error())
		b.Status = StatusDegraded
		b.Checks.Migrations.Status = StatusUnknown
		return b
	}
	if h.sealKey != nil {
		b.Checks.SealKey.Status = h.sealKey(ctx)
		if b.Checks.SealKey.Status != StatusOK {
			b.Status = StatusDegraded
		}
	}
	applied, expected := st.Applied, st.Expected
	b.Checks.Migrations = MigrationCheck{Status: StatusOK, Applied: &applied, Expected: &expected}
	switch {
	case st.Changed:
		b.Checks.Migrations.Status = StatusMismatch
		b.Status = StatusDegraded
	case applied < expected:
		b.Checks.Migrations.Status = StatusPending
		b.Status = StatusDegraded
	}
	return b
}
