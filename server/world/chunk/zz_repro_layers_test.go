package chunk

import (
	"testing"
	"time"

	"github.com/df-mc/dragonfly/server/block/cube"
)

// reproRegistry is a minimal BlockRegistry. Runtime ID 0 is air; any other ID is
// treated as an opaque non-air block. Only the methods the paths under test
// reach are meaningful.
type reproRegistry struct{}

func (reproRegistry) BlockCount() int      { return 2 }
func (reproRegistry) AirRuntimeID() uint32 { return 0 }
func (reproRegistry) RuntimeIDToState(rid uint32) (string, map[string]any, bool) {
	if rid == 0 {
		return "minecraft:air", nil, true
	}
	return "minecraft:stone", nil, true
}
func (reproRegistry) StateToRuntimeID(name string, _ map[string]any) (uint32, bool) {
	if name == "minecraft:air" {
		return 0, true
	}
	return 1, true
}
func (reproRegistry) FilteringBlock(uint32) uint8       { return 0 }
func (reproRegistry) LightBlock(uint32) uint8           { return 0 }
func (reproRegistry) RandomTickBlock(uint32) bool       { return false }
func (reproRegistry) NBTBlock(uint32) bool              { return false }
func (reproRegistry) LiquidDisplacingBlock(uint32) bool { return false }
func (reproRegistry) LiquidBlock(uint32) bool           { return false }
func (reproRegistry) HashToRuntimeID(h uint32) (uint32, bool) {
	return h, true
}
func (reproRegistry) RuntimeIDToHash(rid uint32) (uint32, bool) { return rid, true }

// TestReproLayer255NeverReturns proves that SubChunk.Layer(255) never returns.
//
// sub_chunk.go:71 reads:
//
//	for uint8(len(sub.storages)) <= layer {
//
// len is narrowed to uint8 to compare against the uint8 layer index. The loop
// grows storages to len 255, where uint8(255) <= 255 still holds, so it appends
// once more. At len 256, uint8(256) is 0, and 0 <= layer holds for every uint8,
// so the loop never terminates and appends a 4096-entry storage forever.
//
// Note this needs no decoding and no untrusted input: a single call on a fresh
// sub chunk is enough. SetBlock at layer 255 goes through Layer and so hangs
// identically (sub_chunk.go:95).
func TestReproLayer255NeverReturns(t *testing.T) {
	done := make(chan int, 1)
	go func() {
		sub := NewSubChunk(0)
		sub.Layer(255)
		done <- len(sub.storages)
	}()

	select {
	case n := <-done:
		t.Logf("Layer(255) returned with %d storages", n)
	case <-time.After(2 * time.Second):
		t.Fatal("Layer(255) did not return within 2s: unbounded loop at sub_chunk.go:71. " +
			"The leaked goroutine keeps allocating 4096-entry storages until the process dies.")
	}
}

// TestReproLayer254Terminates is the control: layer 254 is the highest index
// that does terminate today, which pins the boundary.
func TestReproLayer254Terminates(t *testing.T) {
	sub := NewSubChunk(0)
	sub.Layer(254)
	if len(sub.storages) != 255 {
		t.Fatalf("storages = %d, want 255", len(sub.storages))
	}
}

