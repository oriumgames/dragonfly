package main

import (
	"fmt"
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
		ID:      "08-composter-drops-full-composter",
		Title:   "Breaking a full composter drops a composter that is still full",
		Timeout: 180 * time.Second,
		Bug: "`Composter.BreakInfo` uses `oneOf(c)` where `c` is the composter *including its `Level`*:\n\n" +
			"```go\n" +
			"func (c Composter) BreakInfo() BreakInfo {\n" +
			"    return newBreakInfo(0.6, alwaysHarvestable, axeEffective, oneOf(c)).withBreakHandler(func(...) {\n" +
			"        if c.Level == 8 { dropItem(tx, item.NewStack(item.BoneMeal{}, 1), ...) }\n" +
			"    })\n}\n" +
			"```\n\n" +
			"so breaking a level 8 composter yields both the bone meal *and* an item that is a level 8\n" +
			"composter. Re-placing it and harvesting again is free bone meal.",
		Run: runComposter,
	})
}

func runComposter() Result {
	h, err := startHarness(harnessOpts{withClient: true, randomTickSpeed: -1})
	if err != nil {
		return Result{Verdict: Blocked, Reason: "could not start harness: " + err.Error()}
	}
	defer h.Stop()

	var o out
	res := Result{
		Setup: "A composter at (70, -60, 70). The player fills it to level 8 by using hay bales on it (0.85 compost chance)\n" +
			"(`Composter.Activate` -> `fill`, level 7 schedules the tick that promotes it to 8), breaks it,\n" +
			"places the dropped composter item back down and repeats for three cycles. All bone meal\n" +
			"item entities are counted and removed between cycles.",
		ServerSteps: []string{
			"placed the composter with `tx.SetBlock`",
			"filled it with `(*Player).UseItemOnBlock` holding hay bales until `Level` reached 8, letting the real scheduled tick promote 7 -> 8",
			"broke it with `(*Player).BreakBlock` holding an axe",
			"read back the `Level` of the composter *item* that dropped (both as it comes out of `BreakInfo().Drops()` and as it exists on the ground)",
			"re-placed the dropped item with `(*Player).UseItemOnBlock` and counted bone meal each cycle",
		},
		ClientSteps: []string{
			"a real gophertunnel client was connected and kept the chunk loaded so the composter's " +
				"scheduled 7 -> 8 tick actually fired. It sent no packets for this scenario.",
		},
	}

	pos := cube.Pos{70, -60, 70}
	ground := cube.Pos{70, -61, 70}

	fillTo8 := func() (int, error) {
		level := 0
		deadline := time.Now().Add(30 * time.Second)
		for time.Now().Before(deadline) {
			if err := h.Do(func(tx *world.Tx, p *player.Player) {
				for range 20 {
					c, ok := tx.Block(pos).(block.Composter)
					if !ok || c.Level >= 7 {
						break
					}
					p.SetHeldItems(item.NewStack(block.HayBale{}, 64), item.Stack{})
					p.UseItemOnBlock(pos, cube.FaceUp, mgl64.Vec3{0.5, 1, 0.5})
				}
				if c, ok := tx.Block(pos).(block.Composter); ok {
					level = c.Level
				}
			}); err != nil {
				return level, err
			}
			if level == 8 {
				return level, nil
			}
			time.Sleep(400 * time.Millisecond)
			if err := h.Do(func(tx *world.Tx, p *player.Player) {
				if c, ok := tx.Block(pos).(block.Composter); ok {
					level = c.Level
				}
			}); err != nil {
				return level, err
			}
			if level == 8 {
				return level, nil
			}
		}
		return level, nil
	}

	if err := h.Do(func(tx *world.Tx, p *player.Player) {
		preparePlayer(tx, p, mgl64.Vec3{70.5, -60, 72.5})
		removeAllItemEntities(tx)
		clearArea(tx, pos, 2)
		tx.SetBlock(ground, block.Grass{}, nil)
		tx.SetBlock(pos, block.Composter{}, nil)
	}); err != nil {
		return blocked(res, "world call failed: "+err.Error())
	}

	totalBoneMeal, refills, droppedFull := 0, 0, 0
	for cycle := 1; cycle <= 3; cycle++ {
		startLevel := -1
		if err := h.Do(func(tx *world.Tx, p *player.Player) {
			if c, ok := tx.Block(pos).(block.Composter); ok {
				startLevel = c.Level
			}
		}); err != nil {
			return blocked(res, "world call failed: "+err.Error())
		}
		if startLevel < 8 {
			refills++
		}
		level, err := fillTo8()
		if err != nil {
			return blocked(res, "world call failed: "+err.Error())
		}
		o.printf("cycle %d: composter started at Level=%d and reached Level=%d", cycle, startLevel, level)
		if level != 8 {
			o.printf("  could not reach level 8, stopping")
			break
		}

		var (
			rawDropLevel  = -1
			entDropLevel  = -1
			boneMeal      int
			composterItem world.Item
		)
		if err := h.Do(func(tx *world.Tx, p *player.Player) {
			c := tx.Block(pos).(block.Composter)
			drops := c.BreakInfo().Drops(item.ToolNone{}, nil)
			for _, d := range drops {
				if comp, ok := d.Item().(block.Composter); ok {
					rawDropLevel = comp.Level
				}
			}
			p.SetHeldItems(item.NewStack(item.Axe{Tier: item.ToolTierDiamond}, 1), item.Stack{})
			p.BreakBlock(pos)
		}); err != nil {
			return blocked(res, "world call failed: "+err.Error())
		}
		time.Sleep(200 * time.Millisecond)
		if err := h.Do(func(tx *world.Tx, p *player.Player) {
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
				switch it := s.Item().(type) {
				case item.BoneMeal:
					boneMeal += s.Count()
				case block.Composter:
					entDropLevel = it.Level
					composterItem = it
				}
			}
			removeAllItemEntities(tx)
		}); err != nil {
			return blocked(res, "world call failed: "+err.Error())
		}
		totalBoneMeal += boneMeal
		o.printf("  broke it: bone meal dropped = %d", boneMeal)
		o.printf("  BreakInfo().Drops() gave a composter item with Level=%d", rawDropLevel)
		o.printf("  the composter item entity on the ground has Level=%d", entDropLevel)

		if composterItem == nil {
			o.printf("  no composter item dropped, stopping")
			break
		}
		it := composterItem
		var newLevel = -1
		if err := h.Do(func(tx *world.Tx, p *player.Player) {
			tx.SetBlock(pos, nil, nil)
			p.SetHeldItems(item.NewStack(it, 1), item.Stack{})
			p.UseItemOnBlock(ground, cube.FaceUp, mgl64.Vec3{0.5, 1, 0.5})
			if c, ok := tx.Block(pos).(block.Composter); ok {
				newLevel = c.Level
			}
		}); err != nil {
			return blocked(res, "world call failed: "+err.Error())
		}
		o.printf("  placed the dropped composter back down -> Level=%d", newLevel)
		if entDropLevel == 8 && newLevel == 8 {
			droppedFull++
		}
		o.printf("")
	}

	o.printf("total bone meal harvested over the cycles : %d", totalBoneMeal)
	o.printf("times the composter had to be refilled     : %d", refills)
	o.printf("cycles where the dropped composter came back still full: %d", droppedFull)

	res.Observed = o.String()
	res.Expected = "Breaking a full composter should drop an *empty* composter plus the bone meal, so every bone\n" +
		"meal costs a fresh set of compostable items. `BreakInfo().Drops()` should return\n" +
		"`Composter{Level: 0}`, not `Composter{Level: 8}`."

	if droppedFull > 0 {
		res.Verdict = Reproduced
		res.Summary = fmt.Sprintf("%d bone meal harvested from only %d fill(s) - the dropped composter came back full",
			totalBoneMeal, refills)
	} else {
		res.Verdict = NotReproduced
		res.Reason = "`Composter.BreakInfo()` really does return `oneOf(c)` with the level still set - the verbatim\n" +
			"output above shows `BreakInfo().Drops()` handing back a `Composter{Level: 8}` item. It never\n" +
			"reaches the player though: `entity.NewItem` runs the stack through\n" +
			"`item.ReadNBT(item.WriteNBT(i, true), nil)`, and `readItemStack` resolves the item by\n" +
			"`world.ItemByName(\"minecraft:composter\", 0)`, which returns the registered level 0 composter\n" +
			"and discards the block state. So each bone meal still costs a full refill."
		res.Summary = fmt.Sprintf("%d bone meal from %d refill(s); Drops() does hand back a Composter{Level:8} but the item entity NBT round trip resets it to Level 0",
			totalBoneMeal, refills)
	}
	return res
}
