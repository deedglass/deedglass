// Package attack holds the V0 attack suite (spec §13): integration tests run
// on testcontainers against PostgreSQL 16.15, 17.11 and 18.6, one per case in
// attack_meta.json. Every case declares its expected outcome (VALID, BLOCKED,
// DETECTED, INCONCLUSIVE, OUTSIDE SCOPE or NO EFFECT) before it is
// implemented, and an expected outcome is never changed to make a test pass
// without a founder-approved ADR. ATTACK_SUITE.md is generated from the
// metadata plus results. Only the metadata check exists today.
package attack
