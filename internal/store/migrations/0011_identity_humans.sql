-- 0011_identity_humans.sql: what sign-in, sessions and invites (S07) need beyond 0002.
--
-- 0002 already created orgs, users, memberships, sessions and login_tokens with S07's column names.
-- The real gaps are two lookup indexes and one way for an org to add a person it invites.

-- The newest tokens of a user (voiding older ones, the per-user hourly cap).
CREATE INDEX login_tokens_user_time ON deedglass.login_tokens (user_id, created_at DESC);

-- A user's live sessions (logout everywhere, revoking on removal).
CREATE INDEX sessions_user_live ON deedglass.sessions (user_id) WHERE revoked_at IS NULL;

-- Inviting someone new creates their users row from inside the inviting org's transaction. A user
-- row grants nothing (access comes only from memberships), so the runtime role may insert one,
-- but only in its first state. It can't read the row back until a membership in the current org
-- links it (member_read), and an existing person is found through deedglass.preauth_user_by_email.
CREATE POLICY insert_invited ON deedglass.users FOR INSERT TO deedglass_app
  WITH CHECK (status = 'invited' AND last_login_at IS NULL AND email_verified_at IS NULL);
