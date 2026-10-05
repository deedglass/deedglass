package connector_test

import (
	"testing"

	"github.com/deedglass/deedglass/internal/connector"
)

// TestFromStatusRedirect (S34, SR-005): a 3xx that wasn't followed says so and stays unknown.
func TestFromStatusRedirect(t *testing.T) {
	e := connector.FromStatus(302, 0)
	if e.Kind != connector.KindUpstream || e.Code != connector.CodeUpstreamError || e.Retryable ||
		e.AgentMessage != "The service redirected the call (HTTP 302), and Deedglass doesn't follow redirects for this connector. The call may have run." {
		t.Fatalf("%+v", e)
	}
}
