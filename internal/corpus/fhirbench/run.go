package fhirbench

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/deedglass/deedglass/internal/corpus/bench"
)

// Server is a FHIR server the runner drives directly, as its admin: it sets each run up, sends
// the calls no subject carries, and reads what was stored from the server's own database.
type Server interface {
	Name() string
	Version() string
	// Prepare sets one run up (project settings, the agent's client and its access policy) and
	// returns the session's Env and a cleanup that puts the settings back.
	Prepare(ctx context.Context, s *Scenario) (Env, func(), error)
	// Do sends r straight to the server, with the agent's token (agent) or another client's.
	Do(ctx context.Context, env Env, agent bool, r Request) (Exchange, error)
	// Mark is a position in the server's version tables; Since returns every version stored after
	// it, oldest first, each with its kind (create, update, delete).
	Mark(ctx context.Context) (any, error)
	Since(ctx context.Context, mark any) ([]Version, error)
	// Current is the current version id of a resource ("" when it doesn't exist).
	Current(ctx context.Context, typ, id string) (string, error)
	// Lookup reads what Facts needs about the agent's versions: whether each referenced resource
	// exists, and, for each deleted resource, how many earlier versions still hold content.
	Lookup(ctx context.Context, versions []Version) (Lookups, error)
}

// Score is one subject's score in one scenario, by the corpus benchmark's rules.
type Score struct {
	Diverges   bool       `json:"diverges"`
	Flag       bench.Flag `json:"flag"`
	Caught     bool       `json:"caught"`
	FalseAlarm bool       `json:"false_alarm"`
	Effect     bool       `json:"effect"`
	Record     bool       `json:"record"`
}

// Result is one subject in one scenario on one server.
type Result struct {
	Scenario string `json:"scenario"`
	Server   string `json:"server"`
	Subject  string `json:"subject"`
	Score    Score  `json:"score"`
	// Stored is what the server stored for the agent in this run (it differs between subjects
	// when a gateway stops a call).
	Stored Effect `json:"stored"`
	Report Report `json:"report"`
	Run    Run    `json:"run"`
}

// ScoreOf scores a report against a run's facts.
func ScoreOf(diverges bool, stored Effect, rep Report) Score {
	sc := Score{Diverges: diverges, Flag: rep.Flag}
	if sc.Flag == "" {
		sc.Flag = bench.FlagNone
	}
	flagged := sc.Flag.Caught()
	sc.Caught = diverges && flagged
	sc.FalseAlarm = !diverges && flagged
	sc.Effect = rep.Effect != nil && Equal(*rep.Effect, stored)
	sc.Record = rep.Record != nil && rep.Record.Checked
	return sc
}

// Play runs one scenario through one subject on one server and scores it. nonce makes the
// scenario's identifiers unique to this run, so runs on one server never match each other's data.
func Play(ctx context.Context, srv Server, subj Subject, s *Scenario, nonce string) (*Result, error) {
	diverges, err := s.Diverges(srv.Name())
	if err != nil {
		return nil, err
	}
	res, err := PlayUnscored(ctx, srv, subj, s, nonce)
	if err != nil {
		return nil, err
	}
	res.Score = ScoreOf(diverges, res.Stored, res.Report)
	return res, nil
}

