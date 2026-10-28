// Package localstore is the zero-setup store behind `deedglass app` (DL-143, ADR-120): a real
// PostgreSQL 18 cluster that this binary creates, starts and stops in the person's own data
// directory, with no Docker and no database to set up by hand.
//
// Where the PostgreSQL programs come from, in order:
//
//  1. the programs the data directory was created with (store.json records them), so a cluster is
//     never opened by a different major version;
//  2. DEEDGLASS_PG_BIN, a directory holding initdb, pg_ctl and postgres;
//  3. a copy unpacked earlier into <dir>/runtime;
//  4. a PostgreSQL 16 or newer already on this computer (Homebrew, the Linux packages, the Windows
//     installer, or PATH), unless DEEDGLASS_PG_SYSTEM=off;
//  5. a one-time download of the zonky.io build of PostgreSQL 18.6 for this platform from Maven
//     Central, checked against the size and SHA-256 pinned in bundles.go before anything is
//     unpacked. DEEDGLASS_PG_BUNDLE (or the same .jar placed in <dir>/runtime) installs it with no
//     network, which is how an offline machine gets it.
//
// After the first run everything works offline. The cluster listens on 127.0.0.1 only, on a port
// picked at start, with no Unix socket; the superuser and the two Deedglass roles have random
// passwords kept in store.json (0600). The roles are the same as deploy/postgres's
// control-plane-roles.sql: deedglass_migrator owns the schema, deedglass_app is the runtime role
// with row-level security applied.
//
// The package starts processes (initdb, pg_ctl) and never links PostgreSQL: the binary stays
// cgo-free.
package localstore
