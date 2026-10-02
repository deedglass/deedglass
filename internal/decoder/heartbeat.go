package decoder

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"
	"time"

	"github.com/deedglass/deedglass/internal/ids"
)

// The heartbeat (S20 §3.8). Every decoder.heartbeat_interval the runner emits a NON-transactional
// deedglass message on the watched database:
//
//	hb1:<connector_id>:<unix_ms>:<hex hmac-sha256>
//
// The HMAC is over "deedglass/heartbeat/v1\n" + connector_id + ":" + unix_ms with a 32-byte key the
// runner generates at start and holds only in memory. Decoding a valid one shows the slot is
// moving; anything else under hb1: is a forged non-transactional marker.

const heartbeatPrefix = "hb1:"

// HeartbeatVerdict is what a non-transactional deedglass message turned out to be.
type HeartbeatVerdict int

const (
	HeartbeatValid   HeartbeatVerdict = iota // ours, from this runner: the slot is moving
	HeartbeatStale                           // ours by shape, dated before this runner started: counted, ignored
	HeartbeatForeign                         // well-formed for another connector watching the same database: ignored
	HeartbeatForged                          // anything else: forged_marker_nontx
)

// Heartbeat signs and checks one runner's heartbeats.
type Heartbeat struct {
	key       []byte
	connector string
	started   int64 // unix ms
}

// NewHeartbeat returns a heartbeat with a fresh key for connector, started now.
func NewHeartbeat(connector string, now time.Time) *Heartbeat {
	k := make([]byte, 32)
	if _, err := rand.Read(k); err != nil {
		panic(err) // crypto/rand never fails on supported platforms
	}
	return &Heartbeat{key: k, connector: connector, started: now.UnixMilli()}
}

func (h *Heartbeat) mac(ms string) string {
	m := hmac.New(sha256.New, h.key)
	m.Write([]byte("deedglass/heartbeat/v1\n" + h.connector + ":" + ms))
	return hex.EncodeToString(m.Sum(nil))
}

// Content is the message content for a heartbeat at now.
func (h *Heartbeat) Content(now time.Time) string {
	ms := strconv.FormatInt(now.UnixMilli(), 10)
	return heartbeatPrefix + h.connector + ":" + ms + ":" + h.mac(ms)
}

// Check classifies a non-transactional deedglass message's content.
func (h *Heartbeat) Check(content []byte) HeartbeatVerdict {
	s := string(content)
	if !strings.HasPrefix(s, heartbeatPrefix) {
		return HeartbeatForged
	}
	parts := strings.Split(s[len(heartbeatPrefix):], ":")
	if len(parts) != 3 || len(parts[2]) != 64 || !validID[ids.ConnectorKind](parts[0]) {
		return HeartbeatForged
	}
	ms, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || ms <= 0 || parts[1] != strconv.FormatInt(ms, 10) {
		return HeartbeatForged
	}
	if _, err := hex.DecodeString(parts[2]); err != nil || strings.ToLower(parts[2]) != parts[2] {
		return HeartbeatForged
	}
	if parts[0] != h.connector {
		return HeartbeatForeign
	}
	if ms < h.started {
		return HeartbeatStale
	}
	if !hmac.Equal([]byte(parts[2]), []byte(h.mac(parts[1]))) {
		return HeartbeatForged
	}
	return HeartbeatValid
}
