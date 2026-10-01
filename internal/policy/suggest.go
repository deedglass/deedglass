package policy

import "strings"

// damerau is the optimal-string-alignment Damerau–Levenshtein distance over runes: insertions,
// deletions, substitutions and transpositions of adjacent runes each cost 1.
func damerau(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	n, m := len(ra), len(rb)
	if n == 0 {
		return m
	}
	if m == 0 {
		return n
	}
	d := make([][]int, n+1)
	for i := range d {
		d[i] = make([]int, m+1)
		d[i][0] = i
	}
	for j := 0; j <= m; j++ {
		d[0][j] = j
	}
	for i := 1; i <= n; i++ {
		for j := 1; j <= m; j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			d[i][j] = min(d[i-1][j]+1, d[i][j-1]+1, d[i-1][j-1]+cost)
			if i > 1 && j > 1 && ra[i-1] == rb[j-2] && ra[i-2] == rb[j-1] {
				d[i][j] = min(d[i][j], d[i-2][j-2]+1)
			}
		}
	}
	return d[n][m]
}

// suggest returns the closest candidate within distance 2 (case-insensitive), or "". Ties go to
// the earlier candidate, so callers list the likeliest first. A word of one or two runes only
// gets a suggestion at distance 1.
func suggest(word string, candidates []string) string {
	w := strings.ToLower(word)
	limit := 2
	if runeLen(w) <= 2 {
		limit = 1
	}
	best, bestD := "", limit+1
	for _, c := range candidates {
		if d := damerau(w, strings.ToLower(c)); d < bestD {
			best, bestD = c, d
		}
	}
	return best
}

// quoteList renders ["a", "b", "c"] as `a, b and c`.
func quoteList(xs []string) string {
	switch len(xs) {
	case 0:
		return ""
	case 1:
		return xs[0]
	}
	return strings.Join(xs[:len(xs)-1], ", ") + " and " + xs[len(xs)-1]
}

// orList renders ["a", "b", "c"] as `a, b or c`.
func orList(xs []string) string {
	switch len(xs) {
	case 0:
		return ""
	case 1:
		return xs[0]
	}
	return strings.Join(xs[:len(xs)-1], ", ") + " or " + xs[len(xs)-1]
}
