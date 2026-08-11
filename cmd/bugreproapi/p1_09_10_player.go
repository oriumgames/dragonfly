package main

import (
	"time"

	"github.com/df-mc/dragonfly/server/block"
	"github.com/df-mc/dragonfly/server/entity"
	"github.com/df-mc/dragonfly/server/item"
	"github.com/df-mc/dragonfly/server/player"
	"github.com/df-mc/dragonfly/server/session"
	"github.com/df-mc/dragonfly/server/world"
	"github.com/go-gl/mathgl/mgl64"
	"github.com/google/uuid"
)

// ---------------------------------------------------------------------------
// p1-09: ViewEntityArmour panics for an entity whose Armour() returns nil.
// ---------------------------------------------------------------------------

func scenarioViewEntityArmour() *Scenario {
	return &Scenario{
		ID:    "p1-09-viewentityarmour-nil",
		Part:  1,
		Title: "ViewEntityArmour panics for an entity whose Armour() returns nil",
		Claim: "`ViewEntityArmour` panics for an entity whose `Armour()` returns nil.",
		Setup: "A registered `world.EntityType` whose entity has `Armour() *inventory.Armour { return nil }` - the exact method set `Session.ViewEntityArmour` type-asserts to.\n" +
			"The entity is added to a real world and shown to a real `*session.Session` through `world.showEntity`, which calls `ViewEntity`, `ViewEntityItems` and `ViewEntityArmour` for every viewer of the chunk the entity spawns in.",
		Expected: "Showing an entity with no armour inventory is a no-op for the viewer.",
		Timeout:  90 * time.Second,
		Run:      runViewEntityArmour,
	}
}

func runViewEntityArmour(o *Out) {
	w := world.Config{
		Log: discardLogger(), Entities: registryWith(nilArmourType{}),
		SaveInterval: -1, ChunkUnloadInterval: time.Hour,
	}.New()
	defer w.Close()

	sp, err := spawnSessionPlayer(o, w, "Armourless", mgl64.Vec3{8, 64, 8})
	if err != nil {
		o.Verdict(Blocked, "could not spawn a real session player: %v", err)
		return
	}
	o.Logf("spawned a real *session.Session with a real *player.Player at (8,64,8)")
	// Let the session's chunk loader take the chunk so it becomes a viewer.
	time.Sleep(1500 * time.Millisecond)

	h := world.EntitySpawnOpts{Position: mgl64.Vec3{8.5, 64, 8.5}}.New(nilArmourType{}, nilArmourConfig{})
	o.Logf("adding an entity whose Armour() returns nil into the chunk the session views")

	var panicked any
	err = call(w, 20*time.Second, func(tx *world.Tx) {
		defer func() { panicked = recover() }()
		tx.AddEntity(h)
	})
	if err != nil {
		o.Logf("AddEntity transaction error: %v", err)
	}
	if panicked != nil {
		o.Logf("panic while showing the entity to the session: %v", panicked)
	} else {
		o.Logf("no panic when the entity was shown through the world's viewer list")
	}

	// Also call the Session's ViewEntityArmour directly, which is what
	// player.broadcastArmour and session.StartShowingEntity do.
	var direct any
	err = call(w, 20*time.Second, func(tx *world.Tx) {
		defer func() { direct = recover() }()
		e, ok := h.Entity(tx)
		if !ok {
			o.Logf("entity is not in the world; calling on a fresh handle instead")
			return
		}
		sp.s.ViewEntityArmour(e)
	})
	if err != nil {
		o.Logf("direct call transaction error: %v", err)
	}
	if direct != nil {
		o.Logf("panic from Session.ViewEntityArmour: %v", direct)
	} else {
		o.Logf("Session.ViewEntityArmour returned without panicking")
	}
	o.Logf("expected: no panic in either case")

	// And through session.Nop, which is what a sessionless player's viewers use.
	var nopPanic any
	err = call(w, 20*time.Second, func(tx *world.Tx) {
		defer func() { nopPanic = recover() }()
		e, ok := h.Entity(tx)
		if !ok {
			return
		}
		session.Nop.ViewEntityArmour(e)
	})
	if err != nil {
		o.Logf("Nop call transaction error: %v", err)
	}
	o.Logf("session.Nop.ViewEntityArmour panic = %v", nopPanic)

	if panicked != nil || direct != nil || nopPanic != nil {
		o.Verdict(Reproduced, "Session.ViewEntityArmour dereferences a nil *inventory.Armour and panics")
		return
	}
	o.Verdict(Refuted, "no panic was observed on any of the three ViewEntityArmour paths")
}

// ---------------------------------------------------------------------------
// p1-10: Player.Drop reports items it did not drop.
// ---------------------------------------------------------------------------

func scenarioPlayerDrop() *Scenario {
	return &Scenario{
		ID:    "p1-10-player-drop-overreport",
		Part:  1,
		Title: "Player.Drop reports items it did not drop",
		Claim: "`Player.Drop` reports items it did not drop for a stack over the maximum count.",
		Setup: "A real sessionless `*player.Player` in a real world drops an `item.Stack` whose count exceeds the item's `MaxCount()`.\n" +
			"`Player.Drop` returns `s.Count()`, but `entity.NewItemPickupDelay` -> `ItemBehaviourConfig.New` clamps the stack to `MaxCount()` before the entity is spawned. The item entities actually in the world are then counted.",
		Expected: "Drop returns the number of items that really hit the ground.",
		Timeout:  60 * time.Second,
		Run:      runPlayerDrop,
	}
}

func runPlayerDrop(o *Out) {
	w := world.Config{
		Log: discardLogger(), Entities: entity.DefaultRegistry,
		Synchronous: true, SaveInterval: -1,
	}.New()
	defer w.Close()

	id := uuid.New()
	handle := world.EntitySpawnOpts{Position: mgl64.Vec3{8, 64, 8}, ID: id}.New(player.Type, player.Config{UUID: id, Name: "Dropper", Position: mgl64.Vec3{8, 64, 8}})
	reported := 0
	dropped := 0
	var maxCount, wanted int
	err := call(w, 20*time.Second, func(tx *world.Tx) {
		p := tx.AddEntity(handle).(*player.Player)
		st := item.NewStack(block.ShulkerBox{}, 10)
		maxCount, wanted = st.MaxCount(), st.Count()
		o.Logf("dropping %d x %T; the item's MaxCount() is %d", st.Count(), st.Item(), st.MaxCount())
		reported = p.Drop(st)
		for e := range tx.Entities() {
			ent, ok := e.(*entity.Ent)
			if !ok {
				continue
			}
			ib, ok := ent.Behaviour().(*entity.ItemBehaviour)
			if !ok {
				continue
			}
			dropped += ib.Item().Count()
			o.Logf("item entity on the ground: %d x %T", ib.Item().Count(), ib.Item().Item())
		}
	})
	if err != nil {
		o.Verdict(Blocked, "transaction: %v", err)
		return
	}
	o.Logf("Player.Drop returned %d", reported)
	o.Logf("items actually on the ground: %d", dropped)
	o.Logf("expected: Drop returns %d (the clamped count), not %d", maxCount, wanted)

	if reported != dropped {
		o.Verdict(Reproduced, "Drop returned %d but only %d item(s) were spawned; callers such as handleDrop debit the source slot by the reported count, losing %d", reported, dropped, reported-dropped)
	} else {
		o.Verdict(Refuted, "Drop returned %d and %d items were spawned", reported, dropped)
	}
}
