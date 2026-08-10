package entity

import (
	"testing"

	"github.com/df-mc/dragonfly/server/block"
	"github.com/df-mc/dragonfly/server/block/cube"
	"github.com/df-mc/dragonfly/server/item"
	"github.com/df-mc/dragonfly/server/world"
	"github.com/go-gl/mathgl/mgl64"
)

// testHopperDrop drops a single diamond item entity straight down onto the
// hopper at hopperPos from x/z offset off, ticks the world and reports the
// resting Y of the item (or -1 if it was collected) and the number of items in
// the hopper.
func testHopperDrop(t *testing.T, off mgl64.Vec3) (restY float64, collected int) {
	t.Helper()
	w := testWorld(t)
	hopperPos := cube.Pos{0, 1, 0}
	mustDo(t, w, func(tx *world.Tx) {
		tx.SetBlock(cube.Pos{0, 0, 0}, block.Stone{}, nil)
		tx.SetBlock(hopperPos, block.NewHopper(), nil)
	})

	h := NewItem(world.EntitySpawnOpts{Position: off}, item.NewStack(item.Diamond{}, 1))
	mustDo(t, w, func(tx *world.Tx) { tx.AddEntity(h) })
	for range 60 {
		w.AdvanceTick()
	}

	restY = -1
	mustDo(t, w, func(tx *world.Tx) {
		if e, ok := h.Entity(tx); ok {
			restY = e.Position()[1]
		}
		hop := tx.Block(hopperPos).(block.Hopper)
		for _, s := range hop.Inventory(tx, hopperPos).Slots() {
			collected += s.Count()
		}
	})
	return restY, collected
}

// TestHopperCollectsItemInBasin verifies that a hopper collects an item lying inside it, which is the case that
// already worked and must keep working.
func testWorld(t *testing.T) *world.World {
	t.Helper()
	w := world.Config{Synchronous: true, Entities: DefaultRegistry}.New()
	t.Cleanup(func() { _ = w.Close() })
	return w
}

func TestHopperCollectsItemInBasin(t *testing.T) {
	// Control: dropped over the hopper's rim, so it rests on top of the hopper
	// block at y=2.
	restY, collected := testHopperDrop(t, mgl64.Vec3{0.15, 4, 0.15})
	t.Logf("dropped on rim:    restY=%v collected=%v", restY, collected)
	if collected != 1 {
		t.Fatalf("precondition failed: item resting on the hopper rim was not collected (restY=%v)", restY)
	}

	// Dropped dead centre: falls through the rim opening and rests on the
	// basin floor at y=1.625, i.e. inside the hopper block.
	restY, collected = testHopperDrop(t, mgl64.Vec3{0.5, 4, 0.5})
	t.Logf("dropped in centre: restY=%v collected=%v", restY, collected)
	if collected != 1 {
		t.Fatalf("item resting inside the hopper's basin was never collected: "+
			"item still alive at y=%v after 60 ticks, hopper holds %v items (expected 1)", restY, collected)
	}
}

// TestHopperCollectsItemOnEdge verifies that a hopper collects an item resting on its rim. An item is narrower than
// a block, so it can be held up by the hopper with its centre over the block beside it.
func TestHopperCollectsItemOnEdge(t *testing.T) {
	restY, collected := testHopperDrop(t, mgl64.Vec3{1.05, 4, 0.5})
	if collected != 1 {
		t.Fatalf("item over the hopper's edge was never collected: item still alive at y=%v after 60 ticks, "+
			"hopper holds %v items, want 1", restY, collected)
	}
}
