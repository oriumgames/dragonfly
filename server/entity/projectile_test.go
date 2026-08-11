package entity

import (
	"testing"

	"github.com/df-mc/dragonfly/server/block"
	"github.com/df-mc/dragonfly/server/block/cube"
	"github.com/df-mc/dragonfly/server/block/cube/trace"
	"github.com/df-mc/dragonfly/server/item"
	"github.com/df-mc/dragonfly/server/world"
	"github.com/go-gl/mathgl/mgl64"
)

// testCollector is a minimal world.Entity implementing Collector. It records
// every Collect call made to it.
type testCollector struct {
	handle *world.EntityHandle
	data   *world.EntityData
	state  *testCollectorState
}

type testCollectorState struct {
	// full makes Collect report that nothing could be collected, mimicking a
	// player with a full inventory (Player.Collect returns (0, true)).
	full      bool
	collected []item.Stack
}

func (c *testCollector) Close() error            { return nil }
func (c *testCollector) H() *world.EntityHandle  { return c.handle }
func (c *testCollector) Position() mgl64.Vec3    { return c.data.Pos }
func (c *testCollector) Rotation() cube.Rotation { return c.data.Rot }
func (c *testCollector) Collect(s item.Stack) (int, bool) {
	c.state.collected = append(c.state.collected, s)
	if c.state.full {
		return 0, true
	}
	return s.Count(), true
}

type testCollectorConfig struct{ state *testCollectorState }

func (c testCollectorConfig) Apply(data *world.EntityData) { data.Data = c.state }

type testCollectorType struct{}

func (testCollectorType) Open(_ *world.Tx, handle *world.EntityHandle, data *world.EntityData) world.Entity {
	return &testCollector{handle: handle, data: data, state: data.Data.(*testCollectorState)}
}
func (testCollectorType) EncodeEntity() string { return "dragonfly:test_collector" }
func (testCollectorType) BBox(world.Entity) cube.BBox {
	return cube.Box(-0.3, 0, -0.3, 0.3, 1.8, 0.3)
}
func (testCollectorType) DecodeNBT(map[string]any, *world.EntityData) {}
func (testCollectorType) EncodeNBT(*world.EntityData) map[string]any  { return nil }

// testStuckArrow spawns an arrow already stuck in the stone block at (0,1,0)
// together with n collectors standing on top of it, ticks the world and returns
// the collectors' states.
func testStuckArrow(t *testing.T, n int, full bool) []*testCollectorState {
	t.Helper()
	w := testWorld(t)
	mustDo(t, w, func(tx *world.Tx) {
		tx.SetBlock(cube.Pos{0, 1, 0}, block.Stone{}, nil)
	})

	conf := arrowConf
	conf.PickupItem = item.NewStack(item.Arrow{}, 1)
	conf.CollisionPosition = cube.Pos{0, 1, 0}
	arrow := world.EntitySpawnOpts{Position: mgl64.Vec3{0.5, 2, 0.5}}.New(ArrowType, conf)

	states := make([]*testCollectorState, n)
	mustDo(t, w, func(tx *world.Tx) {
		tx.AddEntity(arrow)
		for i := range states {
			states[i] = &testCollectorState{full: full}
			tx.AddEntity(world.EntitySpawnOpts{Position: mgl64.Vec3{0.5, 2, 0.5}}.
				New(testCollectorType{}, testCollectorConfig{state: states[i]}))
		}
	})
	for range 10 {
		w.AdvanceTick()
	}
	return states
}

// TestArrowPickedUpOnce verifies that a stuck arrow is handed to exactly
// one collector. ProjectileBehaviour.tryPickup has no return after a successful
// Collect, so it keeps offering the same arrow to every collector in range.
func TestArrowPickedUpOnce(t *testing.T) {
	states := testStuckArrow(t, 3, false)
	total := 0
	for i, s := range states {
		t.Logf("collector %v received %v arrow stack(s)", i, len(s.collected))
		total += len(s.collected)
	}
	if total != 1 {
		t.Fatalf("a single stuck arrow was collected %v times by %v collectors standing next to it "+
			"(expected exactly 1); every collector in range receives its own copy of the arrow", total, len(states))
	}
}

// TestArrowKeptWhenCollectorFull checks that an arrow is not destroyed when
// the collector could not actually take it. tryPickup discards the returned
// count n and only checks the ok bool; Player.Collect returns (0, true) when
// the inventory is full.
func TestArrowKeptWhenCollectorFull(t *testing.T) {
	w := testWorld(t)
	mustDo(t, w, func(tx *world.Tx) {
		tx.SetBlock(cube.Pos{0, 1, 0}, block.Stone{}, nil)
	})

	conf := arrowConf
	conf.PickupItem = item.NewStack(item.Arrow{}, 1)
	conf.CollisionPosition = cube.Pos{0, 1, 0}
	arrow := world.EntitySpawnOpts{Position: mgl64.Vec3{0.5, 2, 0.5}}.New(ArrowType, conf)

	state := &testCollectorState{full: true}
	mustDo(t, w, func(tx *world.Tx) {
		tx.AddEntity(arrow)
		tx.AddEntity(world.EntitySpawnOpts{Position: mgl64.Vec3{0.5, 2, 0.5}}.
			New(testCollectorType{}, testCollectorConfig{state: state}))
	})
	for range 12 {
		w.AdvanceTick()
	}

	alive := false
	mustDo(t, w, func(tx *world.Tx) {
		_, alive = arrow.Entity(tx)
	})
	t.Logf("collect attempts: %v, arrow still in world: %v", len(state.collected), alive)
	if !alive {
		t.Fatalf("the arrow was destroyed after a collector reported it could collect 0 items "+
			"(%v Collect calls); the arrow item is lost even though nobody received it", len(state.collected))
	}
}

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
