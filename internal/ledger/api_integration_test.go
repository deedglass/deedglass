//go:build integration

package ledger_test

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/deedglass/deedglass/api"
	"github.com/deedglass/deedglass/internal/deed"
	"github.com/deedglass/deedglass/internal/httpx"
	"github.com/deedglass/deedglass/internal/ledger"
	"github.com/deedglass/deedglass/internal/ledger/fake"
)

// The six routes over a real database: shapes match api/openapi.yaml's required fields, foreign
// ids are 404, deeds are served verbatim with their ETag, exports verify.
func TestHTTPRoutes(t *testing.T) {
	en := newEnv(t)
	res := en.run(t, en.org, en.agents, 42, 60)
	orgB, agentsB := en.newOrg(t, "beta")
	resB := en.run(t, orgB, agentsB, 1, 3, fake.Allow)

	srv := httpx.New(httpx.Options{Logger: slog.New(slog.DiscardHandler), Sessions: httpx.DevPrincipal{}})
	srv.Mount(ledger.Routes(en.led)...)
	ts := httptest.NewServer(srv)
	defer ts.Close()
	get := func(path string, hdr ...string) (*http.Response, []byte) {
		t.Helper()
		req, _ := http.NewRequest("GET", ts.URL+path, nil)
		req.Header.Set(httpx.DevOrgHeader, en.org.String())
		for i := 0; i+1 < len(hdr); i += 2 {
			req.Header.Set(hdr[i], hdr[i+1])
		}
		r, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(r.Body)
		r.Body.Close()
		return r, b
	}
	required := func(schema string, body []byte) {
		t.Helper()
		for _, err := range httpx.CheckRequired(api.Spec, schema, body) {
			t.Errorf("%s: %v", schema, err)
		}
	}

	r, b := get("/v1/actions?verdict=MISMATCH&limit=2")
	if r.StatusCode != 200 {
		t.Fatalf("%d %s", r.StatusCode, b)
	}
	required("ActionList", b)
	var list ledger.List[ledger.Action]
	if err := json.Unmarshal(b, &list); err != nil || list.Object != "list" || len(list.Data) == 0 || list.Data[0].Witness.Verdict != "MISMATCH" {
		t.Fatalf("%s", b)
	}
	r, b = get("/v1/actions/" + res.IDs[0])
	required("ActionDetail", b)
	var d ledger.ActionDetail
	if err := json.Unmarshal(b, &d); err != nil || len(d.Events.Data) < 4 || d.Approval == nil {
		t.Fatalf("%d %s", r.StatusCode, b)
	}
	// Another org's action and deed: 404, the same as an id that doesn't exist.
	for _, p := range []string{"/v1/actions/" + resB.IDs[0], "/v1/actions/act_01j9zt3a5c7e9g1j3m5p7r9t1v", "/v1/actions/nope"} {
		r, b = get(p)
		if r.StatusCode != 404 || !strings.Contains(string(b), `"code": "not_found"`) || !strings.Contains(string(b), "in this org") {
			t.Fatalf("%s: %d %s", p, r.StatusCode, b)
		}
	}
	r, b = get("/v1/deeds?order=desc&limit=3")
	required("DeedList", b)
	var dl ledger.List[json.RawMessage]
	if err := json.Unmarshal(b, &dl); err != nil || len(dl.Data) != 3 || !dl.HasMore {
		t.Fatalf("%s", b)
	}
	// One deed, verbatim, with its body_hash as ETag; 304 on If-None-Match.
	sd, err := en.led.GetDeed(ctx, en.org, *d.Deed)
	if err != nil {
		t.Fatal(err)
	}
	r, b = get("/v1/deeds/" + sd.ID)
	if r.StatusCode != 200 || !bytes.Equal(b, sd.Envelope) || r.Header.Get("ETag") != `"`+sd.BodyHash+`"` ||
		r.Header.Get("Cache-Control") != "private, max-age=31536000, immutable" {
		t.Fatalf("%d %v", r.StatusCode, r.Header)
	}
	required("Deed", b)
	if r, _ = get("/v1/deeds/"+sd.ID, "If-None-Match", `"`+sd.BodyHash+`"`); r.StatusCode != 304 {
		t.Fatalf("If-None-Match: %d", r.StatusCode)
	}
	r, b = get("/v1/deeds/"+sd.ID, "Accept", "application/cose")
	if r.StatusCode != 200 || !strings.HasPrefix(r.Header.Get("Content-Type"), "application/cose") {
		t.Fatalf("COSE: %d %v", r.StatusCode, r.Header)
	}
	if _, err := deed.VerifyCOSE(b, mustKeys(t, en)); err != nil {
		t.Fatalf("COSE doesn't verify: %v", err)
	}
	r, b = get("/v1/ledger/check")
	required("LedgerCheck", b)
	var c ledger.ChainCheck
	if err := json.Unmarshal(b, &c); err != nil || c.Status != "intact" || c.Checked != int64(res.Deeds) {
		t.Fatalf("%s", b)
	}
	r, b = get("/v1/deeds/export")
	if r.StatusCode != 200 || r.Header.Get("Content-Type") != "application/gzip" ||
		!strings.Contains(r.Header.Get("Content-Disposition"), ledger.ExportName(en.org.String(), 1, res.LastSeq)) {
		t.Fatalf("%d %v", r.StatusCode, r.Header)
	}
	if rep, _ := verifyBundle(t, en, b); rep.Result != deed.ResultValid {
		t.Fatalf("%+v", rep)
	}
	r, b = get("/v1/deeds/export?from_seq=5000")
	if r.StatusCode != 404 || !strings.Contains(string(b), "The chain ends at") {
		t.Fatalf("%d %s", r.StatusCode, b)
	}
	// Errors use the envelope with param.
	r, b = get("/v1/actions?verdict=FAILED")
	required("ErrorEnvelope", b)
	if r.StatusCode != 400 || !strings.Contains(string(b), `"param": "verdict"`) {
		t.Fatalf("%d %s", r.StatusCode, b)
	}
	if r, _ = get("/v1/deeds?colour=blue"); r.StatusCode != 400 {
		t.Fatalf("unknown param: %d", r.StatusCode)
	}
	// No dev header: 401.
	req, _ := http.NewRequest("GET", ts.URL+"/v1/actions", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != 401 {
		t.Fatalf("no principal: %v %v", resp.StatusCode, err)
	}
	resp.Body.Close()
}

func mustKeys(t testing.TB, en *env) *deed.KeySet {
	rows, err := en.e.DB.SealKeys(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var recs []deed.KeyRecord
	for _, r := range rows {
		recs = append(recs, r.Record())
	}
	ks, err := deed.NewKeySet("http://127.0.0.1:8080", recs)
	if err != nil {
		t.Fatal(err)
	}
	return ks
}
