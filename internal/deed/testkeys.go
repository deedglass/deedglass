package deed

import "crypto/ed25519"

// The published test-vector keys (testdata/deeds/keys/test-keys.json). Their seeds are
// SHA-256("deedglass/test-vector-key/<name>"), so anyone can rebuild them, and so they must never
// seal a real deed: LoadKeyFile refuses them in prod, and outside tests.
// A unit test derives each thumbprint again from its seed.
var testKeyThumbprints = map[string]string{
	"sha256:f6b1d24a4efdc39230905e0c217093f4d1d5e301ac52c9e763a773e864d93f33": "a", // active, then retired at seq 11
	"sha256:3d3876ffac00eac374b0e6e5735718edfcc7637ace18ef40a3ccc492cf98bf60": "b", // active
	"sha256:fdfb7b7d83630f6ada1d5000d134792652f8a149a1af90ea5a2dd542651b1112": "r", // revoked
}

// TestKeySeedLabel is the string hashed to derive test key <name>'s seed.
const TestKeySeedLabel = "deedglass/test-vector-key/"

// IsTestKey reports whether pub is one of the published test-vector keys.
func IsTestKey(pub ed25519.PublicKey) bool {
	_, ok := testKeyThumbprints[Thumbprint(pub)]
	return ok
}
