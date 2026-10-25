package a2a

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/deedglass/deedglass/internal/a2a/a2atest"
)

// TestBrowserShapedRefused: what a web page on the same machine can send never reaches the Gate or
// the peer (audit M-03, attack group T). Each row starts from a request a real A2A client sends and
// changes one thing; the last rows are controls that must pass.
func TestBrowserShapedRefused(t *testing.T) {
	good := func() map[string]string {
		return map[string]string{"A2A-Version": "1.0", "Authorization": "Bearer " + callerKey, "Content-Type": "application/json"}
	}
	with := func(k, v string) map[string]string {
		h := good()
		if v == "" {
			delete(h, k)
		} else {
			h[k] = v
		}
		return h
	}
	rows := []struct {
		name   string
		method string
		query  string
		host   string
		hdr    map[string]string
		status int
		passes bool
	}{
		{"simple cross-site POST (text/plain, query version, Origin)", http.MethodPost, "?A2A-Version=1.0", "",
			map[string]string{"Content-Type": "text/plain", "Origin": "https://pages.example", "Sec-Fetch-Site": "cross-site"}, http.StatusForbidden, false},
		{"Origin, everything else valid", http.MethodPost, "", "", with("Origin", "https://pages.example"), http.StatusForbidden, false},
		{"Origin null", http.MethodPost, "", "", with("Origin", "null"), http.StatusForbidden, false},
		{"Origin naming the proxy itself", http.MethodPost, "", "", with("Origin", "http://127.0.0.1:7420"), http.StatusForbidden, false},
		{"Sec-Fetch-Site cross-site", http.MethodPost, "", "", with("Sec-Fetch-Site", "cross-site"), http.StatusForbidden, false},
		{"Sec-Fetch-Site same-origin", http.MethodPost, "", "", with("Sec-Fetch-Site", "same-origin"), http.StatusForbidden, false},
		{"DNS rebinding Host", http.MethodPost, "", "rebind.attack.test:7420", good(), http.StatusMisdirectedRequest, false},
		{"Host 0.0.0.0", http.MethodPost, "", "0.0.0.0:7420", good(), http.StatusMisdirectedRequest, false},
		{"Host localhost.attack.test", http.MethodPost, "", "localhost.attack.test", good(), http.StatusMisdirectedRequest, false},
		{"text/plain body", http.MethodPost, "", "", with("Content-Type", "text/plain"), http.StatusUnsupportedMediaType, false},
		{"form body", http.MethodPost, "", "", with("Content-Type", "application/x-www-form-urlencoded"), http.StatusUnsupportedMediaType, false},
		{"no Content-Type", http.MethodPost, "", "", with("Content-Type", ""), http.StatusUnsupportedMediaType, false},
		{"no key", http.MethodPost, "", "", with("Authorization", ""), http.StatusUnauthorized, false},
		{"wrong key", http.MethodPost, "", "", with("Authorization", "Bearer the-callers-own-secreT"), http.StatusUnauthorized, false},
		{"key as Basic", http.MethodPost, "", "", with("Authorization", "Basic "+callerKey), http.StatusUnauthorized, false},
		{"query version only", http.MethodPost, "?A2A-Version=1.0", "", with("A2A-Version", ""), http.StatusOK, false},
		{"CORS preflight", http.MethodOptions, "", "", map[string]string{"Origin": "https://pages.example", "Access-Control-Request-Method": "POST"}, http.StatusForbidden, false},
		{"preflight without Origin", http.MethodOptions, "", "", nil, http.StatusForbidden, false},
		// Controls.
		{"control: a real client", http.MethodPost, "", "", good(), http.StatusOK, true},
		{"control: Host localhost", http.MethodPost, "", "localhost:7420", good(), http.StatusOK, true},
		{"control: Host [::1]", http.MethodPost, "", "[::1]:7420", good(), http.StatusOK, true},
		{"control: Sec-Fetch-Site none", http.MethodPost, "", "", with("Sec-Fetch-Site", "none"), http.StatusOK, true},
		{"control: charset parameter", http.MethodPost, "", "", with("Content-Type", "application/json; charset=utf-8"), http.StatusOK, true},
		{"control: lower-case scheme", http.MethodPost, "", "", with("Authorization", "bearer "+callerKey), http.StatusOK, true},
	}
	for _, r := range rows {
		t.Run(r.name, func(t *testing.T) {
			peer := a2atest.New("Researcher")
			defer peer.Close()
			h := newHarness(t, "planner", "researcher", "u_5780", peer)
			req, _ := http.NewRequest(r.method, h.srv.URL+"/"+r.query, strings.NewReader(sendBody(1, MethodSendMessage, "complete the market summary", "")))
			for k, v := range r.hdr {
				req.Header.Set(k, v)
			}
			if r.host != "" {
				req.Host = r.host
			}
			res, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			b, _ := io.ReadAll(res.Body)
			res.Body.Close()
			h.proxy.Wait()
			if res.StatusCode != r.status {
				t.Errorf("HTTP %d, want %d: %s", res.StatusCode, r.status, b)
			}
			for k := range res.Header {
				if strings.HasPrefix(strings.ToLower(k), "access-control-") {
					t.Errorf("CORS header %s sent", k)
				}
			}
			if strings.Contains(string(b), callerKey) {
				t.Error("the answer echoes the key")
			}
			h.gate.mu.Lock()
			n := len(h.gate.submits)
			h.gate.mu.Unlock()
			got := len(peer.Got())
			if r.passes && (n != 1 || got != 1) {
				t.Errorf("control: gate submits %d, peer requests %d; want 1 and 1: %s", n, got, b)
			}
			if !r.passes && (n != 0 || got != 0) {
				t.Errorf("refused request reached the gate (%d) or the peer (%d): %s", n, got, b)
			}
		})
	}
}

// TestCardBrowserRefused: the card answers local clients without a key, but not a browser page or
// a rebinding name (which could read the peer's card cross-origin).
func TestCardBrowserRefused(t *testing.T) {
	peer := a2atest.New("Researcher")
	defer peer.Close()
	h := newHarness(t, "planner", "researcher", "", peer)
	for _, r := range []struct {
		name, host, origin string
		status             int
	}{
		{"plain", "", "", http.StatusOK},
		{"Origin", "", "https://pages.example", http.StatusForbidden},
		{"rebinding Host", "rebind.attack.test:7420", "", http.StatusMisdirectedRequest},
	} {
		req, _ := http.NewRequest(http.MethodGet, h.srv.URL+"/.well-known/agent-card.json", nil)
		if r.origin != "" {
			req.Header.Set("Origin", r.origin)
		}
		if r.host != "" {
			req.Host = r.host
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != r.status {
			t.Errorf("%s: HTTP %d, want %d", r.name, res.StatusCode, r.status)
		}
	}
}

func TestLoopbackHost(t *testing.T) {
	for h, want := range map[string]bool{
		"127.0.0.1": true, "127.0.0.1:7420": true, "127.8.9.10:1": true, "[::1]:7420": true, "[::1]": true, "localhost": true,
		"LOCALHOST:7420": true, "[::ffff:127.0.0.1]:7420": true,
		"": false, "0.0.0.0:7420": false, "10.0.0.1": false, "localhost.attack.test": false, "attack.test:7420": false,
		"[fe80::1%25lo0]:7420": false, "127.0.0.1.nip.io": false, "[::]:7420": false,
	} {
		if got := loopbackHost(h); got != want {
			t.Errorf("loopbackHost(%q) = %v, want %v", h, got, want)
		}
	}
}
