package github

import (
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"flag"
	"os"
	"strings"
	"testing"

	"github.com/deedglass/deedglass/internal/connector"
	"github.com/deedglass/deedglass/internal/connector/saas/saastest"
	"github.com/deedglass/deedglass/internal/vault"
)

var update = flag.Bool("update", false, "rewrite the JWT and downscope goldens")

func golden(t *testing.T, name string, got []byte) {
	t.Helper()
	path := "testdata/" + name
	if *update {
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run with -update)", err)
	}
	if string(want) != string(got) {
		t.Fatalf("%s differs:\n got %s\nwant %s", name, got, want)
	}
}

// TestAppJWTGolden: the App JWT for the fixed test key and clock is byte-stable. The golden keeps
// its decoded header and claims and the SHA-256 of the compact token (never the token itself, so
// no token-shaped string sits in the repository); the signature verifies with the public key.
func TestAppJWTGolden(t *testing.T) {
	key := saastest.TestRSAKey()
	jwt, err := appJWT(key, "Iv23liAbC123", testNow)
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(jwt, ".")
	if len(parts) != 3 {
		t.Fatalf("%d parts", len(parts))
	}
	h, _ := base64.RawURLEncoding.DecodeString(parts[0])
	c, _ := base64.RawURLEncoding.DecodeString(parts[1])
	sig, _ := base64.RawURLEncoding.DecodeString(parts[2])
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(&key.PublicKey, crypto.SHA256, sum[:], sig); err != nil {
		t.Fatalf("signature: %v", err)
	}
	var claims map[string]any
	_ = json.Unmarshal(c, &claims)
	if claims["iat"] != float64(testNow.Unix()-60) || claims["exp"] != float64(testNow.Unix()+540) || claims["iss"] != "Iv23liAbC123" {
		t.Fatalf("claims %v", claims)
	}
	all := sha256.Sum256([]byte(jwt))
	golden(t, "app_jwt.golden", []byte(string(h)+"\n"+string(c)+"\nsha256:"+hex.EncodeToString(all[:])+"\n"))
}

func TestDownscopeGolden(t *testing.T) {
	cfg := Config{Repos: []string{"arjun/deedglass-sandbox", "arjun/docs", "other/docs"}}
	b, _ := json.Marshal(downscopeBody(cfg))
	golden(t, "downscope.golden.json", append(b, '\n'))
	cfg.Repos = append(cfg.Repos, "arjun/*")
	b, _ = json.Marshal(downscopeBody(cfg))
	if string(b) != `{"permissions":{"contents":"write","issues":"write","pull_requests":"write","metadata":"read"}}` {
		t.Fatalf("wildcard body %s", b)
	}
}

func TestCredentialKinds(t *testing.T) {
	pat := Config{Auth: "pat"}
	cases := []struct {
		kind, field, value string
		want               string // "" refused
	}{
		{vault.KindBearer, "token", "github_pat_" + strings.Repeat("a", 30), tokenFineGrained},
		{vault.KindBearer, "token", "ghp_" + strings.Repeat("a", 36), tokenClassic},
		{vault.KindBearer, "token", "ghs_" + strings.Repeat("a", 36), ""},
		{vault.KindBearer, "token", "gho_" + strings.Repeat("a", 36), ""},
		{vault.KindBearer, "token", "ghu_" + strings.Repeat("a", 36), ""},
		{vault.KindBearer, "token", "something-else", ""},
		{vault.KindHeader, "value", "github_pat_x", ""},
	}
	for _, tc := range cases {
		sec := vault.NewSecretForTest(tc.kind, map[string]string{tc.field: tc.value})
		got, _, ce := checkCredential(pat, "github", sec)
		if got != tc.want || (tc.want == "") != (ce != nil) {
			t.Fatalf("%s %s…: %q %v", tc.kind, tc.value[:4], got, ce)
		}
		if ce != nil && (ce.Kind != connector.KindConfig || strings.Contains(ce.AdminDetail, tc.value) || strings.Contains(ce.AgentMessage, tc.value)) {
			t.Fatalf("refusal %+v", ce)
		}
	}
	app := Config{Auth: "app", App: &AppConfig{ClientID: "Iv23liAbC123", AppID: 1, InstallationID: 2}}
	good := vault.NewSecretForTest(vault.KindFields, map[string]string{"private_key": string(saastest.TestRSAKeyPEM())})
	if k, key, ce := checkCredential(app, "github", good); k != tokenApp || key == nil || ce != nil {
		t.Fatalf("app key: %q %v", k, ce)
	}
	for _, bad := range []*vault.Secret{
		vault.NewSecretForTest(vault.KindFields, map[string]string{"private_key": "not a pem"}),
		vault.NewSecretForTest(vault.KindBearer, map[string]string{"token": "github_pat_x"}),
	} {
		if _, _, ce := checkCredential(app, "github", bad); ce == nil || strings.Contains(ce.AdminDetail, "PRIVATE KEY") {
			t.Fatalf("bad app credential: %v", ce)
		}
	}
}

