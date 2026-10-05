package demo_test

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// TestDemoLivePreflight (S37 §6): make demo-live refuses anything but a Stripe test key, never
// prints the key, and names the next step when the Slack tunnel is missing.
func TestDemoLivePreflight(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash isn't installed")
	}
	run := func(key string) (string, int) {
		cmd := exec.Command("bash", "scripts/demo.sh", "live-preflight")
		var env []string
		for _, kv := range os.Environ() {
			if !strings.HasPrefix(kv, "STRIPE_TEST_KEY=") && !strings.HasPrefix(kv, "DEMO_TUNNEL_URL=") {
				env = append(env, kv)
			}
		}
		cmd.Env = append(env, "STRIPE_TEST_KEY="+key, "NO_COLOR=1", "PATH=/nonexistent-cloudflared:"+os.Getenv("PATH"))
		out, _ := cmd.CombinedOutput()
		return string(out), cmd.ProcessState.ExitCode()
	}
	out, code := run("sk_live_51ZZsecretvalue")
	if code != 1 || !strings.Contains(out, "isn't a test-mode key") || strings.Contains(out, "secretvalue") {
		t.Fatalf("a live key: exit %d\n%s", code, out)
	}
	if exec.Command("pgrep", "-f", "cloudflared tunnel").Run() == nil {
		t.Skip("a cloudflared tunnel is running on this machine; the missing-tunnel row can't be checked")
	}
	out, code = run("sk_test_51ZZsecretvalue")
	if code != 1 || !strings.Contains(out, "✓ Stripe · a test-mode key") || !strings.Contains(out, "Run cloudflared tunnel --url http://127.0.0.1:") || strings.Contains(out, "secretvalue") {
		t.Fatalf("no tunnel: exit %d\n%s", code, out)
	}
}
