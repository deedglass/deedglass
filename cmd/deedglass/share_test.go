package main

import (
	"encoding/base64"
	"encoding/json"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/deedglass/deedglass/internal/deed"
	"github.com/deedglass/deedglass/internal/installlinks"
)

func vectorEnvelope(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile("../../testdata/deeds/valid/01-action-verified-l2.json")
	if err != nil {
		t.Fatal(err)
	}
	var v struct {
		Envelope json.RawMessage `json:"envelope"`
	}
	if json.Unmarshal(b, &v) == nil && len(v.Envelope) > 0 {
		return v.Envelope
	}
	return b
}

// TestDeedShareRoundTrip: the link's fragment decodes to the same envelope and a key set that
// verifies it; one changed byte in the deed fails; the limit is enforced.
func TestDeedShareRoundTrip(t *testing.T) {
	kb, err := os.ReadFile("../../testdata/deeds/keys/test-keys.json")
	if err != nil {
		t.Fatal(err)
	}
	ks, err := deed.ParseKeySet(kb)
	if err != nil {
		t.Fatal(err)
	}
	env := vectorEnvelope(t)
	link, err := DeedShareLink(env, ks)
	if err != nil {
		t.Fatal(err)
	}
	t.Log(link)
	u, _ := url.Parse(link)
	if u.Host != "deedglass.com" || u.Path != "/verify" || u.RawQuery != "" {
		t.Fatalf("link %s", link)
	}
	q, _ := url.ParseQuery(u.Fragment)
	d, err1 := base64.RawURLEncoding.DecodeString(q.Get("d"))
	k, err2 := base64.RawURLEncoding.DecodeString(q.Get("k"))
	if err1 != nil || err2 != nil {
		t.Fatal(err1, err2)
	}
	ks2, err := deed.ParseKeySet(k)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := deed.VerifyEnvelope(d, ks2, deed.VerifyOptions{}); err != nil {
		t.Fatalf("the shared deed doesn't verify: %v", err)
	}
	bad := []byte(strings.Replace(string(d), `"seq":`, `"seq":1`, 1))
	if _, err := deed.VerifyEnvelope(bad, ks2, deed.VerifyOptions{}); err == nil {
		t.Fatal("a changed deed verified")
	}
	if installlinks.HasSecret(link) {
		t.Fatal("the link holds a secret pattern")
	}
	big := strings.Replace(string(env), `"object":"deed"`, `"object":"deed","pad":"`+strings.Repeat("x", 6200)+`"`, 1)
	if _, err := DeedShareLink([]byte(big), ks); err == nil {
		t.Fatal("over 6,144 characters was accepted")
	}
}

func TestInstallLinksCLI(t *testing.T) {
	r := cli(t, nil, false, "mcp", "install-links")
	if r.code != ExitOK || !strings.Contains(r.stdout, "claude mcp add --scope user deedglass -- npx -y deedglass mcp serve") || !strings.Contains(r.stdout, "No link holds a key.") {
		t.Fatalf("%d %s %s", r.code, r.stdout, r.stderr)
	}
	r = cli(t, nil, false, "--json", "mcp", "install-links")
	var v struct {
		Links []installlinks.Link `json:"links"`
	}
	if err := json.Unmarshal([]byte(r.stdout), &v); err != nil || len(v.Links) != len(installlinks.Links()) {
		t.Fatalf("%v %s", err, r.stdout)
	}
}
