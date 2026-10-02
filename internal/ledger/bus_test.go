package ledger

import (
	"fmt"
	"testing"
)

func busPayload(org, typ string, n int) string {
	return fmt.Sprintf(`{"v":1,"event":"evt_%d","type":%q,"org":%q,"id":"act_%d","seq":null,"action":"act_%d","state":null,"deed":null,"at":"2026-10-02T10:00:00.000Z"}`,
		n, typ, org, n, n)
}

func TestBusOrderAndGap(t *testing.T) {
	b := NewBus()
	var inline []BusMsg
	b.Handle(func(m BusMsg) { inline = append(inline, m) })
	ch, cancel := b.Subscribe(16)
	defer cancel()
	for i := range 5 {
		b.Notify(busPayload("org_a", EventActionCreated, i))
	}
	b.Reset()
	b.Notify(`{not json`)
	for i := range 5 {
		m := <-ch
		if m.Gap || m.Event == nil || m.Event.ID != fmt.Sprintf("act_%d", i) {
			t.Fatalf("message %d: %+v", i, m)
		}
	}
	if m := <-ch; !m.Gap {
		t.Fatalf("Reset: %+v", m)
	}
	if m := <-ch; !m.Gap {
		t.Fatalf("a payload this build can't read is a Gap: %+v", m)
	}
	if len(inline) != 7 || !inline[5].Gap || inline[0].Event.Org != "org_a" {
		t.Fatalf("inline handler saw %d messages", len(inline))
	}
	if st := b.Stats(); st.Notified != 5 || st.Malformed != 1 || st.Subscribers != 1 {
		t.Fatalf("stats %+v", st)
	}
}

// A full subscriber never blocks the listener; it gets a Gap before the next event it receives.
func TestBusSlowSubscriberGetsGap(t *testing.T) {
	b := NewBus()
	slow, cancelSlow := b.Subscribe(2)
	defer cancelSlow()
	fast, cancelFast := b.Subscribe(100)
	defer cancelFast()
	for i := range 10 {
		b.Notify(busPayload("org_a", EventActionCreated, i))
	}
	// slow holds 0 and 1; 2..9 were dropped.
	for i := range 2 {
		if m := <-slow; m.Gap || m.Event.ID != fmt.Sprintf("act_%d", i) {
			t.Fatalf("slow %d: %+v", i, m)
		}
	}
	b.Notify(busPayload("org_a", EventActionCreated, 10))
	if m := <-slow; !m.Gap {
		t.Fatalf("after dropping, the next message is a Gap: %+v", m)
	}
	if m := <-slow; m.Gap || m.Event.ID != "act_10" {
		t.Fatalf("then the live event: %+v", m)
	}
	for i := range 11 {
		if m := <-fast; m.Gap || m.Event.ID != fmt.Sprintf("act_%d", i) {
			t.Fatalf("fast %d: %+v", i, m)
		}
	}
	if b.Stats().Dropped == 0 {
		t.Fatal("dropped messages are counted")
	}
	cancelSlow()
	cancelSlow() // twice is fine
	if b.Stats().Subscribers != 1 {
		t.Fatal("cancel removes the subscriber")
	}
}
