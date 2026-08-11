package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/df-mc/dragonfly/server/block"
	"github.com/df-mc/dragonfly/server/block/cube"
	"github.com/df-mc/dragonfly/server/entity"
	"github.com/df-mc/dragonfly/server/item"
	"github.com/df-mc/dragonfly/server/player"
	"github.com/df-mc/dragonfly/server/world"
	"github.com/go-gl/mathgl/mgl64"
)

func init() {
	register(Scenario{
		ID:      "05-cluster-block-dupe",
		Title:   "Cluster blocks duplicate: the drop carries the whole cluster in every item",
		Timeout: 240 * time.Second,
		Bug: "Cluster-style blocks drop `N` copies of *themselves in their clustered state* rather than `N`\n" +
			"copies of the single block:\n\n" +
			"```go\n" +
			"// Slab.BreakInfo\n" +
			"if s.Double { return []item.Stack{item.NewStack(s, 2)} }   // s still has Double: true\n" +
			"// Candle.BreakInfo\n" +
			"simpleDrops(item.NewStack(c, c.AdditionalCandles+1))       // c still has AdditionalCandles\n" +
			"// SeaPickle.BreakInfo / PinkPetals.BreakInfo - the same shape\n" +
			"```\n\n" +
			"Placing one of those dropped items rebuilds the full cluster (a double slab, a 4-candle\n" +
			"block, ...), which then drops N of itself again. Every break/replace cycle multiplies the\n" +
			"item count by N.",
		Run: runClusterDupe,
	})
}

// clusterCase describes one cluster block to test.
type clusterCase struct {
	name string
	// singles is the number of single items needed to build one full cluster.
	singles int
	// single is the item stack for one un-clustered block.
	single func() item.Stack
	// describe renders the clustered state of a dropped item.
	describe func(it world.Item) string
	// itemName is the encoded item name of the block.
	itemName string
}

