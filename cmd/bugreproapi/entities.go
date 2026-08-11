package main

import (
	"sync/atomic"

	"github.com/df-mc/dragonfly/server/block/cube"
	"github.com/df-mc/dragonfly/server/entity"
	"github.com/df-mc/dragonfly/server/item/inventory"
	"github.com/df-mc/dragonfly/server/world"
	"github.com/go-gl/mathgl/mgl64"
)

// ---------------------------------------------------------------------------
// leakyType: a world.Entity whose Close does not remove itself from the world.
// world.Entity only requires io.Closer; nothing in the interface says Close
// has to deregister. This is the entity shape the leak claim is about.
// ---------------------------------------------------------------------------

var leakTicks atomic.Int64

type leakyConfig struct{ Vel mgl64.Vec3 }

func (c leakyConfig) Apply(data *world.EntityData) { data.Vel = c.Vel }

type leakyType struct{}

func (leakyType) Open(tx *world.Tx, h *world.EntityHandle, d *world.EntityData) world.Entity {
	return &leakyEntity{tx: tx, h: h, d: d}
}
func (leakyType) EncodeEntity() string { return "bugrepro:leaky" }
func (leakyType) BBox(world.Entity) cube.BBox {
	return cube.Box(-0.25, 0, -0.25, 0.25, 0.5, 0.25)
}
func (leakyType) DecodeNBT(map[string]any, *world.EntityData) {}
func (leakyType) EncodeNBT(*world.EntityData) map[string]any  { return map[string]any{} }

type leakyEntity struct {
	tx *world.Tx
	h  *world.EntityHandle
	d  *world.EntityData
}

// Close deliberately does nothing: it does not remove the entity from the
// world and it does not close the handle.
func (e *leakyEntity) Close() error            { return nil }
func (e *leakyEntity) H() *world.EntityHandle  { return e.h }
func (e *leakyEntity) Position() mgl64.Vec3    { return e.d.Pos }
func (e *leakyEntity) Rotation() cube.Rotation { return e.d.Rot }
func (e *leakyEntity) Tick(*world.Tx, int64) {
	leakTicks.Add(1)
	e.d.Pos = e.d.Pos.Add(e.d.Vel)
}

// ---------------------------------------------------------------------------
// moverType: a well-behaved entity that moves by a fixed velocity every tick.
// Used to walk an entity across a chunk border under the real world ticker.
// ---------------------------------------------------------------------------

type moverConfig struct{ Vel mgl64.Vec3 }

func (c moverConfig) Apply(data *world.EntityData) { data.Vel = c.Vel }

type moverType struct{}

func (moverType) Open(tx *world.Tx, h *world.EntityHandle, d *world.EntityData) world.Entity {
	return &moverEntity{tx: tx, h: h, d: d}
}
func (moverType) EncodeEntity() string { return "bugrepro:mover" }
func (moverType) BBox(world.Entity) cube.BBox {
	return cube.Box(-0.25, 0, -0.25, 0.25, 0.5, 0.25)
}
func (moverType) DecodeNBT(m map[string]any, d *world.EntityData) {
	if v, ok := m["Vel"].([]float32); ok && len(v) == 3 {
		d.Vel = mgl64.Vec3{float64(v[0]), float64(v[1]), float64(v[2])}
	}
}
func (moverType) EncodeNBT(d *world.EntityData) map[string]any {
	return map[string]any{"Vel": []float32{float32(d.Vel[0]), float32(d.Vel[1]), float32(d.Vel[2])}}
}

type moverEntity struct {
	tx *world.Tx
	h  *world.EntityHandle
	d  *world.EntityData
}

func (e *moverEntity) Close() error {
	e.tx.RemoveEntity(e)
	return e.h.Close()
}
func (e *moverEntity) H() *world.EntityHandle  { return e.h }
func (e *moverEntity) Position() mgl64.Vec3    { return e.d.Pos }
func (e *moverEntity) Rotation() cube.Rotation { return e.d.Rot }
func (e *moverEntity) Tick(*world.Tx, int64)   { e.d.Pos = e.d.Pos.Add(e.d.Vel) }

// ---------------------------------------------------------------------------
// nilArmourType: an entity whose Armour() returns nil. Session.ViewEntityArmour
// type-asserts to interface{ Armour() *inventory.Armour } and dereferences the
// result without a nil check.
// ---------------------------------------------------------------------------

type nilArmourConfig struct{}

func (nilArmourConfig) Apply(*world.EntityData) {}

type nilArmourType struct{}

func (nilArmourType) Open(tx *world.Tx, h *world.EntityHandle, d *world.EntityData) world.Entity {
	return &nilArmourEntity{h: h, d: d}
}
func (nilArmourType) EncodeEntity() string { return "bugrepro:nil_armour" }
func (nilArmourType) BBox(world.Entity) cube.BBox {
	return cube.Box(-0.3, 0, -0.3, 0.3, 1.8, 0.3)
}
func (nilArmourType) DecodeNBT(map[string]any, *world.EntityData) {}
func (nilArmourType) EncodeNBT(*world.EntityData) map[string]any  { return map[string]any{} }

type nilArmourEntity struct {
	h *world.EntityHandle
	d *world.EntityData
}

func (e *nilArmourEntity) Close() error              { return nil }
func (e *nilArmourEntity) H() *world.EntityHandle    { return e.h }
func (e *nilArmourEntity) Position() mgl64.Vec3      { return e.d.Pos }
func (e *nilArmourEntity) Rotation() cube.Rotation   { return e.d.Rot }
func (e *nilArmourEntity) Armour() *inventory.Armour { return nil }

// registryWith returns entity.DefaultRegistry extended with the extra types
// passed, keeping the default registry's spawn functions.
func registryWith(extra ...world.EntityType) world.EntityRegistry {
	return entity.DefaultRegistry.Config().New(append(entity.DefaultRegistry.Types(), extra...))
}
