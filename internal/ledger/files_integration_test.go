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
)

// TestSealFileRoute: POST /v1/deeds/files over a real database. An admin session and an org admin
// key seal file.sealed (the label in admin.reason, data_digest over the four fields, the deed
// verifies offline); a viewer and an agent key are refused; every bad field names itself.
func TestSealFileRoute(t *testing.T) {
	en := newEnv(t)
	srv := httpx.New(httpx.Options{Logger: slog.New(slog.DiscardHandler), Sessions: testSessions{}, Keys: testKeys{}})
	srv.Mount(ledger.Routes(en.led)...)
	ts := httptest.NewServer(srv)
	defer ts.Close()
	post := func(body string, hdr ...string) (int, []byte) {
		t.Helper()
		req, _ := http.NewRequest("POST", ts.URL+"/v1/deeds/files", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		for i := 0; i+1 < len(hdr); i += 2 {
			req.Header.Set(hdr[i], hdr[i+1])
		}
		r, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(r.Body)
		r.Body.Close()
		return r.StatusCode, b
	}
	sum := "sha256:" + strings.Repeat("3f9a", 16)
	good := `{"sha256":"` + sum + `","bytes":214388,"media_type":"application/pdf","label":"Full scan report · sample · Acme demo"}`
	admin := []string{"Authorization", "Bearer admin-" + en.org.String()}
	session := []string{testOrgHeader, en.org.String(), testRoleHeader, "admin"}
	ks := mustKeys(t, en)
	for name, hdr := range map[string][]string{"admin key": admin, "admin session": session} {
		code, b := post(good, hdr...)
		if code != 201 {
			t.Fatalf("%s: %d %s", name, code, b)
		}
		for _, err := range httpx.CheckRequired(api.Spec, "SealedFile", b) {
			t.Errorf("%s: %v", name, err)
		}
		var res struct {
			Object string          `json:"object"`
			Deed   json.RawMessage `json:"deed"`
		}
		if err := json.Unmarshal(b, &res); err != nil || res.Object != "sealed_file" {
			t.Fatalf("%s: %v %s", name, err, b)
		}
		env, err := deed.ParseEnvelope(res.Deed)
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := env.MarshalCanonical()
		if _, err := deed.VerifyEnvelope(raw, ks, deed.VerifyOptions{}); err != nil {
			t.Fatalf("%s: the deed doesn't verify: %v", name, err)
		}
		a := env.Body.Admin
		want, _ := ledger.FileDataDigest(ledger.FileSealedData{SHA256: sum, Bytes: 214388, MediaType: "application/pdf", Label: "Full scan report · sample · Acme demo"})
		if env.Body.Kind != "admin" || a == nil || a.Action != "file.sealed" || a.Reason == nil || *a.Reason != "Full scan report · sample · Acme demo" ||
			a.DataDigest == nil || *a.DataDigest != want || a.Target == nil || a.Target.Type != "org" || a.Target.ID != en.org.String() {
			t.Fatalf("%s: admin section %+v", name, a)
		}
		if name == "admin key" && a.Actor.Type != "api_key" || name == "admin session" && a.Actor.Type != "user" {
			t.Fatalf("%s: actor %+v", name, a.Actor)
		}
		// The stored deed is the one returned.
		req, _ := http.NewRequest("GET", ts.URL+"/v1/deeds/"+env.ID, nil)
		req.Header.Set(testOrgHeader, en.org.String())
		r, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		stored, _ := io.ReadAll(r.Body)
		r.Body.Close()
		if !bytes.Equal(stored, raw) {
			t.Fatalf("%s: stored deed differs from the answer", name)
		}
	}
	// Refusals.
	cases := []struct {
		name, body string
		hdr        []string
		code       int
		want       string
	}{
		{"viewer", good, []string{testOrgHeader, en.org.String()}, 403, "insufficient_role"},
		{"agent key", good, []string{"Authorization", "Bearer agent-" + en.org.String()}, 403, "insufficient_scope"},
		{"no principal", good, nil, 401, "authentication_required"},
		{"bad sha256", strings.Replace(good, sum, "sha256:ABC", 1), admin, 400, `"param":"sha256"`},
		{"no bytes", `{"sha256":"` + sum + `","media_type":"application/pdf","label":"x"}`, admin, 400, `"param":"bytes"`},
		{"negative bytes", strings.Replace(good, "214388", "-1", 1), admin, 400, `"param":"bytes"`},
		{"media type", strings.Replace(good, "application/pdf", "image/png", 1), admin, 400, `"param":"media_type"`},
		{"long label", strings.Replace(good, "Full scan report · sample · Acme demo", strings.Repeat("x", 121), 1), admin, 400, `"param":"label"`},
		{"control label", strings.Replace(good, "Full scan", `Full\u0007scan`, 1), admin, 400, `"param":"label"`},
		{"empty label", strings.Replace(good, "Full scan report · sample · Acme demo", "", 1), admin, 400, `"param":"label"`},
		{"unknown field", strings.Replace(good, `"bytes"`, `"contents":"JVBERi0=","bytes"`, 1), admin, 400, "contents"},
		{"too large", `{"label":"` + strings.Repeat("x", 5000) + `"}`, admin, 413, "request_too_large"},
	}
	for _, c := range cases {
		code, b := post(c.body, c.hdr...)
		compact := strings.ReplaceAll(strings.ReplaceAll(string(b), " ", ""), "\n", "")
		if code != c.code || !strings.Contains(compact, strings.ReplaceAll(c.want, " ", "")) {
			t.Errorf("%s: %d %s, want %d with %s", c.name, code, b, c.code, c.want)
		}
	}
}
