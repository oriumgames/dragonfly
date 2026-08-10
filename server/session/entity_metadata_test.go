package session

import (
	"testing"

	"github.com/sandertv/gophertunnel/minecraft/protocol"
)

// TestSetEntityFlag verifies that a flag is set on the word that holds it. Flags are stored across two words and the
// index of a flag in the second word is relative to the first, so setting it on the first word sets an unrelated flag
// and setting it unadjusted on the second sets nothing at all: the shift is evaluated as zero past 64 bits.
func TestSetEntityFlag(t *testing.T) {
	tests := []struct {
		name  string
		index uint8
		key   uint32
		bit   uint8
	}{
		{name: "first flag", index: protocol.EntityDataFlagOnFire, key: protocol.EntityDataKeyFlags, bit: 0},
		{name: "flag in the first word", index: protocol.EntityDataFlagAngry, key: protocol.EntityDataKeyFlags, bit: 25},
		{name: "last flag of the first word", index: 63, key: protocol.EntityDataKeyFlags, bit: 63},
		{name: "first flag of the second word", index: 64, key: protocol.EntityDataKeyFlagsTwo, bit: 0},
		{name: "flag in the second word", index: protocol.EntityDataFlagStunned, key: protocol.EntityDataKeyFlagsTwo, bit: 19},
		{name: "last flag of the second word", index: 127, key: protocol.EntityDataKeyFlagsTwo, bit: 63},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := protocol.NewEntityMetadata()
			setEntityFlag(m, tt.index)

			if got, ok := m[tt.key].(int64); !ok || got != 1<<tt.bit {
				t.Fatalf("flag word %v = %v, want bit %v set", tt.key, m[tt.key], tt.bit)
			}
			other := uint32(protocol.EntityDataKeyFlags)
			if tt.key == other {
				other = protocol.EntityDataKeyFlagsTwo
			}
			if got, ok := m[other].(int64); ok && got != 0 {
				t.Errorf("flag word %v = %v, want 0: the flag was set on the wrong word", other, got)
			}
		})
	}
}

// TestSetEntityFlagOutOfRange verifies that a flag beyond the two words is refused rather than silently setting
// nothing, which is what shifting an int64 that far does.
func TestSetEntityFlagOutOfRange(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("setEntityFlag(128) returned, want a panic: the flag cannot be represented")
		}
	}()
	setEntityFlag(protocol.NewEntityMetadata(), entityFlagWordSize*2)
}
