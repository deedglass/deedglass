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

	// S16: connectors and the vault. Target {connector, con_…}; data: type, connector (the name),
	// config_hash, and for credentials kind, version and kek_id. Never a value or a digest of one
	// (a hash of a low-entropy password invites offline guessing).
	AdminConnectorCreated            = "connector.created"
	AdminConnectorUpdated            = "connector.updated"
	AdminConnectorDeleted            = "connector.deleted"
	AdminConnectorCredentialsUpdated = "connector.credentials_updated"
	AdminConnectorCredentialsRemoved = "connector.credentials_removed"
	AdminVaultRotated                = "vault.rotated" // target {org, org_…}; data: rewrapped, from, to

	// S17: deedglass connector postgres init applied a plan. Target {connector, con_…}; data: type,
	// connector, config_hash, plan_sha256, changes, database, server_version, provider,
	// roles_created. Never the plan's SQL, a password or a verifier.
	AdminConnectorPostgresInitialized = "connector.postgres_initialized"

	// S20: the decoder created or dropped a connector's replication slot. Target {connector, con_…};
	// data: connector (the name), slot, system_id, reason (created: start; dropped: dropped,
	// connector_deleted, not_witnessed). A new slot is a new baseline.
	AdminDecoderSlotCreated = "decoder.slot_created"
	AdminDecoderSlotDropped = "decoder.slot_dropped"

	// S22: approvals and webhooks. Approvals: actor {user, usr_…}, target {approval, apr_…}; data:
	// approval, action, vote, via, count, required, rule_id, context_digest, request_digest (never a
	// payload, a name or an email); the person's reason in admin.reason. countersigned is a vote
	// that doesn't reach the rule's required count; decided is the final approve or a deny.
	// Webhooks: target {webhook, whk_…}; data: url_host, events, reason (disabled: failing).
	AdminApprovalCountersigned = "approval.countersigned"
	AdminApprovalDecided       = "approval.decided"
	AdminWebhookCreated        = "webhook.created"
	AdminWebhookDeleted        = "webhook.deleted"
	AdminWebhookDisabled       = "webhook.disabled"

	// S07: security events, recorded in admin_events without a deed (AppendAdminEvent).
	AdminUserSignedIn  = "user.signed_in"
	AdminUserSignedOut = "user.signed_out"

	// S27: Insights. A person resolved an anomaly episode (target {anomaly, anm_…}; data: anomaly,
	// agent, kind, tool, severity, occurrences; the reason in admin.reason), or changed Insights
	// settings (target {org, org_…}; data: before and after, the settings' values only), which
	// changes what policies that read anomaly.* see.
	AdminAnomalyResolved         = "anomaly.resolved"
	AdminInsightsSettingsChanged = "insights.settings_changed"

	// S28: suggestions. A person accepted a suggestion (target {suggestion, sug_…}; data:
	// suggestion, kind, generator, fingerprint, evidence_digest, replay_result_digest, policy_id,
	// version_id, version_hash, replaced_shadow_version_id; the person's reason in admin.reason;
	// policy.activated (shadow) is sealed just before it), or rejected one (data: suggestion, kind,
	// generator, fingerprint, evidence_digest). Numbers and ids only, never the diff or the YAML.
	AdminSuggestionAccepted = "suggestion.accepted"
	AdminSuggestionRejected = "suggestion.rejected"
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
	AdminConnectorCreated, AdminConnectorUpdated, AdminConnectorDeleted, AdminConnectorCredentialsUpdated,
	AdminConnectorCredentialsRemoved, AdminVaultRotated, AdminConnectorPostgresInitialized,
	AdminDecoderSlotCreated, AdminDecoderSlotDropped,
	AdminApprovalCountersigned, AdminApprovalDecided, AdminWebhookCreated, AdminWebhookDeleted, AdminWebhookDisabled,
	AdminAnomalyResolved, AdminInsightsSettingsChanged,
	AdminSuggestionAccepted, AdminSuggestionRejected,
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
