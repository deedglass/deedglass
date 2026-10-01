package archive

import (
	"strings"

	"github.com/deedglass/deedglass/internal/deed"
)

// readme renders README.txt (S05 §5): plain text, at most 72 columns, the same for the same header.
func readme(h Header) []byte {
	count := deed.FormatCount(h.LastSeq - h.FirstSeq + 1)
	dates := day(h.From)
	if day(h.To) != dates {
		dates += " to " + day(h.To)
	}
	issuer := strings.TrimSuffix(h.Issuer, "/")
	var b strings.Builder
	w := func(lines ...string) {
		for _, l := range lines {
			b.WriteString(l)
			b.WriteByte('\n')
		}
	}
	w("Deedglass deed bundle",
		"",
		count+" "+plural(h.LastSeq-h.FirstSeq+1, "deed", "deeds")+" sealed by "+issuer,
		"org "+h.Org+" · seq "+deed.FormatCount(h.FirstSeq)+" to "+deed.FormatCount(h.LastSeq),
		dates,
		"",
		"What is inside",
		"  deeds.jsonl          one sealed deed per line, in seq order",
		"  keys.json            the seal keys that sealed them",
		"  manifest.json        counts, file hashes and the chain head",
		"  manifest.seal.json   the exporter's seal over manifest.json",
		"",
		"Check it (no account, no network except fetching the keys)",
		"  deedglass verify <this file> \\",
		"    --keys "+issuer+"/.well-known/deedglass/keys.json",
		"",
		"What a deed records",
		"  Which agent acted, for whom, what tool ran, which policy decided,",
		"  and what the Witness saw. Payloads are not included, only digests.",
		"",
		"What it does not show",
		"  Whether an action was wise. Keys inside this bundle come from the",
		"  exporter; pin the issuer's published keys for authenticity.",
		"",
		"Spec: https://docs.deedglass.com/deeds/spec")
	return []byte(b.String())
}

// day is the date part of a deed time.
func day(t string) string {
	if len(t) >= 10 {
		return t[:10]
	}
	return t
}

func plural(n int64, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
