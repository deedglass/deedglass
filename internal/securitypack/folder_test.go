// Folder checks (VerifyFolder) on packs the Pro builder wrote: one changed byte, another org's
// seal, malformed seal files and an answers.json outside the closed schema.
package securitypack_test

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	ee "github.com/deedglass/deedglass/ee/securitypack"
	"github.com/deedglass/deedglass/internal/deed"
	"github.com/deedglass/deedglass/internal/deed/deedtest"
	sp "github.com/deedglass/deedglass/internal/securitypack"
)

var now = deedtest.BActivated.Add(time.Hour)

const days = 24 * time.Hour

// facts is a small org with real names, so the alias test can look for them.
func facts(org string) *sp.Facts {
	at := func(d time.Duration) *time.Time { t := now.Add(-d); return &t }
	return &sp.Facts{
		Org: sp.Org{ID: org, Name: "Acme Store"}, Period: sp.Period{From: now.Add(-90 * days), To: now}, Now: now, Version: "0.1.0",
		Agents: []sp.Agent{{ID: "agt_01k0000000000000000000000a", Name: "refund-helper", Status: "active", HasOwner: true, CreatedAt: now.Add(-40 * days)},
			{ID: "agt_01k0000000000000000000000b", Name: "orders-writer", Status: "active", CreatedAt: now.Add(-30 * days)}},
		Keys: []sp.Key{{ID: "key_01k0000000000000000000000a", AgentID: "agt_01k0000000000000000000000a", CreatedAt: now.Add(-100 * days), LastUsedAt: at(days)}},
		Connectors: []sp.Connector{{ID: "con_01k0000000000000000000000a", Name: "storefront-db", Type: "postgres", Status: "active", Writes: true, Undo: true,
			Credential: &sp.Credential{Version: 1, Alg: sp.VaultAlg, UpdatedAt: now.Add(-10 * days)}},
			{ID: "con_01k0000000000000000000000b", Name: "payments-live", Type: "stripe", Status: "active", Writes: true}},
		Members: sp.Members{OwnersAdmins: 1},
		Egress:  &sp.Egress{Env: "prod"},
		Rules: sp.Rules{Enforce: &sp.ActiveVersion{PolicyID: "pol_01k0000000000000000000000a", Name: "acme-guardrails", Version: 3,
			VersionID: "pv_01k0000000000000000000000a", DeedID: "deed_01k0000000000000000000000a", ActivatedBy: "usr_01k0000000000000000000000a",
			ActivatedAt: now.Add(-20 * days), YAML: policy},
			Changes: []sp.RuleChange{{ActivationID: "pa_01k0000000000000000000000a", DeedID: "deed_01k0000000000000000000000a", By: "usr_01k0000000000000000000000a", At: now.Add(-20 * days)}}},
		Approvals: sp.Approvals{Total: 2, Approved: 2, MedianDecideSeconds: 300},
		Actions:   sp.Actions{Writes: 50, Checked: 10, Proven: 20, Claimed: 20, Gateway: 60, Finished: 60},
		Records: sp.Records{ChainIntact: true, ChainHead: "sha256:" + strings.Repeat("b", 64), Checked: 70, Signer: deedtest.TestKey("b").ID,
			SealKeys: []sp.SealKey{{ID: deedtest.TestKey("b").ID, Status: "active"}}},
		Build: sp.BuildFacts{Version: "0.1.0", Attack: &sp.AttackSummary{Version: "0.1.0", RunOn: "2026-10-15", Matched: 70}},
	}
}

// policy names the connector, so the probes only see it when they use the real name.
const policy = `deedglass: policy/v1
name: acme-guardrails
defaults:
  decision: deny
  reason: No rule allows this action.
rules:
  - id: storefront-destructive
    match: { tool: "storefront-db.*" }
    when: sql.kind in ["drop", "truncate"] || sql.unbounded_write
    decision: deny
    reason: Destructive SQL never runs.
  - id: storefront-writes
    match: { tool: "storefront-db.*" }
    decision: allow
    verify: L2
`

// ports seal with the test key b on an in-memory chain.
type ports struct {
	m     *deedtest.MemStore
	s     *deed.Sealer
	org   string
	n     int
	recs  []byte
	empty bool
}

