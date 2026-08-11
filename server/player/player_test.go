package player

import (
	"context"
	"testing"

	"github.com/df-mc/dragonfly/server/block"
	"github.com/df-mc/dragonfly/server/block/cube"
	"github.com/df-mc/dragonfly/server/entity"
	"github.com/df-mc/dragonfly/server/item"
	"github.com/df-mc/dragonfly/server/item/inventory"
	"github.com/df-mc/dragonfly/server/world"
	"github.com/go-gl/mathgl/mgl64"
	"github.com/google/uuid"
)

// TestDropLeftoverItems verifies that only the items that did not fit in the inventory are dropped. Stack.Grow takes a
// delta rather than an absolute count, so growing the leftover stack by the number of items added duplicated it.
func TestDropLeftoverItems(t *testing.T) {
	tests := []struct {
		name string
		do   func(t *testing.T, p *Player)
	}{
		{
			name: "items moved out of a temporary slot",
			do: func(t *testing.T, p *Player) {
				if err := p.ui.SetItem(0, item.NewStack(item.Stick{}, 5)); err != nil {
					t.Fatalf("SetItem() = %v, want nil", err)
				}
				p.MoveItemsToInventory()
			},
		},
		{
			name: "item produced by using another item",
			do: func(t *testing.T, p *Player) {
				// The held item must not be empty, or the new item is put in the held slot instead.
				p.SetHeldSlot(35)
				p.addNewItem(&item.UseContext{NewItem: item.NewStack(item.Stick{}, 5)})
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// 35 full slots of diamonds and 62 sticks in the last, so exactly 2 of the 5 sticks fit.
			inv := inventory.New(36, nil)
			for slot := range 35 {
				if err := inv.SetItem(slot, item.NewStack(item.Diamond{}, 64)); err != nil {
					t.Fatalf("SetItem(%v) = %v, want nil", slot, err)
				}
			}
			if err := inv.SetItem(35, item.NewStack(item.Stick{}, 62)); err != nil {
				t.Fatalf("SetItem() = %v, want nil", err)
			}

			dropped := droppedItems(t, inv, func(p *Player) { tt.do(t, p) })

			if len(dropped) != 1 {
				t.Fatalf("dropped %v items, want 1 (%v)", len(dropped), dropped)
			}
			if got := dropped[0].Count(); got != 3 {
				t.Errorf("dropped %v sticks, want 3: %v were duplicated", got, got-3)
			}
		})
	}
}

// TestDropOversizedStack verifies that every item of a stack larger than its maximum count is dropped. An item entity
// holds at most a maximum sized stack and discards the rest, so dropping such a stack as one entity destroyed the
// remainder while reporting that all of it had been dropped.
func TestDropOversizedStack(t *testing.T) {
	tests := []struct {
		name       string
		count      int
		wantItems  int
		wantStacks int
	}{
		{name: "stack within the maximum", count: 30, wantItems: 30, wantStacks: 1},
		{name: "stack at the maximum", count: 64, wantItems: 64, wantStacks: 1},
		{name: "stack above the maximum", count: 128, wantItems: 128, wantStacks: 2},
		{name: "stack far above the maximum", count: 200, wantItems: 200, wantStacks: 4},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := world.Config{Synchronous: true, Entities: entity.DefaultRegistry}.New()
			defer w.Close()

			pos := mgl64.Vec3{0, 64, 0}
			conf := Config{Name: "test", UUID: uuid.New(), Position: pos, GameMode: world.GameModeSurvival}
			handle := world.EntitySpawnOpts{Position: pos}.New(Type, conf)

			var reported, items, stacks int
			w.Do(func(tx *world.Tx) {
				p := tx.AddEntity(handle).(*Player)

				reported = p.Drop(item.NewStack(item.Diamond{}, tt.count))

				for e := range tx.Entities() {
					b, ok := e.(interface{ Behaviour() entity.Behaviour })
					if !ok {
						continue
					}
					if it, ok := b.Behaviour().(*entity.ItemBehaviour); ok {
						items, stacks = items+it.Item().Count(), stacks+1
					}
				}
			}).Wait(context.Background())

			if items != tt.wantItems {
				t.Errorf("dropped %v items, want %v: %v were destroyed", items, tt.wantItems, tt.wantItems-items)
			}
			if stacks != tt.wantStacks {
				t.Errorf("dropped %v item entities, want %v", stacks, tt.wantStacks)
			}
			if reported != tt.wantItems {
				t.Errorf("Drop() = %v, want %v", reported, tt.wantItems)
			}
		})
	}
}

