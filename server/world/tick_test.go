package world

import (
	"testing"
)

// TestConfigTick verifies that the tick callback runs once for every tick the World performs, inside the transaction
// that runs it, and is given the tick being performed.
func TestConfigTick(t *testing.T) {
	var ticks []int64
	w := Config{Synchronous: true, Tick: func(tx *Tx, current int64) {
		if tx == nil {
			t.Error("expected a transaction, got nil")
		}
		ticks = append(ticks, current)
	}}.New()
	defer w.Close()

	for range 5 {
		w.AdvanceTick()
	}

	if len(ticks) != 5 {
		t.Fatalf("tick callback ran %v times, want 5", len(ticks))
	}
	for i := 1; i < len(ticks); i++ {
		if ticks[i] != ticks[i-1]+1 {
			t.Fatalf("ticks = %v, want consecutive values", ticks)
		}
	}
}

// TestConfigTickNil verifies that a World with no tick callback still ticks.
func TestConfigTickNil(t *testing.T) {
	w := Config{Synchronous: true}.New()
	defer w.Close()

	w.AdvanceTick()
}
