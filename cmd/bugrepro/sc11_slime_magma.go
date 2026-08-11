package main

import (
	"fmt"
	"time"

	"github.com/df-mc/dragonfly/server/block"
	"github.com/df-mc/dragonfly/server/block/cube"
	"github.com/df-mc/dragonfly/server/player"
	"github.com/df-mc/dragonfly/server/world"
	"github.com/go-gl/mathgl/mgl64"
)

func init() {
	register(Scenario{
		ID:      "11-slime-edge-and-magma",
		Title:   "Fall damage on a slime block edge, and magma that never burns",
		Timeout: 180 * time.Second,
		Bug: "Two off-by-position lookups.\n\n" +
			"**Slime edge.** `(*Player).fall` picks the landing block from the player's *centre* column:\n\n" +
			"```go\n" +
			"pos := cube.PosFromVec3(p.Position())\n" +
			"b := p.tx.Block(pos)\n" +
			"if len(b.Model().BBox(pos, p.tx)) == 0 { pos = pos.Sub(cube.Pos{0, 1}); b = p.tx.Block(pos) }\n" +
			"if h, ok := b.(block.EntityLander); ok { h.EntityLand(pos, p.tx, p, &distance) }\n" +
			"```\n\n" +
			"The player's bounding box is 0.6 wide, so a player can be standing on a slime block while\n" +
			"their centre column is the neighbouring one. The `EntityLander` is then never found and the\n" +
			"full fall damage is applied.\n\n" +
			"**Magma.** `(*Player).checkEntitySteppers` computes the Y of the block being stood on as\n\n" +
			"```go\n" +
			"box := Type.BBox(p).Translate(p.Position()).Grow(-0.0001)\n" +
			"y := int(math.Floor(box.Min()[1] - 0.0001))\n" +
			"```\n\n" +
			"`box.Min()[1]` is the player's feet Y *plus* 0.0001 because of the `Grow(-0.0001)`, so the two\n" +
			"epsilons cancel and `y` floors to the player's own block, not the block underneath. The magma\n" +
			"block one below is never visited and `Magma.EntityStepOn` is never called.",
		Run: runSlimeAndMagma,
	})
}

