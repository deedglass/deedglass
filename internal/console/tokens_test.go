package console

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

// TestTokensMatchDesignMD keeps web/tokens/tokens.json (what the Console builds from) in lockstep
// with DESIGN.md's front matter (what agents and people read): the colours, radii, motion and the
// Console's type scale must exist in both, with the same values (S24 §5.D.1).
func TestTokensMatchDesignMD(t *testing.T) {
	md, err := os.ReadFile("../../DESIGN.md")
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.SplitN(string(md), "\n---\n", 2)
	if len(parts) != 2 || !strings.HasPrefix(parts[0], "---\n") {
		t.Fatal("DESIGN.md has no YAML front matter")
	}
	var design struct {
		Colors     map[string]string         `yaml:"colors"`
		Rounded    map[string]string         `yaml:"rounded"`
		Motion     map[string]string         `yaml:"motion"`
		Typography map[string]map[string]any `yaml:"typography"`
		Components map[string]map[string]any `yaml:"components"`
	}
	if err := yaml.Unmarshal([]byte(strings.TrimPrefix(parts[0], "---\n")), &design); err != nil {
		t.Fatalf("DESIGN.md front matter: %v", err)
	}
	raw, err := os.ReadFile("../../web/tokens/tokens.json")
	if err != nil {
		t.Fatal(err)
	}
	var tok struct {
		Primitives map[string]string `json:"primitives"`
		Radius     map[string]string `json:"radius"`
		Motion     map[string]string `json:"motion"`
		Typography map[string]struct {
			Size     string `json:"size"`
			Line     string `json:"line"`
			Weight   int    `json:"weight"`
			Tracking string `json:"tracking"`
		} `json:"typography"`
	}
	if err := json.Unmarshal(raw, &tok); err != nil {
		t.Fatal(err)
	}
	norm := func(s string) string {
		s = strings.ToLower(strings.ReplaceAll(s, " ", ""))
		s = strings.ReplaceAll(s, "0.", ".")
		return s
	}

	// Colours: the same names and values both ways.
	for k, v := range design.Colors {
		got, ok := tok.Primitives[k]
		if !ok {
			t.Errorf("DESIGN.md colour %s is missing from tokens.json primitives", k)
		} else if norm(got) != norm(v) {
			t.Errorf("colour %s: DESIGN.md %s, tokens.json %s", k, v, got)
		}
	}
	for k := range tok.Primitives {
		if _, ok := design.Colors[k]; !ok {
			t.Errorf("tokens.json primitive %s is missing from DESIGN.md colors", k)
		}
	}
	// Radii.
	for _, k := range []string{"chip", "input", "button", "card", "sheet", "hero", "pill"} {
		if norm(design.Rounded[k]) != norm(tok.Radius[k]) {
			t.Errorf("radius %s: DESIGN.md %q, tokens.json %q", k, design.Rounded[k], tok.Radius[k])
		}
	}
	// Motion.
	for d, k := range map[string]string{"duration-micro": "micro", "duration-component": "component", "duration-sheet": "sheet", "duration-reveal": "reveal", "ease-enter": "ease-enter", "ease-exit": "ease-exit"} {
		if norm(design.Motion[d]) != norm(tok.Motion[k]) {
			t.Errorf("motion %s: DESIGN.md %q, tokens.json %q", d, design.Motion[d], tok.Motion[k])
		}
	}
	// The Console's type scale: typography.console-<name> ⇄ tokens.json typography.<name>.
	for k, v := range tok.Typography {
		d, ok := design.Typography["console-"+k]
		if !ok {
			t.Errorf("tokens.json typography %s is missing from DESIGN.md as console-%s", k, k)
			continue
		}
		str := func(x any) string {
			switch y := x.(type) {
			case string:
				return y
			case int:
				return strings.TrimSpace(strings.Repeat(" ", 0) + itoa(y))
			case float64:
				return strings.TrimRight(strings.TrimRight(jsonNum(y), "0"), ".")
			}
			return ""
		}
		if str(d["fontSize"]) != v.Size || str(d["lineHeight"]) != v.Line || str(d["fontWeight"]) != itoa(v.Weight) || norm(str(d["letterSpacing"])) != norm(v.Tracking) {
			t.Errorf("type %s: DESIGN.md %v, tokens.json %+v", k, d, v)
		}
	}
	for k := range design.Typography {
		if name, ok := strings.CutPrefix(k, "console-"); ok {
			if _, ok := tok.Typography[name]; !ok {
				t.Errorf("DESIGN.md typography %s is missing from tokens.json", k)
			}
		}
	}
	// Components the Console ships are documented.
	for _, c := range []string{"button-primary", "text-input", "table-row", "verdict-badge", "status-pill", "command-palette", "toast", "empty-state", "code-block", "top-bar-console", "sidebar-console"} {
		if _, ok := design.Components[c]; !ok {
			t.Errorf("DESIGN.md components has no %s", c)
		}
	}
}

func itoa(n int) string { b, _ := json.Marshal(n); return string(b) }

func jsonNum(f float64) string { b, _ := json.Marshal(f); return string(b) }
