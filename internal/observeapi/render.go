package observeapi

import (
	"time"

	"github.com/deedglass/deedglass/internal/caught"
	"github.com/deedglass/deedglass/internal/observe"
)

// SessionJSON is the observe_session object.
type SessionJSON struct {
	Object      string        `json:"object"` // "observe_session"
	ID          string        `json:"id"`
	Connector   ConnectorJSON `json:"connector"`
	Label       *string       `json:"label"`
	Agent       *string       `json:"agent"`
	State       string        `json:"state"`
	OpenedAt    time.Time     `json:"opened_at"`
	ClosedAt    *time.Time    `json:"closed_at"`
	OpenedBy    ActorJSON     `json:"opened_by"`
	ClosedBy    *ActorJSON    `json:"closed_by"`
	Flags       []string      `json:"flags"`
	Watched     []string      `json:"watched_tables"`
	Summary     SummaryJSON   `json:"summary"`
	Caught      []caught.Item `json:"caught"`
	Seal        *SealJSON     `json:"seal"`
	Undo        UndoJSON      `json:"undo"`
	Notes       []string      `json:"notes"`
	Observed    string        `json:"observed"`
	Attribution string        `json:"attribution"`
	Frames      *FrameList    `json:"transactions,omitempty"`
}

// ConnectorJSON names the session's connector.
type ConnectorJSON struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// ActorJSON is who opened or closed a session.
type ActorJSON struct {
	Type string  `json:"type"`
	ID   *string `json:"id"`
}

// SummaryJSON is the session's counts.
type SummaryJSON struct {
	Transactions int                       `json:"transactions"`
	Excluded     int                       `json:"excluded"`
	Expired      int                       `json:"expired"`
	Rows         int                       `json:"rows"`
	Tables       map[string]observe.Counts `json:"tables"`
	FirstCommit  *time.Time                `json:"first_commit"`
	LastCommit   *time.Time                `json:"last_commit"`
}

// SealJSON is the seal deed.
type SealJSON struct {
	Deed string `json:"deed"`
}

// UndoJSON is the session's undo state.
type UndoJSON struct {
	Action   *string `json:"action"`
	Possible bool    `json:"possible"`
	FreePath string  `json:"free_path"`
}

// FrameList is the session's transactions.
type FrameList struct {
	Object string      `json:"object"` // "list"
	Total  int         `json:"total"`
	Data   []FrameJSON `json:"data"`
}

// FrameJSON is one transaction of a session.
type FrameJSON struct {
	ID         string                    `json:"id"`
	CommitTime *time.Time                `json:"commit_time"`
	CommitLSN  string                    `json:"commit_lsn"`
	Xid        int64                     `json:"xid"`
	Tables     map[string]observe.Counts `json:"tables"`
	Changes    int                       `json:"changes"`
	Excluded   bool                      `json:"excluded"`
	Expired    bool                      `json:"expired"`
	Flags      []string                  `json:"flags"`
	Diff       *observe.Diff             `json:"diff"`
}

// List is a page of sessions.
type List struct {
	Object     string        `json:"object"`
	Data       []SessionJSON `json:"data"`
	HasMore    bool          `json:"has_more"`
	NextCursor *string       `json:"next_cursor"`
}

// Render is a session as the API returns it; frames (with diffs by frame id) only on the single read.
func Render(x *observe.Session, frames []observe.FrameFacts, diffs map[string]observe.Diff) SessionJSON {
	r := x.Row
	out := SessionJSON{Object: "observe_session", ID: r.ID, Connector: ConnectorJSON{ID: r.ConnectorID, Name: x.Connector.Name},
		Label: r.Label, Agent: r.AgentID, State: r.State, OpenedAt: r.OpenedAt, ClosedAt: r.ClosedAt,
		OpenedBy: ActorJSON{Type: r.OpenedByType, ID: r.OpenedByID}, Flags: append([]string{}, r.Flags...),
		Watched: append([]string{}, x.Connector.Watched...), Caught: x.Summary.Caught, Notes: observe.FlagNotes(r.Flags),
		Observed: observe.NoteObserved, Attribution: observe.NoteAttribution,
		Summary: SummaryJSON{Transactions: x.Summary.Transactions, Excluded: x.Summary.Excluded, Expired: x.Summary.Expired, Rows: x.Summary.Rows,
			Tables: x.Summary.Tables, FirstCommit: x.Summary.FirstCommit, LastCommit: x.Summary.LastCommit}}
	if out.Watched == nil {
		out.Watched = []string{}
	}
	if out.Notes == nil {
		out.Notes = []string{}
	}
	if out.Caught == nil {
		out.Caught = []caught.Item{}
	}
	if r.ClosedByType != nil {
		out.ClosedBy = &ActorJSON{Type: *r.ClosedByType, ID: r.ClosedByID}
	}
	if r.SealDeedID != nil {
		out.Seal = &SealJSON{Deed: *r.SealDeedID}
	}
	out.Undo = UndoJSON{Action: r.UndoActionID, Possible: x.Frames != nil && x.Undoable(), FreePath: caught.FreeSessionPath(r.ID)}
	if x.Summary.Expired > 0 && !contains(out.Flags, observe.FlagFramesExpired) {
		out.Flags = append(out.Flags, observe.FlagFramesExpired)
		out.Notes = append(out.Notes, observe.NoteExpired)
	}
	if frames != nil {
		fl := &FrameList{Object: "list", Total: len(frames), Data: []FrameJSON{}}
		for i := len(frames) - 1; i >= 0; i-- { // newest first
			f := frames[i]
			fj := FrameJSON{ID: f.ID, CommitTime: f.CommitTime, CommitLSN: f.CommitLSN, Xid: f.Xid, Tables: f.Counts, Changes: f.Total,
				Excluded: f.Excluded, Expired: f.Expired, Flags: f.Flags}
			if fj.Flags == nil {
				fj.Flags = []string{}
			}
			if d, ok := diffs[f.ID]; ok {
				dd := d
				fj.Diff = &dd
			}
			fl.Data = append(fl.Data, fj)
		}
		out.Frames = fl
	}
	return out
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}