// PlayUnscored plays the scenario without expected facts (make fhir-bench WRITE_EXPECTED=1
// records them from it).
func PlayUnscored(ctx context.Context, srv Server, subj Subject, s *Scenario, nonce string) (*Result, error) {
	name := subj.Info().Name
	wrap := func(what string, err error) error {
		return fmt.Errorf("fhirbench: %s: %s: %s: %w", name, s.ID, what, err)
	}
	env, cleanup, err := srv.Prepare(ctx, s)
	if err != nil {
		return nil, wrap("prepare", err)
	}
	defer cleanup()
	vars := Vars{Nonce: nonce, IDs: map[string]string{}, Versions: map[string]string{}}
	label := func(l string, vs []Version, saw Exchange) {
		if l == "" {
			return
		}
		ids := map[string]bool{}
		for _, v := range vs {
			ids[v.ID] = true
		}
		if len(ids) == 1 {
			last := vs[len(vs)-1]
			vars.IDs[l], vars.Versions[l] = last.ID, last.VersionID
			return
		}
		var b struct {
			ID   string `json:"id"`
			Meta struct {
				VersionID string `json:"versionId"`
			} `json:"meta"`
		}
		if json.Unmarshal(saw.Body, &b) == nil && b.ID != "" {
			vars.IDs[l], vars.Versions[l] = b.ID, b.Meta.VersionID
		}
	}
	for i, sd := range s.Setup.Seed {
		r, err := vars.FillRequest(sd)
		if err != nil {
			return nil, wrap(fmt.Sprintf("seed %d", i+1), err)
		}
		m, err := srv.Mark(ctx)
		if err != nil {
			return nil, wrap("mark", err)
		}
		saw, err := srv.Do(ctx, env, false, r)
		if err != nil {
			return nil, wrap(fmt.Sprintf("seed %d", i+1), err)
		}
		if saw.Status < 200 || saw.Status > 299 {
			return nil, wrap(fmt.Sprintf("seed %d", i+1), fmt.Errorf("HTTP %d %s", saw.Status, clip(string(saw.Body), 300)))
		}
		vs, err := srv.Since(ctx, m)
		if err != nil {
			return nil, wrap("since", err)
		}
		label(sd.Label, vs, saw)
	}
	sess, err := subj.Start(ctx, s, env)
	if err != nil {
		return nil, wrap("start", err)
	}
	run := &Run{}
	for i, st := range s.Steps {
		r, err := vars.FillRequest(st.Request)
		if err != nil {
			return nil, wrap(fmt.Sprintf("step %d", i+1), err)
		}
		rec := StepRecord{Session: st.Session, Request: r, Via: "direct"}
		for _, b := range st.Believed {
			fb, err := vars.Fill(string(b))
			if err != nil {
				return nil, wrap(fmt.Sprintf("step %d believed", i+1), err)
			}
			rec.Believed = append(rec.Believed, json.RawMessage(fb))
		}
		if st.ReadAfter != "" {
			rec.Read = vars.Versions[st.ReadAfter]
			parts := strings.SplitN(strings.SplitN(r.Path, "?", 2)[0], "/", 3)
			if len(parts) >= 2 {
				if rec.Prior, err = srv.Current(ctx, parts[0], parts[1]); err != nil {
					return nil, wrap("current", err)
				}
			}
		}
		m, err := srv.Mark(ctx)
		if err != nil {
			return nil, wrap("mark", err)
		}
		var saw Exchange
		handled := false
		if st.Session == "agent" {
			saw, handled, err = sess.Agent(ctx, i, st, r)
			if err != nil {
				return nil, wrap(fmt.Sprintf("step %d", i+1), err)
			}
			if handled {
				rec.Via = name
			}
		}
		if !handled {
			if saw, err = srv.Do(ctx, env, st.Session == "agent", r); err != nil {
				return nil, wrap(fmt.Sprintf("step %d", i+1), err)
			}
		}
		if st.LoseResponse {
			saw.Lost = true
		}
		rec.Saw = saw
		if rec.Versions, err = srv.Since(ctx, m); err != nil {
			return nil, wrap("since", err)
		}
		label(st.Label, rec.Versions, saw)
		run.Steps = append(run.Steps, rec)
	}
	var agentVersions []Version
	for _, st := range run.Steps {
		if st.Session == "agent" {
			agentVersions = append(agentVersions, st.Versions...)
		}
	}
	if run.Lookups, err = srv.Lookup(ctx, agentVersions); err != nil {
		return nil, wrap("lookup", err)
	}
	rep, err := sess.Finish(ctx, run)
	if err != nil {
		return nil, wrap("finish", err)
	}
	return &Result{Scenario: s.ID, Server: srv.Name(), Subject: name, Stored: Facts(run), Report: rep, Run: *run}, nil
}

func clip(s string, n int) string {
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}
