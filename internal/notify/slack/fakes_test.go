package slack

import (
	"context"
	"crypto/hmac"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/deedglass/deedglass/internal/approve"
	"github.com/deedglass/deedglass/internal/connector"
	slackc "github.com/deedglass/deedglass/internal/connector/slack"
	"github.com/deedglass/deedglass/internal/ids"
	"github.com/deedglass/deedglass/internal/store"
)

// Test fixtures: two orgs, each with a Slack connector (its own team, app and signing secret).
var (
	orgA  = ids.MustParse[ids.OrgKind]("org_01k6h7kp6msmzrhdzwtcr3jnka")
	orgB  = ids.MustParse[ids.OrgKind]("org_01k6h7kp6msmzrhdzwtcr3jnkb")
	conA  = "con_01k6h7kp6msmzrhdzwtcr3jnka"
	conB  = "con_01k6h7kp6msmzrhdzwtcr3jnkb"
	aprID = "apr_01k6h7kp6msmzrhdzwtcr3jnka"
	actID = "act_01k6h7kp6msmzrhdzwtcr3jnka"
	usrA  = ids.MustParse[ids.UserKind]("usr_01k6h7kp6msmzrhdzwtcr3jnka")
	ctxDg = "sha256:3f9a" + strings.Repeat("0", 56) + "c21e"
	t0    = time.Date(2026, 10, 10, 4, 14, 0, 0, time.UTC)
)

const (
	secretA = "0123456789abcdef0123456789abcdef"
	secretB = "fedcba9876543210fedcba9876543210"
)

// fakeStore is Store in memory.
type fakeStore struct {
	mu     sync.Mutex
	routes map[string]struct {
		org ids.OrgID
		con string
	} // team/app → route
	members  map[string]ids.UserID // lowercase email → user (org A only)
	replays  map[string]bool
	card     *store.ApprovalCardRow
	cardErr  error
	has      bool
	msgs     []store.NotificationMessage
	jobs     []string // "action@delay" records
	shown    map[int64]int
	failed   []string
	inserted []store.NotificationMessage
	routeErr error
}

func newFakeStore() *fakeStore {
	f := &fakeStore{routes: map[string]struct {
		org ids.OrgID
		con string
	}{}, members: map[string]ids.UserID{}, replays: map[string]bool{}, shown: map[int64]int{}, has: true}
	f.routes["T0AAAAAAA/A0AAAAAAA"] = struct {
		org ids.OrgID
		con string
	}{orgA, conA}
	f.routes["T0BBBBBBB/A0BBBBBBB"] = struct {
		org ids.OrgID
		con string
	}{orgB, conB}
	f.members["priya@acme.com"] = usrA
	f.card = cardRow("pending", nil)
	return f
}

func (f *fakeStore) SlackRoute(_ context.Context, team, app string) (ids.OrgID, string, bool, error) {
	if f.routeErr != nil {
		return ids.OrgID{}, "", false, f.routeErr
	}
	r, ok := f.routes[team+"/"+app]
	return r.org, r.con, ok, nil
}

func (f *fakeStore) Connector(_ context.Context, org ids.OrgID, id string) (store.Connector, error) {
	if (org == orgA && id == conA) || (org == orgB && id == conB) {
		return store.Connector{ID: id, Name: "slack", Type: "slack", Status: "active", ConfigHash: "sha256:" + id[4:]}, nil
	}
	return store.Connector{}, store.ErrNotFound
}

func (f *fakeStore) RecordInbound(_ context.Context, org ids.OrgID, digest string, _ time.Time) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	k := org.String() + digest
	if f.replays[k] {
		return false, nil
	}
	f.replays[k] = true
	return true, nil
}

func (f *fakeStore) MemberByEmail(_ context.Context, org ids.OrgID, email string) (ids.UserID, error) {
	if org != orgA {
		return ids.UserID{}, store.ErrNotFound
	}
	if u, ok := f.members[email]; ok {
		return u, nil
	}
	return ids.UserID{}, store.ErrNotFound
}

func (f *fakeStore) ApprovalCard(context.Context, ids.OrgID, string) (*store.ApprovalCardRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.cardErr != nil {
		return nil, f.cardErr
	}
	c := *f.card
	return &c, nil
}

