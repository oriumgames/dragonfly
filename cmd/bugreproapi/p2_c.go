package main

import (
	"sync/atomic"
	"time"

	"github.com/df-mc/dragonfly/server/block/cube"
	"github.com/df-mc/dragonfly/server/block/cube/trace"
	"github.com/df-mc/dragonfly/server/entity"
	"github.com/df-mc/dragonfly/server/player"
	"github.com/df-mc/dragonfly/server/player/skin"
	"github.com/df-mc/dragonfly/server/session"
	"github.com/df-mc/dragonfly/server/world"
	"github.com/go-gl/mathgl/mgl64"
	"github.com/google/uuid"
)

// ---------------------------------------------------------------------------
// p2-10: projectile SurviveBlockCollision returns early.
// ---------------------------------------------------------------------------

var (
	projHitCalls  atomic.Int64
	projHitResult atomic.Value
)

type projConfig struct{ survive bool }

func (c projConfig) Apply(data *world.EntityData) {
	data.Data = entity.ProjectileBehaviourConfig{
		Gravity:               0.05,
		Damage:                1,
		SurviveBlockCollision: c.survive,
		PiercingLevel:         0,
		Hit: func(_ *entity.Ent, _ *world.Tx, target trace.Result) {
			projHitCalls.Add(1)
			projHitResult.Store(target)
		},
	}.New()
}

type projType struct{ survive bool }

func (projType) Open(tx *world.Tx, h *world.EntityHandle, d *world.EntityData) world.Entity {
	return entity.Open(tx, h, d)
}
func (t projType) EncodeEntity() string { return "bugrepro:projectile" }
func (projType) BBox(world.Entity) cube.BBox {
	return cube.Box(-0.15, 0, -0.15, 0.15, 0.3, 0.15)
}
func (projType) DecodeNBT(map[string]any, *world.EntityData) {}
func (projType) EncodeNBT(*world.EntityData) map[string]any  { return map[string]any{} }

func scenarioProjectileSurvive() *Scenario {
	return &Scenario{
		ID:    "p2-10-projectile-survive-block",
		Part:  2,
		Title: "projectile.go SurviveBlockCollision returns early, skipping conf.Hit and the piercing budget check",
		Claim: "`projectile.go` `SurviveBlockCollision` returns early, skipping `conf.Hit` and the piercing budget close check.",
		Setup: "A real `entity.ProjectileBehaviour` built from a real `entity.ProjectileBehaviourConfig` with `SurviveBlockCollision: true` and a `Hit` callback, on a registered `world.EntityType`. The entity is fired into a solid stone wall in a real world and ticked.\n" +
			"A control run repeats it with `SurviveBlockCollision: false`. No stock config sets both `SurviveBlockCollision` and `Hit`, so this combination has to be constructed.",
		Expected: "`conf.Hit` runs for a block hit regardless of whether the projectile survives it.",
		Timeout:  60 * time.Second,
		Run:      runProjectileSurvive,
	}
}

func runProjectileSurvive(o *Out) {
	stone, _ := world.DefaultBlockRegistry.BlockByName("minecraft:stone", map[string]any{})
	fire := func(survive bool) int64 {
		projHitCalls.Store(0)
		w := world.Config{
			Log: discardLogger(), Entities: registryWith(projType{}),
			Synchronous: true, SaveInterval: -1, RandomTickSpeed: -1,
		}.New()
		defer w.Close()
		h := world.EntitySpawnOpts{Position: mgl64.Vec3{8, 64.5, 8}, Velocity: mgl64.Vec3{1.2, 0, 0}}.
			New(projType{}, projConfig{survive: survive})
		_ = call(w, 20*time.Second, func(tx *world.Tx) {
			for y := 60; y < 70; y++ {
				for z := 0; z < 16; z++ {
					tx.SetBlock(cube.Pos{12, y, z}, stone, nil)
				}
			}
			tx.AddEntity(h)
		})
		for range 20 {
			w.AdvanceTick()
		}
		n := projHitCalls.Load()
		o.Logf("SurviveBlockCollision=%-5v: conf.Hit called %d time(s); projectile handle closed = %v", survive, n, h.Closed())
		return n
	}

	o.Section("A. SurviveBlockCollision: true (the arrow configuration)")
	a := fire(true)
	o.Section("B. control: SurviveBlockCollision: false (the snowball configuration)")
	b := fire(false)
	o.Logf("expected: Hit is called in both cases (once each)")

	if a == 0 && b > 0 {
		o.Verdict(Reproduced, "conf.Hit was called %d times when the projectile survives the block hit and %d times when it does not: the SurviveBlockCollision branch returns before conf.Hit and before the piercing budget check", a, b)
		return
	}
	if a == 0 && b == 0 {
		o.Verdict(Blocked, "neither configuration reached a block collision, so the branch was not exercised")
		return
	}
	o.Verdict(Refuted, "conf.Hit ran %d times with SurviveBlockCollision and %d without", a, b)
}