func newPorts(t testing.TB, org string) *ports {
	m := deedtest.NewMemStore(func() time.Time { return now })
	k := deedtest.TestKey("b")
	m.SetKey(k.ID, deed.KeyStatus{Status: deed.KeyActive, ActivatedAt: deedtest.BActivated})
	return &ports{m: m, s: deed.NewSealer(k.Signer), org: org, recs: []byte("\x1f\x8b fake bundle bytes")}
}

func (p *ports) seal(ctx context.Context, d sp.FileData) (json.RawMessage, error) {
	p.n++
	dd, err := deed.DigestValue(struct {
		Bytes     int64  `json:"bytes"`
		Label     string `json:"label"`
		MediaType string `json:"media_type"`
		SHA256    string `json:"sha256"`
	}{d.Bytes, d.Label, d.MediaType, d.SHA256})
	if err != nil {
		return nil, err
	}
	label := d.Label
	draft, err := deed.ForAdmin(deed.AdminSnapshot{Org: p.org, Event: deedtest.ID("adm", p.n), Action: "file.sealed",
		Actor: deed.Ref{Type: "user", ID: "usr_01k0000000000000000000000a"}, Target: &deed.Ref{Type: "org", ID: p.org}, Reason: &label, DataDigest: &dd})
	if err != nil {
		return nil, err
	}
	tx := p.m.Begin()
	env, err := p.s.Seal(ctx, tx, draft)
	if err != nil {
		return nil, err
	}
	tx.Commit()
	return env.MarshalCanonical()
}

func (p *ports) keys(context.Context) (json.RawMessage, error) {
	return json.Marshal(deedtest.KeySet())
}

func (p *ports) export(_ context.Context, _, _ time.Time, w io.Writer) (int64, error) {
	if p.empty {
		return 0, sp.ErrNoRecords
	}
	_, err := w.Write(p.recs)
	return 42, err
}

func (p *ports) req(f *sp.Facts, aliases, records bool) sp.BuildRequest {
	return sp.BuildRequest{Facts: f, Aliases: aliases, Records: records, Export: p.export, Seal: p.seal, Keys: p.keys}
}

// build runs a build and unpacks the folder; it returns the folder's path.
func build(t *testing.T, org string, aliases, records bool) (string, *sp.BuildResult) {
	t.Helper()
	p := newPorts(t, org)
	var buf bytes.Buffer
	res, err := ee.New().Build(context.Background(), p.req(facts(org), aliases, records), &buf)
	if err != nil {
		t.Fatal(err)
	}
	return untar(t, &buf, t.TempDir()), res
}

