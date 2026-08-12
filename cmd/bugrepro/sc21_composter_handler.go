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
		ID:      "21-composter-drops-through-handler",
		Title:   "A composter collected by HandleBlockBreak keeps the level it was broken at",
		Timeout: 180 * time.Second,
		Bug: "Scenario 08 shows that breaking a full composter is harmless because the drop becomes an item\n" +
			"entity, and `entity.NewItem` re-reads the stack from NBT, which discards the block state.\n\n" +
			"`(*Player).BreakBlock` hands the drops to a Handler *before* any of that happens:\n\n" +
			"```go\n" +
			"drops := p.drops(held, b)                             // oneOf(c), level still set\n" +
			"p.Handler().HandleBlockBreak(ctx, pos, &drops, &xp)   // handler sees them raw\n" +
			"...\n" +
			"p.tx.AddEntity(entity.NewItem(opts, drop))            // stripping happens only here\n" +
			"```\n\n" +
			"A server that collects drops straight into the inventory from that handler - a common\n" +
			"auto-pickup feature - therefore never goes through the item entity. This scenario installs\n" +
			"exactly such a Handler and checks whether the composter it collects is still full.",
		Run: runComposterHandler,
	})
}

// autoPickup is a Handler that collects block drops straight into the player's inventory instead of letting them
// become item entities, which is what an auto-pickup feature on a server does.
type autoPickup struct {
	player.NopHandler
	p *player.Player
}

func (h autoPickup) HandleBlockBreak(_ *player.Context, _ cube.Pos, drops *[]item.Stack, _ *int) {
	for _, d := range *drops {
		_, _ = h.p.Inventory().AddItem(d)
	}
	*drops = nil
}

func runComposterHandler() Result {
	h, err := startHarness(harnessOpts{withClient: true, randomTickSpeed: -1})
	if err != nil {
		return Result{Verdict: Blocked, Reason: "could not start harness: " + err.Error()}
	}
	defer h.Stop()

	var o out
	res := Result{
		Setup: "A composter at (70, -60, 70), with a Handler installed on the player that empties the drops\n" +
			"slice into the inventory. The player fills the composter to level 8 with hay bales, breaks it,\n" +
			"and the composter that lands in the inventory is inspected and placed back down.",
		ServerSteps: []string{
			"installed a `player.Handler` whose `HandleBlockBreak` moves every stack of `*drops` into the inventory and empties the slice",
			"filled the composter with `(*Player).UseItemOnBlock` holding hay bales until `Level` reached 8",
			"broke it with `(*Player).BreakBlock`, so the drops reached the Handler and never became item entities",
			"read the `Level` of the composter that arrived in the inventory",
			"placed it back with `(*Player).UseItemOnBlock` and read the `Level` of the block that resulted",
		},
		ClientSteps: []string{
			"a real gophertunnel client was connected and kept the chunk loaded so the composter's " +
				"scheduled 7 -> 8 tick actually fired. It sent no packets for this scenario.",
		},
	}

	pos := cube.Pos{70, -60, 70}
	ground := cube.Pos{70, -61, 70}

	fillTo8 := func() int {
		level := 0
		deadline := time.Now().Add(30 * time.Second)
		for time.Now().Before(deadline) {
			_ = h.Do(func(tx *world.Tx, p *player.Player) {
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
			})
			if level == 8 {
				return level
			}
			time.Sleep(400 * time.Millisecond)
			_ = h.Do(func(tx *world.Tx, p *player.Player) {
				if c, ok := tx.Block(pos).(block.Composter); ok {
					level = c.Level
				}
			})
			if level == 8 {
				return level
			}
		}
		return level
	}

	if err := h.Do(func(tx *world.Tx, p *player.Player) {
		preparePlayer(tx, p, mgl64.Vec3{70.5, -60, 72.5})
		removeAllItemEntities(tx)
		clearArea(tx, pos, 2)
		tx.SetBlock(ground, block.Grass{}, nil)
		tx.SetBlock(pos, block.Composter{}, nil)
		p.Handle(autoPickup{p: p})
	}); err != nil {
		return blocked(res, "world call failed: "+err.Error())
	}

	totalBoneMeal, refills, collectedFull := 0, 0, 0
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
		level := fillTo8()
		o.printf("cycle %d: composter started at Level=%d and reached Level=%d", cycle, startLevel, level)
		if level != 8 {
			o.printf("  could not reach level 8, stopping")
			break
		}

		invLevel, boneMeal := -1, 0
		if err := h.Do(func(tx *world.Tx, p *player.Player) {
			p.Inventory().Clear()
			p.SetHeldItems(item.NewStack(item.Axe{Tier: item.ToolTierDiamond}, 1), item.Stack{})
			p.BreakBlock(pos)

			for _, s := range p.Inventory().Items() {
				if c, ok := s.Item().(block.Composter); ok {
					invLevel = c.Level
				}
			}
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
				if s := ib.Item(); s.Item() == (item.BoneMeal{}) {
					boneMeal += s.Count()
				}
			}
			removeAllItemEntities(tx)
		}); err != nil {
			return blocked(res, "world call failed: "+err.Error())
		}
		totalBoneMeal += boneMeal
		o.printf("  broke it: bone meal dropped = %d", boneMeal)
		o.printf("  the composter collected by the Handler has Level=%d", invLevel)

		newLevel := -1
		if err := h.Do(func(tx *world.Tx, p *player.Player) {
			tx.SetBlock(pos, nil, nil)
			for _, s := range p.Inventory().Items() {
				if _, ok := s.Item().(block.Composter); ok {
					p.SetHeldItems(s, item.Stack{})
					break
				}
			}
			p.UseItemOnBlock(ground, cube.FaceUp, mgl64.Vec3{0.5, 1, 0.5})
			if c, ok := tx.Block(pos).(block.Composter); ok {
				newLevel = c.Level
			}
		}); err != nil {
			return blocked(res, "world call failed: "+err.Error())
		}
		o.printf("  placed it back down -> Level=%d", newLevel)
		if invLevel == 8 && newLevel == 8 {
			collectedFull++
		}
		o.printf("")
	}

	o.printf("total bone meal harvested over the cycles : %d", totalBoneMeal)
	o.printf("times the composter had to be refilled     : %d", refills)
	o.printf("cycles where the collected composter was still full: %d", collectedFull)

	res.Observed = o.String()
	res.Expected = "The composter reaching the Handler should be empty, so collecting drops directly is no\n" +
		"different from picking them up off the ground and every bone meal costs a fresh refill."

	if collectedFull > 0 {
		res.Verdict = Reproduced
		res.Summary = fmt.Sprintf("the Handler collected a Composter{Level:8} which placed back full in %d of the cycles; %d bone meal from %d refill(s)",
			collectedFull, totalBoneMeal, refills)
	} else {
		res.Verdict = NotReproduced
		res.Summary = fmt.Sprintf("the composter reaching the Handler was not full; %d bone meal from %d refill(s)",
			totalBoneMeal, refills)
	}
	return res
}
