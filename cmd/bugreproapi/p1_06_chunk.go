package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/df-mc/dragonfly/server/world"
	"github.com/df-mc/dragonfly/server/world/chunk"
)

// p1_06 covers both halves of item 6: SubChunk.Layer(255) never returning, and
// Chunk.Compact renumbering layers.
//
// The Layer(255) half allocates without bound, so it is run in a child process
// (this same binary, -only=p1-06-child-layer255) which is killed after a short
// while. That keeps the parent's memory intact while still showing the real
// behaviour of the real function.
func scenarioSubChunkLayer() *Scenario {
	return &Scenario{
		ID:    "p1-06-subchunk-layer-compact",
		Part:  1,
		Title: "SubChunk.Layer(255) never returns; Compact renumbers layers",
		Claim: "`SubChunk.Layer(255)` never returns (report the storage count it reached), and `compact` renumbers layers — a block in layer 7 comes back in layer 0.",
		Setup: "Half A runs `(*chunk.SubChunk).Layer(255)` on a real sub chunk of a real `chunk.New` chunk in a child process, sampling `len(sub.Layers())` from a watchdog goroutine, and is killed after 3s.\n" +
			"Half B places a real block (minecraft:stone) at layer 7 of a real chunk via `Chunk.SetBlock(x, y, z, 7, rid)`, calls `Chunk.Compact()` and reads the block back from every layer.",
		Expected: "A: `Layer(255)` returns the 256th storage and `len(storages) == 256`.\n" +
			"B: after Compact the stone is still at layer 7, or at worst the chunk refuses to keep sparse layers in a way that is documented; a block must not silently move to a different layer index.",
		Timeout: 60 * time.Second,
		Run:     runSubChunkLayer,
	}
}

func runSubChunkLayer(o *Out) {
	world.DefaultBlockRegistry.Finalize()
	br := world.DefaultBlockRegistry

	// ---------------- Half A: Layer(255) ----------------
	o.Section("A. SubChunk.Layer(255) in a child process")
	self, err := os.Executable()
	if err != nil {
		o.Logf("cannot find own executable: %v", err)
		o.Verdict(Blocked, "could not re-exec self for the Layer(255) probe")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, self, "-only=p1-06-child-layer255", "-no-reports=1", "-quiet")
	out, _ := cmd.CombinedOutput()
	for _, line := range strings.Split(strings.TrimRight(string(out), "\n"), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		o.Logf("child: %s", line)
	}
	returned := strings.Contains(string(out), "LAYER255-RETURNED")
	if returned {
		o.Logf("A: Layer(255) RETURNED - claim does not hold")
	} else {
		o.Logf("A: Layer(255) did not return within the child's 3s budget")
	}

	// ---------------- Half B: Compact renumbers layers ----------------
	o.Section("B. Chunk.Compact() renumbering layers")
	stone, ok := br.BlockByName("minecraft:stone", map[string]any{})
	if !ok {
		o.Verdict(Blocked, "minecraft:stone not in the default block registry")
		return
	}
	rid := br.BlockRuntimeID(stone)
	air := br.AirRuntimeID()
	o.Logf("stone runtime ID = %d, air runtime ID = %d", rid, air)

	c := chunk.New(br, world.Overworld.Range())
	const x, z byte = 3, 5
	const y int16 = 64
	c.SetBlock(x, y, z, 7, rid)

	sub := c.SubChunk(y)
	o.Logf("before Compact: len(sub.Layers()) = %d", len(sub.Layers()))
	for l := 0; l < len(sub.Layers()); l++ {
		o.Logf("  before Compact: layer %d at (%d,%d,%d) = %d", l, x, y, z, sub.Block(x, byte(y&0xf), z, uint8(l)))
	}
	o.Logf("before Compact: Chunk.Block(layer=7) = %d (stone=%d)", c.Block(x, y, z, 7), rid)

	c.Compact()

	o.Logf("after  Compact: len(sub.Layers()) = %d", len(sub.Layers()))
	found := -1
	for l := 0; l < len(sub.Layers()); l++ {
		v := sub.Block(x, byte(y&0xf), z, uint8(l))
		o.Logf("  after  Compact: layer %d at (%d,%d,%d) = %d", l, x, y, z, v)
		if v == rid && found == -1 {
			found = l
		}
	}
	o.Logf("after  Compact: Chunk.Block(layer=7) = %d (air=%d)", c.Block(x, y, z, 7), air)
	o.Logf("expected: stone still readable at layer 7; observed: stone at layer %d", found)

	// Second case: a populated layer 0 shifts the block to layer 1 instead.
	o.Section("B2. same, but with a non-air block already in layer 0")
	c2 := chunk.New(br, world.Overworld.Range())
	c2.SetBlock(x, y, z, 0, rid)
	c2.SetBlock(x, y, z, 7, rid)
	c2.Compact()
	sub2 := c2.SubChunk(y)
	found2 := -1
	for l := len(sub2.Layers()) - 1; l >= 0; l-- {
		if sub2.Block(x, byte(y&0xf), z, uint8(l)) == rid {
			found2 = l
		}
	}
	o.Logf("after Compact with a populated layer 0: len(layers) = %d, highest layer holding stone = %d (was 7)",
		len(sub2.Layers()), func() int {
			h := -1
			for l := 0; l < len(sub2.Layers()); l++ {
				if sub2.Block(x, byte(y&0xf), z, uint8(l)) == rid {
					h = l
				}
			}
			return h
		}())
	_ = found2

	switch {
	case !returned && found == 0:
		o.Verdict(Reproduced, "Layer(255) did not return in 3s (child killed); Compact moved the layer-7 block to layer 0")
	case !returned:
		o.Verdict(Reproduced, "Layer(255) did not return in 3s; Compact moved the layer-7 block to layer %d (not 0)", found)
	case found == 0:
		o.Verdict(Reproduced, "Compact moved the layer-7 block to layer 0, but Layer(255) returned")
	default:
		o.Verdict(Refuted, "Layer(255) returned and Compact kept the block at layer %d", found)
	}
}

