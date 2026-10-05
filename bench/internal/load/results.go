package load

//deedglass:allow-exec benchmark tooling: reads git, uname, sysctl and pmset to record the machine; never shipped

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Schema is the results format every suite writes (bench/schema.json describes it).
const Schema = "deedglass.bench/v1"

// File is one results file: bench/results/<date>-<host>-<suite>.json.
type File struct {
	Schema     string     `json:"schema"`
	Suite      string     `json:"suite"`
	Quick      bool       `json:"quick,omitempty"`
	StartedAt  string     `json:"started_at"`
	Commit     string     `json:"commit"`
	Dirty      bool       `json:"dirty"`
	SourceHash string     `json:"source_hash"`
	Version    string     `json:"version"`
	Host       Host       `json:"host"`
	Go         string     `json:"go"`
	Postgres   *Postgres  `json:"postgres,omitempty"`
	Scenarios  []Scenario `json:"scenarios"`
	Caveats    []string   `json:"caveats"`
}

// Host is the machine, as captured at the start of the run.
type Host struct {
	Name  string `json:"name"`
	CPU   string `json:"cpu"`
	Cores int    `json:"cores"`
	MemGB int    `json:"mem_gb"`
	OS    string `json:"os"`
	Power string `json:"power"` // "ac", "battery" or "unknown"; bench-check refuses anything but "ac"
}

// Postgres is the server the suite ran against.
type Postgres struct {
	Version  string            `json:"version"`
	Mode     string            `json:"mode"` // "native" (Homebrew keg) or "docker"
	Settings map[string]string `json:"settings"`
}

// Scenario is one measured thing, with every run kept.
type Scenario struct {
	Name        string         `json:"name"`
	Rate        float64        `json:"rate,omitempty"`
	WarmupS     float64        `json:"warmup_s,omitempty"`
	DurationS   float64        `json:"duration_s,omitempty"`
	MaxInFlight int            `json:"max_in_flight,omitempty"`
	Runs        []RunJSON      `json:"runs"`
	Targets     []Goal         `json:"targets,omitempty"`
	Result      string         `json:"result"` // "pass", "fail" or "reported"
	Notes       []string       `json:"notes,omitempty"`
	Extra       map[string]any `json:"extra,omitempty"`
}

// RunJSON is one run's numbers (milliseconds, three decimals) or, for a non-latency scenario, its values.
type RunJSON struct {
	Count        int            `json:"count"`
	Errors       int            `json:"errors"`
	AchievedRate float64        `json:"achieved_rate,omitempty"`
	P50ms        float64        `json:"p50_ms"`
	P90ms        float64        `json:"p90_ms,omitempty"`
	P95ms        float64        `json:"p95_ms,omitempty"`
	P99ms        float64        `json:"p99_ms"`
	P999ms       float64        `json:"p999_ms,omitempty"`
	MaxMs        float64        `json:"max_ms"`
	MeanMs       float64        `json:"mean_ms,omitempty"`
	Statuses     map[string]int `json:"statuses,omitempty"`
	Values       map[string]any `json:"values,omitempty"`
}

// Goal is a contract §12 target: metric op value.
type Goal struct {
	Metric string  `json:"metric"` // p50_ms, p99_ms, max_ms, or a key of Run.Values
	Op     string  `json:"op"`     // "lt"
	Value  float64 `json:"value"`
	Source string  `json:"source"`
	Label  string  `json:"label"` // "p99 < 25 ms", as the report prints it
}

func ms(d time.Duration) float64 { return math.Round(float64(d)/1e3) / 1e3 }

// FromRuns converts RunResults.
func FromRuns(rs []RunResult) []RunJSON {
	out := make([]RunJSON, 0, len(rs))
	for _, r := range rs {
		out = append(out, RunJSON{Count: r.Count, Errors: r.Errors, AchievedRate: math.Round(r.AchievedRate*100) / 100,
			P50ms: ms(r.P50), P90ms: ms(r.P90), P95ms: ms(r.P95), P99ms: ms(r.P99), P999ms: ms(r.P999),
			MaxMs: ms(r.Max), MeanMs: ms(r.Mean), Statuses: r.Statuses})
	}
	return out
}

// MetricOf returns run r's value for a target metric.
func MetricOf(r RunJSON, metric string) (float64, bool) {
	switch metric {
	case "p50_ms":
		return r.P50ms, true
	case "p99_ms":
		return r.P99ms, true
	case "max_ms":
		return r.MaxMs, true
	}
	v, ok := r.Values[metric]
	if !ok {
		return 0, false
	}
	switch x := v.(type) {
	case float64:
		return x, true
	case int:
		return float64(x), true
	case json.Number:
		f, err := x.Float64()
		return f, err == nil
	}
	return 0, false
}

