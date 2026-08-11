package main

import (
	"time"

	"github.com/df-mc/dragonfly/server"
	"github.com/df-mc/dragonfly/server/block/cube"
	"github.com/df-mc/dragonfly/server/entity"
	"github.com/df-mc/dragonfly/server/world"
	"github.com/df-mc/dragonfly/server/world/chunk"
)

// p1_08 shows that a World built from world.Config storms from tick 1 and that
// an unknown biome ID nil-dereferences on the very code path the lightning
// tick takes (Tx.ThunderingAt -> Tx.biome -> Biome.Rainfall).
func scenarioWeatherStorm() *Scenario {
	return &Scenario{
		ID:    "p1-08-weather-storm-nil-biome",
		Part:  1,
		Title: "world.Config storms from tick 1; nil Biome dereferenced on the lightning path",
		Claim: "A world built from `world.Config` storms from tick 1, and a nil `Biome` is dereferenced on the lightning path. Also show that a `server.Config{}` with no `WorldProvider` takes the same path.",
		Setup: "A: `world.Config{Synchronous: true}.New()` is ticked once with `AdvanceTick` and `Raining()`/`Thundering()` are read.\n" +
			"B: `server.Config{}.New()` (no WorldProvider) is created, its overworld ticked once, and the same two flags read.\n" +
			"C: a chunk biome is set to an ID that is not registered (a save from a newer game version), then `Tx.ThunderingAt` is called - the same call `weather.strikeLightning` makes on every lightning tick.",
		Expected: "A/B: a brand new world starts clear: Raining=false, Thundering=false. Weather should only start after the rain/thunder counters have run down.\n" +
			"C: an unknown biome ID must not crash the world tick.",
		Timeout: 60 * time.Second,
		Run:     runWeatherStorm,
	}
}

func runWeatherStorm(o *Out) {
	reproduced := 0

	o.Section("A. world.Config{}.New() -> AdvanceTick()")
	w := world.Config{Log: discardLogger(), Synchronous: true, Entities: entity.DefaultRegistry}.New()
	r0, t0, _ := weatherOf(w)
	o.Logf("tick 0: Raining=%v Thundering=%v", r0, t0)
	w.AdvanceTick()
	r1, t1, cur := weatherOf(w)
	o.Logf("tick %d: Raining=%v Thundering=%v", cur, r1, t1)
	o.Logf("expected at tick 1: Raining=false Thundering=false")
	if r1 && t1 {
		reproduced++
		o.Logf("A: REPRODUCED - the world is raining and thundering from its very first tick")
	} else {
		o.Logf("A: not reproduced (Raining=%v Thundering=%v)", r1, t1)
	}
	// Show how long it stays that way.
	for i := 0; i < 200; i++ {
		w.AdvanceTick()
	}
	r2, t2, cur2 := weatherOf(w)
	o.Logf("tick %d: Raining=%v Thundering=%v", cur2, r2, t2)
	_ = w.Close()

	o.Section("B. server.Config{} with no WorldProvider")
	srv := server.Config{Log: discardLogger()}.New()
	sw := srv.World()
	rb0, tb0, _ := weatherOf(sw)
	o.Logf("server overworld before ticking: Raining=%v Thundering=%v", rb0, tb0)
	// The server's world is not synchronous; it ticks on its own 20x/s.
	time.Sleep(300 * time.Millisecond)
	rb1, tb1, curb := weatherOf(sw)
	o.Logf("server overworld at tick %d: Raining=%v Thundering=%v", curb, rb1, tb1)
	o.Logf("expected: a fresh server world starts clear")
	if rb1 && tb1 {
		reproduced++
		o.Logf("B: REPRODUCED - server.Config{}.New() lands in the same permanent thunderstorm")
	} else {
		o.Logf("B: not reproduced")
	}
	// srv.Close panics unless the server was started with Listen; the world is
	// closed directly instead.
	go func() { _ = sw.Close() }()

	o.Section("C. unknown biome ID on the lightning path (Tx.ThunderingAt -> Tx.biome)")
	w3 := world.Config{Log: discardLogger(), Synchronous: true, Entities: entity.DefaultRegistry}.New()
	defer w3.Close()
	const unknownBiome = 60000
	panicked := make(chan any, 1)
	err := call(w3, 20*time.Second, func(tx *world.Tx) {
		defer func() {
			if r := recover(); r != nil {
				panicked <- r
			}
		}()
		pos := cube.Pos{4, 70, 4}
		// Reach the loaded chunk through the public loader, then write an
		// unregistered biome ID into it, exactly as a save from a newer game
		// version would contain.
		col := loadedChunkFor(tx, pos)
		if col == nil {
			o.Logf("could not obtain a chunk to write the biome into")
			return
		}
		col.SetBiome(uint8(pos[0]&0xf), int16(pos[1]), uint8(pos[2]&0xf), unknownBiome)
		o.Logf("wrote biome ID %d at %v; world.BiomeByID(%d) -> registered=%v", unknownBiome, pos, unknownBiome, biomeRegistered(unknownBiome))
		o.Logf("calling Tx.ThunderingAt(%v), the call weather.strikeLightning makes every lightning tick", pos)
		got := tx.ThunderingAt(pos)
		o.Logf("ThunderingAt returned %v without panicking", got)
	})
	if err != nil {
		o.Logf("C: transaction error: %v", err)
	}
	select {
	case r := <-panicked:
		reproduced++
		o.Logf("C: REPRODUCED - panic on the lightning path: %v", r)
	default:
		o.Logf("C: no panic observed")
	}

	switch reproduced {
	case 0:
		o.Verdict(Refuted, "no storm at tick 1 and no nil biome dereference observed")
	case 3:
		o.Verdict(Reproduced, "world.Config and server.Config both storm from tick 1, and an unknown biome ID panics on the lightning path")
	default:
		o.Verdict(Reproduced, "%d of the 3 sub-claims reproduced (see the output)", reproduced)
	}
}

func weatherOf(w *world.World) (raining, thundering bool, tick int64) {
	r, _ := callVal(w, 10*time.Second, func(tx *world.Tx) [3]int64 {
		var a, b int64
		if tx.Raining() {
			a = 1
		}
		if tx.Thundering() {
			b = 1
		}
		return [3]int64{a, b, tx.CurrentTick()}
	})
	return r[0] == 1, r[1] == 1, r[2]
}

func biomeRegistered(id int) bool {
	_, ok := world.BiomeByID(id)
	return ok
}

// loadedChunkFor returns the *chunk.Chunk backing pos, by asking the world for
// a block there first (which loads or generates the column) and then reaching
// it through a Loader, which is the only exported handle on a Column.
func loadedChunkFor(tx *world.Tx, pos cube.Pos) *chunk.Chunk {
	tx.Block(pos)
	l := world.NewLoader(1, tx.World(), world.NopViewer{})
	l.Move(tx, pos.Vec3())
	l.Load(tx, 16)
	col, ok := l.Chunk(world.ChunkPos{int32(pos[0]) >> 4, int32(pos[2]) >> 4})
	l.Close(tx)
	if !ok {
		return nil
	}
	return col.Chunk
}