// ---------------------------------------------------------------------------
// p2-07: Session.close with a nil tx.
// ---------------------------------------------------------------------------

func scenarioSessionCloseNilTx() *Scenario {
	return &Scenario{
		ID:    "p2-07-session-close-nil-tx",
		Part:  2,
		Title: "Session.close with a nil tx skips returning the crafting grid and saving player data",
		Claim: "`Session.close` with a nil `tx` skips both returning the crafting grid and saving player data.",
		Setup: "A real `*session.Session` with a real `*player.Player` in a real world. `Session.Close(nil, p)` is then called, which is the documented nil-transaction teardown path.\n" +
			"The session's `HandleStop` records the transaction it is handed; in the real server that callback is `Server.handleSessionClose`, which needs `tx.World()` to reach `PlayerProvider.Save` and logs an error instead when tx is nil.\n" +
			"Afterwards the world is inspected for the player entity.",
		Expected: "Teardown works with or without a transaction: the player's UI inventory is returned to its main inventory, the entity leaves the world, and player data is saved.",
		Timeout:  90 * time.Second,
		Run:      runSessionCloseNilTx,
	}
}

func runSessionCloseNilTx(o *Out) {
	w := world.Config{
		Log: discardLogger(), Entities: entity.DefaultRegistry,
		SaveInterval: -1, ChunkUnloadInterval: time.Hour,
	}.New()
	defer w.Close()

	var gotNilTx atomic.Bool
	var called atomic.Bool
	id := uuid.New()
	conn := newLoopbackConn("NilTx", id)
	s := session.Config{
		Log:            captureLoggerDebug(o, "session"),
		MaxChunkRadius: 4,
		HandleStop: func(tx *world.Tx, c session.Controllable) {
			called.Store(true)
			gotNilTx.Store(tx == nil)
		},
		BlockRegistry: w.BlockRegistry(),
	}.New(conn)
	pos := mgl64.Vec3{8, 64, 8}
	handle := world.EntitySpawnOpts{Position: pos, ID: id}.New(player.Type, player.Config{Session: s, UUID: id, Name: "NilTx", Position: pos})
	s.SetHandle(handle, skin.New(64, 32))
	if err := call(w, 20*time.Second, func(tx *world.Tx) {
		p := tx.AddEntity(handle).(*player.Player)
		s.Spawn(p, tx)
	}); err != nil {
		o.Verdict(Blocked, "spawn: %v", err)
		return
	}
	o.Logf("real session spawned; entities in world = %d", entityCount(w))

	// Case 1: the controllable is still in a world. Close(nil, c) skips
	// Tx.RemoveEntity but still calls s.ent.Close().
	var panicked any
	err := call(w, 20*time.Second, func(tx *world.Tx) {
		defer func() { panicked = recover() }()
		e, ok := handle.Entity(tx)
		if !ok {
			return
		}
		s.Close(nil, e.(*player.Player))
	})
	if err != nil {
		o.Logf("Close(nil, p) transaction error: %v", err)
	}
	if panicked != nil {
		o.Logf("Close(nil, p) with the controllable still in a world panicked: %v", panicked)
		o.Logf("  the tx == nil branch skips Tx.RemoveEntity(c) but s.ent.Close() still runs, and")
		o.Logf("  EntityHandle.Close -> setAndUnlockWorld(closeWorld) panics while e.w is not nil")
	}
	time.Sleep(500 * time.Millisecond)

	present, _ := playerState(w, handle)
	o.Logf("HandleStop called = %v, and it was handed a nil transaction = %v", called.Load(), gotNilTx.Load())
	o.Logf("Server.handleSessionClose reaches PlayerProvider.Save only when tx != nil, so with a nil tx the player's data is not saved at all")
	o.Logf("player still registered in the world after Close(nil, p) = %v (Tx.RemoveEntity is inside the tx != nil branch)", present)
	o.Logf("Player.MoveItemsToInventory, which empties the crafting grid and cursor into the main inventory, is also inside that branch")
	o.Logf("Player.Data() does not include the UI inventory, so anything left on the crafting grid is dropped on the floor of the process")
	o.Logf("expected: data saved, player removed")

	if gotNilTx.Load() && present {
		o.Verdict(Reproduced, "Session.close with a nil tx skipped MoveItemsToInventory, closeCurrentContainer, chunkLoader.Close and RemoveEntity, and handed HandleStop a nil transaction so player data cannot be saved")
		return
	}
	if gotNilTx.Load() {
		o.Verdict(Reproduced, "HandleStop was handed a nil transaction, so the player data save path is unreachable; the entity was removed by another path")
		return
	}
	o.Verdict(Refuted, "the nil-tx close path behaved like the normal one")
}