func (f *fakeStore) CardState(ctx context.Context, org ids.OrgID, apr string) (CardState, error) {
	r, err := f.ApprovalCard(ctx, org, apr)
	if err != nil {
		return CardState{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return CardState{Row: r, Conn: store.Connector{ID: conA, Name: "slack", Type: "slack"}, Msgs: append([]store.NotificationMessage(nil), f.msgs...), Has: f.has}, nil
}

func (f *fakeStore) InsertMessage(_ context.Context, m store.NotificationMessage) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	m.ID = int64(len(f.msgs) + 1)
	m.State = "posted"
	f.msgs = append(f.msgs, m)
	f.inserted = append(f.inserted, m)
	return nil
}

func (f *fakeStore) MarkShown(_ context.Context, _ ids.OrgID, id int64, version int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range f.msgs {
		if f.msgs[i].ID == id && f.msgs[i].Version <= version {
			f.msgs[i].Version, f.msgs[i].State = version, "updated"
		}
	}
	f.shown[id] = version
	return nil
}

func (f *fakeStore) MarkFailed(_ context.Context, _ ids.OrgID, _ string, lastError string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failed = append(f.failed, lastError)
	return nil
}

func (f *fakeStore) ApprovalOfAction(_ context.Context, _ ids.OrgID, action string) (string, error) {
	if action == actID {
		return aprID, nil
	}
	return "", store.ErrNotFound
}

func (f *fakeStore) Enqueue(_ context.Context, _ ids.OrgID, action string, _ time.Time, _ int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.jobs = append(f.jobs, action)
	return nil
}

func (f *fakeStore) EnqueueIfCard(_ context.Context, _ ids.OrgID, action string, _ time.Time, _ int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.msgs) == 0 {
		return nil
	}
	f.jobs = append(f.jobs, "ifcard:"+action)
	return nil
}

func (f *fakeStore) jobCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.jobs)
}

// cardRow is an approval row with a snapshot like S22's.
func cardRow(status string, votes []store.CardVote) *store.ApprovalCardRow {
	snap := map[string]any{"v": 1,
		"action": map[string]any{"id": actID, "tool": "github.create_pr", "connector": "github", "connector_type": "github",
			"request_digest": "sha256:aa00" + strings.Repeat("0", 56) + "0bb1"},
		"agent":        map[string]any{"name": "support-bot"},
		"on_behalf_of": nil,
		"policy": map[string]any{"name": "acme-demo", "version": 7, "rule_id": "pull-requests-to-main-need-approval",
			"reason": "Pull requests into main wait for a human.", "required": 1, "approvers": []string{"role:admin"}},
		"args":       map[string]any{"repo": "arjun/deedglass-sandbox", "head": "agent/fix-typo", "base": "main", "title": "Fix typo in README"},
		"expires_at": t0.Add(30 * time.Minute).Format(time.RFC3339)}
	b, _ := json.Marshal(snap)
	req, _ := json.Marshal(map[string]any{"github": map[string]any{"repo": "arjun/deedglass-sandbox", "head": "agent/fix-typo", "base": "main", "enriched": true}})
	return &store.ApprovalCardRow{ApprovalID: aprID, ActionID: actID, Status: status, Context: b, ContextDigest: ctxDg, Required: 1,
		ExpiresAt: t0.Add(30 * time.Minute), Votes: votes, Tool: "github.create_pr", AgentName: "support-bot", ActionState: "pending_approval",
		PolicyRequest: req, Notify: []string{"slack"}}
}

// fakeAPI is the Slack API in memory. VerifyInteraction checks the real v0 signature with the
// connector's own secret, so a request signed for another org fails exactly as in production.
type fakeAPI struct {
	mu        sync.Mutex
	users     map[string]slackc.User
	slow      time.Duration
	domains   []string
	posts     int
	updates   []string // channel/ts
	views     []json.RawMessage
	responds  []string
	postErr   error
	updateErr error
	viewErr   error
}

func newFakeAPI() *fakeAPI {
	return &fakeAPI{users: map[string]slackc.User{
		"U0PRIYA":     {ID: "U0PRIYA", TeamID: "T0AAAAAAA", Name: "priya", RealName: "Priya Raman", Email: "priya@acme.com", EmailConfirmed: true},
		"U0GUEST":     {ID: "U0GUEST", TeamID: "T0AAAAAAA", Email: "guest@acme.com", EmailConfirmed: true, IsRestricted: true},
		"U0ULTRA":     {ID: "U0ULTRA", TeamID: "T0AAAAAAA", Email: "ultra@acme.com", EmailConfirmed: true, IsUltraRestricted: true},
		"U0BOT":       {ID: "U0BOT", TeamID: "T0AAAAAAA", Email: "bot@acme.com", EmailConfirmed: true, IsBot: true},
		"U0GONE":      {ID: "U0GONE", TeamID: "T0AAAAAAA", Email: "gone@acme.com", EmailConfirmed: true, Deleted: true},
		"U0UNCONF":    {ID: "U0UNCONF", TeamID: "T0AAAAAAA", Email: "priya@acme.com", EmailConfirmed: false},
		"U0OTHER":     {ID: "U0OTHER", TeamID: "T0AAAAAAA", Email: "someone@else.com", EmailConfirmed: true},
		"U0NOMAIL":    {ID: "U0NOMAIL", TeamID: "T0AAAAAAA", EmailConfirmed: true},
		"U0WRONGTEAM": {ID: "U0WRONGTEAM", TeamID: "T0ZZZZZZZ", Email: "priya@acme.com", EmailConfirmed: true},
	}}
}

