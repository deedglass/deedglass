package attack

import (
	"bytes"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/deedglass/deedglass/internal/deed"
	"github.com/deedglass/deedglass/internal/deed/deedtest"
)

// CLI-level F01–F04 (S05): the real deedglass binary, run the way an auditor runs it, on deeds an
// attacker changed. Each case passes only when verify exits 1 and names the right failure. Expected
// outcomes are in attack_meta.json and never change here.

var (
	binOnce sync.Once
	binPath string
	binErr  error
)

// deedglassBin builds cmd/deedglass once per test run.
func deedglassBin(t *testing.T) string {
	t.Helper()
	binOnce.Do(func() {
		dir, err := os.MkdirTemp("", "deedglass-attack-")
		if err != nil {
			binErr = err
			return
		}
		binPath = filepath.Join(dir, "deedglass")
		if runtime.GOOS == "windows" {
			binPath += ".exe"
		}
		out, err := exec.Command("go", "build", "-o", binPath, "../cmd/deedglass").CombinedOutput()
		if err != nil {
			binErr = errors.New(string(out))
		}
	})
	if binErr != nil {
		t.Fatalf("build: %v", binErr)
	}
	return binPath
}

type verifyRun struct {
	exit int
	rep  deed.Report
	text string
}

// verifyCLI runs deedglass verify --json, then again without --json for the human text.
func verifyCLI(t *testing.T, args ...string) verifyRun {
	t.Helper()
	bin := deedglassBin(t)
	run := func(extra ...string) (string, int) {
		cmd := exec.Command(bin, append(append([]string{"verify"}, args...), extra...)...)
		cmd.Env = append(os.Environ(), "NO_COLOR=1")
		var out bytes.Buffer
		cmd.Stdout, cmd.Stderr = &out, &out
		err := cmd.Run()
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return out.String(), ee.ExitCode()
		}
		if err != nil {
			t.Fatal(err)
		}
		return out.String(), 0
	}
	js, code := run("--json")
	var r verifyRun
	if err := json.Unmarshal([]byte(js), &r.rep); err != nil {
		t.Fatalf("not a report: %v\n%s", err, js)
	}
	r.text, r.exit = run()
	if r.exit != code {
		t.Fatalf("exit %d with --json, %d without", code, r.exit)
	}
	return r
}

