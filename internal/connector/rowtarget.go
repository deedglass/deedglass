package connector

// RowTarget is the one row a write changed, as the connector's own query language says (TY-04,
// DL-109). The Witness's row read-back checks it without parsing any vendor's language itself:
// the connector recognises the statement, the core only compares what the read-back returns.
type RowTarget struct {
	Table string // "public.orders"
	Key   string // "id = 42"
	// Why is set when the write isn't one the row read-back can check (an UPDATE of many rows, an
	// INSERT); it becomes the note readback_skipped.
	Why string
}

// RowTargeter is a connector type that offers the row read-back for some of its tools (S19 §3.5:
// postgres.execute). applies is false for a tool it never reads back this way.
type RowTargeter interface {
	RowTarget(call Call, defaultSchema string) (t RowTarget, applies bool)
}

// RowTargeting is an Executor whose connector may offer the row read-back.
type RowTargeting interface {
	RowTarget(call Call, defaultSchema string) (RowTarget, bool)
}

// RowTarget implements RowTargeting; applies is false when the bound type isn't a RowTargeter.
func (b *bound) RowTarget(call Call, defaultSchema string) (RowTarget, bool) {
	rt, ok := b.impl.(RowTargeter)
	if !ok {
		return RowTarget{}, false
	}
	return rt.RowTarget(call, defaultSchema)
}
