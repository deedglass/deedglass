package learn

// SetAfterBatch installs the runner's test seam: f runs after every batch, and returning false
// stops the job like a killed runner (no finish; the lease is left to expire).
func SetAfterBatch(r *Runner, f func(processed int64) bool) { r.afterBatch = f }
