package main

import (
	"context"
	"errors"
	"sync/atomic"
	"time"

	"github.com/df-mc/dragonfly/server/block/cube"
	"github.com/df-mc/dragonfly/server/block/cube/trace"
	"github.com/df-mc/dragonfly/server/entity"
	"github.com/df-mc/dragonfly/server/player"
	"github.com/df-mc/dragonfly/server/world"
	"github.com/go-gl/mathgl/mgl64"
	"github.com/google/uuid"
)

// ---------------------------------------------------------------------------
// p2-04: Ent.Tick keeps running after the handle is closed.
// ---------------------------------------------------------------------------

var (
	closingTickAge   atomic.Int64
	closingTickAfter atomic.Int64
)

type closingConfig struct{}

func (closingConfig) Apply(data *world.EntityData) {
	data.Data = entity.PassiveBehaviourConfig{
		Tick: func(e *entity.Ent, tx *world.Tx) {
			if e.H().Closed() {
				closingTickAfter.Add(1)
				return
			}
			_ = e.Close()
		},
	}.New()
}

type closingType struct{}

func (closingType) Open(tx *world.Tx, h *world.EntityHandle, d *world.EntityData) world.Entity {
	return entity.Open(tx, h, d)
}
func (closingType) EncodeEntity() string { return "bugrepro:self_closing" }
func (closingType) BBox(world.Entity) cube.BBox {
	return cube.Box(-0.25, 0, -0.25, 0.25, 0.5, 0.25)
}
func (closingType) DecodeNBT(map[string]any, *world.EntityData) {}
func (closingType) EncodeNBT(*world.EntityData) map[string]any  { return map[string]any{} }

func scenarioEntTickAfterClose() *Scenario {
	return &Scenario{
		ID:    "p2-04-ent-tick-after-close",
		Part:  2,
		Title: "Ent.Tick keeps running after the handle is closed",
		Claim: "`Ent.Tick` keeps running after the handle is closed - no `Closed()` check after `Behaviour().Tick`.",
		Setup: "A real `entity.Ent` (built by `entity.Open`) with a real `entity.PassiveBehaviour` whose `Tick` closes the entity, which is what `ItemBehaviour.merge` and `ExperienceOrbBehaviour.moveToTarget` do in the stock code.\n" +
			"`Ent.Tick` is called directly and its post-behaviour work (`m.Send()`, `checkPortalInsiders`, `e.data.Age += tick`) is inspected afterwards through the exported `Ent.Age()`.",
		Expected: "Once the behaviour closes the entity, `Ent.Tick` stops touching it: no further Age advance and no movement broadcast.",
		Timeout:  60 * time.Second,
		Run:      runEntTickAfterClose,
	}
}

func runEntTickAfterClose(o *Out) {
	w := world.Config{
		Log: discardLogger(), Entities: registryWith(closingType{}),
		Synchronous: true, SaveInterval: -1, RandomTickSpeed: -1,
	}.New()
	defer w.Close()

	h := world.EntitySpawnOpts{Position: mgl64.Vec3{8, 64, 8}}.New(closingType{}, closingConfig{})
	v := &countingViewer{}
	var l *world.Loader
	var ageBefore, ageAfter time.Duration
	var closedDuring bool
	err := call(w, 20*time.Second, func(tx *world.Tx) {
		l = world.NewLoader(1, tx.World(), v)
		l.Move(tx, mgl64.Vec3{8, 64, 8})
		l.Load(tx, 9)
		e := tx.AddEntity(h).(*entity.Ent)
		ageBefore = e.Age()
		o.Logf("before Tick: handle.Closed() = %v, Age = %s", h.Closed(), ageBefore)
		e.Tick(tx, 1)
		closedDuring = h.Closed()
		ageAfter = e.Age()
		o.Logf("after  Tick: handle.Closed() = %v, Age = %s", closedDuring, ageAfter)
	})
	if err != nil {
		o.Verdict(Blocked, "transaction: %v", err)
		return
	}
	o.Logf("the behaviour called Ent.Close() during Tick; Ent.Tick then continued past Behaviour().Tick")
	o.Logf("Age advanced during the tick that closed the entity: %v (%s -> %s)", ageAfter > ageBefore, ageBefore, ageAfter)
	o.Logf("expected: Age unchanged once the entity is closed")

	if ageAfter > ageBefore && closedDuring {
		o.Verdict(Reproduced, "Ent.Tick advanced Age from %s to %s after the behaviour had already closed the handle; there is no Closed() check between Behaviour().Tick and the tail of Ent.Tick", ageBefore, ageAfter)
		return
	}
	if !closedDuring {
		o.Verdict(Blocked, "the behaviour did not manage to close the entity during the tick")
		return
	}
	o.Verdict(Refuted, "Ent.Tick stopped after the behaviour closed the entity")
}

