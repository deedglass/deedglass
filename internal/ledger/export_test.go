package ledger

import (
	"time"

	"github.com/deedglass/deedglass/internal/ids"
)

// OverviewSettingsSQL is what the Overview's transaction sets first.
const OverviewSettingsSQL = overviewSettingsSQL

// OverviewQueries are the Overview's reads with the arguments one 24 h overview at now binds, for
// the bench's EXPLAIN (TestOverviewBudget).
func OverviewQueries(org ids.OrgID, now time.Time) map[string][]any {
	from, to := Window24h.Bounds(now)
	soon := now.Add(ApprovalExpiringSoon)
	return map[string][]any{
		overviewOrgSQL:       {org},
		overviewSeriesSQL:    {org, from, to, Window24h.Bucket.Seconds()},
		overviewPreviousSQL:  {org, from.Add(-Window24h.Span), from},
		overviewApprovalsSQL: {org, soon},
		overviewStripSQL:     {org, OverviewAgentsShown + 1},
		overviewBudgetedSQL:  {org, overviewBudgetAgents},
		overviewSlotsSQL:     {org},
		overviewMismatchSQL:  {org, now.Add(-MismatchLookback), now, AttentionMax},
		overviewExpiringSQL:  {org, soon, AttentionMax},
	}
}
