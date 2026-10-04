// Package doctor is `deedglass doctor` (S32 §3.10): sixteen read-only checks, each with the
// observed value and, when it isn't OK, the exact fix. Doctor never writes: its database session
// is read-only (default_transaction_read_only=on), and it calls only GET routes.
package doctor

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// Status is a check's outcome.
type Status int

const (
	OK Status = iota
	Warn
	Fail
	Skip
)

func (s Status) String() string { return [...]string{"ok", "warn", "fail", "skip"}[s] }

// MarshalText makes Status a JSON string.
func (s Status) MarshalText() ([]byte, error) { return []byte(s.String()), nil }

// Result is one check's line.
type Result struct {
	ID     string        `json:"id"`
	Title  string        `json:"title"`
	Status Status        `json:"status"`
	Detail string        `json:"detail"`
	Fix    string        `json:"fix,omitempty"`
	Doc    string        `json:"doc"`
	Took   time.Duration `json:"-"`
	TookMS int64         `json:"took_ms"`
}

// Check is one doctor check.
type Check struct {
	ID    string
	Title string
	Run   func(ctx context.Context, env *Env) Result
}

// DocURL is the check's anchor in docs/INIT_AND_DOCTOR.md (docs.deedglass.com/doctor#…).
func DocURL(id string) string {
	return "https://docs.deedglass.com/doctor#" + strings.NewReplacer(".", "-", "_", "-").Replace(id)
}

// Timeout bounds every check.
const Timeout = 3 * time.Second

// Run runs the checks in parallel, each with Timeout, and returns them in their given order.
func Run(ctx context.Context, env *Env, checks []Check) []Result {
	out := make([]Result, len(checks))
	var wg sync.WaitGroup
	for i, c := range checks {
		wg.Go(func() {
			cctx, cancel := context.WithTimeout(ctx, Timeout)
			defer cancel()
			t0 := time.Now()
			done := make(chan Result, 1)
			go func() {
				defer func() {
					if p := recover(); p != nil {
						done <- Result{Status: Fail, Detail: "the check itself failed", Fix: "Run deedglass doctor -v and report what it prints."}
					}
				}()
				done <- c.Run(cctx, env)
			}()
			var r Result
			select {
			case r = <-done:
			case <-cctx.Done():
				r = Result{Status: Fail, Detail: fmt.Sprintf("no answer within %s", Timeout), Fix: "Check that the database and the server are running, then run deedglass doctor again."}
			}
			r.ID, r.Title, r.Doc = c.ID, c.Title, DocURL(c.ID)
			r.Took = time.Since(t0)
			r.TookMS = r.Took.Milliseconds()
			out[i] = r
		})
	}
	wg.Wait()
	return out
}

// Summary counts the results.
type Summary struct {
	OK   int `json:"ok"`
	Warn int `json:"warn"`
	Fail int `json:"fail"`
	Skip int `json:"skip"`
}

// Summarize counts results by status.
func Summarize(rs []Result) Summary {
	var s Summary
	for _, r := range rs {
		switch r.Status {
		case OK:
			s.OK++
		case Warn:
			s.Warn++
		case Fail:
			s.Fail++
		case Skip:
			s.Skip++
		}
	}
	return s
}

// Filter keeps the checks whose id matches a pattern ("db.*", "seal.key").
func Filter(cs []Check, pattern string) []Check {
	if pattern == "" {
		return cs
	}
	var out []Check
	for _, c := range cs {
		for _, p := range strings.Split(pattern, ",") {
			p = strings.TrimSpace(p)
			if p == c.ID || (strings.HasSuffix(p, "*") && strings.HasPrefix(c.ID, strings.TrimSuffix(p, "*"))) {
				out = append(out, c)
				break
			}
		}
	}
	return out
}

// IDs lists the check ids, sorted.
func IDs(cs []Check) []string {
	var out []string
	for _, c := range cs {
		out = append(out, c.ID)
	}
	sort.Strings(out)
	return out
}

func ok(detail string) Result        { return Result{Status: OK, Detail: detail} }
func warn(detail, fix string) Result { return Result{Status: Warn, Detail: detail, Fix: fix} }
func fail(detail, fix string) Result { return Result{Status: Fail, Detail: detail, Fix: fix} }
func skip(detail, fix string) Result { return Result{Status: Skip, Detail: detail, Fix: fix} }
func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