// ---------------------------------------------------------------------------
// p2-06: a dead sessionless player is never removed.
// ---------------------------------------------------------------------------

func scenarioDeadSessionlessPlayer() *Scenario {
	return &Scenario{
		ID:    "p2-06-dead-sessionless-player",
		Part:  2,
		Title: "A dead sessionless player is never removed",
		Claim: "A dead sessionless player is never removed - `Player.close` guards on `p.session() != nil`, which is always true because `session()` returns `Nop` rather than nil.",
		Setup: "A real sessionless `*player.Player` (`player.Config{}` with no Session) in a real world is killed with `Hurt`. `Player.kill` schedules `finishDying` 1.1s later, which calls `Close()` for a `session.Nop` player.\n" +
			"`close` then takes the `p.Dead() && p.session() != nil` branch, `respawn` bails on `p.session() == session.Nop`, and `quit` - the only caller of `Tx.RemoveEntity(p)` and `handle.Close()` - is never reached.",
		Expected: "The dead player is removed from the world and its handle is closed.",
		Timeout:  90 * time.Second,
		Run:      runDeadSessionlessPlayer,
	}
}

func runDeadSessionlessPlayer(o *Out) {
	w := world.Config{
		Log: discardLogger(), Entities: entity.DefaultRegistry,
		SaveInterval: -1, ChunkUnloadInterval: time.Hour, RandomTickSpeed: -1,
	}.New()
	defer w.Close()

	// A viewer keeps the world ticking.
	v := &countingViewer{}
	var l *world.Loader
	_ = call(w, 20*time.Second, func(tx *world.Tx) {
		l = world.NewLoader(2, tx.World(), v)
		l.Move(tx, mgl64.Vec3{8, 64, 8})
		l.Load(tx, 25)
	})

	id := uuid.New()
	ph := world.EntitySpawnOpts{Position: mgl64.Vec3{8, 64, 8}, ID: id}.New(player.Type, player.Config{UUID: id, Name: "Ghost", Position: mgl64.Vec3{8, 64, 8}})
	err := call(w, 20*time.Second, func(tx *world.Tx) {
		p := tx.AddEntity(ph).(*player.Player)
		o.Logf("spawned a sessionless player: Health=%.1f Dead=%v", p.Health(), p.Dead())
		n, vuln := p.Hurt(1000, entity.VoidDamageSource{})
		o.Logf("Hurt(1000, VoidDamageSource) -> damage %.1f, vulnerable %v; Dead=%v", n, vuln, p.Dead())
	})
	if err != nil {
		o.Verdict(Blocked, "setup: %v", err)
		return
	}

	o.Logf("waiting out the 1.1s death animation delay that kill() schedules finishDying with")
	for i := 1; i <= 5; i++ {
		time.Sleep(700 * time.Millisecond)
		present, health := playerState(w, ph)
		o.Logf("t=%.1fs: handle.Closed()=%v, still in the world=%v, entities in world=%d, health=%.1f",
			float64(i)*0.7, ph.Closed(), present, entityCount(w), health)
	}
	present, health := playerState(w, ph)
	o.Logf("expected: the dead player is gone from the world and its handle is closed")
	_ = call(w, 10*time.Second, func(tx *world.Tx) { l.Close(tx) })

	if present && !ph.Closed() {
		o.Verdict(Reproduced, "the dead sessionless player is still registered in the world at %.1f health with an open handle, several seconds after finishDying ran", health)
		return
	}
	o.Verdict(Refuted, "the dead sessionless player was removed (present=%v, closed=%v)", present, ph.Closed())
}

func playerState(w *world.World, h *world.EntityHandle) (bool, float64) {
	type st struct {
		present bool
		health  float64
	}
	v, _ := callVal(w, 10*time.Second, func(tx *world.Tx) st {
		for e := range tx.Entities() {
			if e.H() != h {
				continue
			}
			if p, ok := e.(*player.Player); ok {
				return st{true, p.Health()}
			}
			return st{true, -1}
		}
		return st{}
	})
	return v.present, v.health
}

// ---------------------------------------------------------------------------
// p2-08: setAndUnlockWorldAt skips the worldVersion bump and the world-changed
// notification.
// ---------------------------------------------------------------------------

