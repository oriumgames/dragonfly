package main

import (
	"os"
	"time"

	"github.com/df-mc/dragonfly/server/block/cube"
	"github.com/df-mc/dragonfly/server/world"
	"github.com/df-mc/dragonfly/server/world/mcdb"
	"github.com/go-gl/mathgl/mgl64"
)

// ---------------------------------------------------------------------------
// p1-03: entity handles leak when a chunk unloads.
// ---------------------------------------------------------------------------

func scenarioEntityLeak() *Scenario {
	return &Scenario{
		ID:    "p1-03-entity-handle-leak",
		Part:  1,
		Title: "Entity handles leak when a chunk unloads",
		Claim: "Entity handles leak when a chunk unloads - a `world.Entity` whose `Close` does not remove itself. `len(w.entities)` grows across unload/reload cycles and the leaked handle ticks again after a reload.",
		Setup: "A real non-synchronous `world.World` on a real `mcdb` provider with `ChunkUnloadInterval: 150ms` and no viewers, so `closeUnusedChunks` runs for real.\n" +
			"One entity of a registered `world.EntityType` whose `Close()` returns nil without calling `Tx.RemoveEntity` (the `world.Entity` interface only requires `io.Closer`).\n" +
			"`World.closeChunk` calls that `Close`, clears `Column.Entities` and deletes the column, but never touches `w.entities`. The chunk is then reloaded by touching a block in it. Repeat 4 times.\n" +
			"Entity count is read with `Tx.Entities()`, which iterates `w.entities` directly.",
		Expected: "The entity count stays at 1 across every unload/reload cycle, and the entity is ticked by exactly one live handle.",
		Timeout:  120 * time.Second,
		Run:      runEntityLeak,
	}
}

func runEntityLeak(o *Out) {
	dir := mustTempDir(o, "df-leak")
	defer os.RemoveAll(dir)
	db, err := mcdb.Open(dir)
	if err != nil {
		o.Verdict(Blocked, "mcdb.Open: %v", err)
		return
	}
	w := world.Config{
		Log:                 discardLogger(),
		Provider:            db,
		Entities:            registryWith(leakyType{}, moverType{}),
		SaveInterval:        -1,
		ChunkUnloadInterval: 150 * time.Millisecond,
		RandomTickSpeed:     -1,
	}.New()
	defer w.Close()

	pos := cube.Pos{8, 64, 8}
	stone, _ := world.DefaultBlockRegistry.BlockByName("minecraft:stone", map[string]any{})
	h := world.EntitySpawnOpts{Position: mgl64.Vec3{8.5, 64, 8.5}}.New(leakyType{}, leakyConfig{})
	if err := call(w, 10*time.Second, func(tx *world.Tx) {
		tx.SetBlock(pos.Side(cube.FaceDown), stone, nil)
		tx.AddEntity(h)
	}); err != nil {
		o.Verdict(Blocked, "setup: %v", err)
		return
	}
	o.Logf("added 1 entity of type %q at %v", leakyType{}.EncodeEntity(), mgl64.Vec3{8.5, 64, 8.5})
	o.Logf("cycle 0: entities in w.entities = %d", entityCount(w))

	counts := []int{entityCount(w)}
	for cycle := 1; cycle <= 4; cycle++ {
		// Wait for closeUnusedChunks to run: there are no viewers, so every
		// loaded chunk is eligible.
		time.Sleep(400 * time.Millisecond)
		before := leakTicks.Load()
		// Touch a block to force the column back in.
		_ = call(w, 10*time.Second, func(tx *world.Tx) { tx.Block(pos) })
		time.Sleep(200 * time.Millisecond)
		n := entityCount(w)
		counts = append(counts, n)
		o.Logf("cycle %d: after one unload + reload, entities in w.entities = %d (expected 1); ticks recorded by leaky entities since last cycle = %d",
			cycle, n, leakTicks.Load()-before)
	}

	o.Logf("entity count per cycle: %v", counts)
	o.Logf("expected: [1 1 1 1 1]")
	grew := counts[len(counts)-1] > counts[0]

	// Show that a leaked handle ticks again once its chunk is back.
	before := leakTicks.Load()
	// Give the world viewers so entities in loaded chunks are ticked.
	v := &countingViewer{}
	var l *world.Loader
	_ = call(w, 10*time.Second, func(tx *world.Tx) {
		l = world.NewLoader(2, tx.World(), v)
		l.Move(tx, pos.Vec3())
		l.Load(tx, 16)
	})
	time.Sleep(700 * time.Millisecond)
	o.Logf("with a viewer attached, leaky entity Tick calls in 700ms = %d (across %d registered handles)",
		leakTicks.Load()-before, entityCount(w))
	_ = call(w, 10*time.Second, func(tx *world.Tx) { l.Close(tx) })

	if grew {
		o.Verdict(Reproduced, "w.entities grew from %d to %d across 4 unload/reload cycles; closeChunk never removes the handle when Close does not", counts[0], counts[len(counts)-1])
	} else {
		o.Verdict(Refuted, "the entity count stayed at %v across the unload/reload cycles", counts)
	}
}

func entityCount(w *world.World) int {
	n, _ := callVal(w, 10*time.Second, func(tx *world.Tx) int {
		c := 0
		for range tx.Entities() {
			c++
		}
		return c
	})
	return n
}