func TestRefusedTokenSendsNothing(t *testing.T) {
	srv := saastest.New(t)
	c := newConn()
	inst := instance(t, c, cfgJSON(srv.URL, nil), patOpener("ghs_"+strings.Repeat("Z", 36)))
	_, err := c.Execute(ctxWithRedactor(), inst, call(t, "get_repo", `{"repo":"arjun/deedglass-sandbox"}`))
	if ce := mustErr(t, err); ce.Code != connector.CodeValidationFailed || !strings.Contains(ce.AdminDetail, "installation token") {
		t.Fatalf("%+v", ce)
	}
	if len(srv.Requests()) != 0 {
		t.Fatal("a request was sent")
	}
	// No credential at all.
	inst = instance(t, c, cfgJSON(srv.URL, nil), nil)
	_, err = c.Execute(ctxWithRedactor(), inst, call(t, "get_repo", `{"repo":"arjun/deedglass-sandbox"}`))
	if ce := mustErr(t, err); ce.Kind != connector.KindCredentialsMissing {
		t.Fatalf("%+v", ce)
	}
}

func appCfg(base string) json.RawMessage {
	return cfgJSON(base, map[string]any{"auth": "app", "app": map[string]any{"client_id": "Iv23liAbC123", "app_id": 1234567, "installation_id": 76543210}})
}

func appOpener() *opener {
	return &opener{kind: vault.KindFields, fields: map[string]string{"private_key": string(saastest.TestRSAKeyPEM())}}
}

func TestAppExecuteMintsOnce(t *testing.T) {
	srv := server(t, "app_execute")
	c := newConn()
	inst := instance(t, c, appCfg(srv.URL), appOpener())
	args := `{"repo":"arjun/deedglass-sandbox","title":"Flaky checkout test"}`
	res, err := c.Execute(ctxWithRedactor(), inst, call(t, "create_issue", args))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Execute(ctxWithRedactor(), inst, call(t, "create_issue", args)); err != nil {
		t.Fatal(err)
	}
	if srv.Count("POST access_tokens") != 1 {
		t.Fatalf("mints = %d", srv.Count("POST access_tokens"))
	}
	mint := srv.Requests()[0]
	auth := mint.Header.Get("Authorization")
	want, _ := appJWT(saastest.TestRSAKey(), "Iv23liAbC123", testNow)
	if auth != "Bearer "+want {
		t.Fatal("the mint wasn't authenticated with the App JWT")
	}
	golden(t, "downscope.request.golden.json", append(mint.Body, '\n'))
	// The read-back's author is the App's bot.
	_, fs, err := c.NativeReadBack(ctxWithRedactor(), inst, call(t, "create_issue", args), res)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range fs {
		if f.Field == "author" && (!f.Match || f.Observed != "deedglass-sandbox[bot]") {
			t.Fatalf("author %+v", f)
		}
	}
	if n := c.tokens.Mints("github:" + testConn.String() + ":" + fingerprint(vault.NewSecretForTest(vault.KindFields, appOpener().fields))); n != 1 {
		t.Fatalf("token cache mints = %d", n)
	}
}

func TestPermissionsText(t *testing.T) {
	cases := map[string]string{
		"issues=write":                       "Issues: write",
		"pull_requests=write; contents=read": "Pull requests: write or Contents: read",
		"contents=read,metadata=read":        "Contents: read and Metadata: read",
		"":                                   "",
		"=; ,":                               "",
	}
	for in, want := range cases {
		if got := permissionsText(in); got != want {
			t.Errorf("%q → %q, want %q", in, got, want)
		}
	}
}
