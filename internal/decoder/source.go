package decoder

import "context"

// Source is where a runner's raw messages come from (S20 §3.3; ADR-002): today the SQL peek over
// an ordinary connection, then advance. A streaming source (START_REPLICATION, ADR-002's next
// step) would satisfy the same interface with the same SafeParse and assembler behind it.
type Source interface {
	// Peek streams whole committed transactions and non-transactional messages, in decoding order,
	// from the slot's confirmed position. maxChanges is checked only at transaction boundaries.
	Peek(ctx context.Context, maxChanges int, fn func(Raw) error) (PeekStats, error)
	// Advance moves the slot to `to` (pg_replication_slot_advance) and returns its end LSN.
	Advance(ctx context.Context, to LSN) (LSN, error)
	// Close ends the source.
	Close(ctx context.Context) error
}

// PeekSource is the peek-then-advance Source over a Session.
type PeekSource struct {
	S    *Session
	Slot string
	Pub  string
}

var _ Source = (*PeekSource)(nil)

// Peek implements Source.
func (p *PeekSource) Peek(ctx context.Context, maxChanges int, fn func(Raw) error) (PeekStats, error) {
	return p.S.Peek(ctx, p.Slot, p.Pub, maxChanges, fn)
}

// Advance implements Source.
func (p *PeekSource) Advance(ctx context.Context, to LSN) (LSN, error) {
	return p.S.Advance(ctx, p.Slot, to)
}

// Close implements Source.
func (p *PeekSource) Close(ctx context.Context) error { return p.S.Close(ctx) }
