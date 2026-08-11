package main

import (
	"fmt"
	"time"

	"github.com/df-mc/dragonfly/server/block"
	"github.com/df-mc/dragonfly/server/block/cube"
	"github.com/df-mc/dragonfly/server/item"
	"github.com/df-mc/dragonfly/server/player"
	"github.com/df-mc/dragonfly/server/world"
	"github.com/go-gl/mathgl/mgl64"
)

func init() {
	register(Scenario{
		ID:      "03-decorated-pot-overfill",
		Title:   "Decorated pot overfills past its max stack and eats the excess",
		Timeout: 240 * time.Second,
		Bug: "`DecoratedPot.Activate` (a player right-clicking) refuses to add when\n" +
			"`p.Item.Count() == p.Item.MaxCount()`, but `DecoratedPot.InsertItem` (the hopper path) has no\n" +
			"such check:\n\n" +
			"```go\n" +
			"func (p DecoratedPot) InsertItem(h Hopper, pos cube.Pos, tx *world.Tx) bool {\n" +
			"    for sourceSlot, sourceStack := range h.inventory.Slots() {\n" +
			"        if !sourceStack.Empty() && sourceStack.Comparable(p.Item) {\n" +
			"            if p.Item.Empty() { p.Item = sourceStack.Grow(-sourceStack.Count() + 1) } else { p.Item = p.Item.Grow(1) }\n" +
			"            ...\n" +
			"```\n\n" +
			"so a hopper can push the pot's stack arbitrarily above its max count. Breaking the pot drops\n" +
			"`p.Item` through `entity.NewItem`, which clamps the stack to its max count, so everything above\n" +
			"64 is silently destroyed.",
		Run: runDecoratedPotOverfill,
	})
}

