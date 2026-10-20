// TestMedplumExample runs examples/medplum/run.sh (make medplum-example-it): Medplum 5.2.6 on
// PostgreSQL 18.6 in Docker, a serve from this tree, the agent's three scenarios and the bot
// handler, and checks each outcome the README shows. It runs only when DEEDGLASS_IT_MEDPLUM_EXAMPLE=1
// and Docker answers, so go test ./... and CI skip it. Synthetic data only; the script removes every
// container, volume and state file after the run.
package medplum_test

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestMedplumExample(t *testing.T) {
	if os.Getenv("DEEDGLASS_IT_MEDPLUM_EXAMPLE") != "1" {
		t.Skip("DEEDGLASS_IT_MEDPLUM_EXAMPLE isn't 1 (make medplum-example-it sets it)")
	}
	docker, err := exec.LookPath("docker")
	if err != nil {
		t.Skip("docker isn't on PATH")
	}
	if err := exec.Command(docker, "info").Run(); err != nil {
		t.Skip("the Docker daemon isn't running")
	}
	cmd := exec.Command("./run.sh")
	cmd.Env = append(os.Environ(), "NO_COLOR=1", "KEEP=", "APPROVE=auto")
	out, err := cmd.CombinedOutput()
	t.Logf("\n%s", out)
	if err != nil {
		t.Fatalf("run.sh: %v", err)
	}
	s := string(out)
	for _, want := range []string{
		"✓ (a) · medplum.create · executed · Proven · Matches",
		"✕ (b) · medplum.create · executed · Proven · Doesn't match",
		"L1 fhir_field_changed: medplum.create reported success, but Observation/",
		"shows note as null",
		"L2 version_field_changed:",
		"◐ (c) · medplum.update · pending approval · apr_",
		"✓ (c) · medplum.update · executed · Proven · Matches",
		"✓ (bot) · medplum.create · executed · Matches",
		"✕ L2 · MISMATCH",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("the output has no %q", want)
		}
	}
	for _, never := range []string{"isn't a Deedglass setting", "Terminated"} {
		if strings.Contains(s, never) {
			t.Errorf("the output has %q", never)
		}
	}
}
