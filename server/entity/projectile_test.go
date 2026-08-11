package entity

import (
	"testing"

	"github.com/df-mc/dragonfly/server/block/cube"
	"github.com/df-mc/dragonfly/server/block/cube/trace"
	"github.com/df-mc/dragonfly/server/world"
	"github.com/go-gl/mathgl/mgl64"
)

// TestProjectileHitOnBlockCollision verifies that the Hit callback of a
// projectile runs when it collides with a block, whether the projectile breaks
// on impact like a snowball or survives it like an arrow.
func TestProjectileHitOnBlockCollision(t *testing.T) {
	tests := []struct {
		name    string
		survive bool
	}{
		{name: "projectile breaking on impact", survive: false},
		{name: "projectile surviving impact", survive: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var hits int
			var hitBlock bool
			conf := ProjectileBehaviourConfig{
				Damage:                -1,
				SurviveBlockCollision: test.survive,
				Hit: func(_ *Ent, _ *world.Tx, target trace.Result) {
					hits++
					if _, ok := target.(trace.BlockResult); ok {
						hitBlock = true
					}
				},
			}

			w := world.Config{Synchronous: true, Entities: DefaultRegistry}.New()
			defer w.Close()

			h := world.EntitySpawnOpts{Position: mgl64.Vec3{8, 64.5, 8}, Velocity: mgl64.Vec3{1.2, 0, 0}}.
				New(testProjectileType{}, conf)
			w.Do(func(tx *world.Tx) {
				stone, ok := tx.World().BlockRegistry().BlockByName("minecraft:stone", map[string]any{})
				if !ok {
					t.Fatal("expected minecraft:stone to be registered")
				}
				for y := 60; y < 70; y++ {
					for z := range 16 {
						tx.SetBlock(cube.Pos{12, y, z}, stone, nil)
					}
				}
				tx.AddEntity(h)
			})
			for range 20 {
				w.AdvanceTick()
			}

			if got, want := hits, 1; got != want {
				t.Fatalf("Hit calls: got %v, want %v", got, want)
			}
			if got, want := hitBlock, true; got != want {
				t.Fatalf("Hit called with a trace.BlockResult: got %v, want %v", got, want)
			}
		})
	}
}

// testProjectileType is a world.EntityType for an entity driven purely by a
// ProjectileBehaviour, so a ProjectileBehaviourConfig can be exercised without
// a stock projectile's other settings.
type testProjectileType struct{}

func (testProjectileType) Open(tx *world.Tx, handle *world.EntityHandle, data *world.EntityData) world.Entity {
	return &Ent{tx: tx, handle: handle, data: data}
}

func (testProjectileType) EncodeEntity() string { return "dragonfly:test_projectile" }

func (testProjectileType) BBox(world.Entity) cube.BBox {
	return cube.Box(-0.125, 0, -0.125, 0.125, 0.25, 0.125)
}

func (testProjectileType) DecodeNBT(map[string]any, *world.EntityData) {}

func (testProjectileType) EncodeNBT(*world.EntityData) map[string]any { return map[string]any{} }