// Judge is the median of t's metric across runs (the middle of three), and whether it meets t.
// A run with errors fails the scenario whatever its numbers say.
func (s *Scenario) Judge(t Goal) (median float64, ok bool) {
	if len(s.Runs) == 0 {
		return 0, false
	}
	var vs []float64
	errs := 0
	for _, r := range s.Runs {
		v, have := MetricOf(r, t.Metric)
		if !have {
			return 0, false
		}
		vs = append(vs, v)
		errs += r.Errors
	}
	sort.Float64s(vs)
	median = vs[len(vs)/2]
	return median, errs == 0 && t.Op == "lt" && median < t.Value
}

// Met reports whether every target of s is met.
func (s *Scenario) Met() bool {
	for _, t := range s.Targets {
		if _, ok := s.Judge(t); !ok {
			return false
		}
	}
	return true
}

// Finish sets each scenario's Result from its targets.
func (f *File) Finish() {
	for i := range f.Scenarios {
		s := &f.Scenarios[i]
		switch {
		case len(s.Targets) == 0:
			s.Result = "reported"
		case s.Met():
			s.Result = "pass"
		default:
			s.Result = "fail"
		}
	}
}

// NewFile captures the environment for a suite. root is the repo root.
func NewFile(root, suite, version string, quick bool) *File {
	commit, dirty := gitState(root)
	sh, _ := SourceHash(root)
	return &File{Schema: Schema, Suite: suite, Quick: quick, StartedAt: time.Now().UTC().Format(time.RFC3339),
		Commit: commit, Dirty: dirty, SourceHash: sh, Version: version, Host: CaptureHost(), Go: runtime.Version(),
		Scenarios: []Scenario{}, Caveats: []string{}}
}

// Path is where f is written: bench/results/<date>-<host>-bench-<suite>[-quick].json. The "bench-"
// infix keeps these apart from the raw files earlier sessions wrote under <date>-<host>-<name>.json.
func (f *File) Path(root string) string {
	name := fmt.Sprintf("%s-%s-bench-%s", f.StartedAt[:10], f.Host.Name, f.Suite)
	if f.Quick {
		name += "-quick"
	}
	return filepath.Join(root, "bench", "results", name+".json")
}

// Write writes f (indented, trailing newline) and returns its path.
func (f *File) Write(root string) (string, error) {
	f.Finish()
	if err := f.Validate(); err != nil {
		return "", err
	}
	b, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return "", err
	}
	p := f.Path(root)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return "", err
	}
	return p, os.WriteFile(p, append(b, '\n'), 0o644)
}

// Validate checks the fields bench-check relies on.
func (f *File) Validate() error {
	var miss []string
	if f.Schema != Schema {
		miss = append(miss, "schema")
	}
	if f.Suite == "" {
		miss = append(miss, "suite")
	}
	if _, err := time.Parse(time.RFC3339, f.StartedAt); err != nil {
		miss = append(miss, "started_at")
	}
	if !strings.HasPrefix(f.SourceHash, "sha256:") {
		miss = append(miss, "source_hash")
	}
	if f.Host.Name == "" || f.Host.CPU == "" || f.Host.Cores == 0 {
		miss = append(miss, "host")
	}
	switch f.Host.Power {
	case "ac", "battery", "unknown":
	default:
		miss = append(miss, "host.power")
	}
	if len(f.Scenarios) == 0 {
		miss = append(miss, "scenarios")
	}
	for _, s := range f.Scenarios {
		if s.Name == "" || len(s.Runs) == 0 {
			miss = append(miss, "scenario "+s.Name)
		}
	}
	if len(miss) > 0 {
		return fmt.Errorf("results file is missing or has invalid: %s", strings.Join(miss, ", "))
	}
	return nil
}

// Read reads and validates one results file.
func Read(path string) (*File, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var f File
	if err := json.Unmarshal(b, &f); err != nil {
		return nil, fmt.Errorf("%s: %w", filepath.Base(path), err)
	}
	if err := f.Validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", filepath.Base(path), err)
	}
	return &f, nil
}

// SourceDirs are what a benchmark measures. A results file whose source_hash differs from the
// working tree's is stale: bench-check refuses it (S35 §3.2). Test files don't count.
var SourceDirs = []string{"internal/gate", "internal/policy", "internal/deed", "internal/witness", "internal/decoder",
	"internal/connector/postgres", "cmd/deedglass"}

