package entity

import (
	"testing"

	"github.com/df-mc/dragonfly/server/block"
	"github.com/df-mc/dragonfly/server/block/cube"
	"github.com/df-mc/dragonfly/server/item"
	"github.com/df-mc/dragonfly/server/world"
	"github.com/go-gl/mathgl/mgl64"
)

// TestHopperCollectsItemInBasin verifies that a hopper collects an item lying inside it, which is the case that
// already worked and must keep working.
func TestHopperCollectsItemInBasin(t *testing.T) {
	// Control: dropped over the hopper's rim, so it rests on top of the hopper
	// block at y=2.
	restY, collected := testHopperDrop(t, mgl64.Vec3{0.15, 4, 0.15}, nil)
	t.Logf("dropped on rim:    restY=%v collected=%v", restY, collected)
	if collected != 1 {
		t.Fatalf("precondition failed: item resting on the hopper rim was not collected (restY=%v)", restY)
	}

	// Dropped dead centre: falls through the rim opening and rests on the
	// basin floor at y=1.625, i.e. inside the hopper block.
	restY, collected = testHopperDrop(t, mgl64.Vec3{0.5, 4, 0.5}, nil)
	t.Logf("dropped in centre: restY=%v collected=%v", restY, collected)
	if collected != 1 {
		t.Fatalf("item resting inside the hopper's basin was never collected: "+
			"item still alive at y=%v after 60 ticks, hopper holds %v items (expected 1)", restY, collected)
	}
}

// TestHopperCollectsItemOnEdge verifies that a hopper collects an item resting on its rim. An item is narrower than
// a block, so it can be held up by the hopper with its centre over the block beside it.
func TestHopperCollectsItemOnEdge(t *testing.T) {
	restY, collected := testHopperDrop(t, mgl64.Vec3{1.05, 4, 0.5}, nil)
	if collected != 1 {
		t.Fatalf("item over the hopper's edge was never collected: item still alive at y=%v after 60 ticks, "+
			"hopper holds %v items, want 1", restY, collected)
	}
}

// TestHopperIgnoresItemBelowBasin verifies that a hopper does not collect an item lying beside it below the floor of
// its basin. A carpet is thinner than that floor, so an item resting on one shares the hopper's layer while being out
// of its reach.
func TestHopperIgnoresItemBelowBasin(t *testing.T) {
	// Spawned at rest on the carpet rather than dropped, so that it never passes the height the hopper does reach.
	restY, collected := testHopperDrop(t, mgl64.Vec3{1.05, 1.0625, 0.5}, func(tx *world.Tx) {
		tx.SetBlock(cube.Pos{1, 0, 0}, block.Stone{}, nil)
		tx.SetBlock(cube.Pos{1, 1, 0}, block.Carpet{}, nil)
	})
	if collected != 0 {
		t.Fatalf("item resting below the hopper's basin was collected: hopper holds %v items, want 0 (restY=%v)",
			collected, restY)
	}
}

// testHopperDrop places a hopper at y=1 on top of a stone block, applies setUp if it is not nil, and puts a single
// diamond item entity at off. It ticks the world and reports the resting Y of the item, or -1 if it was collected,
// along with the number of items in the hopper.
func testHopperDrop(t *testing.T, off mgl64.Vec3, setUp func(tx *world.Tx)) (restY float64, collected int) {
	t.Helper()
	w := testWorld(t)
	hopperPos := cube.Pos{0, 1, 0}
	mustDo(t, w, func(tx *world.Tx) {
		tx.SetBlock(cube.Pos{0, 0, 0}, block.Stone{}, nil)
		tx.SetBlock(hopperPos, block.NewHopper(), nil)
		if setUp != nil {
			setUp(tx)
		}
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

// testWorld returns a synchronous World that is closed when the test ends.
func testWorld(t *testing.T) *world.World {
	t.Helper()
	w := world.Config{Synchronous: true, Entities: DefaultRegistry}.New()
	t.Cleanup(func() { _ = w.Close() })
	return w
}
