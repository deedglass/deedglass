package policy

import (
	"regexp"
	"strconv"
)

var durationRE = regexp.MustCompile(`^([0-9]{1,9})(s|m|h|d)$`)

// parseDuration reads `<n><unit>` (s, m, h, d) as seconds.
func parseDuration(s string) (int64, bool) {
	m := durationRE.FindStringSubmatch(s)
	if m == nil {
		return 0, false
	}
	n, err := strconv.ParseInt(m[1], 10, 64)
	if err != nil {
		return 0, false
	}
	switch m[2] {
	case "m":
		n *= 60
	case "h":
		n *= 3600
	case "d":
		n *= 86400
	}
	return n, true
}
