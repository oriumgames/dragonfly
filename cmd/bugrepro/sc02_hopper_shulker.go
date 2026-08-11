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
		ID:      "02-hopper-shulker-destroys-item",
		Title:   "Hopper feeding a shulker box destroys shulker box items",
		Timeout: 120 * time.Second,
		Bug: "A shulker box inventory installs `canStoreInShulkerBox` as its slot validator, which rejects\n" +
			"nested shulker boxes. `(*Inventory).setItem` silently *no-ops* when the validator rejects the\n" +
			"stack, but `AddItem` still walks its empty-slot loop, sees the stack it wanted to insert as\n" +
			"consumed and returns `(first, nil)` - success. `Hopper.insertItem` only checks the error:\n\n" +
			"```go\n" +
			"_, err := container.Inventory(tx, pos).AddItem(sourceStack.Grow(-sourceStack.Count() + 1))\n" +
			"if err != nil {\n    return false\n}\n" +
			"_ = h.inventory.SetItem(sourceSlot, sourceStack.Grow(-1))\n" +
			"```\n\n" +
			"so it removes the item from the hopper even though the shulker box never stored it. The item\n" +
			"is destroyed.",
		Run: runHopperShulker,
	})
}

func runHopperShulker() Result {
	h, err := startHarness(harnessOpts{withClient: true, randomTickSpeed: -1})
	if err != nil {
		return Result{Verdict: Blocked, Reason: "could not start harness: " + err.Error()}
	}
	defer h.Stop()

	var o out
	res := Result{
		Setup: "A hopper at (10, -60, 10) facing north into an undyed shulker box at (10, -60, 9). One shulker\n" +
			"box item is placed in the hopper's first slot. The world ticker (the real one, 20 tps) then ticks\n" +
			"the hopper, which tries to insert the shulker box item into the shulker box.",
		ServerSteps: []string{
			"placed the hopper and the shulker box with `tx.SetBlock`",
			"put 1 shulker box item into the hopper inventory with `Inventory().SetItem`",
			"let the real world ticker call `Hopper.Tick` (no manual ticking)",
			"read the hopper inventory, the shulker box inventory and all item entities back",
		},
		ClientSteps: []string{
			"a real gophertunnel client was connected and standing next to the blocks - that is what keeps the " +
				"chunk loaded so the world ticker actually ticks the hopper block entity. It sent no packets for this scenario.",
		},
	}

	hopperPos := cube.Pos{10, -60, 10}
	boxPos := cube.Pos{10, -60, 9}

	if err := h.Do(func(tx *world.Tx, p *player.Player) {
		preparePlayer(tx, p, mgl64.Vec3{10.5, -60, 12.5})
		removeAllItemEntities(tx)
		clearArea(tx, hopperPos, 3)

		hop := block.NewHopper()
		hop.Facing = cube.FaceNorth
		tx.SetBlock(hopperPos, hop, nil)

		box := block.NewShulkerBox()
		box.Facing = cube.FaceUp
		tx.SetBlock(boxPos, box, nil)
	}); err != nil {
		return blocked(res, "world call failed: "+err.Error())
	}

	var before string
	if err := h.Do(func(tx *world.Tx, p *player.Player) {
		hop := tx.Block(hopperPos).(block.Hopper)
		_ = hop.Inventory(tx, hopperPos).SetItem(0, item.NewStack(block.NewShulkerBox(), 1))
		before = invSummary(hop.Inventory(tx, hopperPos))
	}); err != nil {
		return blocked(res, "world call failed: "+err.Error())
	}
	o.printf("hopper at %v facing %v, shulker box at %v", hopperPos, cube.FaceNorth, boxPos)
	o.printf("hopper inventory before ticking:")
	o.printf("  %s", before)

	// Let the real world ticker run. The hopper transfer cooldown is 8 ticks.
	time.Sleep(3 * time.Second)

	var (
		hopperAfter, boxAfter string
		hopperItems, boxItems int
		groundLines           []string
		groundTotal           int
	)
	if err := h.Do(func(tx *world.Tx, p *player.Player) {
		hop := tx.Block(hopperPos).(block.Hopper)
		box := tx.Block(boxPos).(block.ShulkerBox)
		hopperAfter = invSummary(hop.Inventory(tx, hopperPos))
		boxAfter = invSummary(box.Inventory(tx, boxPos))
		hopperItems = len(hop.Inventory(tx, hopperPos).Items())
		boxItems = len(box.Inventory(tx, boxPos).Items())
		counts, lines := groundItems(tx)
		groundLines = lines
		for _, c := range counts {
			groundTotal += c
		}
	}); err != nil {
		return blocked(res, "world call failed: "+err.Error())
	}

	o.printf("")
	o.printf("after 3s of real world ticking:")
	o.printf("  hopper inventory      : %s", hopperAfter)
	o.printf("  shulker box inventory : %s", boxAfter)
	if len(groundLines) == 0 {
		o.printf("  item entities in world: none")
	}
	for _, l := range groundLines {
		o.printf("  %s", l)
	}
	o.printf("")
	o.printf("shulker box items alive: hopper=%d  box=%d  ground=%d  TOTAL=%d   (expected 1)",
		hopperItems, boxItems, groundTotal, hopperItems+boxItems+groundTotal)

	total := hopperItems + boxItems + groundTotal
	res.Observed = o.String()
	res.Expected = "The shulker box cannot store another shulker box, so the hopper should refuse the transfer and\n" +
		"keep holding the item: 1 shulker box item still in the hopper, 0 in the box, nothing on the ground.\n" +
		"Total shulker box items in existence: 1."

	if total == 0 {
		res.Verdict = Reproduced
		res.Summary = fmt.Sprintf("shulker box item destroyed: %d items left (expected 1)", total)
	} else {
		res.Verdict = NotReproduced
		res.Summary = fmt.Sprintf("%d shulker box items still exist (expected 1)", total)
	}
	return res
}
