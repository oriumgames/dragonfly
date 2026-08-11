package main

import (
	"fmt"
	"time"

	"github.com/df-mc/dragonfly/server/block"
	"github.com/df-mc/dragonfly/server/block/cube"
	"github.com/df-mc/dragonfly/server/entity"
	"github.com/df-mc/dragonfly/server/player"
	"github.com/df-mc/dragonfly/server/world"
	"github.com/go-gl/mathgl/mgl64"
)

func init() {
	register(Scenario{
		ID:      "09-anvil-no-fall-damage",
		Title:   "A falling anvil deals no damage no matter how far it fell",
		Timeout: 120 * time.Second,
		Bug: "`FallingBlockBehaviour.damageEntities` scales the damage by the distance the block fell:\n\n" +
			"```go\n" +
			"dist := math.Ceil(f.passive.fallDistance - 1.0)\n" +
			"if dist <= 0 { return }\n" +
			"dmg := math.Min(math.Floor(dist*damagePerBlock), maxDamage)\n" +
			"```\n\n" +
			"but `PassiveBehaviour.Tick` accumulates that distance from the **velocity** delta, not the\n" +
			"position delta:\n\n" +
			"```go\n" +
			"p.fallDistance = math.Max(p.fallDistance-m.dvel[1], 0)   // dvel, not dpos\n" +
			"```\n\n" +
			"`dvel[1]` is at most the per-tick gravity step and goes to zero once terminal velocity or the\n" +
			"ground is reached, so `fallDistance` never grows past a fraction of a block. `dist` is\n" +
			"therefore always <= 0 and the anvil never hurts anything.",
		Run: runAnvilFallDamage,
	})
}

func runAnvilFallDamage() Result {
	h, err := startHarness(harnessOpts{withClient: true, randomTickSpeed: -1})
	if err != nil {
		return Result{Verdict: Blocked, Reason: "could not start harness: " + err.Error()}
	}
	defer h.Stop()

	var o out
	res := Result{
		Setup: "Player in survival at (80.5, -60, 80.5) with full health. A real `entity.FallingBlock`\n" +
			"holding a `block.Anvil` is spawned directly above the player at increasing heights and left to\n" +
			"fall onto them. Health and the entity's accumulated fall distance are read afterwards.",
		ServerSteps: []string{
			"spawned `entity.NewFallingBlock(opts, block.Anvil{})` above the player with `tx.AddEntity`",
			"let the real world ticker run the falling block's movement and `solidify`/`damageEntities`",
			"sampled the falling entity's Y position every 50ms to measure how far it actually fell",
			"read `(*Player).Health()` before and after",
		},
		ClientSteps: []string{
			"a real gophertunnel client was connected and kept the chunk loaded so the falling block " +
				"entity actually ticked. It sent no packets for this scenario.",
		},
	}

	px, pz := 80.5, 80.5
	worst := 0.0
	var heights = []int{10, 20, 40}

	for _, height := range heights {
		var (
			before, after float64
			highestY      = -1000.0
			lowestY       = 1000.0
			landed        string
		)
		if err := h.Do(func(tx *world.Tx, p *player.Player) {
			preparePlayer(tx, p, mgl64.Vec3{px, -60, pz})
			removeAllItemEntities(tx)
			for y := -59; y <= -59+height+2; y++ {
				tx.SetBlock(cube.Pos{80, y, 80}, nil, nil)
			}
			p.Heal(20, healSource{})
			before = p.Health()
			tx.AddEntity(entity.NewFallingBlock(world.EntitySpawnOpts{
				Position: mgl64.Vec3{px, float64(-60 + height), pz},
			}, block.Anvil{}))
		}); err != nil {
			return blocked(res, "world call failed: "+err.Error())
		}

		// Wait for the anvil to land, sampling its position as it falls.
		deadline := time.Now().Add(20 * time.Second)
		for time.Now().Before(deadline) {
			time.Sleep(50 * time.Millisecond)
			done := false
			if err := h.Do(func(tx *world.Tx, p *player.Player) {
				found := false
				for e := range tx.Entities() {
					ent, ok := e.(*entity.Ent)
					if !ok {
						continue
					}
					if _, ok := ent.Behaviour().(*entity.FallingBlockBehaviour); ok {
						found = true
						y := ent.Position()[1]
						highestY = max(highestY, y)
						lowestY = min(lowestY, y)
					}
				}
				if !found {
					done = true
					after = p.Health()
					landed = fmt.Sprintf("%T", tx.Block(cube.Pos{80, -60, 80}))
				}
			}); err != nil {
				return blocked(res, "world call failed: "+err.Error())
			}
			if done {
				break
			}
		}
		if landed == "" {
			if err := h.Do(func(tx *world.Tx, p *player.Player) { after = p.Health() }); err != nil {
				return blocked(res, "world call failed: "+err.Error())
			}
		}
		dmg := before - after
		if dmg > worst {
			worst = dmg
		}
		expected := min(float64(height-1)*2, 40)
		o.printf("anvil dropped from %d blocks above the player:", height)
		o.printf("  measured drop (sampled entity Y)          : %.2f -> %.2f = %.2f blocks",
			highestY, lowestY, highestY-lowestY)
		o.printf("  block left where the player stands        : %s", landed)
		o.printf("  player health before / after              : %.1f / %.1f", before, after)
		o.printf("  damage dealt                              : %.1f   (expected about %.0f: 2 per block, capped at 40)",
			dmg, expected)
		o.printf("")
	}

	res.Observed = o.String()
	res.Expected = "`Anvil.Damage()` returns 2 damage per block fallen with a cap of 40, so an anvil falling\n" +
		"10 blocks onto a player should deal about 18 damage and one falling 40 blocks should deal the\n" +
		"full 40 (a lethal hit on a 20 health player)."

	if worst == 0 {
		res.Verdict = Reproduced
		res.Summary = "0.0 damage from anvils falling 10, 20 and 40 blocks (expected up to 40)"
	} else {
		res.Verdict = NotReproduced
		res.Summary = fmt.Sprintf("worst damage observed was %.1f", worst)
	}
	return res
}

// healSource is a world.HealingSource used to top the player back up.
type healSource struct{}

func (healSource) HealingSource() {}
