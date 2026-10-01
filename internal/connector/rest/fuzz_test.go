package rest

import (
	"net/url"
	"path"
	"strings"
	"testing"
)

// FuzzTemplatePath: a path built from any argument value stays under an allowed prefix, is
// clean, and never holds a dot segment.
func FuzzTemplatePath(f *testing.F) {
	for _, s := range []string{"42", "../admin", "42/../../x", "%2e%2e", "..", ".", "a%2Fb", "~x", "a.b-c_d", "\x00", "é"} {
		f.Add(s, "q")
	}
	base, _ := url.Parse("https://orders.example.com/api")
	st := &state{cfg: Config{AllowedPathPrefixes: []string{"/v1/orders"}}, base: base}
	tool := Tool{Name: "get", Method: "GET", Path: "/v1/orders/{id}/items/{item}", Args: map[string]Arg{
		"id": {In: "path", Type: "string"}, "item": {In: "path", Type: "string"}}}
	f.Fuzz(func(t *testing.T, id, item string) {
		b, err := st.build(tool, map[string]any{"id": id, "item": item})
		if err != nil {
			return
		}
		if path.Clean(b.path) != b.path || !strings.HasPrefix(b.path, "/v1/orders/") {
			t.Fatalf("%q %q built %q", id, item, b.path)
		}
		for _, seg := range strings.Split(b.path, "/") {
			if seg == "." || seg == ".." {
				t.Fatalf("dot segment in %q", b.path)
			}
		}
		if !strings.HasPrefix(b.full.EscapedPath(), "/api/v1/orders/") {
			t.Fatalf("url path %q", b.full.EscapedPath())
		}
	})
}

// BenchmarkBuild is the REST connector's own work per call, excluding the network: templating the
// path, the query, the body and the headers.
func BenchmarkBuild(b *testing.B) {
	base, _ := url.Parse("https://orders.example.com")
	st := &state{cfg: Config{AllowedPathPrefixes: []string{"/v1/orders"}}, base: base}
	tool := Tool{Name: "update_status", Method: "PATCH", Path: "/v1/orders/{id}", Args: map[string]Arg{
		"id": {In: "path", Type: "string"}, "status": {In: "body", Type: "string"}, "src": {In: "header", Header: "X-Source", Type: "string"}}}
	b.ReportAllocs()
	for b.Loop() {
		args, _ := decodeArgs([]byte(`{"id":"42","status":"shipped","src":"agent"}`))
		if _, err := st.build(tool, args); err != nil {
			b.Fatal(err)
		}
	}
}
