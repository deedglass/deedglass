package learn

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

// The sentences the anomaly detectors produce. The CLI prints them and the Console (S28)
// shows them through the API, so the words live here once. Voice: numbers first, calm, no
// adjectives, no alarm. Time ranges read "11:00 to 12:00 IST" in the org's zone.

// AnomalySeparator joins a title's fact and its baseline.
const AnomalySeparator = " · "

// AnomalySpan writes a window as "14:00 to 15:00 IST" in loc (UTC when nil). When the
// zone abbreviation changes inside the window (a DST switch), both ends name theirs.
func AnomalySpan(start, end time.Time, loc *time.Location) string {
	if loc == nil {
		loc = time.UTC
	}
	s, e := start.In(loc), end.In(loc)
	sz, ez := s.Format("MST"), e.Format("MST")
	if sz != ez {
		return s.Format("15:04") + " " + sz + " to " + e.Format("15:04") + " " + ez
	}
	return s.Format("15:04") + " to " + e.Format("15:04") + " " + sz
}

// TitleNewTool: "first github.merge_pr (write), 15:00 to 16:00 IST".
func TitleNewTool(tool, sideEffect, span string) string {
	if sideEffect == "" {
		sideEffect = SideUnknown
	}
	return "first " + tool + " (" + sideEffect + "), " + span
}

// TitleVolumeSpike: "212 calls, 14:00 to 15:00 IST · 9.4× its usual 22.6".
func TitleVolumeSpike(n int, mean float64, span string) string {
	head := anmPlural(n, "call", "calls") + ", " + span
	if mean <= 0 {
		return head + AnomalySeparator + "usually 0 an hour"
	}
	return head + AnomalySeparator + anmDecimal(float64(n)/mean) + "× its usual " + anmDecimal(mean)
}

// TitleOffHours: "12 calls, 02:00 to 03:00 IST · usually none at this hour", or with a
// share above 0, "… · usually 0.3% of its calls at this hour".
func TitleOffHours(n int, share float64, span string) string {
	head := anmPlural(n, "call", "calls") + ", " + span + AnomalySeparator
	if share <= 0 {
		return head + "usually none at this hour"
	}
	return head + "usually " + anmPercent(share) + " of its calls at this hour"
}

// TitleDenySpike: "9 of 24 calls denied, 10:00 to 11:00 IST · usually 2%".
func TitleDenySpike(denied, n int, baselineRate float64, span string) string {
	return anmCount(denied) + " of " + anmPlural(n, "call", "calls") + " denied, " + span +
		AnomalySeparator + "usually " + anmPercent(baselineRate)
}

// TitleMismatchSpike: "3 MISMATCH verdicts, 11:00 to 12:00 IST · usually 0".
func TitleMismatchSpike(m int, mean float64, span string) string {
	return anmPlural(m, "MISMATCH verdict", "MISMATCH verdicts") + ", " + span +
		AnomalySeparator + "usually " + anmDecimal(mean)
}

// TitleUserRefBurst: "61 new on-behalf-of users, 16:00 to 17:00 IST · usually 2 an hour".
func TitleUserRefBurst(r int, mean float64, span string) string {
	return anmPlural(r, "new on-behalf-of user", "new on-behalf-of users") + ", " + span +
		AnomalySeparator + "usually " + anmDecimal(mean) + " an hour"
}

// BaselineBuildingSentence: "support-bot has 41 hours of history. Volume and hours checks
// start after 72 hours."
func BaselineBuildingSentence(agentName string, hours int) string {
	return fmt.Sprintf("%s has %s of history. Volume and hours checks start after %s.",
		agentName, anmPlural(hours, "hour", "hours"), anmPlural(int(MinHistory/time.Hour), "hour", "hours"))
}

// anmPlural writes "1 call" or "1,212 calls".
func anmPlural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return anmCount(n) + " " + many
}

// anmCount writes an integer with thousands separators: 1212 → "1,212".
func anmCount(n int) string {
	return anmGroup(strconv.Itoa(n))
}

// anmDecimal writes one decimal, drops a trailing ".0" and groups thousands:
// 22.6 → "22.6", 2.0 → "2", 1234.56 → "1,234.6". A value above 0 that rounds to 0
// reads "under 0.1".
func anmDecimal(v float64) string {
	r := math.Round(v*10) / 10
	if v > 0 && r == 0 {
		return "under 0.1"
	}
	s := strings.TrimSuffix(strconv.FormatFloat(r, 'f', 1, 64), ".0")
	whole, frac, found := strings.Cut(s, ".")
	if found {
		return anmGroup(whole) + "." + frac
	}
	return anmGroup(whole)
}

// anmPercent writes a rate as a percentage: 0.02 → "2%", 0.003 → "0.3%".
func anmPercent(rate float64) string {
	return anmDecimal(rate*100) + "%"
}

// anmGroup inserts thousands separators into a string of digits with an optional sign.
func anmGroup(digits string) string {
	sign := ""
	if strings.HasPrefix(digits, "-") {
		sign, digits = "-", digits[1:]
	}
	if len(digits) <= 3 {
		return sign + digits
	}
	var b strings.Builder
	head := len(digits) % 3
	if head > 0 {
		b.WriteString(digits[:head])
	}
	for i := head; i < len(digits); i += 3 {
		if b.Len() > 0 {
			b.WriteByte(',')
		}
		b.WriteString(digits[i : i+3])
	}
	return sign + b.String()
}

// anmHourLabel writes an org-local hour of day as "02:00".
func anmHourLabel(hour int) string {
	return fmt.Sprintf("%02d:00", hour)
}