func runSlimeAndMagma() Result {
	h, err := startHarness(harnessOpts{withClient: true, randomTickSpeed: -1})
	if err != nil {
		return Result{Verdict: Blocked, Reason: "could not start harness: " + err.Error()}
	}
	defer h.Stop()

	var o out
	res := Result{
		Setup: "Part A: a single slime block at (100, -60, 100) sitting on the flat grass. The player is\n" +
			"dropped 20 blocks onto it twice: once landing on its centre and once landing on its edge, with\n" +
			"the fall driven entirely by real `PlayerAuthInput` packets from the gophertunnel client.\n" +
			"Part B: a magma block at (110, -60, 110). The player stands on top of it for 5 seconds.",
		ServerSteps: []string{
			"placed the slime and magma blocks with `tx.SetBlock`",
			"read `(*Player).Health()` and `(*Player).OnGround()` back",
			"part B control: called `block.Magma{}.EntityStepOn(pos, tx, p)` directly to prove the block itself works",
		},
		ClientSteps: []string{
			"the whole fall in part A was driven by real `packet.PlayerAuthInput` packets sent by the " +
				"gophertunnel client, one per simulated tick, following a normal Bedrock free-fall curve " +
				"(v -= 0.08 each tick, v *= 0.98). The server turned those into `(*Player).Move` calls, which " +
				"is what runs `updateFallState` and `fall`.",
			"in part B the client sent PlayerAuthInput packets holding the player still on the magma block.",
		},
	}

	// --- Part A: slime block ---
	slime := cube.Pos{100, -60, 100}
	type landing struct {
		name string
		x, z float64
	}
	landings := []landing{
		{"centre of the slime block", 100.5, 100.5},
		{"edge of the slime block (centre column is the neighbour)", 101.15, 100.5},
	}

	dropFrom := 20.0
	var centreDmg, edgeDmg float64
	for i, l := range landings {
		if err := h.Do(func(tx *world.Tx, p *player.Player) {
			preparePlayer(tx, p, mgl64.Vec3{l.x, -59 + dropFrom, l.z})
			for x := 99; x <= 102; x++ {
				for z := 99; z <= 102; z++ {
					for y := -60; y <= -59+int(dropFrom)+2; y++ {
						tx.SetBlock(cube.Pos{x, y, z}, nil, nil)
					}
					tx.SetBlock(cube.Pos{x, -61, z}, block.Grass{}, nil)
				}
			}
			tx.SetBlock(slime, block.Slime{}, nil)
			p.Heal(20, healSource{})
		}); err != nil {
			return blocked(res, "world call failed: "+err.Error())
		}
		// Let the client sync to the teleport before driving the fall.
		startY := -59 + dropFrom
		for range 5 {
			_ = h.MoveTo(mgl64.Vec3{l.x, startY, l.z})
			time.Sleep(50 * time.Millisecond)
		}

		// Drive a normal Bedrock free fall from the client.
		y, v := startY, 0.0
		for range 200 {
			v = (v - 0.08) * 0.98
			y += v
			if y <= -59 {
				y = -59
			}
			if err := h.MoveTo(mgl64.Vec3{l.x, y, l.z}); err != nil {
				return blocked(res, "sending PlayerAuthInput failed: "+err.Error())
			}
			time.Sleep(50 * time.Millisecond)
			if y == -59 {
				break
			}
		}
		// A couple of stationary ticks so the landing is registered.
		for range 3 {
			_ = h.MoveTo(mgl64.Vec3{l.x, -59, l.z})
			time.Sleep(50 * time.Millisecond)
		}

		var hp float64
		var onGround bool
		var pos mgl64.Vec3
		var centreBlock string
		if err := h.Do(func(tx *world.Tx, p *player.Player) {
			hp, onGround, pos = p.Health(), p.OnGround(), p.Position()
			bp := cube.PosFromVec3(pos)
			b := tx.Block(bp)
			if len(b.Model().BBox(bp, tx)) == 0 {
				bp = bp.Sub(cube.Pos{0, 1})
				b = tx.Block(bp)
			}
			centreBlock = fmt.Sprintf("%v -> %T", bp, b)
		}); err != nil {
			return blocked(res, "world call failed: "+err.Error())
		}
		dmg := 20 - hp
		if i == 0 {
			centreDmg = dmg
		} else {
			edgeDmg = dmg
		}
		o.printf("part A, landing on the %s", l.name)
		o.printf("  fell %.0f blocks, ended at %.2f,%.2f,%.2f (onGround=%v)", dropFrom, pos[0], pos[1], pos[2], onGround)
		o.printf("  (*Player).fall would resolve the landing block as %s", centreBlock)
		o.printf("  health after: %.1f -> damage %.1f", hp, dmg)
		o.printf("")
	}

	// --- Part B: magma ---
	magma := cube.Pos{110, -60, 110}
	var magmaDmg, directDmg float64
	if err := h.Do(func(tx *world.Tx, p *player.Player) {
		preparePlayer(tx, p, mgl64.Vec3{110.5, -59, 110.5})
		for x := 109; x <= 111; x++ {
			for z := 109; z <= 111; z++ {
				tx.SetBlock(cube.Pos{x, -60, z}, block.Grass{}, nil)
				tx.SetBlock(cube.Pos{x, -59, z}, nil, nil)
			}
		}
		tx.SetBlock(magma, block.Magma{}, nil)
		p.Heal(20, healSource{})
	}); err != nil {
		return blocked(res, "world call failed: "+err.Error())
	}
	for range 100 {
		_ = h.MoveTo(mgl64.Vec3{110.5, -59, 110.5})
		time.Sleep(50 * time.Millisecond)
	}
	var hp float64
	var steppedY int
	if err := h.Do(func(tx *world.Tx, p *player.Player) {
		hp = p.Health()
		magmaDmg = 20 - hp
		// Show which Y checkEntitySteppers would look at.
		box := player.Type.BBox(p).Translate(p.Position()).Grow(-0.0001)
		steppedY = int(box.Min()[1] - 0.0001)
	}); err != nil {
		return blocked(res, "world call failed: "+err.Error())
	}
	o.printf("part B, standing on a magma block at %v for 5 seconds", magma)
	o.printf("  player feet Y = -59, magma block Y = -60")
	o.printf("  the Y checkEntitySteppers derives from the player bbox: %d (it needs -60)", steppedY)
	o.printf("  health after 5s: %.1f -> damage %.1f   (expected repeated 1 damage hits)", hp, magmaDmg)

	if err := h.Do(func(tx *world.Tx, p *player.Player) {
		before := p.Health()
		block.Magma{}.EntityStepOn(magma, tx, p)
		directDmg = before - p.Health()
	}); err != nil {
		return blocked(res, "world call failed: "+err.Error())
	}
	o.printf("  control: calling Magma{}.EntityStepOn(pos, tx, p) directly deals %.1f damage, so the block itself works",
		directDmg)

	res.Observed = o.String()
	res.Expected = "Part A: landing anywhere on a slime block should cancel the fall damage, so both landings\n" +
		"should show 0 damage.\n" +
		"Part B: standing on a magma block should burn the player for 1 damage repeatedly (a few hits\n" +
		"over 5 seconds after damage immunity)."

	slimeBug := centreDmg == 0 && edgeDmg > 0
	magmaBug := magmaDmg == 0 && directDmg > 0
	switch {
	case slimeBug && magmaBug:
		res.Verdict = Reproduced
		res.Summary = fmt.Sprintf("slime centre %.0f dmg vs edge %.0f dmg (both should be 0); magma dealt %.0f over 5s (expected > 0)",
			centreDmg, edgeDmg, magmaDmg)
	case slimeBug || magmaBug:
		res.Verdict = Reproduced
		res.Summary = fmt.Sprintf("partial: slime centre %.0f / edge %.0f, magma %.0f over 5s", centreDmg, edgeDmg, magmaDmg)
	default:
		res.Verdict = NotReproduced
		res.Summary = fmt.Sprintf("slime centre %.0f / edge %.0f, magma %.0f over 5s", centreDmg, edgeDmg, magmaDmg)
	}
	return res
}
