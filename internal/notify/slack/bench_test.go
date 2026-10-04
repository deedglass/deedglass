package slack

import "testing"

// BenchmarkBuildApprovalCard builds the pending card (S29 budget: under 50 µs per card).
func BenchmarkBuildApprovalCard(b *testing.B) {
	v := cardBase()
	b.ReportAllocs()
	for b.Loop() {
		if _, _, err := Card(v); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkBuildApprovalCardInjection builds the injection card (50 KiB of arguments).
func BenchmarkBuildApprovalCardInjection(b *testing.B) {
	v := cardCases()["injection"]
	b.ReportAllocs()
	for b.Loop() {
		if _, _, err := Card(v); err != nil {
			b.Fatal(err)
		}
	}
}
