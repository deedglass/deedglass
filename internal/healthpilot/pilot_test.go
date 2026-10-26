package healthpilot

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/deedglass/deedglass/internal/cli/ui"
	"github.com/deedglass/deedglass/internal/connector/fhir/fhirtest"
	"github.com/deedglass/deedglass/internal/deed"
)

var update = flag.Bool("update", false, "rewrite the golden files")

// fixed makes a run reproducible: ids from a counter, a fixed seal key and a fixed clock.
func fixed(t *testing.T, region string) Options {
	t.Helper()
	n := 0
	return Options{
		Region: region,
		Dir:    t.TempDir(),
		Width:  80,
		Now:    func() time.Time { return time.Date(2026, 10, 27, 9, 30, 0, 0, time.UTC) },
		NewID: func(prefix string) (string, error) {
			n++
			return fmt.Sprintf("%s_01m58nq2jthtfkkv8qdejq%04d", prefix, n), nil
		},
		Seed: bytes.Repeat([]byte{7}, 32),
	}
}

// The scenario outcomes, written before the code (CLI_STYLE.md; the pilot's four scenes).
type want struct {
	decision, rule, policyRule string
	required                   int
	approvers                  []string
	executed                   bool
	status                     int
	verdict                    string
	field                      string // the field that doesn't match (scene 2)
}

var wantScenes = map[string][3]want{
	RegionDefault: {
		{decision: "require_approval", rule: "lockdown.write-needs-person", policyRule: "verify-levels", required: 1,
			approvers: []string{"role:admin"}, executed: true, status: 201, verdict: "VERIFIED"},
		{decision: "require_approval", rule: "lockdown.write-needs-person", policyRule: "verify-levels", required: 1,
			approvers: []string{"role:admin"}, executed: true, status: 201, verdict: "MISMATCH", field: "valueQuantity.value"},
		{decision: "require_approval", rule: "orders-need-clinician", policyRule: "orders-need-clinician", required: 1,
			approvers: []string{"user:clinician-1@example.org", "user:clinician-2@example.org"}},
	},
	RegionIndia: {
		{decision: "require_approval", rule: "lockdown.write-needs-person", policyRule: "verify-levels", required: 1,
			approvers: []string{"role:admin"}, executed: true, status: 201, verdict: "VERIFIED"},
		{decision: "require_approval", rule: "lockdown.write-needs-person", policyRule: "verify-levels", required: 1,
			approvers: []string{"role:admin"}, executed: true, status: 201, verdict: "MISMATCH", field: "valueQuantity.value"},
		{decision: "require_approval", rule: "preauth-large-two-people", policyRule: "preauth-large-two-people", required: 2,
			approvers: []string{"role:admin"}},
	},
}

func TestScenarioOutcomes(t *testing.T) {
	for region, ws := range wantScenes {
		t.Run(region, func(t *testing.T) {
			o := fixed(t, region)
			var out bytes.Buffer
			o.Out = &out
			r, err := Run(context.Background(), o)
			if err != nil {
				t.Fatalf("Run: %v\n%s", err, out.String())
			}
			if !r.PHIMode || !r.Lockdown || r.Store != StoreSynthetic {
				t.Fatalf("setup: phi %v lockdown %v store %q", r.PHIMode, r.Lockdown, r.Store)
			}
			if len(r.Scenes) != 4 {
				t.Fatalf("scenes: %d", len(r.Scenes))
			}
			for i, w := range ws {
				s := r.Scenes[i]
				if s.Decision != w.decision || s.Rule != w.rule || s.PolicyRule != w.policyRule || s.Required != w.required ||
					strings.Join(s.Approvers, ",") != strings.Join(w.approvers, ",") || s.Executed != w.executed ||
					s.HTTPStatus != w.status || s.Verdict != w.verdict {
					t.Errorf("scene %d: got %+v\nwant %+v", i+1, s, w)
				}
				if s.Decision == "require_approval" && s.ExpiresIn != 900 {
					t.Errorf("scene %d: lockdown approvals expire within 15 minutes, got %ds", i+1, s.ExpiresIn)
				}
				if w.field != "" {
					found := false
					for _, f := range s.Fields {
						found = found || (f.Field == w.field && !f.Match)
					}
					if !found {
						t.Errorf("scene %d: no mismatch on %s in %+v", i+1, w.field, s.Fields)
					}
				}
			}
			s3 := r.Scenes[2]
			if s3.Approval == "" || !strings.HasPrefix(s3.ConsoleURL, DefaultConsoleURL+"/app/approvals/apr_") || s3.Executed {
				t.Errorf("scene 3: approval %q link %q executed %v", s3.Approval, s3.ConsoleURL, s3.Executed)
			}
			if r.StoreWrites != 2 {
				t.Errorf("the store holds %d writes; scene 3 must not reach it (want 2)", r.StoreWrites)
			}
			s4 := r.Scenes[3]
			if r.Records != 2 || !r.ChainOK || !r.TamperSeen || s4.Verdict != "" {
				t.Errorf("scene 4: records %d chain %v tamper %v", r.Records, r.ChainOK, r.TamperSeen)
			}
		})
	}
}

