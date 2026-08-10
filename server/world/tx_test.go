package world

import (
	"io"
	"log/slog"
	"testing"

	"github.com/df-mc/dragonfly/server/block/cube"
)

// TestBlockLight verifies that BlockLight reports only the light emitted by blocks, where Light folds in the skylight
// reaching the position. Spawning rules are driven by block light, and Light returns the higher of the two, so it
// cannot be inverted to recover it.
func TestBlockLight(t *testing.T) {
	w := Config{Log: slog.New(slog.NewTextHandler(io.Discard, nil))}.New()
	defer w.Close()

	pos := cube.Pos{0, 40, 0}

	runWorld(w, func(tx *Tx) {
		if sky, light := tx.SkyLight(pos), tx.Light(pos); sky != 15 || light != 15 {
			t.Fatalf("SkyLight = %v and Light = %v, want 15: the position is open to the sky", sky, light)
		}
		if got := tx.BlockLight(pos); got != 0 {
			t.Errorf("BlockLight = %v, want 0: no block emits light here", got)
		}

		// Light emitted by a block is stored separately from the skylight, and only BlockLight reads it on its own.
		c := tx.World().chunks[chunkPosFromBlockPos(pos)]
		c.SubChunk(int16(pos[1])).SetBlockLight(uint8(pos[0]&15), uint8(pos[1]&15), uint8(pos[2]&15), 12)

		if got := tx.BlockLight(pos); got != 12 {
			t.Errorf("BlockLight = %v, want 12", got)
		}
		if got := tx.Light(pos); got != 15 {
			t.Errorf("Light = %v, want 15: the skylight is still higher", got)
		}
	})
}

// TestBlockLightUnloadedChunk verifies that BlockLight does not load a chunk, matching Light rather than SkyLight.
func TestBlockLightUnloadedChunk(t *testing.T) {
	w := Config{Log: slog.New(slog.NewTextHandler(io.Discard, nil))}.New()
	defer w.Close()

	far := cube.Pos{5000, 40, 5000}
	runWorld(w, func(tx *Tx) {
		if got := tx.BlockLight(far); got != 0 {
			t.Errorf("BlockLight = %v, want 0 for an unloaded chunk", got)
		}
		if _, ok := w.chunks[chunkPosFromBlockPos(far)]; ok {
			t.Error("BlockLight loaded the chunk, want it left unloaded")
		}
	})
}