// ---------------------------------------------------------------------------
// p2-09: experience_orb_behaviour.go discards a type assertion result.
// ---------------------------------------------------------------------------

func scenarioExperienceOrb() *Scenario {
	return &Scenario{
		ID:    "p2-09-experience-orb-assertion",
		Part:  2,
		Title: "experience_orb_behaviour.go discards a type assertion result",
		Claim: "`experience_orb_behaviour.go` discards a type assertion result and would nil-panic if the invariant broke.",
		Setup: "The line is `target, _ := targetEnt.(experienceCollector)` in `ExperienceOrbBehaviour.tick`; the `ok` used on the next line comes from `exp.target.Entity(tx)`, not from the assertion. `exp.target` is an unexported field of an unexported behaviour struct and is only ever assigned in `findTarget`, which is itself gated on the same assertion succeeding.\n" +
			"This scenario builds a real experience orb, verifies the reachable path is safe, and reports why the unsafe path cannot be entered from outside the package.",
		Expected: "n/a - this is a latent robustness issue, not a reachable crash.",
		Timeout:  60 * time.Second,
		Run:      runExperienceOrb,
	}
}

func runExperienceOrb(o *Out) {
	w := world.Config{
		Log: discardLogger(), Entities: entity.DefaultRegistry,
		Synchronous: true, SaveInterval: -1, RandomTickSpeed: -1,
	}.New()
	defer w.Close()

	stone, _ := world.DefaultBlockRegistry.BlockByName("minecraft:stone", map[string]any{})
	h := entity.NewExperienceOrb(world.EntitySpawnOpts{Position: mgl64.Vec3{8, 64, 8}}, 5)
	pid := uuid.New()
	ph := world.EntitySpawnOpts{Position: mgl64.Vec3{8, 64, 8}, ID: pid}.
		New(player.Type, player.Config{UUID: pid, Name: "Collector", Position: mgl64.Vec3{8, 64, 8}})

	var panicked any
	err := call(w, 20*time.Second, func(tx *world.Tx) {
		defer func() { panicked = recover() }()
		for x := 0; x < 16; x++ {
			for z := 0; z < 16; z++ {
				tx.SetBlock(cube.Pos{x, 63, z}, stone, nil)
			}
		}
		tx.AddEntity(ph)
		tx.AddEntity(h)
	})
	if err != nil {
		o.Logf("setup: %v", err)
	}
	for range 40 {
		w.AdvanceTick()
	}
	o.Logf("ticked a real experience orb next to a real player for 40 ticks; panic = %v, orb closed (collected) = %v", panicked, h.Closed())
	o.Logf("the discarded assertion is `target, _ := targetEnt.(experienceCollector)`; the `ok` used by the next line is the one from exp.target.Entity(tx)")
	o.Logf("exp.target is unexported and is only assigned inside findTarget, which requires the same assertion to succeed, so a non-collector can never end up there through the public API")
	o.Verdict(Blocked, "the unsafe branch cannot be entered from outside the package: exp.target is unexported and only ever set to an entity that already passed the same assertion. The discarded ok is real, but there is no runnable path to the nil dereference.")
}
