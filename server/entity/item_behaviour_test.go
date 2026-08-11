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
	restY, collected := testHopperCollect(t, world.EntitySpawnOpts{Position: mgl64.Vec3{0.15, 4, 0.15}}, nil)
	t.Logf("dropped on rim:    restY=%v collected=%v", restY, collected)
	if collected != 1 {
		t.Fatalf("precondition failed: item resting on the hopper rim was not collected (restY=%v)", restY)
	}

	// Dropped dead centre: falls through the rim opening and rests on the
	// basin floor at y=1.625, i.e. inside the hopper block.
	restY, collected = testHopperCollect(t, world.EntitySpawnOpts{Position: mgl64.Vec3{0.5, 4, 0.5}}, nil)
	t.Logf("dropped in centre: restY=%v collected=%v", restY, collected)
	if collected != 1 {
		t.Fatalf("item resting inside the hopper's basin was never collected: "+
			"item still alive at y=%v after 60 ticks, hopper holds %v items (expected 1)", restY, collected)
	}
}

// TestHopperCollectsItemOnEdge verifies that a hopper collects an item resting on its rim. An item is narrower than
// a block, so it can be held up by the hopper with its centre over the block beside it.
func TestHopperCollectsItemOnEdge(t *testing.T) {
	restY, collected := testHopperCollect(t, world.EntitySpawnOpts{Position: mgl64.Vec3{1.05, 4, 0.5}}, nil)
	if collected != 1 {
		t.Fatalf("item over the hopper's edge was never collected: item still alive at y=%v after 60 ticks, "+
			"hopper holds %v items, want 1", restY, collected)
	}
}

// TestHopperCollectsItemThroughPartialBlock verifies that a hopper collects an item resting on a block placed on it
// that is not a full block tall. A hopper reaches a block further up than it is tall, so such an item is still within
// its reach.
func TestHopperCollectsItemThroughPartialBlock(t *testing.T) {
	tests := []struct {
		name string
		b    world.Block
	}{
		{name: "carpet", b: block.Carpet{}},
		{name: "bottom slab", b: block.Slab{Block: block.Stone{}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			restY, collected := testHopperCollect(t, world.EntitySpawnOpts{Position: mgl64.Vec3{0.5, 5, 0.5}},
				func(tx *world.Tx) { tx.SetBlock(cube.Pos{0, 2, 0}, test.b, nil) })
			if collected != 1 {
				t.Fatalf("item resting on a %v placed on the hopper was never collected: item still alive at y=%v "+
					"after 60 ticks, hopper holds %v items, want 1", test.name, restY, collected)
			}
		})
	}
}

// TestHopperCollectsItemInFlight verifies that a hopper collects an item that is within its reach without resting on
// anything. The item is thrown across the hopper high enough to leave the column it is in before it could land on it.
func TestHopperCollectsItemInFlight(t *testing.T) {
	opts := world.EntitySpawnOpts{Position: mgl64.Vec3{-1, 2.6, 0.5}, Velocity: mgl64.Vec3{0.9, 0, 0}}
	restY, collected := testHopperCollect(t, opts, func(tx *world.Tx) {
		// A floor for the item to land on once it is past the hopper, so that a failure leaves it somewhere the test
		// can report rather than falling out of the world.
		for x := -3; x <= 3; x++ {
			tx.SetBlock(cube.Pos{x, 0, 0}, block.Stone{}, nil)
		}
	})
	if collected != 1 {
		t.Fatalf("item thrown across the hopper was never collected: item at y=%v after 60 ticks, "+
			"hopper holds %v items, want 1", restY, collected)
	}
}

// TestHopperIgnoresItemBelowBasin verifies that a hopper does not collect an item lying beside it below the floor of
// its basin. A carpet is thinner than that floor, so an item resting on one shares the hopper's layer while being out
// of its reach.
func TestHopperIgnoresItemBelowBasin(t *testing.T) {
	// Spawned at rest on the carpet rather than dropped, so that it never passes the height the hopper does reach.
	opts := world.EntitySpawnOpts{Position: mgl64.Vec3{1.05, 1.0625, 0.5}}
	restY, collected := testHopperCollect(t, opts, func(tx *world.Tx) {
		tx.SetBlock(cube.Pos{1, 0, 0}, block.Stone{}, nil)
		tx.SetBlock(cube.Pos{1, 1, 0}, block.Carpet{}, nil)
	})
	if collected != 0 {
		t.Fatalf("item resting below the hopper's basin was collected: hopper holds %v items, want 0 (restY=%v)",
			collected, restY)
	}
}

// TestHopperIgnoresItemAboveReach verifies that a hopper does not collect an item resting on a full block placed on
// it, which is the block above the highest one it reaches into.
func TestHopperIgnoresItemAboveReach(t *testing.T) {
	restY, collected := testHopperCollect(t, world.EntitySpawnOpts{Position: mgl64.Vec3{0.5, 5, 0.5}},
		func(tx *world.Tx) { tx.SetBlock(cube.Pos{0, 2, 0}, block.Stone{}, nil) })
	if collected != 0 {
		t.Fatalf("item resting on a full block placed on the hopper was collected: hopper holds %v items, want 0 "+
			"(restY=%v)", collected, restY)
	}
}

// testHopperCollect places a hopper at y=1 on top of a stone block, applies setUp if it is not nil, and spawns a
// single diamond item entity with the options passed. It ticks the world and reports the resting Y of the item, or -1
// if it was collected or is gone, along with the number of items in the hopper.
func testHopperCollect(t *testing.T, opts world.EntitySpawnOpts, setUp func(tx *world.Tx)) (restY float64, collected int) {
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

	h := NewItem(opts, item.NewStack(item.Diamond{}, 1))
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