func runDecoratedPotOverfill() Result {
	h, err := startHarness(harnessOpts{withClient: true, randomTickSpeed: -1})
	if err != nil {
		return Result{Verdict: Blocked, Reason: "could not start harness: " + err.Error()}
	}
	defer h.Stop()

	var o out
	res := Result{
		Setup: "An empty decorated pot at (20, -60, 20) with a hopper directly above it at (20, -59, 20),\n" +
			"facing down into the pot. The hopper is loaded with 128 bricks (two 64 stacks). The real world\n" +
			"ticker then drains the hopper into the pot.",
		ServerSteps: []string{
			"placed the decorated pot and the downward-facing hopper with `tx.SetBlock`",
			"filled hopper slots 0 and 1 with 64 bricks each (128 total)",
			"let the real world ticker call `Hopper.Tick` - no manual ticking, no manual `InsertItem` calls",
			"polled the pot's stack count until the hopper ran dry",
			"broke the pot with `(*Player).BreakBlock` and counted the resulting item entities",
		},
		ClientSteps: []string{
			"a real gophertunnel client stood next to the setup so the chunk stayed loaded and the world " +
				"ticker actually ticked the hopper. It sent no packets for this scenario.",
		},
	}

	potPos := cube.Pos{20, -60, 20}
	hopPos := cube.Pos{20, -59, 20}
	const fed = 128

	if err := h.Do(func(tx *world.Tx, p *player.Player) {
		preparePlayer(tx, p, mgl64.Vec3{20.5, -60, 22.5})
		removeAllItemEntities(tx)
		clearArea(tx, potPos, 3)
		tx.SetBlock(potPos, block.DecoratedPot{}, nil)

		hop := block.NewHopper()
		hop.Facing = cube.FaceDown
		tx.SetBlock(hopPos, hop, nil)
	}); err != nil {
		return blocked(res, "world call failed: "+err.Error())
	}
	if err := h.Do(func(tx *world.Tx, p *player.Player) {
		hop := tx.Block(hopPos).(block.Hopper)
		_ = hop.Inventory(tx, hopPos).SetItem(0, item.NewStack(item.Brick{}, 64))
		_ = hop.Inventory(tx, hopPos).SetItem(1, item.NewStack(item.Brick{}, 64))
	}); err != nil {
		return blocked(res, "world call failed: "+err.Error())
	}
	o.printf("decorated pot at %v, hopper above at %v facing down", potPos, hopPos)
	o.printf("hopper loaded with 128 bricks (2 x 64); brick max stack size is %d",
		item.NewStack(item.Brick{}, 1).MaxCount())
	o.printf("")

	// Wait for the hopper to drain. Each transfer costs 8 ticks of cooldown.
	deadline := time.Now().Add(150 * time.Second)
	lastCount, stable := -1, 0
	potCount, hopperLeft := 0, 0
	for time.Now().Before(deadline) {
		time.Sleep(2 * time.Second)
		if err := h.Do(func(tx *world.Tx, p *player.Player) {
			pot := tx.Block(potPos).(block.DecoratedPot)
			potCount = pot.Item.Count()
			hop := tx.Block(hopPos).(block.Hopper)
			hopperLeft = 0
			for _, s := range hop.Inventory(tx, hopPos).Slots() {
				hopperLeft += s.Count()
			}
		}); err != nil {
			return blocked(res, "world call failed: "+err.Error())
		}
		o.printf("  t=%4.0fs  pot holds %3d bricks, hopper still holds %3d",
			150-time.Until(deadline).Seconds(), potCount, hopperLeft)
		if potCount == lastCount {
			stable++
			if stable >= 2 {
				break
			}
		} else {
			stable = 0
		}
		lastCount = potCount
		if hopperLeft == 0 {
			break
		}
	}

	o.printf("")
	o.printf("pot stack after the hopper drained : %d bricks (max stack size %d)", potCount,
		item.NewStack(item.Brick{}, 1).MaxCount())

	// Break the pot and count what actually drops.
	var dropLines []string
	dropped := 0
	if err := h.Do(func(tx *world.Tx, p *player.Player) {
		removeAllItemEntities(tx)
		p.SetGameMode(world.GameModeSurvival)
		p.SetHeldItems(item.Stack{}, item.Stack{})
		p.BreakBlock(potPos)
	}); err != nil {
		return blocked(res, "world call failed: "+err.Error())
	}
	time.Sleep(300 * time.Millisecond)
	if err := h.Do(func(tx *world.Tx, p *player.Player) {
		counts, lines := groundItems(tx)
		dropped = counts["minecraft:brick"]
		dropLines = lines
	}); err != nil {
		return blocked(res, "world call failed: "+err.Error())
	}
	o.printf("")
	o.printf("after breaking the pot:")
	for _, l := range dropLines {
		o.printf("  %s", l)
	}
	o.printf("bricks fed into the pot   : %d", fed)
	o.printf("bricks dropped when broken: %d", dropped)
	o.printf("bricks destroyed          : %d", fed-dropped)

	res.Observed = o.String()
	res.Expected = fmt.Sprintf(
		"The hopper should stop inserting once the pot holds a full stack (64 bricks), leaving the other\n"+
			"64 in the hopper; breaking the pot should then drop all %d bricks between the pot and the hopper.\n"+
			"Observed instead: the pot accepted %d and only %d came back out.", fed, potCount, dropped)

	switch {
	case potCount > 64 && dropped < potCount:
		res.Verdict = Reproduced
		res.Summary = fmt.Sprintf("pot reached %d bricks (max stack 64), breaking it dropped only %d - %d destroyed",
			potCount, dropped, potCount-dropped)
	case potCount > 64:
		res.Verdict = Reproduced
		res.Summary = fmt.Sprintf("pot reached %d bricks, above the 64 max stack", potCount)
	default:
		res.Verdict = NotReproduced
		res.Summary = fmt.Sprintf("pot only reached %d bricks, dropped %d", potCount, dropped)
	}
	return res
}
