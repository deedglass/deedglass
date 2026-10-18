package licence

import (
	"time"
)

// RefreshEvery is how old the last check may be before a Pro feature checks the licence again. A
// clock that can't be trusted makes a check due sooner (clock.go).
const RefreshEvery = 24 * time.Hour

// State is what a token allows at a moment.
type State struct {
	Active       bool      // Pro features may run
	DaysSince    int       // whole days since the last successful check
	OfflineUntil time.Time // the last moment Pro keeps working without a check
	PeriodEnd    time.Time // zero when the plan has no end date
	Expired      bool      // the paid period plus its grace is over (not only a missed check)
	ModeMismatch bool      // a test-mode token on a live build, or the reverse
	DueRefresh   bool      // a check is due: the last one is older than RefreshEvery, or the clock can't be trusted (clock.go)
	ClockBack    bool      // the clock runs behind the last check or the latest time seen: Pro waits for an online check
}

// Evaluate applies the grace formula (S48 §3.6), pure and clock-injected:
//
//	ok_validation = now ≤ validated_at + grace_days
//	ok_period     = period_end == null || now ≤ period_end + grace_days
//	pro_active    = sig_valid && livemode matches && ok_validation && ok_period
//	              && now ≥ validated_at − ClockSkew
//
// A clock behind validated_at by more than ClockSkew can't be trusted (clock.go): the state is
// ClockBack, not active, and a refresh is due. The caller has already verified the signature.
func Evaluate(t Token, now time.Time, live bool) State {
	v, _ := time.Parse(TimeLayout, t.ValidatedAt)
	grace := time.Duration(t.GraceDays) * 24 * time.Hour
	s := State{OfflineUntil: v.Add(grace), ModeMismatch: t.Livemode != live}
	if now.After(v) {
		s.DaysSince = int(now.Sub(v) / (24 * time.Hour))
	}
	s.DueRefresh = now.Sub(v) >= RefreshEvery
	okPeriod := true
	if t.PeriodEnd != nil {
		pe, _ := time.Parse(TimeLayout, *t.PeriodEnd)
		s.PeriodEnd = pe
		if pe.Add(grace).Before(s.OfflineUntil) {
			s.OfflineUntil = pe.Add(grace)
		}
		okPeriod = !now.After(pe.Add(grace))
		s.Expired = !okPeriod
	}
	okValidation := !now.After(v.Add(grace))
	s.Active = !s.ModeMismatch && okValidation && okPeriod
	return behind(s, now, v)
}
