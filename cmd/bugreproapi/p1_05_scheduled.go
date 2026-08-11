package main

import (
	"os"
	"time"

	"github.com/df-mc/dragonfly/server/block/cube"
	"github.com/df-mc/dragonfly/server/world"
	"github.com/df-mc/dragonfly/server/world/mcdb"
)

// p1_05 drives a real scheduled block update through a real chunk unload and
// reload and counts how many times the block's ScheduledTick fires.
func scenarioScheduledTwice() *Scenario {
	return &Scenario{
		ID:    "p1-05-scheduled-update-twice",
		Part:  1,
		Title: "Scheduled block update runs twice after a chunk unload and reload",
		Claim: "A scheduled block update runs twice after a chunk unload and reload.",
		Setup: "A real `world.Block` implementing `world.ScheduledTicker` is registered onto an unimplemented vanilla block state in a real `world.BlockRegistry`, and that registry is shared by the World and its `mcdb` provider on disk.\n" +
			"The block is placed and `Tx.ScheduleBlockUpdate` is called with a 30s delay. The chunk is unloaded by the real `closeUnusedChunks` (no viewers, `ChunkUnloadInterval: 150ms`), which saves the pending update to disk with `columnTo` and then drops it from the queue with `removeChunk`.\n" +
			"While the chunk is unloaded, the same update is scheduled again - `World.scheduleBlockUpdate` only bounds-checks the position, it does not require the chunk to be loaded. The chunk is then touched, so `addChunk` -> `columnFrom` re-adds the on-disk copy on top.\n" +
			"The world is then ticked past the due tick and the ScheduledTick calls are counted.",
		Expected: "The update runs exactly once.",
		Timeout:  180 * time.Second,
		Run:      runScheduledTwice,
	}
}

func runScheduledTwice(o *Out) {
	br := world.NewBlockRegistry()
	b, ok := registerCounterBlock(br)
	if !ok {
		o.Verdict(Blocked, "no unimplemented vanilla block state was available to register a ScheduledTicker onto")
		return
	}
	br.Finalize()
	name, _ := b.EncodeBlock()
	o.Logf("registered a ScheduledTicker onto the vanilla state %q (runtime ID %d)", name, br.BlockRuntimeID(b))

	dir := mustTempDir(o, "df-sched")
	defer os.RemoveAll(dir)
	db, err := mcdb.Config{Blocks: br}.Open(dir)
	if err != nil {
		o.Verdict(Blocked, "mcdb open: %v", err)
		return
	}
	w := world.Config{
		Log:                 discardLogger(),
		Provider:            db,
		Blocks:              br,
		Entities:            registryWith(),
		SaveInterval:        -1,
		ChunkUnloadInterval: 150 * time.Millisecond,
		RandomTickSpeed:     -1,
	}.New()
	defer w.Close()

	pos := cube.Pos{8, 64, 8}
	const delay = 30 * time.Second

	scheduledTickCount.Store(0)
	if err := call(w, 10*time.Second, func(tx *world.Tx) {
		tx.SetBlock(pos, b, nil)
		tx.ScheduleBlockUpdate(pos, b, delay)
	}); err != nil {
		o.Verdict(Blocked, "setup: %v", err)
		return
	}
	o.Logf("placed the block at %v and scheduled one update %s out (600 ticks)", pos, delay)

	// Let the world save and unload the chunk for real.
	w.Save()
	o.Logf("World.Save() written; waiting for closeUnusedChunks to unload the chunk")
	time.Sleep(500 * time.Millisecond)

	// Schedule the same update again while the chunk is unloaded.
	_ = call(w, 10*time.Second, func(tx *world.Tx) {
		tx.ScheduleBlockUpdate(pos, b, delay)
	})
	o.Logf("scheduled the same update again while the chunk was unloaded")

	// Bring the chunk back: addChunk -> columnFrom re-adds the on-disk copy.
	_ = call(w, 20*time.Second, func(tx *world.Tx) { tx.Block(pos) })
	o.Logf("touched the chunk; columnFrom has re-added the on-disk scheduled update")

	// Drive the world forward past the due tick. A world with no viewers stops
	// ticking, so a Loader is attached.
	v := &countingViewer{}
	var l *world.Loader
	_ = call(w, 20*time.Second, func(tx *world.Tx) {
		l = world.NewLoader(2, tx.World(), v)
		l.Move(tx, pos.Vec3())
		l.Load(tx, 25)
	})
	o.Logf("attached a Loader so the world ticks; waiting out the 30s delay")

	deadline := time.Now().Add(70 * time.Second)
	last := int64(-1)
	for time.Now().Before(deadline) {
		time.Sleep(2 * time.Second)
		n := scheduledTickCount.Load()
		if n != last {
			o.Logf("ScheduledTick calls so far: %d", n)
			last = n
		}
		if n >= 2 {
			break
		}
	}
	// Give any second entry a chance to fire.
	time.Sleep(3 * time.Second)
	n := scheduledTickCount.Load()
	o.Logf("total ScheduledTick calls for one logical update: %d", n)
	o.Logf("expected: 1")
	_ = call(w, 10*time.Second, func(tx *world.Tx) { l.Close(tx) })

	switch {
	case n >= 2:
		o.Verdict(Reproduced, "the scheduled block update ran %d times: the unload wrote it to disk, the queue entry was re-created while the chunk was out, and the reload added the disk copy on top", n)
	case n == 1:
		o.Verdict(Refuted, "the scheduled block update ran exactly once through the unload/reload cycle")
	default:
		o.Verdict(Refuted, "the scheduled block update never ran (%d calls) - it was lost, not duplicated", n)
	}
}