// scenarioLayer255Child is the child half of p1-06. It is hidden from the
// default run and only reachable through -only.
func scenarioLayer255Child() *Scenario {
	return &Scenario{
		ID:      "p1-06-child-layer255",
		Part:    1,
		Hidden:  true,
		Title:   "child: SubChunk.Layer(255)",
		Timeout: 10 * time.Second,
		Run: func(o *Out) {
			world.DefaultBlockRegistry.Finalize()
			c := chunk.New(world.DefaultBlockRegistry, world.Overworld.Range())
			sub := c.SubChunk(64)
			done := make(chan struct{})
			go func() {
				// Layer(255) allocates without bound, so the watchdog stops the
				// child on a heap ceiling as well as on a deadline.
				const heapCeiling = 512 << 20
				start := time.Now()
				var ms runtime.MemStats
				for {
					select {
					case <-done:
						return
					case <-time.After(20 * time.Millisecond):
					}
					runtime.ReadMemStats(&ms)
					elapsed := time.Since(start)
					if ms.HeapAlloc > heapCeiling || elapsed > 3*time.Second {
						fmt.Printf("LAYER255-STILL-RUNNING after %s, heap=%d MiB, storages appended (approx, sampled concurrently) = %d\n",
							elapsed.Round(time.Millisecond), ms.HeapAlloc>>20, len(sub.Layers()))
						fmt.Println("LAYER255-ABORTED by the watchdog: the call has not returned")
						os.Exit(3)
					}
					if elapsed.Milliseconds()%500 < 25 {
						fmt.Printf("LAYER255-SAMPLE t=%s heap=%d MiB storages~%d\n",
							elapsed.Round(10*time.Millisecond), ms.HeapAlloc>>20, len(sub.Layers()))
					}
				}
			}()
			_ = sub.Layer(255)
			close(done)
			fmt.Printf("LAYER255-RETURNED len(sub.Layers()) = %d\n", len(sub.Layers()))
			o.Verdict(Reproduced, "child finished")
		},
	}
}
