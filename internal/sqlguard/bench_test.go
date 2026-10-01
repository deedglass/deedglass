package sqlguard

import (
	"strings"
	"testing"
)

// sqlOfSize builds a realistic statement of about n bytes: a join with a long IN list.
func sqlOfSize(n int) string {
	var b strings.Builder
	b.WriteString("SELECT o.id, o.status, c.name FROM public.orders o JOIN public.customers c ON c.id = o.customer_id WHERE o.id IN (")
	for i := 0; b.Len() < n-40; i++ {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString("4242")
	}
	b.WriteString(") AND o.status = 'open' ORDER BY o.id")
	return b.String()
}

func benchAnalyze(b *testing.B, n int) {
	s := sqlOfSize(n)
	b.SetBytes(int64(len(s)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		Analyze(s, Options{})
	}
}

func BenchmarkAnalyze200B(b *testing.B)   { benchAnalyze(b, 200) }
func BenchmarkAnalyze4KiB(b *testing.B)   { benchAnalyze(b, 4<<10) }
func BenchmarkAnalyze256KiB(b *testing.B) { benchAnalyze(b, 256<<10-64) }
