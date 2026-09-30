//go:build integration

package store

import "github.com/jackc/pgx/v5/pgxpool"

// PoolForTest exposes the pool to integration tests (a plain transaction with no org, the
// pooled-connection leak test). It is compiled only with the integration build tag.
func (db *DB) PoolForTest() *pgxpool.Pool { return db.pool }
