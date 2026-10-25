package licence

import "testing"

// DL-131: this build turns every known feature on without a key; KeyRequired turns them all off;
// a listed feature stays free under a key-required policy.
func TestPolicy(t *testing.T) {
	if !Current.AllFree {
		t.Fatal("DL-131: this build must run every feature without a key")
	}
	for _, f := range KnownFeatures() {
		if !Current.IsFree(f) {
			t.Errorf("%s needs a key under the current policy", f)
		}
		if KeyRequired.IsFree(f) {
			t.Errorf("%s is free under KeyRequired", f)
		}
	}
	p := Policy{Free: []string{FeatureScanFull}}
	if !p.IsFree(FeatureScanFull) || p.IsFree(FeatureAgency) {
		t.Fatal("a listed feature is free and only that one")
	}
}