// SourceHash is sha256 over the path and contents of every non-test .go file under SourceDirs.
func SourceHash(root string) (string, error) {
	h := sha256.New()
	var files []string
	for _, d := range SourceDirs {
		err := filepath.WalkDir(filepath.Join(root, d), func(p string, e fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if e.IsDir() && (e.Name() == "testdata" || strings.HasPrefix(e.Name(), ".")) {
				return filepath.SkipDir
			}
			if !e.IsDir() && strings.HasSuffix(p, ".go") && !strings.HasSuffix(p, "_test.go") {
				files = append(files, p)
			}
			return nil
		})
		if err != nil {
			return "", err
		}
	}
	sort.Strings(files)
	for _, p := range files {
		b, err := os.ReadFile(p)
		if err != nil {
			return "", err
		}
		rel, _ := filepath.Rel(root, p)
		fmt.Fprintf(h, "%s\x00%d\x00", filepath.ToSlash(rel), len(b))
		h.Write(b)
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil)), nil
}

func gitState(root string) (string, bool) {
	out, err := exec.Command("git", "-C", root, "rev-parse", "HEAD").Output()
	if err != nil {
		return "unknown", true
	}
	st, _ := exec.Command("git", "-C", root, "status", "--porcelain").Output()
	return strings.TrimSpace(string(out)), len(strings.TrimSpace(string(st))) > 0
}

// CaptureHost reads the machine's facts. Power comes from pmset on macOS and
// /sys/class/power_supply on Linux; anything unreadable is "unknown", which bench-check refuses.
func CaptureHost() Host {
	h := Host{Cores: runtime.NumCPU(), Power: "unknown"}
	// The machine's own hostname often carries a person's name, and these files are committed, so the
	// label is <os>-<arch> unless DEEDGLASS_BENCH_HOST names the machine (for example "cx23").
	h.Name = sanitizeHost(os.Getenv("DEEDGLASS_BENCH_HOST"))
	if out, err := exec.Command("uname", "-sr").Output(); err == nil {
		h.OS = strings.TrimSpace(string(out))
	}
	switch runtime.GOOS {
	case "darwin":
		if out, err := exec.Command("sysctl", "-n", "machdep.cpu.brand_string").Output(); err == nil {
			h.CPU = strings.TrimSpace(string(out))
		}
		if out, err := exec.Command("sysctl", "-n", "hw.memsize").Output(); err == nil {
			if n, err := strconv.ParseInt(strings.TrimSpace(string(out)), 10, 64); err == nil {
				h.MemGB = int(n >> 30)
			}
		}
		if out, err := exec.Command("pmset", "-g", "batt").Output(); err == nil {
			h.Power = powerFromPmset(string(out))
		}
	case "linux":
		if b, err := os.ReadFile("/proc/cpuinfo"); err == nil {
			for _, l := range strings.Split(string(b), "\n") {
				if k, v, ok := strings.Cut(l, ":"); ok && strings.TrimSpace(k) == "model name" {
					h.CPU = strings.TrimSpace(v)
					break
				}
			}
		}
		if b, err := os.ReadFile("/proc/meminfo"); err == nil {
			var kb int64
			_, _ = fmt.Sscanf(string(b), "MemTotal: %d kB", &kb)
			h.MemGB = int(kb >> 20)
		}
		h.Power = powerFromSysfs("/sys/class/power_supply")
	}
	if h.CPU == "" {
		h.CPU = runtime.GOARCH
	}
	return h
}

func sanitizeHost(s string) string {
	s = strings.ToLower(s)
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			b.WriteRune(r)
		}
	}
	if b.Len() == 0 {
		return runtime.GOOS + "-" + runtime.GOARCH
	}
	return b.String()
}

func powerFromPmset(out string) string {
	switch {
	case strings.Contains(out, "'AC Power'"):
		return "ac"
	case strings.Contains(out, "'Battery Power'"):
		return "battery"
	}
	return "unknown"
}

// powerFromSysfs: a machine with no battery (a server, a VM) is on AC.
func powerFromSysfs(dir string) string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "ac"
	}
	sawBattery := false
	for _, e := range entries {
		t, _ := os.ReadFile(filepath.Join(dir, e.Name(), "type"))
		switch strings.TrimSpace(string(t)) {
		case "Mains":
			if on, _ := os.ReadFile(filepath.Join(dir, e.Name(), "online")); strings.TrimSpace(string(on)) == "1" {
				return "ac"
			}
		case "Battery":
			sawBattery = true
		}
	}
	if sawBattery {
		return "battery"
	}
	return "ac"
}

// ErrNoResults is returned when a directory holds no results for a suite.
var ErrNoResults = errors.New("no results")
