-- 0030_weekly.sql: S52's Monday email, "What your agents did this week" (ADR-101). No new table:
-- the summary is computed from the org's own rows, and the two additions below only record who
-- wants the email and that each org's week was sent once.
--
-- memberships.weekly_email: the owner setting (default on). Only owners get the email; the column
-- exists on every membership so a person promoted to owner keeps the choice they made. A person
-- turns it off with the one-click link in each email or in Console › Settings.
--
-- learn_runs gains the kind 'weekly': one row per org and ISO week (insert-on-conflict-skip), the
-- same exactly-once guard the Insights digest uses, so several serve instances send one email.

ALTER TABLE deedglass.memberships ADD COLUMN weekly_email boolean NOT NULL DEFAULT true;

ALTER TABLE deedglass.learn_runs DROP CONSTRAINT learn_runs_kind_check;
ALTER TABLE deedglass.learn_runs ADD CONSTRAINT learn_runs_kind_check CHECK (kind IN ('detect', 'suggest', 'digest', 'weekly'));
