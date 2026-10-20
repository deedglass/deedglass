package bench

import (
	"context"
	"fmt"
	"strings"

	"github.com/deedglass/deedglass/internal/corpus"
)

// Score is one subject's score in one scenario.
type Score struct {
	// Diverges: the agent's report differs from what the database commits with no gateway.
	Diverges bool `json:"diverges"`
	Flag     Flag `json:"flag"`
	// Caught: the scenario diverges and the subject flagged it before or after the commit.
	Caught bool `json:"caught"`
	// FalseAlarm: the scenario doesn't diverge and the subject flagged it before or after anyway.
	FalseAlarm bool `json:"false_alarm"`
	// Effect: the subject's account of what committed equals what committed in this run.
	Effect bool `json:"effect"`
	// Record: the subject's record passed its offline check and covers every action.
	Record bool `json:"record"`
}

// Result is one subject in one scenario.
type Result struct {
	Scenario string `json:"scenario"`
	Subject  string `json:"subject"`
	Score    Score  `json:"score"`
	// Committed is what committed for the agent in this run (it differs between subjects when a
	// gateway stops an action).
	Committed Effect     `json:"committed"`
	AgentSaw  [][]string `json:"agent_saw"`
	Report    Report     `json:"report"`
}

// RunOptions configure a run.
type RunOptions struct {
	// DSN reaches a throwaway server as a superuser, with wal_level=logical.
	DSN string
	// Keep leaves each scenario's database in place.
	Keep bool
}

// Play runs one scenario through one subject and scores it.
func Play(ctx context.Context, subj Subject, s *corpus.Scenario, o RunOptions) (*Result, error) {
	sess, err := subj.Start(ctx, s)
	if err != nil {
		return nil, fmt.Errorf("bench: %s: %s: start: %w", subj.Info().Name, s.ID, err)
	}
	var (
		rep    Report
		finErr error
	)
	obs, err := corpus.Run(ctx, s, corpus.Options{
		DSN:           o.DSN,
		Keep:          o.Keep,
		Database:      dbName(subj.Info().Name, s.ID),
		Prepare:       sess.Prepare,
		Agent:         sess.Agent,
		IgnoreSchemas: sess.IgnoreSchemas(),
		After: func(ctx context.Context) error {
			rep, finErr = sess.Finish(ctx)
			return finErr
		},
	})
	if err != nil {
		return nil, fmt.Errorf("bench: %s: %w", subj.Info().Name, err)
	}
	committed := TrueEffect(obs.Commits)
	r := &Result{Scenario: s.ID, Subject: subj.Info().Name, Committed: committed, AgentSaw: obs.AgentSaw, Report: rep}
	r.Score = score(s, committed, rep)
	return r, nil
}

func score(s *corpus.Scenario, committed Effect, rep Report) Score {
	sc := Score{Diverges: Diverges(s), Flag: rep.Flag}
	if sc.Flag == "" {
		sc.Flag = FlagNone
	}
	flagged := sc.Flag.Caught()
	sc.Caught = sc.Diverges && flagged
	sc.FalseAlarm = !sc.Diverges && flagged
	sc.Effect = rep.Effect != nil && Equal(*rep.Effect, committed)
	sc.Record = rep.Record != nil && rep.Record.Checked
	return sc
}

// dbName is the scenario's database for one subject: bench_<subject>_<id>, lowercase, at most
// 63 bytes.
func dbName(subject, id string) string {
	clean := func(s string) string {
		var b strings.Builder
		for _, r := range strings.ToLower(s) {
			switch {
			case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
				b.WriteRune(r)
			default:
				b.WriteByte('_')
			}
		}
		return b.String()
	}
	n := "bench_" + clean(subject) + "_" + clean(id)
	if len(n) > 63 {
		n = n[:63]
	}
	return n
}
