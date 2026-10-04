package vault

import (
	"strings"
	"testing"
)

func TestScrubGoogle(t *testing.T) {
	for _, s := range []string{"ya29.a0AfB_byC1dgSENTINEL0123456789", "1//0dgSENTINELgmailREFRESH9xyzzy", "GOCSPX-dgSENTINELclientSECRETabc",
		"rk_test_51dgSENTINELstripeKEY0000", "sk_live_51dgSENTINELliveKEY00000"} {
		out := Scrub("token " + s + " end")
		if strings.Contains(out, "SENTINEL") {
			t.Errorf("%q survived: %q", s[:8], out)
		}
	}
}
