package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/deedglass/deedglass/internal/cli/ui"
	"github.com/deedglass/deedglass/internal/httpx"
	"github.com/deedglass/deedglass/internal/ledger"
	"github.com/deedglass/deedglass/pkg/deedglass"
)

// deedglass actions list --follow over the live event stream (S24): with an API key it follows
// GET /v1/events on the server and prints S15's lines as actions wait for a person or are sealed.
// A server without the stream (404) falls back to S15's polling of the Ledger in direct database
// mode, which is also what runs without an API key.

// The follow's own copy.
const (
	msgFollowNoScope   = "This key can't read events. Create a key with actions:read in Console › Agents."
	msgFollowResumed   = "Reconnecting " + ui.Sep + " resumed from %s " + ui.Sep + " nothing missed"
	msgFollowRefreshed = "Reconnecting " + ui.Sep + " some updates may have been missed " + ui.Sep + " the list was refreshed"
	msgFollowResync    = "Some live updates may have been missed " + ui.Sep + " the list was refreshed"
)

// errFollowNoStream means the server has no /v1/events: poll instead.
var errFollowNoStream = errors.New("the server has no event stream")

// followStream is a seam for tests (the reconnect schedule's sleep).
var followStream = func(s *deedglass.EventStream) {}

// runActionsFollowSSE follows the stream until Ctrl-C.
func runActionsFollowSSE(ctx context.Context, e *Env, c *deedglass.Client, f ledger.ActionFilter) error {
	ctx, stop := followStop(ctx)
	defer stop()
	st := e.Style
	var who struct {
		Org struct {
			Slug string `json:"slug"`
		} `json:"org"`
	}
	if _, err := c.Do(ctx, http.MethodGet, "/v1/whoami", nil, &who); err != nil {
		if ctx.Err() != nil {
			return nil
		}
		return apiErr(e, c, err)
	}
	start := followNow().Add(-2 * time.Second)
	fl := &follower{e: e, c: c, f: f, start: start, shown: map[string]string{}}
	s := &deedglass.EventStream{Client: c, Types: []string{"action.*", ledger.EventDeedSealed}, Agent: f.Agent}
	s.OnReconnect = func(time.Duration, error) {
		fl.reconnecting, fl.resumeFrom = true, s.LastEventID
	}
	followStream(s)
	hellos := 0
	err := s.Run(ctx, func(ev deedglass.StreamEvent) error {
		switch ev.Type {
		case "hello":
			hellos++
			var h deedglass.StreamHello
			_ = json.Unmarshal(ev.Data, &h)
			if hellos == 1 {
				what := "Following actions " + ui.Sep + " org " + who.Org.Slug
				if conn := actionsListValues.connector; conn != "" {
					what += " " + ui.Sep + " connector " + conn
				}
				fmt.Fprintln(e.Stdout, st.Dim(what+" "+ui.Sep+" live "+ui.Sep+" Ctrl-C to stop"))
				fl.reconnecting = false
				return nil
			}
			if fl.reconnecting && h.Resumed {
				fmt.Fprintln(e.Stdout, st.Violet(ui.Pending)+" "+fmt.Sprintf(msgFollowResumed, fl.resumeFrom))
				fl.reconnecting = false
			}
		case "resync":
			if fl.reconnecting {
				fmt.Fprintln(e.Stdout, st.Violet(ui.Pending)+" "+msgFollowRefreshed)
			} else {
				fmt.Fprintln(e.Stdout, st.Violet(ui.Pending)+" "+msgFollowResync)
			}
			fl.reconnecting = false
			fl.refresh(ctx)
		case "session_ended":
		default:
			fl.event(ctx, ev)
		}
		return nil
	})
	var se *deedglass.SessionEndedError
	var de *deedglass.Error
	switch {
	case err == nil:
		return nil
	case errors.As(err, &se):
		msg := se.Message
		if msg == "" {
			msg = "The server ended the stream."
		}
		return failNext(e, ExitNotAuthed, firstNonEmpty(se.Code, "session_ended"), msg, "")
	case errors.As(err, &de) && de.Status == http.StatusNotFound && hellos == 0:
		return errFollowNoStream
	case errors.As(err, &de) && de.Code == string(httpx.CodeInsufficientScope):
		return failNext(e, ExitNotAuthed, de.Code, msgFollowNoScope, "")
	}
	return apiErr(e, c, err)
}

// follower turns stream events into S15's lines.
type follower struct {
	e            *Env
	c            *deedglass.Client
	f            ledger.ActionFilter
	start        time.Time
	shown        map[string]string
	reconnecting bool
	resumeFrom   string
}

