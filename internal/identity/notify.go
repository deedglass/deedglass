package identity

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"

	"github.com/deedglass/deedglass/internal/ids"
	"github.com/deedglass/deedglass/internal/store"
)

// identityMsg is a deedglass_identity payload (S08 §3.6), at most 200 bytes:
//
//	{"v":1,"t":"agent","org":"org_…","id":"agt_…","s":"stopped"}
//	{"v":1,"t":"key","org":"org_…","id":"key_…","p":"jJbEEMWk","s":"revoked"}
//	{"v":1,"t":"org","org":"org_…","id":"org_…","s":"stopped"}
//	{"v":1,"t":"ping","from":"<instance>","n":42}
//
// It never carries a secret, a name or a reason. "p" is a key's public prefix, the cache's key.
type identityMsg struct {
	V    int    `json:"v"`
	T    string `json:"t"` // agent | key | org | member | ping
	Org  string `json:"org,omitempty"`
	ID   string `json:"id,omitempty"`
	P    string `json:"p,omitempty"`
	S    string `json:"s,omitempty"`
	From string `json:"from,omitempty"`
	N    uint64 `json:"n,omitempty"`
}

func (m identityMsg) encode() (string, error) {
	m.V = 1
	b, err := json.Marshal(m)
	if err != nil {
		return "", err
	}
	if len(b) > store.MaxNotifyPayload {
		return "", errors.New("identity: notification payload over 200 bytes")
	}
	return string(b), nil
}

func decodeIdentityMsg(payload string) (identityMsg, bool) {
	var m identityMsg
	if len(payload) > store.MaxNotifyPayload || json.Unmarshal([]byte(payload), &m) != nil || m.V != 1 || m.T == "" {
		return identityMsg{}, false
	}
	return m, true
}

// notify sends m on deedglass_identity inside tx: delivered only if tx commits, and a failure
// rolls the whole change back.
func (s *Service) notify(ctx context.Context, tx pgx.Tx, m identityMsg) error {
	if s.failNotify != nil {
		if err := s.failNotify(); err != nil {
			return err
		}
	}
	p, err := m.encode()
	if err != nil {
		return err
	}
	return s.db.NotifyIdentity(ctx, tx, p)
}

func parseAgent(s string) ids.AgentID {
	id, _ := ids.Parse[ids.AgentKind](s)
	return id
}

func parseOrg(s string) ids.OrgID {
	id, _ := ids.Parse[ids.OrgKind](s)
	return id
}
