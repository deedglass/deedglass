// Package pgsql embeds the SQL migrations in sql/ (roles, schema, the
// controlled function txv.execute_transfer_v1 and the txv_pub publication)
// and implements the version-branching installer and the privilege audit
// (spec §4). `txv install` refuses minors below 16.15 / 17.11 / 18.6, creates
// the publication before the slot, checks that the live prosrc hash equals the
// bundle's fn_src_sha256, pins the txv_verifier session settings, and with
// --harden (self-hosted only) revokes PUBLIC EXECUTE on
// pg_logical_emit_message as defence in depth. `txv audit` runs the §4.5
// hardening checklist at install, at verifier start and inside every boundary
// snapshot: no DML grants for application or executor roles, a SECURITY
// DEFINER function owned by txv_owner with a pinned search_path, no user
// triggers or rules on boundary tables, forced RLS, REPLICA IDENTITY FULL, and
// the extensions present.
package pgsql
