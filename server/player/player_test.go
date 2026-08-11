package player

import (
	"context"
	"testing"

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