func writeLines(t *testing.T, name string, lines [][]byte) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	var b bytes.Buffer
	for _, l := range lines {
		b.Write(l)
		b.WriteByte('\n')
	}
	if err := os.WriteFile(p, b.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func writeKeys(t *testing.T, ks *deed.KeySet) string {
	t.Helper()
	b, err := json.Marshal(ks)
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "keys.json")
	if err := os.WriteFile(p, b, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func detected(t *testing.T, id string, r verifyRun, code string, seq int64) {
	t.Helper()
	f := r.rep.FirstFailure()
	if r.exit != 1 || r.rep.Result != deed.ResultInvalid || f == nil || f.Code != code || f.Seq == nil || *f.Seq != seq {
		t.Fatalf("%s: exit %d, failures %+v; want %s at seq %d\n%s", id, r.exit, r.rep.Failures, code, seq, r.text)
	}
	if !strings.HasPrefix(r.text, "✕ ") {
		t.Fatalf("%s: text %q", id, r.text)
	}
	t.Logf("%s: DETECTED by deedglass verify (%s at seq %d): %s", id, code, seq, strings.SplitN(r.text, "\n", 2)[0])
}

// F01 · Deeds sealed by the wrong key → DETECTED. An attacker edits deed 8 (its VERIFIED verdict becomes MISMATCH), re-seals it with their
// own key and ships their own keys.json. Against the issuer's pinned keys every variant fails.
func TestF01WrongKeyCLI(t *testing.T) {
	g := deedtest.NewChain(20, deedtest.ChainOpts{})
	real := writeKeys(t, deedtest.KeySet())
	evilID := deedtest.ID("sk", 66)
	evil, err := deed.NewFileSigner(evilID, bytes.Repeat([]byte{66}, 32))
	if err != nil {
		t.Fatal(err)
	}
	edit := func(b *deed.Body) { b.Witness.Verdict = "MISMATCH" }
	// 1. Re-sealed with the attacker's own key id.
	lines := append([][]byte(nil), g.Lines...)
	lines[7] = deedtest.Canonical(deedtest.Reseal(g.Deeds[7], deedtest.Key{ID: evilID, Signer: evil}, edit))
	detected(t, "F01 (own key id)", verifyCLI(t, writeLines(t, "a.jsonl", lines), "--keys", real), "seal_key_unknown", 8)
	// 2. Re-sealed by the attacker's key, but naming the issuer's key id.
	lines[7] = deedtest.Canonical(deedtest.Reseal(g.Deeds[7], deedtest.Key{ID: deedtest.TestKey("a").ID}, edit, impostor{id: deedtest.TestKey("a").ID, s: evil}))
	detected(t, "F01 (issuer's key id)", verifyCLI(t, writeLines(t, "b.jsonl", lines), "--keys", real), "seal_invalid", 8)
	// 3. Pinning by thumbprint excludes the attacker even inside the issuer's key set.
	ksWithEvil, err := deed.NewKeySet(deedtest.Issuer, append(deedtest.KeyRecords(), deed.KeyRecord{ID: evilID, PublicKey: evil.PublicKey(),
		Status: deed.KeyActive, ActivatedAt: deedtest.AActivated}))
	if err != nil {
		t.Fatal(err)
	}
	lines[7] = deedtest.Canonical(deedtest.Reseal(g.Deeds[7], deedtest.Key{ID: evilID, Signer: evil}, edit))
	r := verifyCLI(t, writeLines(t, "c.jsonl", lines), "--keys", writeKeys(t, ksWithEvil),
		"--pin", deed.Thumbprint(deedtest.TestKey("a").Signer.PublicKey()), "--pin", deed.Thumbprint(deedtest.TestKey("b").Signer.PublicKey()))
	detected(t, "F01 (pinned)", r, "seal_key_unknown", 8)
}

type impostor struct {
	id string
	s  deed.Signer
}

// impostor signs with one key while naming another.
func (i impostor) KeyID() string                 { return i.id }
func (i impostor) PublicKey() ed25519.PublicKey  { return i.s.PublicKey() }
func (i impostor) Sign(m []byte) ([]byte, error) { return i.s.Sign(m) }

// F02 · One byte flipped in an export → DETECTED, with the expected and computed hashes.
func TestF02ByteFlippedCLI(t *testing.T) {
	g := deedtest.NewChain(20, deedtest.ChainOpts{})
	lines := append([][]byte(nil), g.Lines...)
	l := bytes.Clone(lines[11])
	i := bytes.Index(l, []byte(`"latency_ms":`)) + len(`"latency_ms":`)
	l[i]++ // 41 → 51: one byte, still valid JSON
	lines[11] = l
	r := verifyCLI(t, writeLines(t, "deeds.jsonl", lines), "--keys", writeKeys(t, deedtest.KeySet()))
	detected(t, "F02", r, "body_hash_mismatch", 12)
	f := r.rep.FirstFailure()
	if f.Expected == nil || f.Computed == nil || *f.Expected != g.Deeds[11].BodyHash || *f.Expected == *f.Computed {
		t.Fatalf("hashes: %+v", f)
	}
	if !strings.Contains(r.text, "✕ Deed 12 was changed after it was sealed.") || r.rep.Intact != 11 {
		t.Fatalf("%s", r.text)
	}
}

// F03 · A deed deleted from the middle → DETECTED (the chain jumps).
func TestF03DeletedFromChainCLI(t *testing.T) {
	g := deedtest.NewChain(20, deedtest.ChainOpts{})
	lines := append(append([][]byte(nil), g.Lines[:9]...), g.Lines[10:]...) // deed 10 removed
	r := verifyCLI(t, writeLines(t, "deeds.jsonl", lines), "--keys", writeKeys(t, deedtest.KeySet()))
	detected(t, "F03", r, "chain_seq_gap", 11)
	if !strings.Contains(r.text, "Deed 10 is missing. The chain jumps from 9 to 11.") {
		t.Fatalf("%s", r.text)
	}
	// Renumbering cannot hide it: the next deed's prev_hash names the removed one.
	renum := append([][]byte(nil), g.Lines[:9]...)
	for _, d := range g.Deeds[10:] {
		renum = append(renum, deedtest.Canonical(deedtest.Reseal(d, deedtest.TestKey("b"), func(b *deed.Body) { b.Seq-- })))
	}
	r = verifyCLI(t, writeLines(t, "renumbered.jsonl", renum), "--keys", writeKeys(t, deedtest.KeySet()))
	detected(t, "F03 (renumbered)", r, "chain_prev_hash_mismatch", 10)
}

// F04 · Deeds presented for another org or another deployment → DETECTED.
func TestF04AnotherDeploymentCLI(t *testing.T) {
	ours := deedtest.NewChain(5, deedtest.ChainOpts{Org: deedtest.Org(2)})
	theirs := deedtest.NewChain(5, deedtest.ChainOpts{Org: deedtest.Org(3)})
	ks := writeKeys(t, deedtest.KeySet())
	// Another org's export, checked as ours.
	r := verifyCLI(t, writeLines(t, "theirs.jsonl", theirs.Lines), "--keys", ks, "--org", deedtest.Org(2))
	detected(t, "F04 (other org)", r, "chain_org_mismatch", 1)
	// Another org's deed spliced into ours.
	mixed := append(append([][]byte(nil), ours.Lines[:2]...), theirs.Lines[2])
	detected(t, "F04 (spliced)", verifyCLI(t, writeLines(t, "mixed.jsonl", mixed), "--keys", ks), "chain_org_mismatch", 3)
	// Another deployment: same org id, sealed by that deployment's own keys.
	otherDeploy, _ := deed.NewFileSigner(deedtest.ID("sk", 44), bytes.Repeat([]byte{44}, 32))
	k := deedtest.Key{ID: otherDeploy.KeyID(), Signer: otherDeploy}
	foreign := deedtest.NewChain(5, deedtest.ChainOpts{Org: deedtest.Org(2), KeyFor: func(int64) deedtest.Key { return k }})
	detected(t, "F04 (other deployment)", verifyCLI(t, writeLines(t, "foreign.jsonl", foreign.Lines), "--keys", ks), "seal_key_unknown", 1)
}