// ---------------------------------------------------------------------------
// p1-04: an entity crossing a chunk border is saved twice.
// ---------------------------------------------------------------------------

func scenarioEntityDuplicate() *Scenario {
	return &Scenario{
		ID:    "p1-04-entity-saved-twice",
		Part:  1,
		Title: "Entity crossing a chunk border is saved twice",
		Claim: "An entity crossing a chunk border is saved twice - 3 entities after a reload where there were 2.",
		Setup: "Run 1: a real `mcdb` world gets two entities in chunk (0,0) and is saved and closed. Chunk (0,0) on disk holds both.\n" +
			"Run 2: the same directory is reopened. Loading chunk (0,0) gives it `modified == false` (`columnFrom` builds a fresh Column). A block is set in chunk (1,0) so that column *is* modified. One entity then walks across the border into chunk (1,0) under the real world ticker; `ticker.tickEntities` moves the handle between the two columns and marks neither modified.\n" +
			"`World.Save` and `World.close` then skip chunk (0,0) because `saveChunk` requires `c.modified`, so its stale on-disk entity list survives, while chunk (1,0) is written with the same entity.\n" +
			"Run 3: the directory is reopened once more and the entities are counted.",
		Expected: "Run 3 finds 2 entities: the two that were saved.",
		Timeout:  120 * time.Second,
		Run:      runEntityDuplicate,
	}
}

func runEntityDuplicate(o *Out) {
	dir := mustTempDir(o, "df-dup")
	defer os.RemoveAll(dir)

	stone, _ := world.DefaultBlockRegistry.BlockByName("minecraft:stone", map[string]any{})
	open := func() (*world.World, error) {
		db, err := mcdb.Open(dir)
		if err != nil {
			return nil, err
		}
		return world.Config{
			Log:             discardLogger(),
			Provider:        db,
			Entities:        registryWith(leakyType{}, moverType{}),
			Synchronous:     true,
			SaveInterval:    -1,
			RandomTickSpeed: -1,
		}.New(), nil
	}

	// -------- run 1 --------
	o.Section("run 1: two entities in chunk (0,0)")
	w1, err := open()
	if err != nil {
		o.Verdict(Blocked, "mcdb.Open: %v", err)
		return
	}
	mover := world.EntitySpawnOpts{Position: mgl64.Vec3{15.0, 64, 8}}.New(moverType{}, moverConfig{Vel: mgl64.Vec3{0.5, 0, 0}})
	stay := world.EntitySpawnOpts{Position: mgl64.Vec3{2.0, 64, 8}}.New(moverType{}, moverConfig{})
	_ = call(w1, 10*time.Second, func(tx *world.Tx) {
		tx.SetBlock(cube.Pos{8, 60, 8}, stone, nil)
		tx.AddEntity(mover)
		tx.AddEntity(stay)
	})
	o.Logf("run 1: %d entities, both in chunk (0,0)", entityCount(w1))
	w1.Save()
	_ = w1.Close()

	// -------- run 2 --------
	o.Section("run 2: the entity walks across the border into chunk (1,0)")
	w2, err := open()
	if err != nil {
		o.Verdict(Blocked, "mcdb.Open (run 2): %v", err)
		return
	}
	_ = call(w2, 20*time.Second, func(tx *world.Tx) {
		tx.Block(cube.Pos{8, 60, 8})  // load chunk (0,0): modified == false
		tx.Block(cube.Pos{24, 60, 8}) // load chunk (1,0)
		tx.SetBlock(cube.Pos{24, 60, 8}, stone, nil)
	})
	o.Logf("run 2: %d entities loaded from disk", entityCount(w2))
	o.Logf("run 2: chunk (0,0) was loaded, not written to -> Column.modified == false")
	o.Logf("run 2: chunk (1,0) had a block set -> Column.modified == true")

	positions := func(w *world.World) []mgl64.Vec3 {
		v, _ := callVal(w, 10*time.Second, func(tx *world.Tx) []mgl64.Vec3 {
			var out []mgl64.Vec3
			for e := range tx.Entities() {
				out = append(out, e.Position())
			}
			return out
		})
		return v
	}
	o.Logf("run 2: entity positions before ticking: %v", positions(w2))
	for range 6 {
		w2.AdvanceTick()
	}
	o.Logf("run 2: entity positions after 6 ticks: %v (x >= 16 is chunk (1,0))", positions(w2))
	o.Logf("run 2: %d entities in the world", entityCount(w2))
	w2.Save()
	_ = w2.Close()

	// -------- run 3 --------
	o.Section("run 3: reopen and count")
	w3, err := open()
	if err != nil {
		o.Verdict(Blocked, "mcdb.Open (run 3): %v", err)
		return
	}
	defer w3.Close()
	_ = call(w3, 20*time.Second, func(tx *world.Tx) {
		tx.Block(cube.Pos{8, 60, 8})
		tx.Block(cube.Pos{24, 60, 8})
	})
	n := entityCount(w3)
	o.Logf("run 3: entity positions: %v", positions(w3))
	o.Logf("run 3: %d entities after the reload; expected 2", n)

	if n > 2 {
		o.Verdict(Reproduced, "%d entities after the reload where there were 2: the border crossing left the entity in the stale on-disk copy of the old chunk as well as the new one", n)
	} else {
		o.Verdict(Refuted, "%d entities after the reload, as expected", n)
	}
}
