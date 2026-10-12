package mcpb

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/deedglass/deedglass/internal/installlinks"
)

func TestBundles(t *testing.T) {
	bin := []byte("\x7fELF fake binary")
	icon := []byte("\x89PNG\r\n\x1a\nfake")
	for _, p := range Platforms {
		var a, b bytes.Buffer
		if err := Write(&a, p, installlinks.Version, bin, icon); err != nil {
			t.Fatal(err)
		}
		Write(&b, p, installlinks.Version, bin, icon)
		if !bytes.Equal(a.Bytes(), b.Bytes()) {
			t.Fatalf("%s: two writes differ", p.ID)
		}
		zr, err := zip.NewReader(bytes.NewReader(a.Bytes()), int64(a.Len()))
		if err != nil {
			t.Fatal(err)
		}
		files := map[string]*zip.File{}
		for _, f := range zr.File {
			files[f.Name] = f
		}
		if len(files) != 3 || files["manifest.json"] == nil || files["icon.png"] == nil || files[p.Binary()] == nil {
			t.Fatalf("%s: entries %v", p.ID, files)
		}
		if files[p.Binary()].Mode().Perm() != 0o755 {
			t.Errorf("%s: binary mode %v", p.ID, files[p.Binary()].Mode())
		}
		rc, _ := files["manifest.json"].Open()
		raw, _ := io.ReadAll(rc)
		var m map[string]any
		dec := json.NewDecoder(bytes.NewReader(raw))
		if err := dec.Decode(&m); err != nil {
			t.Fatal(err)
		}
		// The spec's required fields.
		for _, k := range []string{"manifest_version", "name", "version", "description", "author", "server"} {
			if m[k] == nil {
				t.Errorf("%s: no %s", p.ID, k)
			}
		}
		srv := m["server"].(map[string]any)
		cfg := srv["mcp_config"].(map[string]any)
		if srv["type"] != "binary" || srv["entry_point"] != p.Binary() || cfg["command"] != "${__dirname}/"+p.Binary() {
			t.Errorf("%s: server %v", p.ID, srv)
		}
		if args := cfg["args"].([]any); len(args) != 2 || args[0] != "mcp" || args[1] != "serve" {
			t.Errorf("%s: args %v", p.ID, args)
		}
		env := cfg["env"].(map[string]any)
		if env["DEEDGLASS_API_KEY"] != "${user_config.api_key}" || env["DEEDGLASS_SERVER"] != "${user_config.server_url}" {
			t.Errorf("%s: env %v", p.ID, env)
		}
		uc := m["user_config"].(map[string]any)
		if k := uc["api_key"].(map[string]any); k["sensitive"] != true || k["required"] != false || k["type"] != "string" {
			t.Errorf("%s: api_key %v", p.ID, k)
		}
		if s := uc["server_url"].(map[string]any); s["default"] != DefaultServer {
			t.Errorf("%s: server_url %v", p.ID, s)
		}
		if pl := m["compatibility"].(map[string]any)["platforms"].([]any); len(pl) != 1 || pl[0] != p.OS {
			t.Errorf("%s: platforms %v", p.ID, pl)
		}
		for _, bad := range []string{"dg_live_", "dg_test_", "://", "@"} {
			for _, k := range []string{"DEEDGLASS_API_KEY"} {
				if strings.Contains(env[k].(string), bad) {
					t.Errorf("%s: %s holds %q", p.ID, k, bad)
				}
			}
		}
		if bytes.Contains(raw, []byte("dg_live_")) || bytes.Contains(raw, []byte("dg_test_")) {
			t.Errorf("%s: the manifest holds a key", p.ID)
		}
		// The asset name matches what the install links point at.
		found := false
		for _, l := range installlinks.Links() {
			if strings.HasSuffix(l.URL, "/"+p.FileName(installlinks.Version)) {
				found = true
			}
		}
		if !found {
			t.Errorf("%s: no install link points at %s", p.ID, p.FileName(installlinks.Version))
		}
	}
	if err := Write(io.Discard, Platforms[0], "0.1.0", nil, nil); err == nil {
		t.Error("an empty binary was bundled")
	}
	var noIcon bytes.Buffer
	Write(&noIcon, Platforms[0], "0.1.0", []byte("x"), nil)
	zr, _ := zip.NewReader(bytes.NewReader(noIcon.Bytes()), int64(noIcon.Len()))
	if len(zr.File) != 2 || NewManifest(Platforms[0], "0.1.0", false).Icon != "" {
		t.Error("an icon appeared without one")
	}
	if _, ok := ByID("linux-x64"); ok {
		t.Error("ByID found an unbundled platform")
	}
}
