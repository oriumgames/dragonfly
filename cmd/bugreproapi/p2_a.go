package main

import (
	"os"
	"time"

	"github.com/df-mc/dragonfly/server/block/cube"
	"github.com/df-mc/dragonfly/server/block/cube/trace"
	"github.com/df-mc/dragonfly/server/entity"
	"github.com/df-mc/dragonfly/server/item"
	"github.com/df-mc/dragonfly/server/player"
	"github.com/df-mc/dragonfly/server/world"
	"github.com/df-mc/dragonfly/server/world/mcdb"
	"github.com/go-gl/mathgl/mgl64"
	"github.com/google/uuid"
)

// ---------------------------------------------------------------------------
// p2-01: firework explosion damage through walls.
// ---------------------------------------------------------------------------

func scenarioFireworkWall() *Scenario {
	return &Scenario{
		ID:    "p2-01-firework-through-walls",
		Part:  2,
		Title: "Firework explosion damage passes through walls",
		Claim: "`firework_behaviour.go` uses `trace.Perform`, which reportedly returns ok for block hits too, so the line-of-sight guard never rejects.",
		Setup: "A real firework entity (`entity.FireworkType` with a real `entity.FireworkBehaviourConfig`) is ticked until it expires next to a real sessionless `*player.Player`, with a solid stone wall built between them.\n" +
			"A control run repeats the same setup with no wall. `trace.Perform` is also called directly against a wall to show what it returns for a pure block hit.",
		Expected: "With a wall in the way the player takes no damage; without a wall it does.",
		Timeout:  120 * time.Second,
		Run:      runFireworkWall,
	}
}

func runFireworkWall(o *Out) {
	stone, _ := world.DefaultBlockRegistry.BlockByName("minecraft:stone", map[string]any{})

	o.Section("what trace.Perform returns for a pure block hit")
	w0 := world.Config{Log: discardLogger(), Entities: entity.DefaultRegistry, Synchronous: true, SaveInterval: -1}.New()
	_ = call(w0, 20*time.Second, func(tx *world.Tx) {
		for y := 60; y < 70; y++ {
			for z := 4; z < 12; z++ {
				tx.SetBlock(cube.Pos{8, y, z}, stone, nil)
			}
		}
		start, end := mgl64.Vec3{4, 64.5, 8}, mgl64.Vec3{12, 64.5, 8}
		res, ok := trace.Perform(start, end, tx, cube.Box(-0.3, 0, -0.3, 0.3, 1.8, 0.3), nil)
		o.Logf("trace.Perform(%v -> %v) through a solid stone wall: ok = %v, result type = %T", start, end, ok, res)
		o.Logf("expected for a line-of-sight guard: ok = false (there is no entity to hit, only a wall)")
	})
	_ = w0.Close()

	run := func(label string, wall bool) (float64, float64) {
		w := world.Config{Log: discardLogger(), Entities: entity.DefaultRegistry, Synchronous: true, SaveInterval: -1, RandomTickSpeed: -1}.New()
		defer w.Close()
		victimPos := mgl64.Vec3{11.5, 64, 8.5}
		fwPos := mgl64.Vec3{8.5, 64, 8.5}
		id := uuid.New()
		ph := world.EntitySpawnOpts{Position: victimPos, ID: id}.New(player.Type, player.Config{UUID: id, Name: "Victim", Position: victimPos})

		fw := world.EntitySpawnOpts{Position: fwPos}.New(entity.FireworkType, entity.FireworkBehaviourConfig{
			Firework: item.Firework{
				Duration: 100 * time.Millisecond,
				Explosions: []item.FireworkExplosion{
					{Shape: item.FireworkShapeSmallSphere(), Colour: item.ColourRed()},
					{Shape: item.FireworkShapeSmallSphere(), Colour: item.ColourBlue()},
				},
			},
			ExistenceDuration: 5 * time.Second / 20,
		})

		var before, after float64
		_ = call(w, 20*time.Second, func(tx *world.Tx) {
			for x := 0; x < 16; x++ {
				for z := 0; z < 16; z++ {
					tx.SetBlock(cube.Pos{x, 63, z}, stone, nil)
				}
			}
			if wall {
				for y := 60; y < 70; y++ {
					for z := 4; z < 12; z++ {
						tx.SetBlock(cube.Pos{10, y, z}, stone, nil)
					}
				}
			}
			p := tx.AddEntity(ph).(*player.Player)
			before = p.Health()
			n := 0
			for range tx.EntitiesWithin(cube.Box(fwPos[0], fwPos[1], fwPos[2], fwPos[0], fwPos[1], fwPos[2]).Grow(5.25)) {
				n++
			}
			o.Logf("%s: entities within the explosion box before the firework is added: %d", label, n)
			dmg, vuln := p.Hurt(1, entity.ProjectileDamageSource{})
			o.Logf("%s: control Hurt(1, ProjectileDamageSource) -> %.1f damage, vulnerable=%v, health now %.1f", label, dmg, vuln, p.Health())
			before = p.Health()
			tx.AddEntity(fw)
		})
		for range 20 {
			w.AdvanceTick()
		}
		o.Logf("%s: firework handle closed after ticking (i.e. it exploded) = %v", label, fw.Closed())
		_ = call(w, 20*time.Second, func(tx *world.Tx) {
			if e, ok := ph.Entity(tx); ok {
				after = e.(*player.Player).Health()
			}
		})
		o.Logf("%s: victim health %.1f -> %.1f", label, before, after)
		return before, after
	}

	o.Section("A. wall between the firework and the victim")
	b1, a1 := run("with a solid 1-block stone wall at x=10", true)
	o.Section("B. control: no wall")
	b2, a2 := run("with no wall", false)

	o.Logf("expected: damage only in B")
	hurtBehindWall := a1 < b1
	hurtInOpen := a2 < b2
	switch {
	case hurtBehindWall && hurtInOpen:
		o.Verdict(Reproduced, "the victim took damage through a solid wall (%.1f -> %.1f) exactly as it did in the open (%.1f -> %.1f): the trace.Perform guard accepts block hits", b1, a1, b2, a2)
	case hurtBehindWall:
		o.Verdict(Reproduced, "the victim took damage through a solid wall (%.1f -> %.1f)", b1, a1)
	case !hurtInOpen:
		o.Verdict(Blocked, "the control run took no damage either, so the scenario did not exercise the damage path")
	default:
		o.Verdict(Refuted, "the wall blocked the damage (%.1f -> %.1f) while the open control was hurt (%.1f -> %.1f)", b1, a1, b2, a2)
	}
}

