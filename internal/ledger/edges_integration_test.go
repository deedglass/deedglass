//go:build integration

package ledger_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/deedglass/deedglass/internal/deed"
	"github.com/deedglass/deedglass/internal/httpx"
	"github.com/deedglass/deedglass/internal/ledger"
	"github.com/deedglass/deedglass/internal/ledger/fake"
)

// Edge and error paths: HTTP refusals, a read-only Ledger, deed filters, admin-event and amendment
// validation, retention settings.
func TestEdges(t *testing.T) {
	en := newEnv(t, ledger.WithIssuer("https://api.deedglass.example"))
	res := en.run(t, en.org, en.agents, 42, 120)

	// HTTP refusals, through a read-only Ledger (no seal key).
	ro := ledger.New(en.e.DB, nil, nil, ledger.WithLogger(slog.New(slog.DiscardHandler)))
	if ro.SignerKeyID() != "" || en.led.SignerKeyID() != en.signer.KeyID() || en.led.Issuer() != "https://api.deedglass.example" {
		t.Fatal("accessors")
	}
	srv := httpx.New(httpx.Options{Logger: slog.New(slog.DiscardHandler), Sessions: httpx.DevPrincipal{}})
	srv.Mount(ledger.Routes(ro)...)
	ts := httptest.NewServer(srv)
	defer ts.Close()
	get := func(path string, hdr ...string) (int, string) {
		req, _ := http.NewRequest("GET", ts.URL+path, nil)
		req.Header.Set(httpx.DevOrgHeader, en.org.String())
		for i := 0; i+1 < len(hdr); i += 2 {
			req.Header.Set(hdr[i], hdr[i+1])
		}
		r, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer r.Body.Close()
		b, _ := io.ReadAll(r.Body)
		return r.StatusCode, string(b)
	}
	sd, err := en.led.GetDeed(ctx, en.org, "1")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		path, accept string
		code         int
		want         string
	}{
		{"/v1/deeds/" + sd.ID, "application/cose", 406, "COSE output is not enabled"},
		{"/v1/deeds/deed_01j9zt3q5s7v9w1y3a5c7e9g1j", "", 404, "No deed deed_01j9zt3q5s7v9w1y3a5c7e9g1j in this org"},
		{"/v1/deeds/812", "", 404, "No deed 812 in this org"},
		{"/v1/deeds/" + sd.ID + "?pretty=1", "", 400, "takes no query parameters"},
		{"/v1/actions/" + res.IDs[0] + "?expand=events", "", 400, "takes no query parameters"},
		{"/v1/deeds?order=sideways", "", 400, `"param": "order"`},
		{"/v1/deeds/export?from_seq=9&to_seq=2", "", 400, `"param": "from_seq"`},
		{"/v1/ledger/check?from_seq=5000", "", 404, "The chain ends at"},
		{"/v1/ledger/check?issued_at[gte]=yesterday", "", 400, `"param": "issued_at[gte]"`},
		{"/v1/ledger/check?from_seq=10&to_seq=20", "", 200, `"status": "intact"`},
		{"/v1/actions/" + strings.Repeat("x", 80), "", 404, "No action with that id in this org"},
	} {
		code, body := get(c.path, "Accept", c.accept)
		if code != c.code || !strings.Contains(body, c.want) {
			t.Errorf("%s: %d %s", c.path, code, body)
		}
	}
	empty, _ := en.newOrg(t, "hollow")
	srv2 := httpx.New(httpx.Options{Logger: slog.New(slog.DiscardHandler), Sessions: httpx.DevPrincipal{}})
	srv2.Mount(ledger.Routes(en.led)...)
	ts2 := httptest.NewServer(srv2)
	defer ts2.Close()
	req, _ := http.NewRequest("GET", ts2.URL+"/v1/deeds/export", nil)
	req.Header.Set(httpx.DevOrgHeader, empty.String())
	r, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(r.Body)
	r.Body.Close()
	if r.StatusCode != 404 || !strings.Contains(string(b), "no deeds yet") {
		t.Errorf("empty org export: %d %s", r.StatusCode, b)
	}
	if _, err := ro.Export(ctx, en.org, ledger.Range{}, io.Discard); !errors.Is(err, ledger.ErrNoSigner) {
		t.Errorf("read-only export: %v", err)
	}

	// Deed filters: kind, action, descending pages.
	page := func(q string) *ledger.List[json.RawMessage] {
		v, _ := url.ParseQuery(q)
		f, p, err := ledger.ParseDeedQuery(v)
		if err != nil {
			t.Fatal(err)
		}
		l, err := en.led.ListDeeds(ctx, en.org, f, p)
		if err != nil {
			t.Fatal(err)
		}
		return l
	}
	var amended string
	for _, id := range res.IDs {
		d, _ := en.led.GetAction(ctx, en.org, id)
		if len(d.Deeds) == 2 {
			amended = id
			break
		}
	}
	if amended == "" {
		t.Fatal("no amended action in seed 42 / 120")
	}
	if l := page("action=" + amended); len(l.Data) != 2 {
		t.Errorf("deeds of one action: %d", len(l.Data))
	}
	if l := page("kind=amendment"); len(l.Data) == 0 {
		t.Error("no amendments listed")
	}
	first := page("order=desc&limit=10&from_seq=5&to_seq=40")
	if !first.HasMore {
		t.Fatal("expected more")
	}
	v := url.Values{"order": {"desc"}, "limit": {"10"}, "from_seq": {"5"}, "to_seq": {"40"}, "cursor": {*first.NextCursor}}
	f, p, _ := ledger.ParseDeedQuery(v)
	second, err := en.led.ListDeeds(ctx, en.org, f, p)
	if err != nil || len(second.Data) != 10 {
		t.Fatalf("%v %d", err, len(second.Data))
	}
	var e1, e2 deed.Envelope
	_ = json.Unmarshal(first.Data[9], &e1)
	_ = json.Unmarshal(second.Data[0], &e2)
	if e1.Body.Seq != 31 || e2.Body.Seq != 30 {
		t.Fatalf("desc pages: %d then %d", e1.Body.Seq, e2.Body.Seq)
	}
	if _, err := en.led.GetDeed(ctx, en.org, "not-a-deed"); !errors.Is(err, ledger.ErrNotFound) {
		t.Error(err)
	}
	if _, err := en.led.GetAction(ctx, en.org, "act_bad"); !errors.Is(err, ledger.ErrNotFound) {
		t.Error(err)
	}

	// Range by issued_at, for export and check.
	from, _ := deed.ParseTime(e2.Body.IssuedAt)
	var jl strings.Builder
	if _, _, n, err := en.led.ExportJSONL(ctx, en.org, ledger.Range{IssuedGTE: &from}, &jl); err != nil || n == 0 {
		t.Fatalf("by time: %d %v", n, err)
	}
	var emptyErr *ledger.EmptyRangeError
	if _, _, _, err := en.led.ExportJSONL(ctx, en.org, ledger.Range{FromSeq: 9000}, io.Discard); !errors.As(err, &emptyErr) || emptyErr.Error() == "" {
		t.Errorf("jsonl empty: %v", err)
	}

	// Admin events: refusals before anything is written.
	for _, e := range []ledger.AdminEvent{
		{Action: ledger.AdminLedgerPruned, Actor: deed.Ref{Type: "system", ID: "operator"}, Reason: strings.Repeat("r", 281)},
		{Action: ledger.AdminLedgerPruned, Actor: deed.Ref{Type: "robot", ID: "x"}},
		{Action: ledger.AdminLedgerPruned, Actor: deed.Ref{Type: "user"}},
		{Action: ledger.AdminLedgerPruned, Actor: deed.Ref{Type: "system", ID: "operator"}, Data: []int{1}},
		{Action: "Ledger.Pruned", Actor: deed.Ref{Type: "system", ID: "operator"}},
	} {
		err := en.e.DB.OrgTx(ctx, en.org, func(ctx context.Context, tx pgx.Tx) error {
			_, err := en.led.RecordAdmin(ctx, tx, e)
			return err
		})
		if err == nil {
			t.Errorf("%+v was accepted", e)
		}
	}
	// A user actor with a target agent is recorded with its id.
	err = en.led.OrgTxRetry(ctx, en.org, func(ctx context.Context, tx pgx.Tx) error {
		_, err := en.led.RecordAdmin(ctx, tx, ledger.AdminEvent{Action: ledger.AdminLedgerPruned,
			Actor: deed.Ref{Type: "user", ID: "usr_01j9z8m2k4p6r8t0v2x4z6b8d0"}, Agent: &deed.AgentRef{ID: en.agents[0].ID.String(), Name: en.agents[0].Name},
			Target: &deed.Ref{Type: "agent", ID: en.agents[0].ID.String()}, Reason: "test", RequestID: "req_01j9z8m2k4p6r8t0v2x4z6b8d0"})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}

	// Amendments only on sealed actions; AppendEvent needs an action.
	pending := ""
	for i, id := range res.IDs {
		if fake.Generate(42, 120, nil, 3)[i].Kind == fake.Pending {
			pending = id
		}
	}
	err = en.e.DB.OrgTx(ctx, en.org, func(ctx context.Context, tx pgx.Tx) error {
		_, err := en.led.SealAmendment(ctx, tx, pending, deed.WitnessSection{Level: "L1", Verdict: "VERIFIED", Method: "readback.x"}, nil)
		return err
	})
	if !errors.Is(err, ledger.ErrStateConflict) {
		t.Errorf("amend a pending action: %v", err)
	}
	err = en.e.DB.OrgTx(ctx, en.org, func(ctx context.Context, tx pgx.Tx) error {
		_, err := en.led.SealAmendment(ctx, tx, "act_01j9zt3a5c7e9g1j3m5p7r9t1v", deed.WitnessSection{}, nil)
		return err
	})
	if !errors.Is(err, ledger.ErrNotFound) {
		t.Errorf("amend a missing action: %v", err)
	}
	err = en.e.DB.OrgTx(ctx, en.org, func(ctx context.Context, tx pgx.Tx) error {
		return en.led.AppendEvent(ctx, tx, "act_01j9zt3a5c7e9g1j3m5p7r9t1v", ledger.EventData{})
	})
	if !errors.Is(err, ledger.ErrNotFound) {
		t.Errorf("append to a missing action: %v", err)
	}
	err = en.e.DB.OrgTx(ctx, en.org, func(ctx context.Context, tx pgx.Tx) error {
		_, err := en.led.SealAction(ctx, tx, "act_01j9zt3a5c7e9g1j3m5p7r9t1v")
		return err
	})
	if !errors.Is(err, ledger.ErrNotFound) {
		t.Errorf("seal a missing action: %v", err)
	}

	// Retention: the default, then the org's setting.
	if d, err := en.led.RetentionDays(ctx, en.org); err != nil || d != ledger.DefaultRetentionDays {
		t.Fatalf("%d %v", d, err)
	}
	if _, err := en.e.Admin(t).Exec(ctx, `UPDATE deedglass.orgs SET retention_days = 30 WHERE id = $1`, en.org); err != nil {
		t.Fatal(err)
	}
	if d, err := en.led.RetentionDays(ctx, en.org); err != nil || d != 30 {
		t.Fatalf("%d %v", d, err)
	}
}
