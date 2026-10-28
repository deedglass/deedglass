package localapi

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/deedglass/deedglass/internal/httpx"
	"github.com/deedglass/deedglass/internal/installlinks"
)

func code(t *testing.T, err error) string {
	t.Helper()
	var he *httpx.Error
	if !errors.As(err, &he) {
		t.Fatalf("want an httpx error, got %v", err)
	}
	return string(he.Code)
}

func TestNotLocal(t *testing.T) {
	var a *API
	for _, rt := range Routes(a) {
		r := httptest.NewRequest(rt.Method, strings.Replace(rt.Pattern, "{id}", "cursor", 1), strings.NewReader("{}"))
		r.SetPathValue("id", "cursor")
		if c := code(t, rt.Handler(httptest.NewRecorder(), r)); c != string(httpx.CodeNotFound) {
			t.Errorf("%s %s outside the app: %s, want not_found", rt.Method, rt.Pattern, c)
		}
	}
}

func TestLocalState(t *testing.T) {
	dir := t.TempDir()
	a := &API{Dir: dir, Address: "http://127.0.0.1:4180"}
	get := func() Local {
		w := httptest.NewRecorder()
		if err := a.getLocal(w, httptest.NewRequest("GET", "/v1/local", nil)); err != nil {
			t.Fatal(err)
		}
		var l Local
		if err := json.Unmarshal(w.Body.Bytes(), &l); err != nil {
			t.Fatal(err)
		}
		return l
	}
	if l := get(); !l.Local || !l.FirstRun || l.Path != nil || l.Address != a.Address || l.Object != "local" {
		t.Fatalf("fresh: %+v", l)
	}
	patch := func(body string) error {
		r := httptest.NewRequest("PATCH", "/v1/local", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		return a.patchLocal(httptest.NewRecorder(), r)
	}
	if err := patch(`{"path":"doctor"}`); code(t, err) != string(httpx.CodeValidationFailed) {
		t.Fatal("an unknown path is refused")
	}
	if err := patch(`{}`); code(t, err) != string(httpx.CodeValidationFailed) {
		t.Fatal("an empty patch is refused")
	}
	if err := patch(`{"path":"clinic","first_run":false}`); err != nil {
		t.Fatal(err)
	}
	if l := get(); l.FirstRun || l.Path == nil || *l.Path != "clinic" {
		t.Fatalf("after patch: %+v", l)
	}
	st, err := os.Stat(filepath.Join(dir, StateFile))
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("local.json: %v %v", st, err)
	}
}

func TestStartUnknownClient(t *testing.T) {
	a := &API{Dir: t.TempDir()}
	r := httptest.NewRequest("POST", "/v1/connect/../x/start", nil)
	r.SetPathValue("id", "../x")
	if c := code(t, a.startConnect(httptest.NewRecorder(), r)); c != string(httpx.CodeNotFound) {
		t.Fatalf("got %s", c)
	}
}

// Every link, config and command installs deedglass mcp serve --client <id> and holds no key.
func TestLinksHoldNoKey(t *testing.T) {
	dir := t.TempDir()
	a := &API{Dir: dir, Address: "http://127.0.0.1:4180", Run: Runner{Command: "/Applications/My Tools/deedglass"}}
	for _, id := range Clients {
		it := Item{ID: id}
		a.links(&it)
		any := false
		for _, p := range []*string{it.Link, it.WebLink, it.Config, it.Command, it.Address} {
			if p == nil {
				continue
			}
			any = true
			if installlinks.HasSecret(*p) {
				t.Errorf("%s holds a key: %s", id, *p)
			}
		}
		if !any {
			t.Errorf("%s has nothing to connect with", id)
		}
	}
	it := Item{ID: Cursor}
	a.links(&it)
	u, err := url.Parse(*it.Link)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := base64.StdEncoding.DecodeString(u.Query().Get("config"))
	if err != nil {
		t.Fatal(err)
	}
	var obj installlinks.ServerObject
	if err := json.Unmarshal(raw, &obj); err != nil {
		t.Fatal(err)
	}
	if obj.Command != "/Applications/My Tools/deedglass" || strings.Join(obj.Args, " ") != "mcp serve --client cursor" {
		t.Fatalf("cursor installs %+v", obj)
	}
	it = Item{ID: Terminal}
	a.links(&it)
	if !strings.Contains(*it.Command, "-- '/Applications/My Tools/deedglass' mcp serve --client terminal") {
		t.Fatalf("terminal command: %s", *it.Command)
	}
}

func TestStateRoundTrip(t *testing.T) {
	dir := t.TempDir()
	if s := ReadState(dir); !s.FirstRun {
		t.Fatal("no file is a first run")
	}
	p := "it"
	if err := WriteState(dir, State{FirstRun: false, Path: &p, Address: "http://127.0.0.1:1"}); err != nil {
		t.Fatal(err)
	}
	if s := ReadState(dir); s.FirstRun || *s.Path != "it" || s.Address != "http://127.0.0.1:1" {
		t.Fatalf("%+v", s)
	}
	_ = http.StatusOK
}