// ---------------------------------------------------------------------------
// p2-05: saveChunk never clears Column.modified.
// ---------------------------------------------------------------------------

func scenarioSaveChunkModified() *Scenario {
	return &Scenario{
		ID:    "p2-05-savechunk-modified",
		Part:  2,
		Title: "saveChunk never clears c.modified, so every touched chunk is rewritten on every autosave",
		Claim: "`saveChunk` never clears `c.modified`, so every touched chunk is rewritten on every autosave. Quantify the cost.",
		Setup: "A real world on a real `mcdb` provider, wrapped in a counting `world.Provider` that records every `StoreColumn` call and the encoded byte volume.\n" +
			"25 chunks are touched once with a single `Tx.SetBlock` each, then `World.Save()` is called ten times with no further modification.",
		Expected: "The first Save writes the 25 dirty chunks; the nine following Saves write nothing, because a saved chunk is clean.",
		Timeout:  180 * time.Second,
		Run:      runSaveChunkModified,
	}
}

func runSaveChunkModified(o *Out) {
	dir := mustTempDir(o, "df-save")
	defer os.RemoveAll(dir)
	db, err := mcdb.Open(dir)
	if err != nil {
		o.Verdict(Blocked, "mcdb.Open: %v", err)
		return
	}
	cp := newCountingProvider(db)
	w := world.Config{
		Log: discardLogger(), Provider: cp, Entities: entity.DefaultRegistry,
		Synchronous: true, SaveInterval: -1, RandomTickSpeed: -1,
	}.New()
	defer w.Close()

	stone, _ := world.DefaultBlockRegistry.BlockByName("minecraft:stone", map[string]any{})
	const n = 5
	_ = call(w, 60*time.Second, func(tx *world.Tx) {
		for cx := 0; cx < n; cx++ {
			for cz := 0; cz < n; cz++ {
				tx.SetBlock(cube.Pos{cx*16 + 8, 64, cz*16 + 8}, stone, nil)
			}
		}
	})
	o.Logf("touched %d chunks with one SetBlock each", n*n)

	base, baseBytes, _ := cp.Totals()
	o.Logf("StoreColumn calls before the first Save: %d", base)

	var prev int
	for i := 1; i <= 10; i++ {
		w.Save()
		total, bytes, _ := cp.Totals()
		o.Logf("Save #%2d: StoreColumn calls this save = %3d, cumulative = %4d, cumulative encoded sub-chunk bytes = %d",
			i, total-base-prev, total-base, bytes-baseBytes)
		prev = total - base
	}
	total, bytes, per := cp.Totals()
	o.Logf("expected: 25 StoreColumn calls in total (one per dirty chunk, on the first Save)")
	o.Logf("observed: %d StoreColumn calls, %d encoded sub-chunk bytes", total-base, bytes-baseBytes)
	sample := 0
	for _, v := range per {
		if v > sample {
			sample = v
		}
	}
	o.Logf("most-written single chunk: %d writes for one SetBlock", sample)
	o.Logf("cost model: with the default SaveInterval of 10 minutes, a chunk touched once is rewritten %.0f times a day", 6*24.0)

	if total-base > n*n {
		o.Verdict(Reproduced, "%d StoreColumn calls for %d chunks modified once (%dx the necessary work); Column.modified is set in 7 places and cleared in none", total-base, n*n, (total-base)/(n*n))
	} else {
		o.Verdict(Refuted, "only %d StoreColumn calls for %d modified chunks across 10 saves", total-base, n*n)
	}
}
