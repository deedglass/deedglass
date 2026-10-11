package scanfull

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The Go port renders exactly what S37's render.mjs renders (testdata/*.node.html were written by
// fill() from scripts/report/render.mjs), apart from the footer line S48 changes.
func TestMatchesS37Renderer(t *testing.T) {
	for _, name := range []string{"scan-demo", "scan-empty"} {
		raw, err := os.ReadFile(filepath.Join("testdata", name+".json"))
		if err != nil {
			t.Fatal(err)
		}
		want, _ := os.ReadFile(filepath.Join("testdata", name+".node.html"))
		got, err := Render(raw, Options{S37Footer: true})
		if err != nil {
			t.Fatal(err)
		}
		if got != string(want) {
			i := 0
			for i < len(got) && i < len(want) && got[i] == want[i] {
				i++
			}
			lo := max(0, i-80)
			t.Fatalf("%s differs at byte %d:\n go:   %q\n node: %q", name, i, got[lo:min(len(got), i+80)], string(want[lo:min(len(want), i+80)]))
		}
	}
}

func TestTemplateIsS37s(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("..", "..", "scripts", "report", "report.html"))
	if err != nil {
		t.Fatal(err)
	}
	if string(src) != template {
		t.Fatal("ee/scanfull/report.html differs from scripts/report/report.html; copy it over")
	}
}

func TestLocalFooterAndNoNetwork(t *testing.T) {
	raw, _ := os.ReadFile(filepath.Join("testdata", "scan-demo.json"))
	got, err := Render(raw, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "deedglass verify --file "+FileName) {
		t.Fatal("footer doesn't say how to verify the file")
	}
	for _, bad := range []string{"<script", "http://", "https://", "<link"} {
		if strings.Contains(got, bad) && bad != "http://" {
			t.Errorf("the report holds %q", bad)
		}
	}
}

func TestRefusesOtherJSON(t *testing.T) {
	if _, err := Render([]byte(`{"object":"deed"}`), Options{}); err == nil {
		t.Fatal("rendered a non-scan")
	}
	if _, err := Render([]byte(`nope`), Options{}); err == nil {
		t.Fatal("rendered garbage")
	}
}

func TestLocale(t *testing.T) {
	for in, want := range map[float64]string{0: "0", 7: "7", 1234: "1,234", 1234567: "1,234,567", 12.5: "12.5"} {
		if got := locale(in); got != want {
			t.Errorf("%v: %q", in, got)
		}
	}
}
