package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/df-mc/dragonfly/server/block"
	"github.com/df-mc/dragonfly/server/block/cube"
	"github.com/df-mc/dragonfly/server/player"
	"github.com/df-mc/dragonfly/server/world"
	"github.com/go-gl/mathgl/mgl64"
)

func init() {
	register(Scenario{
		ID:      "12-midair-fall-damage-overhang",
		Title:   "Fall damage applied in mid-air when passing an overhang",
		Timeout: 240 * time.Second,
		Bug: "`(*Player).checkOnGround` builds a swept box that reaches *backwards* along the whole movement\n" +
			"of the tick, in every axis:\n\n" +
			"```go\n" +
			"box := Type.BBox(p).Translate(p.Position()).Extend(mgl64.Vec3{0, -0.05}).Extend(deltaPos.Mul(-1.0))\n" +
			"```\n\n" +
			"For a falling player that box is one to four blocks tall, and if the player is also drifting\n" +
			"sideways it reaches back horizontally too. Any block anywhere in that swept volume - including\n" +
			"a platform the player is merely passing *beside* or has just fallen past - makes\n" +
			"`checkOnGround` return true. `updateFallState` then takes the `p.OnGround()` branch in mid-air,\n" +
			"cashes in the whole accumulated `fallDistance` through `(*Player).fall` and resets it. The\n" +
			"player is hurt while still tens of blocks above the ground.",
		Run: runMidAirFallDamage,
	})
}