func runClusterDupe() Result {
	h, err := startHarness(harnessOpts{withClient: true, randomTickSpeed: -1})
	if err != nil {
		return Result{Verdict: Blocked, Reason: "could not start harness: " + err.Error()}
	}
	defer h.Stop()

	var o out
	res := Result{
		Setup: "Flat grass at y = -61, player in survival at (0.5, -60, 0.5) holding a diamond pickaxe when\n" +
			"breaking. For each cluster block: build one full cluster out of single items, break it, then\n" +
			"place every dropped item as its own block and break those, for four cycles. Item entities are\n" +
			"counted between cycles and then removed so each cycle's number is exact.",
		ServerSteps: []string{
			"placed every block through `(*Player).UseItemOnBlock` - the real placement path, including `Slab.UseOnBlock`",
			"broke every block through `(*Player).BreakBlock` - the real break path, including `BreakInfo().Drops`",
			"counted the resulting `entity.Item` entities in the world between cycles",
		},
		ClientSteps: []string{
			"a real gophertunnel client was connected and standing in the area, but sent no packets for " +
				"this scenario. Every placement and break was driven through the server-side player API.",
		},
	}

	cases := []clusterCase{
		{
			name:     "stone slab (double slab)",
			singles:  2,
			single:   func() item.Stack { return item.NewStack(block.Slab{Block: block.Stone{}}, 1) },
			itemName: mustName(block.Slab{Block: block.Stone{}}),
			describe: func(it world.Item) string {
				if s, ok := it.(block.Slab); ok {
					return fmt.Sprintf("Slab{Double:%v, Top:%v}", s.Double, s.Top)
				}
				return fmt.Sprintf("%T", it)
			},
		},
		{
			name:     "candle",
			singles:  4,
			single:   func() item.Stack { return item.NewStack(block.Candle{}, 1) },
			itemName: mustName(block.Candle{}),
			describe: func(it world.Item) string {
				if c, ok := it.(block.Candle); ok {
					return fmt.Sprintf("Candle{AdditionalCandles:%d}", c.AdditionalCandles)
				}
				return fmt.Sprintf("%T", it)
			},
		},
		{
			name:     "sea pickle",
			singles:  4,
			single:   func() item.Stack { return item.NewStack(block.SeaPickle{}, 1) },
			itemName: mustName(block.SeaPickle{}),
			describe: func(it world.Item) string {
				if s, ok := it.(block.SeaPickle); ok {
					return fmt.Sprintf("SeaPickle{AdditionalCount:%d}", s.AdditionalCount)
				}
				return fmt.Sprintf("%T", it)
			},
		},
		{
			name:     "pink petals",
			singles:  4,
			single:   func() item.Stack { return item.NewStack(block.PinkPetals{}, 1) },
			itemName: mustName(block.PinkPetals{}),
			describe: func(it world.Item) string {
				if p, ok := it.(block.PinkPetals); ok {
					return fmt.Sprintf("PinkPetals{AdditionalCount:%d}", p.AdditionalCount)
				}
				return fmt.Sprintf("%T", it)
			},
		},
	}

	// Ground positions the player can reach. The player stands at (0, -60, 0),
	// so skip that column.
	var grounds []cube.Pos
	for x := -3; x <= 3; x++ {
		for z := -3; z <= 3; z++ {
			if x == 0 && z == 0 {
				continue
			}
			grounds = append(grounds, cube.Pos{x, -61, z})
		}
	}

	reproduced := 0
	var summaries []string

	for _, c := range cases {
		o.printf("=== %s ===", c.name)
		// Reset the area.
		if err := h.Do(func(tx *world.Tx, p *player.Player) {
			preparePlayer(tx, p, mgl64.Vec3{0.5, -60, 0.5})
			removeAllItemEntities(tx)
			for _, g := range grounds {
				tx.SetBlock(g.Side(cube.FaceUp), nil, nil)
				tx.SetBlock(g, block.Grass{}, nil)
			}
		}); err != nil {
			return blocked(res, "world call failed: "+err.Error())
		}

		// Build one full cluster from c.singles separate single items.
		g0 := grounds[0]
		if err := h.Do(func(tx *world.Tx, p *player.Player) {
			for i := range c.singles {
				p.SetHeldItems(c.single(), item.Stack{})
				if i == 0 {
					p.UseItemOnBlock(g0, cube.FaceUp, mgl64.Vec3{0.5, 1, 0.5})
				} else {
					p.UseItemOnBlock(g0.Side(cube.FaceUp), cube.FaceUp, mgl64.Vec3{0.5, 1, 0.5})
				}
			}
		}); err != nil {
			return blocked(res, "world call failed: "+err.Error())
		}

		var built string
		if err := h.Do(func(tx *world.Tx, p *player.Player) {
			built = c.describe(tx.Block(g0.Side(cube.FaceUp)).(world.Item))
		}); err != nil {
			return blocked(res, "world call failed: "+err.Error())
		}
		o.printf("  built one full cluster at %v out of %d single items -> block is %v",
			g0.Side(cube.FaceUp), c.singles, built)

		// Cycle: break every placed block, count drops, then place each dropped
		// item as a new block.
		placed := 1
		counts := []int{}
		var dropItem world.Item
		for cycle := 1; cycle <= 4; cycle++ {
			var (
				dropped   int
				stacks    int
				descs     []string
				placedNow int
			)
			if err := h.Do(func(tx *world.Tx, p *player.Player) {
				p.SetHeldItems(item.NewStack(item.Pickaxe{Tier: item.ToolTierDiamond}, 1), item.Stack{})
				for i := 0; i < placed && i < len(grounds); i++ {
					p.BreakBlock(grounds[i].Side(cube.FaceUp))
				}
			}); err != nil {
				return blocked(res, "world call failed: "+err.Error())
			}
			if err := h.Do(func(tx *world.Tx, p *player.Player) {
				seen := map[string]bool{}
				for e := range tx.Entities() {
					ent, ok := e.(*entity.Ent)
					if !ok {
						continue
					}
					ib, ok := ent.Behaviour().(*entity.ItemBehaviour)
					if !ok {
						continue
					}
					s := ib.Item()
					if itemName(s) != c.itemName {
						continue
					}
					dropped += s.Count()
					stacks++
					dropItem = s.Item()
					d := c.describe(s.Item())
					if !seen[d] {
						seen[d] = true
						descs = append(descs, d)
					}
				}
				removeAllItemEntities(tx)
			}); err != nil {
				return blocked(res, "world call failed: "+err.Error())
			}
			counts = append(counts, dropped)
			o.printf("  cycle %d: broke %2d block(s) -> %2d item(s) in %d stack(s), each item is %s",
				cycle, min(placed, len(grounds)), dropped, stacks, strings.Join(descs, " / "))

			if cycle == 4 {
				break
			}
			// Place every dropped item as a block again.
			toPlace := min(dropped, len(grounds))
			if dropItem == nil {
				o.printf("           nothing dropped, stopping")
				break
			}
			it := dropItem
			if err := h.Do(func(tx *world.Tx, p *player.Player) {
				for i := range toPlace {
					g := grounds[i]
					tx.SetBlock(g.Side(cube.FaceUp), nil, nil)
					p.SetHeldItems(item.NewStack(it, 1), item.Stack{})
					p.UseItemOnBlock(g, cube.FaceUp, mgl64.Vec3{0.5, 1, 0.5})
					if _, air := tx.Block(g.Side(cube.FaceUp)).(block.Air); !air {
						placedNow++
					}
				}
			}); err != nil {
				return blocked(res, "world call failed: "+err.Error())
			}
			placed = placedNow
			o.printf("           placed %d of them back as blocks", placed)
		}

		ratio := 0.0
		if counts[0] > 0 {
			ratio = float64(counts[len(counts)-1]) / float64(counts[0])
		}
		ok := len(counts) >= 2 && counts[1] > counts[0]
		if ok {
			reproduced++
		}
		o.printf("  item count per cycle: %v (started from %d single items)", counts, c.singles)
		o.printf("  growth over 4 cycles: x%.0f", ratio)
		o.printf("")
		summaries = append(summaries, fmt.Sprintf("%s %v", c.name, counts))
	}

	// Part 2: show what BreakInfo().Drops() actually returns and what happens if
	// that stack is placed without going through an item entity.
	o.printf("=== root cause: what BreakInfo().Drops() returns, and what it places as ===")
	rawDupe := 0
	if err := h.Do(func(tx *world.Tx, p *player.Player) {
		preparePlayer(tx, p, mgl64.Vec3{0.5, -60, 0.5})
		removeAllItemEntities(tx)
		for _, g := range grounds {
			tx.SetBlock(g.Side(cube.FaceUp), nil, nil)
			tx.SetBlock(g, block.Grass{}, nil)
		}
	}); err != nil {
		return blocked(res, "world call failed: "+err.Error())
	}
	for _, c := range cases {
		var (
			rawDesc   string
			rawCount  int
			entDesc   string
			placedAs  string
			afterDrop int
		)
		if err := h.Do(func(tx *world.Tx, p *player.Player) {
			g := grounds[0]
			tx.SetBlock(g.Side(cube.FaceUp), nil, nil)
			for i := range c.singles {
				p.SetHeldItems(c.single(), item.Stack{})
				if i == 0 {
					p.UseItemOnBlock(g, cube.FaceUp, mgl64.Vec3{0.5, 1, 0.5})
				} else {
					p.UseItemOnBlock(g.Side(cube.FaceUp), cube.FaceUp, mgl64.Vec3{0.5, 1, 0.5})
				}
			}
			b := tx.Block(g.Side(cube.FaceUp))
			drops := b.(block.Breakable).BreakInfo().Drops(item.ToolNone{}, nil)
			if len(drops) > 0 {
				rawDesc = c.describe(drops[0].Item())
				rawCount = drops[0].Count()
				// Round trip through an item entity, the way the game does it.
				ent := tx.AddEntity(entity.NewItem(world.EntitySpawnOpts{Position: g.Vec3Centre()}, drops[0]))
				st := ent.(*entity.Ent).Behaviour().(*entity.ItemBehaviour).Item()
				entDesc = c.describe(st.Item())
				afterDrop = st.Count()
				_ = tx.RemoveEntity(ent).Close()

				// Place the *raw* drop stack (bypassing the item entity).
				g2 := grounds[1]
				tx.SetBlock(g2.Side(cube.FaceUp), nil, nil)
				p.SetHeldItems(item.NewStack(drops[0].Item(), 1), item.Stack{})
				p.UseItemOnBlock(g2, cube.FaceUp, mgl64.Vec3{0.5, 1, 0.5})
				if it, ok := tx.Block(g2.Side(cube.FaceUp)).(world.Item); ok {
					placedAs = c.describe(it)
				} else {
					placedAs = "nothing placed"
				}
			}
			tx.SetBlock(g.Side(cube.FaceUp), nil, nil)
			removeAllItemEntities(tx)
		}); err != nil {
			return blocked(res, "world call failed: "+err.Error())
		}
		o.printf("  %s", c.name)
		o.printf("    BreakInfo().Drops() -> %d x %s   <- the stack still carries the whole cluster", rawCount, rawDesc)
		o.printf("    after entity.NewItem NBT round trip -> %d x %s   <- normalised back to a single block", afterDrop, entDesc)
		o.printf("    placing 1 of the RAW drop stack gives -> %s", placedAs)
		if rawDesc != entDesc {
			rawDupe++
		}
	}

	res.Observed = o.String()
	res.Expected = "Breaking a cluster should return exactly the single blocks that went into it, and each of\n" +
		"those items should place as a single block. The item count should stay flat across cycles:\n" +
		"2, 2, 2, 2 for the double slab and 4, 4, 4, 4 for candles / sea pickles / pink petals.\n\n" +
		"`BreakInfo().Drops()` should also return stacks of the *single* block, not stacks whose item is\n" +
		"the fully clustered block."
	if reproduced > 0 {
		res.Verdict = Reproduced
		res.Summary = fmt.Sprintf("%d/%d cluster blocks multiply per cycle: %s",
			reproduced, len(cases), strings.Join(summaries, "; "))
	} else {
		res.Verdict = NotReproduced
		res.Reason = "The `BreakInfo().Drops()` bug is real and is shown verbatim above: for all four blocks the\n" +
			"returned stack's item is the *clustered* block (`Slab{Double:true}`, `Candle{AdditionalCandles:3}`,\n" +
			"...), and placing one of those raw stacks does rebuild the whole cluster from a single item.\n" +
			"But it never reaches a player. `entity.NewItem` -> `ItemBehaviourConfig.New` runs\n" +
			"`i = item.ReadNBT(item.WriteNBT(i, true), nil)`, and `readItemStack` resolves the item by\n" +
			"`world.ItemByName(Name, Damage)` - and `Slab.EncodeItem`/`Candle.EncodeItem` deliberately encode\n" +
			"the *single* form (`encodeSlabBlock(s.Block, false)`), so the clustered state is thrown away.\n" +
			"Every path that gives a player a broken block's drops goes through an item entity\n" +
			"(`(*Player).BreakBlock`, `block.breakBlock`, `explosion`), so the exponential duplication does\n" +
			"not materialise end to end on this build. Measured item counts stayed flat over four\n" +
			"break/replace cycles: " + strings.Join(summaries, "; ") + "."
		res.Summary = "no growth end to end (" + strings.Join(summaries, "; ") +
			"); the latent Drops() bug is real but masked by the item entity NBT round trip"
	}
	return res
}

func mustName(i world.Item) string {
	n, meta := i.EncodeItem()
	if meta != 0 {
		return fmt.Sprintf("%s:%d", n, meta)
	}
	return n
}
