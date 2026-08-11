package entity

import (
	"testing"

	"github.com/df-mc/dragonfly/server/item"
	"github.com/df-mc/dragonfly/server/item/potion"
	"github.com/df-mc/dragonfly/server/world"
	"github.com/go-gl/mathgl/mgl64"
)

// TestNewArrowPickupItem verifies that arrows built through the constructors
// give an arrow back when they are picked up. A projectile without a pickup
// item is destroyed by the collector and yields nothing.
func TestNewArrowPickupItem(t *testing.T) {
	pos := mgl64.Vec3{8, 64, 8}
	strength := potion.Strength()

	tests := []struct {
		name string
		make func(opts world.EntitySpawnOpts, owner world.Entity) *world.EntityHandle
		want item.Stack
	}{
		{
			name: "NewArrow",
			make: NewArrow,
			want: item.NewStack(item.Arrow{}, 1),
		},
		{
			name: "NewArrowWithDamage",
			make: func(opts world.EntitySpawnOpts, owner world.Entity) *world.EntityHandle {
				return NewArrowWithDamage(opts, 5, owner)
			},
			want: item.NewStack(item.Arrow{}, 1),
		},
		{
			name: "NewTippedArrow",
			make: func(opts world.EntitySpawnOpts, owner world.Entity) *world.EntityHandle {
				return NewTippedArrow(opts, owner, strength)
			},
			want: item.NewStack(item.Arrow{Tip: strength}, 1),
		},
		{
			name: "NewTippedArrowWithDamage",
			make: func(opts world.EntitySpawnOpts, owner world.Entity) *world.EntityHandle {
				return NewTippedArrowWithDamage(opts, 5, owner, strength)
			},
			want: item.NewStack(item.Arrow{Tip: strength}, 1),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			w := world.Config{Synchronous: true, Entities: DefaultRegistry}.New()
			defer w.Close()

			w.Do(func(tx *world.Tx) {
				owner := tx.AddEntity(NewItem(world.EntitySpawnOpts{Position: pos}, item.NewStack(item.Arrow{}, 1)))
				arrow := tx.AddEntity(test.make(world.EntitySpawnOpts{Position: pos}, owner)).(*Ent)
				got := arrow.Behaviour().(*ProjectileBehaviour).conf.PickupItem
				if !got.Comparable(test.want) || got.Count() != test.want.Count() {
					t.Fatalf("pickup item: got %v, want %v", got, test.want)
				}
			})
		})
	}
}
