// Licensed under ee/LICENSE (FSL-1.1-ALv2).
package scribeeval

import (
	"errors"
	"sync"
	"time"
)

var (
	ErrRingBufferFull = errors.New("scribe-eval: ring buffer capacity exceeded")
)

// RingBufferStats holds runtime metrics for the volatile in-memory ring buffer.
type RingBufferStats struct {
	Capacity      int   `json:"capacity"`
	ActiveSlots   int   `json:"active_slots"`
	TotalIngested int64 `json:"total_ingested"`
	TotalEvicted  int64 `json:"total_evicted"`
}

type ringSlot struct {
	event     *EvalEvent
	createdAt time.Time
}

// RingBuffer provides volatile in-memory session management with zeroization on release.
type RingBuffer struct {
	mu            sync.Mutex
	capacity      int
	slots         []ringSlot
	head          int
	tail          int
	count         int
	totalIngested int64
	totalEvicted  int64
}

// NewRingBuffer allocates an in-memory ephemeral buffer with the given capacity.
func NewRingBuffer(capacity int) *RingBuffer {
	if capacity <= 0 {
		capacity = 512
	}
	return &RingBuffer{
		capacity: capacity,
		slots:    make([]ringSlot, capacity),
	}
}

// Push adds an event to the ring buffer. If the buffer is full, the oldest uncommitted
// event is zeroized, evicted, and replaced.
func (rb *RingBuffer) Push(e *EvalEvent) error {
	if e == nil {
		return ErrNilEvent
	}

	rb.mu.Lock()
	defer rb.mu.Unlock()

	// If at capacity, overwrite and evict the oldest slot
	if rb.count == rb.capacity {
		oldest := &rb.slots[rb.tail]
		if oldest.event != nil {
			memclr(oldest.event)
			oldest.event = nil
		}
		rb.tail = (rb.tail + 1) % rb.capacity
		rb.count--
		rb.totalEvicted++
	}

	rb.slots[rb.head] = ringSlot{
		event:     e,
		createdAt: time.Now(),
	}
	rb.head = (rb.head + 1) % rb.capacity
	rb.count++
	rb.totalIngested++
	return nil
}

// EvictExpired zeroes and clears slots older than the given timeout duration (e.g. 300s).
func (rb *RingBuffer) EvictExpired(timeout time.Duration, now time.Time) int {
	rb.mu.Lock()
	defer rb.mu.Unlock()

	evicted := 0
	curr := rb.tail
	remaining := rb.count

	for i := 0; i < remaining; i++ {
		slot := &rb.slots[curr]
		if slot.event != nil && now.Sub(slot.createdAt) > timeout {
			memclr(slot.event)
			slot.event = nil
			rb.totalEvicted++
			evicted++
		}
		curr = (curr + 1) % rb.capacity
	}

	// Compact tail pointer to next valid slot
	for rb.count > 0 && rb.slots[rb.tail].event == nil {
		rb.tail = (rb.tail + 1) % rb.capacity
		rb.count--
	}

	return evicted
}

// Size returns the active count of events stored in volatile memory.
func (rb *RingBuffer) Size() int {
	rb.mu.Lock()
	defer rb.mu.Unlock()
	return rb.count
}

// Stats returns an immutable copy of ring buffer diagnostic counters.
func (rb *RingBuffer) Stats() RingBufferStats {
	rb.mu.Lock()
	defer rb.mu.Unlock()
	return RingBufferStats{
		Capacity:      rb.capacity,
		ActiveSlots:   rb.count,
		TotalIngested: rb.totalIngested,
		TotalEvicted:  rb.totalEvicted,
	}
}

// Purge wipes all active slots in the buffer with zero bytes.
func (rb *RingBuffer) Purge() {
	rb.mu.Lock()
	defer rb.mu.Unlock()

	for i := range rb.slots {
		if rb.slots[i].event != nil {
			memclr(rb.slots[i].event)
			rb.slots[i].event = nil
		}
		rb.slots[i].createdAt = time.Time{}
	}
	rb.head = 0
	rb.tail = 0
	rb.count = 0
}

// memclr zeroes out all fields of an EvalEvent struct before freeing memory.
func memclr(e *EvalEvent) {
	if e == nil {
		return
	}
	e.SchemaVersion = ""
	e.EvalEventID = ""
	e.DataOrigin = ""
	e.ISOWeek = ""
	e.RegionJurisdiction = ""
	e.CareSetting = ""
	e.ClinicianRoleBucket = ""
	e.ScribeAdapterFamily = ""
	e.FHIRResourceType = ""
	e.CodeSystem = ""
	e.ConceptCode = ""
	e.ConceptDisplayCanonical = ""
	e.DeltaClass = ""
	e.WitnessVerdict = ""
	e.SilentDivergenceDetected = false
	e.ReviewLatencyMS = 0
	e.PromptInjectionFlag = false
	e.NegationMismatchFlag = false
	e.UCUMMismatchFlag = false

	clearSlot(&e.ScribeProposedStruct)
	clearSlot(&e.ClinicianFinalStruct)
}

func clearSlot(s *StructuredSlot) {
	if s == nil {
		return
	}
	s.Present = false
	s.DoseValue = nil
	s.DoseUnit = nil
	s.FrequencyCode = nil
	s.RouteCode = nil
	s.Quantity = nil
	s.StatusCode = nil
	s.NegationFlag = nil
	s.UCUMUnitCanonical = nil
}