func untar(t *testing.T, r io.Reader, dst string) string {
	t.Helper()
	tr := tar.NewReader(r)
	folder := ""
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if h.Typeflag == tar.TypeDir {
			folder = filepath.Join(dst, strings.TrimSuffix(h.Name, "/"))
			if err := os.MkdirAll(folder, 0o755); err != nil {
				t.Fatal(err)
			}
			continue
		}
		b, _ := io.ReadAll(tr)
		if err := os.WriteFile(filepath.Join(dst, h.Name), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return folder
}

const orgA = "org_01k0000000000000000000000a"
const orgB = "org_01k0000000000000000000000b"

func TestOneChangedByteFails(t *testing.T) {
	for _, name := range []string{sp.FileIndex, sp.FileAnswers, sp.FileRecords, sp.FileMarkdown, sp.FileReadme} {
		dir, _ := build(t, orgA, false, true)
		path := filepath.Join(dir, name)
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		b[len(b)/2] ^= 0x01
		if err := os.WriteFile(path, b, 0o644); err != nil {
			t.Fatal(err)
		}
		r := sp.VerifyFolder(dir, nil)
		if r.OK || !strings.Contains(strings.Join(r.Problems, "\n"), name+": it was changed after it was sealed") {
			t.Errorf("%s: %+v", name, r.Problems)
		}
	}
}

func TestOtherOrgsSealFails(t *testing.T) {
	a, _ := build(t, orgA, false, true)
	b, _ := build(t, orgB, false, true)
	// B's seal over A's files: the digests differ and the org differs.
	sb, _ := os.ReadFile(filepath.Join(b, sp.FileSeal))
	if err := os.WriteFile(filepath.Join(a, sp.FileSeal), sb, 0o644); err != nil {
		t.Fatal(err)
	}
	if r := sp.VerifyFolder(a, nil); r.OK {
		t.Fatal("A's files verified under B's seal")
	}
	// A seal file that claims A but carries B's deeds: refused per file.
	var s sp.SealFile
	_ = json.Unmarshal(sb, &s)
	s.Org = orgA
	raw, _ := sp.MarshalSeal(&s)
	_ = os.WriteFile(filepath.Join(a, sp.FileSeal), raw, 0o644)
	r := sp.VerifyFolder(a, nil)
	if r.OK || !strings.Contains(strings.Join(r.Problems, "\n"), "belongs to "+orgB) {
		t.Fatalf("%v", r.Problems)
	}
}

func TestSealFileRefusals(t *testing.T) {
	for name, edit := range map[string]func(*sp.SealFile){
		"unknown path": func(s *sp.SealFile) { s.Files[0].Path = "../etc/passwd" },
		"duplicate":    func(s *sp.SealFile) { s.Files = append(s.Files, s.Files[0]) },
		"label":        func(s *sp.SealFile) { s.Files[0].Label = "something else" },
		"media type":   func(s *sp.SealFile) { s.Files[1].MediaType = "text/plain" },
		"keys":         func(s *sp.SealFile) { s.Keys = json.RawMessage(`{"object":"nope"}`) },
		"missing file": func(s *sp.SealFile) { s.Files = s.Files[1:] },
		"bad deed":     func(s *sp.SealFile) { s.Files[2].Deed = json.RawMessage(`{"object":"deed"}`) },
	} {
		dir, _ := build(t, orgA, false, true)
		raw, _ := os.ReadFile(filepath.Join(dir, sp.FileSeal))
		var s sp.SealFile
		if err := json.Unmarshal(raw, &s); err != nil {
			t.Fatal(err)
		}
		edit(&s)
		out, _ := sp.MarshalSeal(&s)
		_ = os.WriteFile(filepath.Join(dir, sp.FileSeal), out, 0o644)
		if r := sp.VerifyFolder(dir, nil); r.OK {
			t.Errorf("%s: verified", name)
		}
	}
	// Unknown field, wrong object, missing file.
	dir, _ := build(t, orgA, false, true)
	raw, _ := os.ReadFile(filepath.Join(dir, sp.FileSeal))
	for _, bad := range [][]byte{bytes.Replace(raw, []byte(`"v":1`), []byte(`"v":1,"x":1`), 1), bytes.Replace(raw, []byte(sp.ObjectSeal), []byte("other"), 1)} {
		_ = os.WriteFile(filepath.Join(dir, sp.FileSeal), bad, 0o644)
		if r := sp.VerifyFolder(dir, nil); r.OK {
			t.Error("a malformed seal file verified")
		}
	}
	_ = os.Remove(filepath.Join(dir, sp.FileSeal))
	if r := sp.VerifyFolder(dir, nil); r.OK || !strings.Contains(r.Problems[0], "doesn't exist") {
		t.Errorf("%v", r.Problems)
	}
}

func TestAnswersSchemaEnforcedByVerify(t *testing.T) {
	// An answers.json with an unknown field fails even when re-sealed consistently.
	dir, _ := build(t, orgA, false, true)
	path := filepath.Join(dir, sp.FileAnswers)
	b, _ := os.ReadFile(path)
	b = bytes.Replace(b, []byte(`"v": 1,`), []byte(`"v": 1, "extra": true,`), 1)
	_ = os.WriteFile(path, b, 0o644)
	p := newPorts(t, orgA)
	raw, _ := os.ReadFile(filepath.Join(dir, sp.FileSeal))
	var s sp.SealFile
	_ = json.Unmarshal(raw, &s)
	for i, f := range s.Files {
		if f.Path == sp.FileAnswers {
			sum, n, _ := sp.HashFile(path)
			env, err := p.seal(context.Background(), sp.FileData{SHA256: sum, Bytes: n, MediaType: f.MediaType, Label: f.Label})
			if err != nil {
				t.Fatal(err)
			}
			s.Files[i].SHA256, s.Files[i].Bytes, s.Files[i].Deed = sum, n, env
		}
	}
	out, _ := sp.MarshalSeal(&s)
	_ = os.WriteFile(filepath.Join(dir, sp.FileSeal), out, 0o644)
	r := sp.VerifyFolder(dir, nil)
	if r.OK || !strings.Contains(strings.Join(r.Problems, "\n"), "answers.json") {
		t.Fatalf("%v", r.Problems)
	}
}
