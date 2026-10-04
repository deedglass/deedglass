// Package learnstore holds the Postgres adapters of Insights (S27, internal/learn): the replay
// input scanner, replay jobs and their claim, the shadow and stats aggregates, anomaly episodes
// and their history, Insights settings and learn_runs. Every org read runs in a short org
// transaction under row-level security (read-only with a 5 s statement timeout where it only
// reads); the only cross-org reads are the SECURITY DEFINER claim functions of migration 0024,
// which return ids.
package learnstore
