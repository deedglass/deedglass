package identity

import (
	"bytes"
	"context"
	"testing"

	"github.com/deedglass/deedglass/internal/httpx"
)

// TestKeyAuthWithoutDatabase (S34, SR-008): a key authenticator built without a database answers a
// well-formed key with service_unavailable instead of dereferencing nil.
func TestKeyAuthWithoutDatabase(t *testing.T) {
	k, err := NewKeyAuth(KeyAuthOptions{Pepper: bytes.Repeat([]byte{7}, 32)})
	if err != nil {
		t.Fatal(err)
	}
	key, _, err := NewAPIKey(ModeTest)
	if err != nil {
		t.Fatal(err)
	}
	_, err = k.AuthenticateKey(context.Background(), key)
	if e := httpx.AsError(err); e == nil || e.Code != httpx.CodeServiceUnavailable {
		t.Fatalf("%v", err)
	}
}