// TestBlocksUnder verifies the range of blocks a Player is standing on. Both the block the Player came to rest on and
// the block it steps on are looked up through this: it is the layer below the Player's feet, and it spans every column
// the Player's bounding box covers rather than only the one its centre is over.
func TestBlocksUnder(t *testing.T) {
	tests := []struct {
		name      string
		pos       mgl64.Vec3
		low, high cube.Pos
	}{
		{name: "on top of a block", pos: mgl64.Vec3{0.5, 11, 0.5}, low: cube.Pos{0, 10, 0}, high: cube.Pos{0, 10, 0}},
		{name: "on top of a slab", pos: mgl64.Vec3{0.5, 10.5, 0.5}, low: cube.Pos{0, 10, 0}, high: cube.Pos{0, 10, 0}},
		{name: "over the edge of a block", pos: mgl64.Vec3{-0.25, 11, 0.5}, low: cube.Pos{-1, 10, 0}, high: cube.Pos{0, 10, 0}},
		{name: "over a corner of a block", pos: mgl64.Vec3{1.25, 11, 1.25}, low: cube.Pos{0, 10, 0}, high: cube.Pos{1, 10, 1}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := world.Config{Synchronous: true, Entities: entity.DefaultRegistry}.New()
			defer w.Close()

			conf := Config{Name: "test", UUID: uuid.New(), Position: tt.pos, GameMode: world.GameModeSurvival}
			handle := world.EntitySpawnOpts{Position: tt.pos}.New(Type, conf)

			w.Do(func(tx *world.Tx) {
				p := tx.AddEntity(handle).(*Player)
				p.data.Pos = tt.pos

				low, high := p.blocksUnder()
				if low != tt.low || high != tt.high {
					t.Errorf("blocksUnder() at %v = %v..%v, want %v..%v", tt.pos, low, high, tt.low, tt.high)
				}
			})
		})
	}
}

// TestFallOnBlockEdge verifies that a block landed on is found anywhere under the Player rather than only below its
// centre. The Player is 0.6 blocks wide, so it may come to rest on the edge of a block with its centre over the block
// beside it, which used to hide blocks such as slime from the fall damage that landing on them cancels.
func TestFallOnBlockEdge(t *testing.T) {
	// The slime block occupies x and z in [0, 1] and y in [10, 11], so the Player stands at y 11.
	slime := cube.Pos{0, 10, 0}

	tests := []struct {
		name string
		// x is the horizontal centre of the Player. The Player is supported by the slime block for any centre within
		// 0.3 of it, which reaches past the edges of the block itself.
		x        float64
		wantHurt bool
	}{
		{name: "centre over the block", x: 0.5},
		{name: "centre over the edge of the block", x: 0.05},
		{name: "centre past the edge of the block", x: -0.25},
		{name: "centre past the far edge of the block", x: 1.25},
		{name: "beside the block", x: 3, wantHurt: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := world.Config{Synchronous: true, Entities: entity.DefaultRegistry}.New()
			defer w.Close()

			pos := mgl64.Vec3{tt.x, 11, 0.5}
			conf := Config{Name: "test", UUID: uuid.New(), Position: pos, GameMode: world.GameModeSurvival}
			handle := world.EntitySpawnOpts{Position: pos}.New(Type, conf)

			w.Do(func(tx *world.Tx) {
				tx.SetBlock(slime, block.Slime{}, nil)
				p := tx.AddEntity(handle).(*Player)
				p.data.Pos = pos

				before := p.Health()
				p.fall(20)

				if hurt := p.Health() < before; hurt != tt.wantHurt {
					t.Errorf("fall(20) at x %v hurt = %v, want %v: the slime block was not found", tt.x, hurt, tt.wantHurt)
				}
			})
		})
	}
}

