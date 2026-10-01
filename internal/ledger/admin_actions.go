package ledger

import "regexp"

// Admin actions: the admin_events.action and deed admin.action values, "<object>.<past verb>".
// Every session adds its own constants here, so nobody invents a variant spelling
// (api_key.created is right; key.created is wrong).
const (
	AdminLedgerPruned = "ledger.pruned" // S06: retention removed payloads (Prune)

	// Planned, each added by its session with the exact spelling below:
	//   S07  org.created org.updated member.invited member.joined member.role_changed member.removed
	//        login_link.issued (sign-ins and sign-outs are admin events without deeds)
	//   S08  agent.created agent.updated agent.suspended agent.resumed agent.stopped agent.started
	//        org.stopped_all org.started_all api_key.created api_key.rolled api_key.revoked
	//   S09  agent.intent_key_added agent.intent_key_removed agent.intent_mode_changed
	//   S12  policy.activated policy.deactivated
	//   S16  connector.credentials_updated
	//   S20  decoder.slot_created decoder.slot_dropped
	//   S22  approval.decided approval.countersigned webhook.created webhook.deleted webhook.disabled
	//   S27  anomaly.resolved insights.settings_changed
	//   S28  suggestion.accepted suggestion.rejected
)

// AdminActions lists every admin action this build records. Tests check each name against the
// pattern and for duplicates; RecordAdmin refuses names not in the list.
var AdminActions = []string{
	AdminLedgerPruned,
}

var reAdminAction = regexp.MustCompile(`^[a-z_]+\.[a-z_]+$`)

func knownAdminAction(a string) bool {
	for _, x := range AdminActions {
		if x == a {
			return true
		}
	}
	return false
}