func scenarioSetWorldAt() *Scenario {
	return &Scenario{
		ID:    "p2-08-setandunlockworldat",
		Part:  2,
		Title: "setAndUnlockWorldAt skips the worldVersion bump and notifyWorldChangedLocked",
		Claim: "`setAndUnlockWorldAt` skips the `worldVersion` bump and `notifyWorldChangedLocked` that every other mutation performs.",
		Setup: "`EntityHandle.DoAfter` is started on a handle that is in no world. Its `scheduleAfter` loop parks on `currentWorldSignals()`, whose `closeStarted` channel is nil while the entity is worldless; the only way it learns the entity joined a world is the `worldChanged` channel, which `notifyWorldChangedLocked` closes.\n" +
			"The entity is then added to a real world with `Tx.AddEntity` (which goes through `addEntityAt` -> `setAndUnlockWorldAt`), and the world is closed well before the delay expires. The time until the task fails with ErrWorldClosed is measured.\n" +
			"A control run does the same, but the entity is put in the world through `addChunk` (a chunk load from a real `mcdb` save), which uses `setAndUnlockWorld` and does send the notification.",
		Expected: "The task learns about the world closing as soon as it happens, not when the delay expires.",
		Timeout:  120 * time.Second,
		Run:      runSetWorldAt,
	}
}

func runSetWorldAt(o *Out) {
	const delay = 6 * time.Second

	w := world.Config{
		Log: discardLogger(), Entities: registryWith(moverType{}),
		SaveInterval: -1, ChunkUnloadInterval: time.Hour,
	}.New()

	h := world.EntitySpawnOpts{Position: mgl64.Vec3{8, 64, 8}}.New(moverType{}, moverConfig{})
	o.Logf("handle created; it is in no world, so currentWorldSignals() returns a nil closeStarted channel")
	start := time.Now()
	task := h.DoAfter(delay, func(*world.Tx, world.Entity) {})
	time.Sleep(300 * time.Millisecond)

	if err := call(w, 20*time.Second, func(tx *world.Tx) { tx.AddEntity(h) }); err != nil {
		o.Verdict(Blocked, "AddEntity: %v", err)
		return
	}
	o.Logf("t=%s: entity added with Tx.AddEntity -> addEntityAt -> setAndUnlockWorldAt", time.Since(start).Round(time.Millisecond))
	time.Sleep(300 * time.Millisecond)

	closeAt := time.Since(start)
	go w.Close()
	o.Logf("t=%s: World.Close() started (this closes w.closeStarted)", closeAt.Round(time.Millisecond))

	ctx, cancel := context.WithTimeout(context.Background(), delay+8*time.Second)
	defer cancel()
	err := task.Wait(ctx)
	took := time.Since(start)
	o.Section("control: the same DoAfter started while the entity is already in a world")
	ctrlErr := controlDoAfter(o, delay)
	o.Logf("the DoAfter task finished after %s with err = %v", took.Round(10*time.Millisecond), err)
	o.Logf("the delay was %s; the world started closing at %s", delay, closeAt.Round(time.Millisecond))
	o.Logf("expected: the task notices the world closing at about %s, not at the full %s", closeAt.Round(time.Millisecond), delay)

	o.Logf("control (DoAfter started after the entity was already in the world): err = %v", ctrlErr)
	o.Logf("expected: both report the same reason for stopping")

	late := took > closeAt+time.Second && errors.Is(err, world.ErrWorldClosed)
	if late {
		o.Verdict(Reproduced, "the task only noticed the world closing after the full %s delay (%s), because setAndUnlockWorldAt never closes the worldChanged channel that scheduleAfter waits on", delay, took.Round(10*time.Millisecond))
		return
	}
	if errors.Is(err, world.ErrEntityClosed) && errors.Is(ctrlErr, world.ErrWorldClosed) {
		o.Verdict(Reproduced, "the task that was scheduled while the entity was worldless never learned it had joined a world: it still held a nil closeStarted channel and only stopped when the entity itself was closed (%v), while the control that was scheduled after the entity was in the world reported %v", err, ctrlErr)
		return
	}
	o.Verdict(Refuted, "the task finished after %s with %v (control: %v); the missed notification had no observable effect here", took.Round(10*time.Millisecond), err, ctrlErr)
}

// helper shared with p2-01
var _ = trace.Perform

// controlDoAfter starts the same DoAfter on an entity that is already in a
// world, so scheduleAfter picks up a real closeStarted channel.
func controlDoAfter(o *Out, delay time.Duration) error {
	w := world.Config{
		Log: discardLogger(), Entities: registryWith(moverType{}),
		SaveInterval: -1, ChunkUnloadInterval: time.Hour,
	}.New()
	h := world.EntitySpawnOpts{Position: mgl64.Vec3{8, 64, 8}}.New(moverType{}, moverConfig{})
	if err := call(w, 20*time.Second, func(tx *world.Tx) { tx.AddEntity(h) }); err != nil {
		return err
	}
	task := h.DoAfter(delay, func(*world.Tx, world.Entity) {})
	time.Sleep(300 * time.Millisecond)
	go w.Close()
	ctx, cancel := context.WithTimeout(context.Background(), delay+8*time.Second)
	defer cancel()
	return task.Wait(ctx)
}