func secretOf(inst *connector.Instance) string {
	if inst.ID.String() == conB {
		return secretB
	}
	return secretA
}

func (a *fakeAPI) Approvals(*connector.Instance) (slackc.ApprovalSettings, error) {
	return slackc.ApprovalSettings{Enabled: true, Channel: "#deedglass-approvals", RequireEmailDomains: a.domains}, nil
}

func (a *fakeAPI) PostApproval(_ context.Context, _ *connector.Instance, blocks json.RawMessage, _ string) (string, string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.postErr != nil {
		return "", "", a.postErr
	}
	if !json.Valid(blocks) {
		return "", "", errors.New("bad blocks")
	}
	a.posts++
	return "C0APPROVALS", "1760069640.000100", nil
}

func (a *fakeAPI) UpdateApproval(_ context.Context, _ *connector.Instance, ch, ts string, _ json.RawMessage, _ string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.updateErr != nil {
		return a.updateErr
	}
	a.updates = append(a.updates, ch+"/"+ts)
	return nil
}

func (a *fakeAPI) LookupUser(ctx context.Context, _ *connector.Instance, id string) (slackc.User, error) {
	if a.slow > 0 {
		select {
		case <-time.After(a.slow):
		case <-ctx.Done():
			return slackc.User{}, ctx.Err()
		}
	}
	u, ok := a.users[id]
	if !ok {
		return slackc.User{}, &connector.Error{Kind: connector.KindNotFound, Code: connector.CodeUpstreamNotFound, AgentMessage: "user_not_found"}
	}
	return u, nil
}

func (a *fakeAPI) OpenView(_ context.Context, _ *connector.Instance, trigger string, view json.RawMessage) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.viewErr != nil {
		return a.viewErr
	}
	a.views = append(a.views, view)
	return nil
}

func (a *fakeAPI) Respond(_ context.Context, _ *connector.Instance, _ string, text string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.responds = append(a.responds, text)
	return nil
}

func (a *fakeAPI) VerifyInteraction(_ context.Context, inst *connector.Instance, h http.Header, body []byte, now time.Time) error {
	ts := h.Get("X-Slack-Request-Timestamp")
	n, err := strconv.ParseInt(ts, 10, 64)
	if err != nil || now.Unix()-n > 300 || n-now.Unix() > 300 {
		return errors.New("timestamp")
	}
	want := slackc.Sign([]byte(secretOf(inst)), ts, body)
	if !hmac.Equal([]byte(want), []byte(h.Get("X-Slack-Signature"))) {
		return errors.New("signature")
	}
	return nil
}

func (a *fakeAPI) said() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.responds...)
}

// fakeConns turns rows into instances.
type fakeConns struct {
	sums map[string]connector.Connector
}

func (c fakeConns) Instance(org ids.OrgID, row store.Connector) (*connector.Instance, error) {
	id, err := ids.Parse[ids.ConnectorKind](row.ID)
	if err != nil {
		return nil, err
	}
	return connector.NewInstance(id, org, row.Name, row.Type, json.RawMessage(`{}`), row.ConfigHash, connector.Env{Name: "dev"}, nil), nil
}

func (c fakeConns) Type(t string) (connector.Connector, bool) {
	x, ok := c.sums[t]
	return x, ok
}

// fakeDecider records Decide calls and answers err.
type fakeDecider struct {
	mu    sync.Mutex
	calls []approve.DecideInput
	err   error
}

func (d *fakeDecider) Decide(_ context.Context, in approve.DecideInput) (*approve.Approval, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.calls = append(d.calls, in)
	if d.err != nil {
		return nil, d.err
	}
	return &approve.Approval{Action: actID}, nil
}

func (d *fakeDecider) count() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.calls)
}