// TestReproGuardsWrapAt256Storages proves that fixing Layer alone is not enough.
//
// Once Layer is corrected to compare in a wider type, Layer(255) legitimately
// grows storages to len 256. The three remaining guards narrow len the same way
// and all misbehave at that length. This test constructs the 256-storage state
// directly (which the fixed Layer will produce) and shows each guard failing.
//
// All four sites must therefore change together; fixing only sub_chunk.go:71
// trades a hang for silent data loss.
func TestReproGuardsWrapAt256Storages(t *testing.T) {
	t.Run("SubChunk.Block returns air for a populated layer", func(t *testing.T) {
		sub := NewSubChunk(0)
		for len(sub.storages) < 256 {
			sub.storages = append(sub.storages, emptyStorage(0))
		}
		sub.storages[3].Set(1, 2, 3, 1)

		// sub_chunk.go:87 -- uint8(256) is 0, so 0 <= 3 short-circuits to air.
		if got := sub.Block(1, 2, 3, 3); got != 1 {
			t.Fatalf("Block(layer 3) = %d, want 1 (the block that is actually stored). "+
				"Guard at sub_chunk.go:87 wrapped and reported the layer as absent.", got)
		}
	})

	t.Run("Chunk.SetBlock silently discards a break", func(t *testing.T) {
		c := New(reproRegistry{}, cube.Range{0, 15})
		sub := c.sub[c.SubIndex(0)]
		for len(sub.storages) < 256 {
			sub.storages = append(sub.storages, emptyStorage(0))
		}
		sub.storages[0].Set(4, 0, 4, 1)

		// Breaking the block: write air at layer 0. chunk.go:115 reads
		//   if uint8(len(sub.storages)) <= layer && block == chunk.air { return }
		// uint8(256) is 0, so every layer reads as absent and the write is dropped.
		c.SetBlock(4, 0, 4, 0, 0)

		if got := sub.storages[0].At(4, 0, 4); got != 0 {
			t.Fatalf("after setting air, storage holds %d, want 0. "+
				"Guard at chunk.go:115 wrapped and discarded the write: breaking a block "+
				"silently does nothing, reported as success.", got)
		}
	})
}

// TestReproCompactRenumbersLayers proves SubChunk.compact renumbers layers.
//
// sub_chunk.go:138 drops EVERY all-air storage and closes the gap, so surviving
// layers move down. Layer numbers are semantic in Bedrock: layer 0 is the block,
// layer 1 is the waterlogging layer. A sub chunk whose layer 0 is air and whose
// layer 1 holds water therefore comes out with the water in layer 0 -- the
// waterlogging becomes a solid liquid block.
//
// Only TRAILING all-air storages carry no information; an internal one does.
func TestReproCompactRenumbersLayers(t *testing.T) {
	const water uint32 = 1
	c := New(reproRegistry{}, cube.Range{0, 15})

	// Waterlogging: layer 1 holds water, layer 0 stays air.
	c.SetBlock(5, 0, 5, 1, water)
	if got := c.Block(5, 0, 5, 1); got != water {
		t.Fatalf("precondition: Block(layer 1) = %d, want %d", got, water)
	}
	if got := c.Block(5, 0, 5, 0); got != 0 {
		t.Fatalf("precondition: Block(layer 0) = %d, want 0 (air)", got)
	}

	c.Compact()

	if got := c.Block(5, 0, 5, 1); got != water {
		t.Errorf("after Compact, Block(layer 1) = %d, want %d: the waterlogging layer was dropped", got, water)
	}
	if got := c.Block(5, 0, 5, 0); got != 0 {
		t.Errorf("after Compact, Block(layer 0) = %d, want 0 (air): water was renumbered from "+
			"layer 1 down into layer 0, turning waterlogging into a solid liquid block", got)
	}
}

// TestReproCompactKeepsTrailingAirDrop is the control: dropping a TRAILING
// all-air storage is correct and must keep working after the fix, since a layer
// past the last stored layer already reads as air.
func TestReproCompactKeepsTrailingAirDrop(t *testing.T) {
	c := New(reproRegistry{}, cube.Range{0, 15})
	c.SetBlock(5, 0, 5, 0, 1)
	sub := c.sub[c.SubIndex(0)]
	sub.storages = append(sub.storages, emptyStorage(0)) // trailing all-air layer 1

	c.Compact()

	if len(sub.storages) != 1 {
		t.Fatalf("storages = %d, want 1: the trailing all-air layer should still be dropped", len(sub.storages))
	}
	if got := c.Block(5, 0, 5, 0); got != 1 {
		t.Fatalf("Block(layer 0) = %d, want 1", got)
	}
}
