package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/df-mc/dragonfly/server/block"
	"github.com/df-mc/dragonfly/server/block/cube"
	"github.com/df-mc/dragonfly/server/entity/effect"
	"github.com/df-mc/dragonfly/server/item"
	"github.com/df-mc/dragonfly/server/item/enchantment"
	"github.com/df-mc/dragonfly/server/player"
	"github.com/df-mc/dragonfly/server/world"
	"github.com/go-gl/mathgl/mgl64"
)

func init() {
	register(Scenario{
		ID:      "19-poison-respiration-leggings",
		Title:   "Poison damage rate, Respiration levels and leggings durability",
		Timeout: 480 * time.Second,
		Bug: "Three numbers that are wrong, all measured here rather than looked up.\n\n" +
			"**Poison.** `effect.poison.Apply` fires on `interval := max(50>>(eff.Level()-1), 1)` ticks, so\n" +
			"level I hurts once every 50 ticks (2.5 s).\n\n" +
			"**Respiration.** `tickAirSupply` skips the air-supply decrement with probability\n" +
			"`Respiration.Chance(level) = 1.0 / float64(level+1)`. That probability *falls* as the level\n" +
			"rises (1/2, 1/3, 1/4), so a higher Respiration level protects **less** than a lower one - the\n" +
			"exact opposite of the doc comment on the enchantment (\"extends underwater breathing time by\n" +
			"+15 seconds per enchantment level\").\n\n" +
			"**Leggings.** `Leggings.DurabilityInfo` uses `BaseDurability + BaseDurability/2.5` while the\n" +
			"helmet uses `BaseDurability`, the chestplate `+/2.2` and the boots `+/5.5`. Those three match\n" +
			"vanilla's 11x/16x/13x material multipliers; leggings should be 15x, i.e. `+ BaseDurability/2.75`.",
		Run: runValues,
	})
}