func runMidAirFallDamage() Result {
	h, err := startHarness(harnessOpts{withClient: true, randomTickSpeed: -1})
	if err != nil {
		return Result{Verdict: Blocked, Reason: "could not start harness: " + err.Error()}
	}
	defer h.Stop()

	var o out
	res := Result{
		Setup: "A one-block-wide stone overhang at x = 120, y = -40, running along z. The ground is at\n" +
			"y = -61. The player is dropped from y = -20, i.e. 20 blocks above the overhang and 41 blocks\n" +
			"above the ground, at x positions just beside the overhang. Three cases: falling straight down\n" +
			"flush beside it, falling while drifting away from it, and falling while pressed into it.\n" +
			"Health is sampled after every simulated tick so mid-air damage is visible.",
		ServerSteps: []string{
			"built the overhang and the floor with `tx.SetBlock`",
			"sampled `(*Player).Health()`, `OnGround()` and `Position()` after every tick",
		},
		ClientSteps: []string{
			"the entire fall was driven by real `packet.PlayerAuthInput` packets from the gophertunnel " +
				"client, one per simulated tick, following a Bedrock free-fall curve (v -= 0.08, v *= 0.98). " +
				"The server turned each into `(*Player).Move`, which is what runs `checkOnGround`, " +
				"`updateFallState` and `fall`.",
		},
	}

	overhangY := -40
	floorY := -61

	build := func() error {
		return h.Do(func(tx *world.Tx, p *player.Player) {
			for x := 118; x <= 124; x++ {
				for z := 118; z <= 124; z++ {
					for y := floorY + 1; y <= -18; y++ {
						tx.SetBlock(cube.Pos{x, y, z}, nil, nil)
					}
					tx.SetBlock(cube.Pos{x, floorY, z}, block.Grass{}, nil)
				}
			}
			for z := 118; z <= 124; z++ {
				tx.SetBlock(cube.Pos{120, overhangY, z}, block.Stone{}, nil)
			}
		})
	}

	type fallCase struct {
		name   string
		startX float64
		driftX float64
	}
	cases := []fallCase{
		{"straight down, flush against the overhang (bbox touches x=121.0 exactly)", 121.3, 0},
		{"drifting away from the overhang at 0.2 blocks/tick", 121.3, 0.2},
		{"pressed into the overhang (client reports a 0.05 overlap)", 120.95, 0},
	}

	var summaries []string
	reproduced := 0

	for _, c := range cases {
		if err := build(); err != nil {
			return blocked(res, "world call failed: "+err.Error())
		}
		startY := -20.0
		if err := h.Do(func(tx *world.Tx, p *player.Player) {
			preparePlayer(tx, p, mgl64.Vec3{c.startX, startY, 121.5})
			// A large health pool so a single fall cannot kill the player: a
			// dead player stops moving and would mask later hits.
			p.SetMaxHealth(400)
			p.Heal(400, healSource{})
		}); err != nil {
			return blocked(res, "world call failed: "+err.Error())
		}
		for range 5 {
			_ = h.MoveTo(mgl64.Vec3{c.startX, startY, 121.5})
			time.Sleep(50 * time.Millisecond)
		}

		var startHP float64
		if err := h.Do(func(tx *world.Tx, p *player.Player) { startHP = p.Health() }); err != nil {
			return blocked(res, "world call failed: "+err.Error())
		}
		var (
			x, y, v      = c.startX, startY, 0.0
			hp           = startHP
			midAirHits   []string
			landedDamage float64
			firstHitY    = 0.0
		)
		for range 300 {
			v = (v - 0.08) * 0.98
			y += v
			x += c.driftX
			landed := false
			if y <= float64(floorY)+1 {
				y = float64(floorY) + 1
				landed = true
			}
			if err := h.MoveTo(mgl64.Vec3{x, y, 121.5}); err != nil {
				return blocked(res, "sending PlayerAuthInput failed: "+err.Error())
			}
			time.Sleep(50 * time.Millisecond)

			var newHP float64
			var onGround bool
			var actualY float64
			if err := h.Do(func(tx *world.Tx, p *player.Player) {
				newHP, onGround, actualY = p.Health(), p.OnGround(), p.Position()[1]
			}); err != nil {
				return blocked(res, "world call failed: "+err.Error())
			}
			if newHP < hp {
				dmg := hp - newHP
				if !landed && actualY > float64(floorY)+2 {
					if firstHitY == 0 {
						firstHitY = actualY
					}
					midAirHits = append(midAirHits, fmt.Sprintf(
						"%.1f damage at y=%.2f (%.1f blocks above the floor, onGround=%v)",
						dmg, actualY, actualY-float64(floorY)-1, onGround))
				} else {
					landedDamage += dmg
				}
				hp = newHP
			}
			if landed {
				break
			}
		}
		// A few stationary ticks so the landing registers.
		for range 3 {
			_ = h.MoveTo(mgl64.Vec3{x, float64(floorY) + 1, 121.5})
			time.Sleep(50 * time.Millisecond)
		}
		var finalHP float64
		if err := h.Do(func(tx *world.Tx, p *player.Player) { finalHP = p.Health() }); err != nil {
			return blocked(res, "world call failed: "+err.Error())
		}

		o.printf("case: %s", c.name)
		o.printf("  dropped from y=%.0f, overhang at y=%d, floor at y=%d", startY, overhangY, floorY)
		if len(midAirHits) == 0 {
			o.printf("  no mid-air damage")
		}
		for _, hit := range midAirHits {
			o.printf("  MID-AIR: %s", hit)
		}
		o.printf("  damage on landing : %.1f", landedDamage+(hp-finalHP))
		o.printf("  health %.0f -> %.0f (total damage %.1f)", startHP, finalHP, startHP-finalHP)
		o.printf("")
		if len(midAirHits) > 0 {
			reproduced++
			summaries = append(summaries, fmt.Sprintf("%s -> %d mid-air hit(s), first at y=%.1f (%.0f blocks up)",
				c.name, len(midAirHits), firstHitY, firstHitY-float64(floorY)-1))
		} else {
			summaries = append(summaries, c.name+" -> no mid-air damage")
		}
	}

	res.Observed = o.String()
	res.Expected = "Fall damage should only ever be applied when the player actually lands on something. A player\n" +
		"falling past a platform 21 blocks above the floor should take a single hit at the floor, never\n" +
		"one in mid-air."

	if reproduced > 0 {
		res.Verdict = Reproduced
		res.Summary = strings.Join(summaries, "; ")
	} else {
		res.Verdict = NotReproduced
		res.Summary = strings.Join(summaries, "; ")
	}
	return res
}
