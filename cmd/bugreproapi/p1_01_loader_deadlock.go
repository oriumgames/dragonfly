package main

import (
	"time"

	"github.com/df-mc/dragonfly/server/entity"
	"github.com/df-mc/dragonfly/server/world"
	"github.com/go-gl/mathgl/mgl64"
)

// p1_01 drives a real world.Loader on a real, NON-synchronous World.
func scenarioLoaderDeadlock() *Scenario {
	return &Scenario{
		ID:    "p1-01-loader-deadlock",
		Part:  1,
		Title: "Loader deadlock freezes a world",
		Claim: "A player teleported on join into a chunk within render distance that is not yet loaded freezes the world. Needs a NON-synchronous world - every existing test in the repo sets `Synchronous: true`, which is exactly the immunity condition.",
		Setup: "A: the literal join sequence, on a non-synchronous `world.World`: inside one transaction, `world.NewLoader(8, w, viewer)` then `Loader.Move` to a far, unloaded chunk (the teleport) then `Loader.Load`, then the world is probed with `world.Call`.\n" +
			"B: `Loader.ChangeWorld(tx, other)` is executed on the owner goroutine of the loader's own world (the goroutine every block, entity and loader callback runs on), with that world's 128-deep transaction queue full. `ChangeWorld` calls `l.w.exec(...)`, an unconditional `w.queue <- ntx` send.",
		Expected: "A/B: the world keeps handling transactions. `world.Call` returns promptly.",
		Timeout:  120 * time.Second,
		Leaks:    true,
		Run:      runLoaderDeadlock,
	}
}

func runLoaderDeadlock(o *Out) {
	// ---------------------------------------------------------------
	o.Section("A. teleport on join into an unloaded chunk in render distance")
	wa := world.Config{
		Log:                 discardLogger(),
		Entities:            entity.DefaultRegistry,
		SaveInterval:        -1,
		ChunkUnloadInterval: time.Hour,
	}.New()

	v := &countingViewer{}
	var la *world.Loader
	err := call(wa, 10*time.Second, func(tx *world.Tx) {
		// Exactly what session.Spawn does: build the loader inside a
		// transaction and Move it to the player's position.
		la = world.NewLoader(8, tx.World(), v)
		la.Move(tx, mgl64.Vec3{0, 64, 0})
		// The teleport: the target chunk is inside render distance but has
		// never been loaded.
		la.Move(tx, mgl64.Vec3{100, 64, 100})
		la.Load(tx, 4)
	})
	if err != nil {
		o.Logf("A: the join transaction itself did not complete: %v", err)
	} else {
		o.Logf("A: join transaction completed")
	}
	for i := range 5 {
		ok := alive(wa, 3*time.Second)
		o.Logf("A: probe %d: world handles transactions = %v (chunks viewed so far: %d)", i+1, ok, v.chunks())
		if !ok {
			break
		}
		_ = call(wa, 3*time.Second, func(tx *world.Tx) { la.Load(tx, 4) })
		time.Sleep(100 * time.Millisecond)
	}
	aAlive := alive(wa, 3*time.Second)
	o.Logf("A: world alive after the join sequence = %v, chunks delivered to the viewer = %d", aAlive, v.chunks())
	if aAlive {
		o.Logf("A: NOT REPRODUCED - the literal teleport-on-join sequence did not freeze the world; the async chunk load path kept up")
	}
	if aAlive {
		_ = call(wa, 3*time.Second, func(tx *world.Tx) { la.Close(tx) })
		go wa.Close()
	}

	// ---------------------------------------------------------------
	o.Section("B. Loader.ChangeWorld on its own world's owner goroutine")
	w1 := world.Config{Log: discardLogger(), Entities: entity.DefaultRegistry, SaveInterval: -1, ChunkUnloadInterval: time.Hour}.New()
	w2 := world.Config{Log: discardLogger(), Entities: entity.DefaultRegistry, SaveInterval: -1, ChunkUnloadInterval: time.Hour}.New()

	var lb *world.Loader
	if err := call(w1, 10*time.Second, func(tx *world.Tx) {
		lb = world.NewLoader(4, tx.World(), world.NopViewer{})
		lb.Move(tx, mgl64.Vec3{0, 64, 0})
		lb.Load(tx, 8)
	}); err != nil {
		o.Verdict(Blocked, "could not set up the loader in w1: %v", err)
		return
	}
	time.Sleep(400 * time.Millisecond)
	_ = call(w1, 10*time.Second, func(tx *world.Tx) { lb.Load(tx, 16) })
	time.Sleep(400 * time.Millisecond)
	o.Logf("B: loader created in w1 with %d chunks loaded", loadedCount(lb))

	// Control run: the same pin and the same full queue, but a harmless job.
	// This shows the setup by itself does not wedge the world.
	ctrl := pin(w1)
	saturate(w1, 400)
	time.Sleep(300 * time.Millisecond)
	o.Logf("B control: owner parked, 400 tasks submitted, queue full")
	o.Logf("B control: a harmless job on the owner returned = %v", ctrl.do(func(*world.Tx) {}, 5*time.Second))
	ctrl.release()
	o.Logf("B control: after releasing the owner, w1 handles transactions = %v", alive(w1, 10*time.Second))

	owner := pin(w1)
	o.Logf("B: w1's owner goroutine is parked inside a transaction again")
	saturate(w1, 400)
	time.Sleep(300 * time.Millisecond)
	o.Logf("B: submitted 400 more tasks to w1; its 128-deep queue is full")

	changed := owner.start(func(tx *world.Tx) {
		lb.ChangeWorld(tx, w2)
	}, 5*time.Second)

	select {
	case <-changed:
		o.Logf("B: ChangeWorld returned")
	case <-time.After(5 * time.Second):
		o.Logf("B: ChangeWorld has not returned after 5s")
	}

	owner.release()
	o.Logf("B: released the owner (closed the job channel); a healthy world would resume here")
	bAlive := alive(w1, 8*time.Second)
	o.Logf("B: w1 handles transactions = %v", bAlive)
	o.Logf("B: w2 handles transactions = %v", alive(w2, 5*time.Second))

	if !bAlive {
		o.Section("B. blocked goroutines")
		dump := goroutineDump("world.(*Loader).ChangeWorld", "world.(*World).exec", "world.(*World).handleTransactions")
		for _, line := range splitLines(dump) {
			o.Logf("%s", line)
		}
		o.Verdict(Reproduced, "variant B froze the world: Loader.ChangeWorld ran on its own world's owner goroutine and blocked forever in World.exec's unconditional queue send, and the world never recovered even after the owner was released. Variant A, the literal teleport-on-join sequence, did NOT freeze the world")
		return
	}

	owner.release()
	go w1.Close()
	go w2.Close()
	if !aAlive {
		o.Verdict(Reproduced, "the join sequence froze the world: it stopped handling transactions")
		return
	}
	o.Verdict(Refuted, "neither the join sequence nor ChangeWorld on the owner goroutine froze the world")
}

func loadedCount(l *world.Loader) int {
	n := 0
	for x := int32(-8); x <= 8; x++ {
		for z := int32(-8); z <= 8; z++ {
			if _, ok := l.Chunk(world.ChunkPos{x, z}); ok {
				n++
			}
		}
	}
	return n
}
