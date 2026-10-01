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

	// S08: agents, API keys and Stop. Actors: {user, usr_…}, {api_key, key_…} for org admin keys,
	// or {system, operator} for database-direct CLI commands.
	AdminAgentCreated   = "agent.created"   // → agt; data: name, labels, budgets
	AdminAgentUpdated   = "agent.updated"   // → agt; data: the changed fields and new labels/budgets
	AdminAgentSuspended = "agent.suspended" // → agt
	AdminAgentResumed   = "agent.resumed"   // → agt
	AdminAgentStopped   = "agent.stopped"   // → agt
	AdminAgentStarted   = "agent.started"   // → agt; the reason is required
	AdminOrgStoppedAll  = "org.stopped_all" // → org; data: agents_affected
	AdminOrgStartedAll  = "org.started_all" // → org; the reason is required
	AdminAPIKeyCreated  = "api_key.created" // → key; data: key, mode, scopes
	AdminAPIKeyRolled   = "api_key.rolled"  // → the new key; data: from, to, overlap_s
	AdminAPIKeyRevoked  = "api_key.revoked" // → key

	// S09: signed intents. Same actors as S08.
	AdminAgentIntentKeyAdded   = "agent.intent_key_added"    // → agt; data: kid
	AdminAgentIntentKeyRemoved = "agent.intent_key_removed"  // → agt; data: kid
	AdminAgentIntentMode       = "agent.intent_mode_changed" // → agt; data: from, to

	// S12: policies. Target {policy_version, pv_…}; data: policy_id, name, version, version_id,
	// version_hash, source_hash, mode, activation_id, previous_version_id, acknowledged_warnings.
	// The deed's policy block stays null (S04's validator refuses one on an admin deed).
	AdminPolicyActivated   = "policy.activated"
	AdminPolicyDeactivated = "policy.deactivated"

	// S07: security events, recorded in admin_events without a deed (AppendAdminEvent).
	AdminUserSignedIn  = "user.signed_in"
	AdminUserSignedOut = "user.signed_out"

	// Planned, each added by its session with the exact spelling below:
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
	AdminAgentCreated, AdminAgentUpdated, AdminAgentSuspended, AdminAgentResumed, AdminAgentStopped, AdminAgentStarted,
	AdminOrgStoppedAll, AdminOrgStartedAll, AdminAPIKeyCreated, AdminAPIKeyRolled, AdminAPIKeyRevoked,
	AdminAgentIntentKeyAdded, AdminAgentIntentKeyRemoved, AdminAgentIntentMode,
	AdminPolicyActivated, AdminPolicyDeactivated,
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
