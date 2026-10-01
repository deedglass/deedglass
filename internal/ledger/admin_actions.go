package ledger

import "regexp"

// Admin actions: the admin_events.action and deed admin.action values, "<object>.<past verb>".
// Every session adds its own constants here, so nobody invents a variant spelling
// (api_key.created is right; key.created is wrong).
const (
	AdminLedgerPruned = "ledger.pruned" // S06: retention removed payloads (Prune)

	// S07: people. Sealed: every change to who can act or what they may do.
	AdminOrgCreated        = "org.created"         // operator → org
	AdminOrgUpdated        = "org.updated"         // usr → org
	AdminMemberInvited     = "member.invited"      // usr → mem
	AdminMemberJoined      = "member.joined"       // invitee → mem
	AdminMemberRoleChanged = "member.role_changed" // usr → mem
	AdminMemberRemoved     = "member.removed"      // usr → mem
	AdminLoginLinkIssued   = "login_link.issued"   // operator → usr

	// S07: security events, recorded in admin_events without a deed (AppendAdminEvent).
	AdminUserSignedIn  = "user.signed_in"
	AdminUserSignedOut = "user.signed_out"

	// Planned, each added by its session with the exact spelling below:
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
	AdminOrgCreated, AdminOrgUpdated, AdminMemberInvited, AdminMemberJoined, AdminMemberRoleChanged,
	AdminMemberRemoved, AdminLoginLinkIssued,
}

// AdminEventActions are admin events recorded without a deed (AppendAdminEvent): security events
// that don't change who can act. RecordAdmin refuses them, and AppendAdminEvent refuses the rest.
var AdminEventActions = []string{AdminUserSignedIn, AdminUserSignedOut}

var reAdminAction = regexp.MustCompile(`^[a-z_]+\.[a-z_]+$`)

func knownAdminAction(a string) bool {
	for _, x := range AdminActions {
		if x == a {
			return true
		}
	}
	return false
}
