package gate

import (
	"os"
	"testing"

	"github.com/deedglass/deedglass/internal/gate/lifecycle"
)

// TestMain turns an illegal edge into a panic at its call site for every test in this package
// (S13 §3.1): in production the same call returns ErrIllegalTransition.
func TestMain(m *testing.M) {
	lifecycle.PanicOnIllegal(true)
	os.Exit(m.Run())
}