func runValues() Result {
	h, err := startHarness(harnessOpts{withClient: true, randomTickSpeed: -1})
	if err != nil {
		return Result{Verdict: Blocked, Reason: "could not start harness: " + err.Error()}
	}
	defer h.Stop()

	var o out
	res := Result{
		Setup: "Part A: a player with a 400 health pool given `effect.Poison` at levels I, II and III, with\n" +
			"health sampled every 25 ms so the interval between poison hits can be measured.\n" +
			"Part B: a 3-deep water column at (170, -60, 170); the player is put at the bottom with a\n" +
			"helmet carrying Respiration 0, 1, 2 and 3 in turn, and the time until the first drowning hit is\n" +
			"measured. Each run starts with a 5 second air supply rather than the full 15 to keep the run\n" +
			"time down; the enchantment works as a per-tick probability so the shape of the result is the\n" +
			"same. The enchantment is probabilistic, so each level is measured over several runs.\n" +
			"Part C: the `DurabilityInfo().MaxDurability` the server itself reports for every armour piece\n" +
			"of every tier.",
		ServerSteps: []string{
			"applied the effects with `(*Player).AddEffect` and sampled `(*Player).Health()` in a loop",
			"built the water column with `tx.SetLiquid` and equipped helmets with `(*Player).Armour().SetHelmet`",
			"read `DurabilityInfo().MaxDurability` straight off the real item types",
		},
		ClientSteps: []string{
			"a real gophertunnel client is connected and keeps the chunks ticking. It sent no packets for this scenario.",
		},
	}

	// ---- Part A: poison damage rate ----
	o.printf("=== part A: poison damage rate (measured) ===")
	var poisonIntervals []string
	for level := 1; level <= 3; level++ {
		if err := h.Do(func(tx *world.Tx, p *player.Player) {
			preparePlayer(tx, p, mgl64.Vec3{170.5, -60, 175.5})
			for _, e := range p.Effects() {
				p.RemoveEffect(e.Type())
			}
			p.SetMaxHealth(400)
			p.Heal(400, healSource{})
			// Keep the food bar low so natural regeneration does not mask the
			// poison ticks.
			p.SetFood(6)
			p.AddEffect(effect.New(effect.Poison, level, 60*time.Second))
		}); err != nil {
			return blocked(res, "world call failed: "+err.Error())
		}
		var hits []time.Time
		last := 0.0
		start := time.Now()
		for time.Since(start) < 25*time.Second && len(hits) < 8 {
			time.Sleep(25 * time.Millisecond)
			var hp float64
			if err := h.Do(func(tx *world.Tx, p *player.Player) { hp = p.Health() }); err != nil {
				return blocked(res, "world call failed: "+err.Error())
			}
			if last == 0 {
				last = hp
				continue
			}
			if hp < last {
				hits = append(hits, time.Now())
				last = hp
			}
		}
		if len(hits) < 2 {
			o.printf("poison %d: only %d damage hits observed in 25s", level, len(hits))
			poisonIntervals = append(poisonIntervals, fmt.Sprintf("level %d: <2 hits", level))
			continue
		}
		total := hits[len(hits)-1].Sub(hits[0])
		avg := total / time.Duration(len(hits)-1)
		ticks := avg.Seconds() * 20
		o.printf("poison %d: %d hits, mean interval %s (%.0f ticks)", level, len(hits), avg.Round(time.Millisecond), ticks)
		poisonIntervals = append(poisonIntervals, fmt.Sprintf("level %d every %.0f ticks", level, ticks))
	}
	o.printf("")

	// ---- Part B: respiration ----
	o.printf("=== part B: time underwater before the first drowning hit (measured) ===")
	waterPos := cube.Pos{170, -60, 170}
	buildWater := func() error {
		return h.Do(func(tx *world.Tx, p *player.Player) {
			for x := 169; x <= 171; x++ {
				for z := 169; z <= 171; z++ {
					tx.SetBlock(cube.Pos{x, -61, z}, block.Stone{}, nil)
					for y := -60; y <= -56; y++ {
						tx.SetBlock(cube.Pos{x, y, z}, nil, nil)
						tx.SetLiquid(cube.Pos{x, y, z}, block.Water{Depth: 8, Still: true})
					}
				}
			}
		})
	}
	if err := buildWater(); err != nil {
		return blocked(res, "world call failed: "+err.Error())
	}

	const runsPerLevel = 3
	var respLines []string
	means := map[int]float64{}
	for level := 0; level <= enchantment.Respiration.MaxLevel(); level++ {
		var times []float64
		for run := 0; run < runsPerLevel; run++ {
			if err := h.Do(func(tx *world.Tx, p *player.Player) {
				preparePlayer(tx, p, mgl64.Vec3{float64(waterPos[0]) + 0.5, float64(waterPos[1]), float64(waterPos[2]) + 0.5})
				for _, e := range p.Effects() {
					p.RemoveEffect(e.Type())
				}
				p.SetFood(6)
				p.SetMaxHealth(400)
				p.Heal(400, healSource{})
				// Start each run with a 5 second air supply instead of the full 15
				// so the measurement is quick; the enchantment's effect is a
				// per-tick probability, so the shape of the result is the same.
				p.SetAirSupply(5 * time.Second)
				helmet := item.NewStack(item.Helmet{Tier: item.ArmourTierDiamond{}}, 1)
				if level > 0 {
					helmet = helmet.WithEnchantments(item.NewEnchantment(enchantment.Respiration, level))
				}
				p.Armour().SetHelmet(helmet)
			}); err != nil {
				return blocked(res, "world call failed: "+err.Error())
			}
			start := time.Now()
			hurt := 0.0
			for time.Since(start) < 45*time.Second {
				time.Sleep(50 * time.Millisecond)
				var hp float64
				var breathing time.Duration
				if err := h.Do(func(tx *world.Tx, p *player.Player) {
					hp, breathing = p.Health(), p.AirSupply()
				}); err != nil {
					return blocked(res, "world call failed: "+err.Error())
				}
				_ = breathing
				if hp < 400 {
					hurt = time.Since(start).Seconds()
					break
				}
			}
			if hurt == 0 {
				hurt = -1
			}
			times = append(times, hurt)
		}
		sum, n := 0.0, 0
		for _, t := range times {
			if t > 0 {
				sum += t
				n++
			}
		}
		mean := -1.0
		if n > 0 {
			mean = sum / float64(n)
		}
		means[level] = mean
		o.printf("respiration %d (skip chance %.3f): runs %v, mean %.1fs to first drowning damage",
			level, respirationChance(level), fmtFloats(times), mean)
		respLines = append(respLines, fmt.Sprintf("resp %d -> %.1fs", level, mean))
	}
	o.printf("")

	// ---- Part C: armour durability ----
	o.printf("=== part C: MaxDurability reported by the server for every armour piece ===")
	tiers := []struct {
		name string
		t    item.ArmourTier
	}{
		{"leather", item.ArmourTierLeather{}},
		{"golden", item.ArmourTierGold{}},
		{"chainmail", item.ArmourTierChain{}},
		{"iron", item.ArmourTierIron{}},
		{"diamond", item.ArmourTierDiamond{}},
		{"netherite", item.ArmourTierNetherite{}},
	}
	o.printf("%-10s %8s %8s %8s %8s | leggings/helmet ratio (should be 15/11 = 1.3636)",
		"tier", "helmet", "chest", "leggings", "boots")
	badRatios := 0
	for _, t := range tiers {
		hd := item.Helmet{Tier: t.t}.DurabilityInfo().MaxDurability
		cd := item.Chestplate{Tier: t.t}.DurabilityInfo().MaxDurability
		ld := item.Leggings{Tier: t.t}.DurabilityInfo().MaxDurability
		bd := item.Boots{Tier: t.t}.DurabilityInfo().MaxDurability
		ratio := float64(ld) / float64(hd)
		expected := int(t.t.BaseDurability() + t.t.BaseDurability()/2.75)
		mark := ""
		if ld != expected {
			mark = fmt.Sprintf("  <- vanilla 15x would be %d", expected)
			badRatios++
		}
		o.printf("%-10s %8d %8d %8d %8d | %.4f%s", t.name, hd, cd, ld, bd, ratio, mark)
	}
	o.printf("")
	o.printf("helmet/chest/boots ratios to the helmet base: 1.0000 / 1.4545 (16/11) / 1.1818 (13/11) - all vanilla.")

	res.Observed = o.String()
	res.Expected = "Poison I in vanilla hurts once every 25 ticks (1.25 s), doubling in rate per level.\n" +
		"Respiration should make the player last *longer* underwater at every extra level.\n" +
		"Leggings should be 15x the material base, i.e. the same 15/11 ratio to the helmet that the\n" +
		"chestplate (16/11) and the boots (13/11) already have."

	respBroken := means[1] > 0 && means[2] > 0 && means[2] < means[1]
	if means[3] > 0 && means[1] > 0 && means[3] < means[1] {
		respBroken = true
	}

	var parts []string
	parts = append(parts, "poison "+strings.Join(poisonIntervals, ", "))
	parts = append(parts, "respiration "+strings.Join(respLines, ", "))
	parts = append(parts, fmt.Sprintf("%d/%d leggings tiers off the vanilla 15x", badRatios, len(tiers)))

	if respBroken || badRatios > 0 {
		res.Verdict = Reproduced
		res.Summary = strings.Join(parts, "; ")
	} else {
		res.Verdict = NotReproduced
		res.Summary = strings.Join(parts, "; ")
	}
	return res
}

func respirationChance(level int) float64 {
	if level == 0 {
		return 0
	}
	return enchantment.Respiration.Chance(level)
}

func fmtFloats(f []float64) string {
	var parts []string
	for _, v := range f {
		if v < 0 {
			parts = append(parts, "none")
			continue
		}
		parts = append(parts, fmt.Sprintf("%.1fs", v))
	}
	return "[" + strings.Join(parts, " ") + "]"
}
