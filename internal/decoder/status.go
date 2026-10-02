package decoder

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/deedglass/deedglass/internal/store"
)

// View is the decoder object (GET /v1/connectors/{id}/decoder; deedglass decoder status --json).
type View struct {
	Object          string           `json:"object"` // "decoder"
	Connector       string           `json:"connector"`
	Name            string           `json:"name"`
	Slot            string           `json:"slot"`
	Publication     string           `json:"publication"`
	Status          string           `json:"status"`
	Health          string           `json:"health"` // healthy | degraded | halted | waiting | connecting | pending | dropped | disabled
	Reason          *string          `json:"reason"`
	Detail          map[string]any   `json:"detail"`
	Since           time.Time        `json:"since"`
	Transport       string           `json:"transport"` // peek
	Server          ViewServer       `json:"server"`
	Positions       ViewPositions    `json:"positions"`
	Lag             ViewLag          `json:"lag"`
	WALStatus       *string          `json:"wal_status"`
	Watching        []string         `json:"watching"`
	LastCommit      *ViewCommit      `json:"last_commit"`
	LastHeartbeatAt *time.Time       `json:"last_heartbeat_at"`
	Frames          map[string]int64 `json:"frames"`
	Recent          []ViewFrame      `json:"recent"`
	UpdatedAt       time.Time        `json:"updated_at"`
}

// ViewServer is the watched server.
type ViewServer struct {
	Version  *string `json:"version"`
	SystemID *string `json:"system_id"`
	Timeline *int    `json:"timeline"`
}

// ViewPositions are the slot's WAL positions.
type ViewPositions struct {
	CheckpointLSN     *string `json:"checkpoint_lsn"`
	ConfirmedFlushLSN *string `json:"confirmed_flush_lsn"`
	RestartLSN        *string `json:"restart_lsn"`
	CurrentWALLSN     *string `json:"current_wal_lsn"`
}

// ViewLag is how far behind the decoder is.
type ViewLag struct {
	Bytes         *int64   `json:"bytes"`          // from the slot's confirmed position to the server's
	Seconds       *float64 `json:"seconds"`        // since the decoder last read everything flushed
	RetainedBytes *int64   `json:"retained_bytes"` // WAL the slot keeps (from restart_lsn)
}

// ViewCommit is the last decoded commit.
type ViewCommit struct {
	LSN string     `json:"lsn"`
	At  *time.Time `json:"at"`
}

// ViewFrame is one recent frame (no images).
type ViewFrame struct {
	CommitLSN *string                     `json:"commit_lsn"`
	Class     string                      `json:"class"`
	Action    *string                     `json:"action"`
	Counts    map[string]map[string]int64 `json:"counts"`
	Flags     []string                    `json:"flags"`
	At        time.Time                   `json:"at"`
}

// MaxRecent is the most recent frames a view carries (?recent=N).
const MaxRecent = 50

// BuildView turns a slot row and its recent frames into the decoder object.
func BuildView(s store.DecoderSlot, name string, recent []store.DecodedFrame) View {
	v := View{Object: "decoder", Connector: s.ConnectorID, Name: name, Slot: s.SlotName, Publication: s.Publication,
		Status: s.Status, Health: HealthWord(s.Status), Reason: s.StatusReason, Detail: map[string]any{}, Since: s.StatusSince.UTC(),
		Transport: "peek", WALStatus: s.WALStatus, Watching: s.Watching, LastHeartbeatAt: s.LastHeartbeatAt,
		Frames: map[string]int64{}, Recent: []ViewFrame{}, UpdatedAt: s.UpdatedAt.UTC()}
	if len(s.StatusDetail) > 0 {
		_ = json.Unmarshal(s.StatusDetail, &v.Detail)
	}
	if v.Watching == nil {
		v.Watching = []string{}
	}
	if s.ServerVersionNum != nil {
		ver := VersionText(*s.ServerVersionNum)
		v.Server.Version = &ver
	}
	v.Server.SystemID, v.Server.Timeline = s.SystemID, s.TimelineID
	v.Positions = ViewPositions{CheckpointLSN: s.CheckpointLSN, ConfirmedFlushLSN: s.ConfirmedFlushLSN, RestartLSN: s.RestartLSN,
		CurrentWALLSN: s.CurrentWALLSN}
	v.Lag = ViewLag{Bytes: s.LagBytes, Seconds: s.LagSeconds, RetainedBytes: s.RetainedBytes}
	if s.LastCommitLSN != nil {
		v.LastCommit = &ViewCommit{LSN: *s.LastCommitLSN, At: s.LastCommitAt}
	}
	for _, c := range Classes {
		v.Frames[string(c)] = s.Frames[string(c)]
	}
	for _, f := range recent {
		vf := ViewFrame{CommitLSN: f.CommitLSN, Class: f.Class, Action: f.ActionID, Counts: map[string]map[string]int64{},
			Flags: f.Flags, At: f.CreatedAt.UTC()}
		if vf.Flags == nil {
			vf.Flags = []string{}
		}
		if len(f.Counts) > 0 {
			_ = json.Unmarshal(f.Counts, &vf.Counts)
		}
		v.Recent = append(v.Recent, vf)
	}
	return v
}

// HealthWord is the one word a status shows as.
func HealthWord(status string) string {
	switch status {
	case StatusRunning:
		return "healthy"
	case "":
		return "pending"
	}
	return status
}

// VersionText turns server_version_num into 18.6 (or 9.6.24 style for old servers).
func VersionText(n int) string {
	if n >= 100000 {
		return fmt.Sprintf("%d.%d", n/10000, n%10000)
	}
	return fmt.Sprintf("%d.%d.%d", n/10000, n/100%100, n%100)
}