func TestGolden(t *testing.T) {
	for name, region := range map[string]string{"pilot": RegionDefault, "pilot_india": RegionIndia} {
		t.Run(name, func(t *testing.T) {
			o := fixed(t, region)
			var out bytes.Buffer
			o.Out = &out
			o.DisplayDir = "deedglass-pilot"
			if _, err := Run(context.Background(), o); err != nil {
				t.Fatal(err)
			}
			got := out.Bytes()
			for i, l := range strings.Split(string(got), "\n") {
				if w := ui.Width(l); w > 80 {
					t.Errorf("line %d is %d columns wide: %q", i+1, w, l)
				}
				if strings.Contains(l, "\x1b") {
					t.Errorf("line %d has an escape code with colour off: %q", i+1, l)
				}
			}
			path := filepath.Join("testdata", name+".golden")
			if *update {
				if err := os.WriteFile(path, got, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("%v (run go test -update)", err)
			}
			if !bytes.Equal(got, want) {
				t.Errorf("output differs from %s (go test -update rewrites it):\n%s", path, got)
			}
		})
	}
}

func TestColourRepeats(t *testing.T) {
	o := fixed(t, RegionDefault)
	var out bytes.Buffer
	o.Out, o.Style = &out, ui.Style{Color: true}
	if _, err := Run(context.Background(), o); err != nil {
		t.Fatal(err)
	}
	plain := fixed(t, RegionDefault)
	var p bytes.Buffer
	plain.Out = &p
	if _, err := Run(context.Background(), plain); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "\x1b[") {
		t.Fatal("no colour with Color on")
	}
	// Colour never carries meaning: without the codes the text is the same (paths aside).
	strip := func(s string) string {
		return regexp.MustCompile(`/[^ ]*/deedglass-pilot|/\S+?/\d+`).ReplaceAllString(ui.StripANSI(s), "DIR")
	}
	a, b := strip(out.String()), strip(p.String())
	if len(strings.Split(a, "\n")) != len(strings.Split(b, "\n")) {
		t.Fatalf("colour changed the layout:\n%s\n---\n%s", a, b)
	}
}

func TestFilesAndReport(t *testing.T) {
	o := fixed(t, RegionDefault)
	o.Out = &bytes.Buffer{}
	r, err := Run(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"report", "deeds", "keys", "review_pack"} {
		if _, err := os.Stat(r.Files[k]); err != nil {
			t.Fatalf("file %s: %v", k, err)
		}
	}
	rep, _ := os.ReadFile(r.Files["report"])
	for _, s := range []string{"# Deedglass health pilot report", "Synthetic data only", "healthcare-fhir", "Doesn't match",
		"orders-need-clinician", "deedglass verify deeds.jsonl --keys keys.json", "--fhir-url"} {
		if !bytes.Contains(rep, []byte(s)) {
			t.Errorf("the report doesn't say %q", s)
		}
	}
	// The sealed records check offline with the published key set alone.
	raw, _ := os.ReadFile(r.Files["keys"])
	ks, err := deed.ParseKeySet(raw)
	if err != nil {
		t.Fatal(err)
	}
	f, _ := os.Open(r.Files["deeds"])
	defer f.Close()
	vr, err := deed.VerifyStream(context.Background(), f, deed.StreamOptions{Keys: ks, KeysPinned: true})
	if err != nil || vr.Result != deed.ResultValid || vr.Deeds != 2 {
		t.Fatalf("verify: %v %+v", err, vr)
	}
}

var banned = regexp.MustCompile(`(?i)\b(trustless|guaranteed?|certified|certification|compliance|compliant|proves)\b`)

func TestNoBannedWords(t *testing.T) {
	o := fixed(t, RegionIndia)
	var out bytes.Buffer
	o.Out = &out
	r, err := Run(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	rep, _ := os.ReadFile(r.Files["report"])
	for name, b := range map[string][]byte{"output": out.Bytes(), "report": rep, "review pack": ReviewPack()} {
		if m := banned.Find(b); m != nil {
			t.Errorf("%s uses the banned word %q", name, m)
		}
	}
}

func TestReviewPack(t *testing.T) {
	p := ReviewPack()
	for _, s := range []string{"# Hospital IT review pack", "```mermaid", "Where data lives", "Threat model", "Security answers",
		"Clinical-safety boundary", "Operational requirements", "Pilot success measures", "deedglass health pilot"} {
		if !bytes.Contains(p, []byte(s)) {
			t.Errorf("the review pack has no %q", s)
		}
	}
}

// TestSandbox points --fhir-url at a second in-memory server standing in for a hospital's sandbox:
// scene 1 writes there, scene 2 stays on the synthetic store.
func TestSandbox(t *testing.T) {
	sb := fhirtest.New()
	defer sb.Close()
	o := fixed(t, RegionDefault)
	o.FHIRURL = sb.Base()
	var out bytes.Buffer
	o.Out = &out
	r, err := Run(context.Background(), o)
	if err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	if r.Store != StoreSandbox || r.Scenes[0].Store != StoreSandbox || r.Scenes[1].Store != StoreSynthetic || r.Scenes[0].Verdict != "VERIFIED" {
		t.Fatalf("sandbox run: %+v", r)
	}
	if sb.Writes() != 2 { // the synthetic Patient and scene 1's Observation
		t.Fatalf("the sandbox holds %d writes, want 2", sb.Writes())
	}
}

func TestFast(t *testing.T) {
	start := time.Now()
	o := fixed(t, RegionDefault)
	o.Out = &bytes.Buffer{}
	if _, err := Run(context.Background(), o); err != nil {
		t.Fatal(err)
	}
	if d := time.Since(start); d > 20*time.Second {
		t.Fatalf("a run took %s; the pilot must finish well inside 2 minutes", d)
	}
}