// event handles one action.* or deed.sealed event: a hydrated action waiting for approval prints
// at once; a sealed action deed fetches the action as sealed and prints it.
func (fl *follower) event(ctx context.Context, sev deedglass.StreamEvent) {
	var ev deedglass.Event
	if json.Unmarshal(sev.Data, &ev) != nil {
		return
	}
	switch {
	case strings.HasPrefix(ev.Type, "action."):
		var a ledger.Action
		if ev.Hydrated && json.Unmarshal(ev.Data.Object, &a) == nil && a.ID != "" {
			if a.Status == ledger.StatusPendingApproval {
				fl.show(a)
			}
			return
		}
		var thin struct {
			ID    string `json:"id"`
			State string `json:"state"`
		}
		if json.Unmarshal(ev.Data.Object, &thin) == nil && thin.State == "pending_approval" {
			fl.fetch(ctx, thin.ID)
		}
	case ev.Type == ledger.EventDeedSealed:
		var d struct {
			Kind   string  `json:"kind"`
			Action *string `json:"action"`
		}
		if json.Unmarshal(ev.Data.Object, &d) == nil && d.Action != nil && d.Kind != "admin" {
			fl.fetch(ctx, *d.Action)
		}
	}
}

// fetch reads one action and shows it.
func (fl *follower) fetch(ctx context.Context, id string) {
	var a ledger.Action
	if _, err := fl.c.Do(ctx, http.MethodGet, "/v1/actions/"+url.PathEscape(id), nil, &a); err != nil {
		if ctx.Err() == nil {
			fmt.Fprintln(fl.e.Stderr, fl.e.Style.Amber(ui.Unknown+" Couldn't read "+id+"; it is in deedglass actions list."))
		}
		return
	}
	fl.show(a)
}

// refresh lists the actions since the follow started (after a resync) and shows what changed.
func (fl *follower) refresh(ctx context.Context) {
	v := actionsListValues.values(followNow())
	v.Del("cursor")
	v.Set("created_at[gte]", fl.start.UTC().Format(time.RFC3339Nano))
	v.Set("limit", "100")
	var list ledger.List[ledger.Action]
	if _, err := fl.c.Do(ctx, http.MethodGet, "/v1/actions?"+v.Encode(), nil, &list); err != nil {
		if ctx.Err() == nil {
			fmt.Fprintln(fl.e.Stderr, fl.e.Style.Amber(ui.Unknown+" The Ledger didn't answer; the next event will catch up."))
		}
		return
	}
	for i := len(list.Data) - 1; i >= 0; i-- {
		fl.show(list.Data[i])
	}
}

// show prints a once per state worth a line, when it matches the flags.
func (fl *follower) show(a ledger.Action) {
	if time.Time(a.CreatedAt).Before(fl.start) || !followMatch(fl.f, a) {
		return
	}
	key, ok := followKey(a)
	if !ok || fl.shown[a.ID] == key {
		return
	}
	fl.shown[a.ID] = key
	writeFollowLine(fl.e.Stdout, fl.e.Style, a)
}

// followMatch applies actions list's filters to one action (the stream filters only by agent).
func followMatch(f ledger.ActionFilter, a ledger.Action) bool {
	in := func(vs []string, v string) bool { return len(vs) == 0 || slices.Contains(vs, v) }
	verdict, level := "", ""
	if a.Witness != nil {
		verdict, level = a.Witness.Verdict, a.Witness.Level
	}
	decision := ""
	if a.Policy.Decision != nil {
		decision = *a.Policy.Decision
	}
	switch {
	case f.Agent != "" && a.Agent.ID != f.Agent,
		!toolMatch(f.Tool, a.Tool),
		!in(f.States, a.State), !in(f.Decisions, decision), !in(f.Verdicts, verdict), !in(f.Levels, level),
		f.OnBehalfOf != "" && (a.OnBehalfOf == nil || *a.OnBehalfOf != f.OnBehalfOf):
		return false
	}
	if f.Q != "" {
		q := strings.ToLower(f.Q)
		code := ""
		if a.Execution != nil && a.Execution.ErrorCode != nil {
			code = *a.Execution.ErrorCode
		}
		if !strings.Contains(strings.ToLower(a.Tool), q) && !strings.Contains(strings.ToLower(code), q) {
			return false
		}
	}
	return true
}

// toolMatch is the --tool glob: exact, or * for any run of characters.
func toolMatch(glob, tool string) bool {
	if glob == "" {
		return true
	}
	parts := strings.Split(glob, "*")
	if len(parts) == 1 {
		return glob == tool
	}
	if !strings.HasPrefix(tool, parts[0]) {
		return false
	}
	rest := tool[len(parts[0]):]
	for i, p := range parts[1:] {
		last := i == len(parts)-2
		switch {
		case last:
			return strings.HasSuffix(rest, p)
		default:
			j := strings.Index(rest, p)
			if j < 0 {
				return false
			}
			rest = rest[j+len(p):]
		}
	}
	return true
}
