package main

import (
	"time"

	"github.com/df-mc/dragonfly/server/entity"
	"github.com/df-mc/dragonfly/server/world"
	"github.com/go-gl/mathgl/mgl64"
)

// p1_02: two Loaders swapping worlds in opposite directions.
func scenarioTwoLoaders() *Scenario {
	return &Scenario{
		ID:    "p1-02-two-loaders-swap",
		Part:  1,
		Title: "Two Loaders changing worlds in opposite directions freeze both",
		Claim: "Two Loaders changing worlds in opposite directions freeze both.",
		Setup: "Two real, non-synchronous Worlds W1 and W2. Loader A lives in W1, loader B in W2, both with real chunks loaded.\n" +
			"A's `ChangeWorld(tx, W2)` runs on W2's owner goroutine and B's `ChangeWorld(tx, W1)` runs on W1's owner goroutine - the direction `session.handleWorldSwitch` uses, since it runs inside a transaction of the destination world.\n" +
			"`ChangeWorld` then calls `l.w.exec(...)` on the *source* world: A sends into W1's queue, B sends into W2's queue. Both queues are saturated first, which is the state a busy world's 128-deep queue is in whenever its owner is inside a long transaction.",
		Expected: "Both worlds keep handling transactions and both loaders finish switching.",
		Timeout:  120 * time.Second,
		Leaks:    true,
		Run:      runTwoLoaders,
	}
}

func runTwoLoaders(o *Out) {
	mk := func() *world.World {
		return world.Config{
			Log: discardLogger(), Entities: entity.DefaultRegistry,
			SaveInterval: -1, ChunkUnloadInterval: time.Hour,
		}.New()
	}
	w1, w2 := mk(), mk()

	var la, lb *world.Loader
	if err := call(w1, 10*time.Second, func(tx *world.Tx) {
		la = world.NewLoader(4, tx.World(), world.NopViewer{})
		la.Move(tx, mgl64.Vec3{0, 64, 0})
		la.Load(tx, 8)
	}); err != nil {
		o.Verdict(Blocked, "setting up loader A: %v", err)
		return
	}
	if err := call(w2, 10*time.Second, func(tx *world.Tx) {
		lb = world.NewLoader(4, tx.World(), world.NopViewer{})
		lb.Move(tx, mgl64.Vec3{0, 64, 0})
		lb.Load(tx, 8)
	}); err != nil {
		o.Verdict(Blocked, "setting up loader B: %v", err)
		return
	}
	time.Sleep(400 * time.Millisecond)
	_ = call(w1, 10*time.Second, func(tx *world.Tx) { la.Load(tx, 16) })
	_ = call(w2, 10*time.Second, func(tx *world.Tx) { lb.Load(tx, 16) })
	time.Sleep(400 * time.Millisecond)
	o.Logf("loader A is in W1 (%d chunks), loader B is in W2 (%d chunks)", loadedCount(la), loadedCount(lb))

	// Park both owners, then saturate both queues.
	o1, o2 := pin(w1), pin(w2)
	o.Logf("both owner goroutines are parked inside a transaction")
	saturate(w1, 400)
	saturate(w2, 400)
	time.Sleep(400 * time.Millisecond)
	o.Logf("400 tasks submitted to each world; both 128-deep queues are full")

	// A moves W1 -> W2 on W2's owner; B moves W2 -> W1 on W1's owner.
	aDone := o2.start(func(tx *world.Tx) { la.ChangeWorld(tx, w2) }, 5*time.Second)
	bDone := o1.start(func(tx *world.Tx) { lb.ChangeWorld(tx, w1) }, 5*time.Second)
	o.Logf("A.ChangeWorld(W1 -> W2) started on W2's owner; B.ChangeWorld(W2 -> W1) started on W1's owner")

	aRet, bRet := waitDone(aDone, 8*time.Second), waitDone(bDone, 100*time.Millisecond)
	o.Logf("A.ChangeWorld returned = %v", aRet)
	o.Logf("B.ChangeWorld returned = %v", bRet)

	o1.release()
	o2.release()
	o.Logf("released both owners; a healthy pair of worlds resumes here")
	a1 := alive(w1, 8*time.Second)
	a2 := alive(w2, 5*time.Second)
	o.Logf("W1 handles transactions = %v", a1)
	o.Logf("W2 handles transactions = %v", a2)
	o.Logf("expected: both true, both ChangeWorld calls returned")

	if !a1 && !a2 {
		o.Section("blocked goroutines")
		for _, line := range splitLines(goroutineDump("world.(*Loader).ChangeWorld", "world.(*World).exec")) {
			o.Logf("%s", line)
		}
		o.Verdict(Reproduced, "both worlds stopped handling transactions: each owner is blocked in World.exec sending into the other world's full queue")
		return
	}
	if !a1 || !a2 {
		o.Section("blocked goroutines")
		for _, line := range splitLines(goroutineDump("world.(*Loader).ChangeWorld", "world.(*World).exec")) {
			o.Logf("%s", line)
		}
		o.Verdict(Reproduced, "one of the two worlds stopped handling transactions (W1 alive=%v, W2 alive=%v)", a1, a2)
		return
	}
	o1.release()
	o2.release()
	go w1.Close()
	go w2.Close()
	o.Verdict(Refuted, "both worlds kept handling transactions through the opposite world switches")
}

func waitDone(c <-chan struct{}, d time.Duration) bool {
	select {
	case <-c:
		return true
	case <-time.After(d):
		return false
	}
}
