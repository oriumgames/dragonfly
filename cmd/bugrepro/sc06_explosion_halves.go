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
		ID:      "06-explosion-drops-both-halves",
		Title:   "Explosions drop both halves of two-block structures (bed, door, double plant)",
		Timeout: 180 * time.Second,
		Bug: "`ExplosionConfig.Explode` walks `affectedBlocks` and, for every breakable block, calls\n" +
			"`breakable.BreakInfo().Drops(...)`. A bed, a door and a double plant occupy two block\n" +
			"positions each, and both positions are in `affectedBlocks`, so both drop a full item:\n\n" +
			"```go\n" +
			"for _, pos := range affectedBlocks {\n" +
			"    ...\n" +
			"    if itemDropChance > r.Float64() {\n" +
			"        for _, drop := range breakable.BreakInfo().Drops(item.ToolNone{}, nil) {\n" +
			"            dropItem(tx, drop, pos.Vec3Centre())\n" +
			"        }\n" +
			"    }\n}\n```\n\n" +
			"`entity.NewTNT` explodes with `ExplosionConfig{ItemDropChance: 1}`, so real TNT drops both\n" +
			"halves every single time. Breaking the same structure by hand correctly yields one item.",
		Run: runExplosionHalves,
	})
}

func runExplosionHalves() Result {
	h, err := startHarness(harnessOpts{withClient: true, randomTickSpeed: -1})
	if err != nil {
		return Result{Verdict: Blocked, Reason: "could not start harness: " + err.Error()}
	}
	defer h.Stop()

	var o out
	res := Result{
		Setup: "Flat grass. For each of a bed, an oak door and a sunflower (double plant): the player places\n" +
			"the block with its real placement path, is teleported 60 blocks away (still inside view distance\n" +
			"so the chunk keeps ticking), and a real `entity.TNT` with a 1s fuse is spawned one block away.\n" +
			"After the TNT detonates the item entities left in the world are counted. The same structure is\n" +
			"then broken by hand with `(*Player).BreakBlock` as a control.",
		ServerSteps: []string{
			"placed the bed / door / double plant with `(*Player).UseItemOnBlock`",
			"spawned a real `entity.NewTNT` handle with a 1s fuse next to the structure and let it tick down",
			"counted the item entities left after the explosion, grouped by Go item type",
			"control: rebuilt the structure and broke it with `(*Player).BreakBlock`, counting the drops",
		},
		ClientSteps: []string{
			"a real gophertunnel client was connected and kept the chunks loaded so the TNT entity actually " +
				"ticked its fuse. It sent no packets for this scenario.",
		},
	}

	type structCase struct {
		name     string
		typeName string
		held     func() item.Stack
	}
	cases := []structCase{
		{"bed", "block.Bed", func() item.Stack { return item.NewStack(block.Bed{}, 1) }},
		{"oak door", "block.WoodDoor", func() item.Stack { return item.NewStack(block.WoodDoor{Wood: block.OakWood()}, 1) }},
		{"sunflower (double plant)", "block.DoubleFlower", func() item.Stack { return item.NewStack(block.DoubleFlower{}, 1) }},
	}

	base := cube.Pos{50, -61, 50}
	safe := mgl64.Vec3{110.5, -60, 50.5}
	near := mgl64.Vec3{50.5, -60, 54.5}

	reproduced := 0
	var summaries []string

	for _, c := range cases {
		o.printf("=== %s ===", c.name)

		// Explosion run.
		if err := h.Do(func(tx *world.Tx, p *player.Player) {
			preparePlayer(tx, p, near)
			removeAllItemEntities(tx)
			for x := -4; x <= 4; x++ {
				for z := -4; z <= 4; z++ {
					for y := 1; y <= 4; y++ {
						tx.SetBlock(base.Add(cube.Pos{x, y, z}), nil, nil)
					}
					tx.SetBlock(base.Add(cube.Pos{x, 0, z}), block.Grass{}, nil)
				}
			}
			p.SetHeldItems(c.held(), item.Stack{})
			p.UseItemOnBlock(base, cube.FaceUp, mgl64.Vec3{0.5, 1, 0.5})
		}); err != nil {
			return blocked(res, "world call failed: "+err.Error())
		}

		var placedDesc []string
		if err := h.Do(func(tx *world.Tx, p *player.Player) {
			for y := 1; y <= 2; y++ {
				for dz := 0; dz <= 1; dz++ {
					b := tx.Block(base.Add(cube.Pos{0, y, dz}))
					if _, air := b.(block.Air); air {
						continue
					}
					placedDesc = append(placedDesc, fmt.Sprintf("%v = %T%+v", base.Add(cube.Pos{0, y, dz}), b, b))
				}
			}
		}); err != nil {
			return blocked(res, "world call failed: "+err.Error())
		}
		o.printf("  placed: %s", strings.Join(placedDesc, " | "))
		if len(placedDesc) == 0 {
			o.printf("  could not place the structure, skipping")
			continue
		}

		if err := h.Do(func(tx *world.Tx, p *player.Player) {
			p.Teleport(safe)
			tx.AddEntity(entity.NewTNT(world.EntitySpawnOpts{
				Position: base.Add(cube.Pos{2, 1, 0}).Vec3Centre(),
			}, time.Second))
		}); err != nil {
			return blocked(res, "world call failed: "+err.Error())
		}
		time.Sleep(3 * time.Second)

		var explosionLines []string
		explosionDrops := 0
		if err := h.Do(func(tx *world.Tx, p *player.Player) {
			counts, lines := groundItemsByType(tx)
			explosionDrops = counts[c.typeName]
			for _, l := range lines {
				if strings.Contains(l, c.typeName) {
					explosionLines = append(explosionLines, l)
				}
			}
			removeAllItemEntities(tx)
		}); err != nil {
			return blocked(res, "world call failed: "+err.Error())
		}
		for _, l := range explosionLines {
			o.printf("  after TNT: %s", l)
		}
		o.printf("  TNT explosion dropped %d %s item(s)   (expected 1)", explosionDrops, c.name)

		// Control: break by hand.
		controlDrops := 0
		if err := h.Do(func(tx *world.Tx, p *player.Player) {
			preparePlayer(tx, p, near)
			removeAllItemEntities(tx)
			for x := -4; x <= 4; x++ {
				for z := -4; z <= 4; z++ {
					for y := 1; y <= 4; y++ {
						tx.SetBlock(base.Add(cube.Pos{x, y, z}), nil, nil)
					}
					tx.SetBlock(base.Add(cube.Pos{x, 0, z}), block.Grass{}, nil)
				}
			}
			p.SetHeldItems(c.held(), item.Stack{})
			p.UseItemOnBlock(base, cube.FaceUp, mgl64.Vec3{0.5, 1, 0.5})
			p.SetHeldItems(item.Stack{}, item.Stack{})
			p.BreakBlock(base.Add(cube.Pos{0, 1, 0}))
		}); err != nil {
			return blocked(res, "world call failed: "+err.Error())
		}
		if err := h.Do(func(tx *world.Tx, p *player.Player) {
			counts, _ := groundItemsByType(tx)
			controlDrops = counts[c.typeName]
			removeAllItemEntities(tx)
		}); err != nil {
			return blocked(res, "world call failed: "+err.Error())
		}
		o.printf("  control: breaking the same structure by hand dropped %d %s item(s)", controlDrops, c.name)
		o.printf("")

		if explosionDrops > controlDrops && explosionDrops >= 2 {
			reproduced++
		}
		summaries = append(summaries, fmt.Sprintf("%s: TNT %d vs hand %d", c.name, explosionDrops, controlDrops))
	}

	res.Observed = o.String()
	res.Expected = "A bed, a door and a double plant are single items occupying two block positions. Whether they\n" +
		"are broken by hand or blown up, exactly 1 item should drop."
	if reproduced > 0 {
		res.Verdict = Reproduced
		res.Summary = fmt.Sprintf("%d/%d structures drop double from TNT - %s",
			reproduced, len(cases), strings.Join(summaries, "; "))
	} else {
		res.Verdict = NotReproduced
		res.Summary = strings.Join(summaries, "; ")
	}
	return res
}