// TestCheckOnGround verifies that a Player is only considered to be on the ground when a block is below it. The box
// checked is extended back over the movement of the tick so that ground moved past within one tick is not missed, but
// extending it back over a fall grows it upwards, which used to make a block moved past sideways count as ground.
func TestCheckOnGround(t *testing.T) {
	// The platform occupies x and z in [0, 1] and y in [10, 11].
	platform := cube.Pos{0, 10, 0}

	tests := []struct {
		name     string
		pos      mgl64.Vec3
		deltaPos mgl64.Vec3
		want     bool
	}{
		{
			// Falling alongside the platform and moving into its column on the tick the player passes its bottom
			// face. The player is a full block below the platform and touches nothing.
			name:     "falling past the bottom edge of an overhang",
			pos:      mgl64.Vec3{-0.1, 7.7, 0.5},
			deltaPos: mgl64.Vec3{0.2, -1, 0},
			want:     false,
		},
		{
			name:     "falling straight down alongside the platform",
			pos:      mgl64.Vec3{-0.5, 7.7, 0.5},
			deltaPos: mgl64.Vec3{0, -1, 0},
			want:     false,
		},
		{
			// Standing on top of the platform.
			name:     "standing on a block",
			pos:      mgl64.Vec3{0.5, 11, 0.5},
			deltaPos: mgl64.Vec3{},
			want:     true,
		},
		{
			// Moving horizontally off the top of the platform within a single tick, as when running down stairs. The
			// backwards sweep over the horizontal movement must still find the block that was left.
			name:     "moving off a block within one tick",
			pos:      mgl64.Vec3{1.6, 11, 0.5},
			deltaPos: mgl64.Vec3{1, 0, 0},
			want:     true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := world.Config{Synchronous: true, Entities: entity.DefaultRegistry}.New()
			defer w.Close()

			conf := Config{Name: "test", UUID: uuid.New(), Position: tt.pos, GameMode: world.GameModeSurvival}
			handle := world.EntitySpawnOpts{Position: conf.Position}.New(Type, conf)

			w.Do(func(tx *world.Tx) {
				tx.SetBlock(platform, block.Stone{}, nil)
				p := tx.AddEntity(handle).(*Player)
				p.data.Pos = tt.pos

				if got := p.checkOnGround(tt.deltaPos); got != tt.want {
					t.Errorf("checkOnGround(%v) at %v = %v, want %v", tt.deltaPos, tt.pos, got, tt.want)
				}
			})
		})
	}
}

// TestCloseSessionlessPlayer verifies that closing a player without a session removes it from its world and closes its
// handle, whether it is alive or dead. A dead player is respawned before it is torn down, but a player with no session
// has nothing to respawn for, so it must not take that path.
func TestCloseSessionlessPlayer(t *testing.T) {
	tests := []struct {
		name string
		kill bool
	}{
		{name: "alive player"},
		{name: "dead player", kill: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			w := world.Config{Synchronous: true}.New()
			defer w.Close()

			id, pos := uuid.New(), mgl64.Vec3{8, 64, 8}
			h := world.EntitySpawnOpts{Position: pos, ID: id}.New(Type, Config{UUID: id, Name: "Test", Position: pos})

			w.Do(func(tx *world.Tx) {
				p := tx.AddEntity(h).(*Player)
				if test.kill {
					p.Hurt(p.MaxHealth(), entity.VoidDamageSource{})
				}
				if got, want := p.Dead(), test.kill; got != want {
					t.Fatalf("Dead() before Close: got %v, want %v", got, want)
				}
				_ = p.Close()
			})

			if got, want := h.Closed(), true; got != want {
				t.Fatalf("handle closed after Close: got %v, want %v", got, want)
			}
			var present bool
			w.Do(func(tx *world.Tx) {
				for e := range tx.Entities() {
					if e.H() == h {
						present = true
					}
				}
			})
			if got, want := present, false; got != want {
				t.Fatalf("player still in the world after Close: got %v, want %v", got, want)
			}
		})
	}
}

// droppedItems runs f with a Player using the inventory passed, and returns the items that ended up on the ground.
func droppedItems(t *testing.T, inv *inventory.Inventory, f func(p *Player)) []item.Stack {
	t.Helper()

	w := world.Config{Synchronous: true, Entities: entity.DefaultRegistry}.New()
	defer w.Close()

	conf := Config{
		Name:      "test",
		UUID:      uuid.New(),
		Position:  mgl64.Vec3{0, 64, 0},
		Inventory: inv,
		GameMode:  world.GameModeSurvival,
	}
	handle := world.EntitySpawnOpts{Position: conf.Position}.New(Type, conf)

	var dropped []item.Stack
	w.Do(func(tx *world.Tx) {
		f(tx.AddEntity(handle).(*Player))

		for e := range tx.Entities() {
			b, ok := e.(interface{ Behaviour() entity.Behaviour })
			if !ok {
				continue
			}
			if it, ok := b.Behaviour().(*entity.ItemBehaviour); ok {
				dropped = append(dropped, it.Item())
			}
		}
	}).Wait(context.Background())
	return dropped
}
