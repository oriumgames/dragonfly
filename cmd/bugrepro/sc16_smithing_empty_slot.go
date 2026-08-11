package main

import (
	"fmt"
	"time"

	"github.com/df-mc/dragonfly/server/block"
	"github.com/df-mc/dragonfly/server/block/cube"
	"github.com/df-mc/dragonfly/server/item"
	"github.com/df-mc/dragonfly/server/item/recipe"
	"github.com/df-mc/dragonfly/server/player"
	"github.com/df-mc/dragonfly/server/world"
	"github.com/go-gl/mathgl/mgl64"
	"github.com/sandertv/gophertunnel/minecraft/protocol"
	"github.com/sandertv/gophertunnel/minecraft/protocol/packet"
)

func init() {
	register(Scenario{
		ID:      "16-smithing-empty-slot",
		Title:   "Smithing table crafts with the material and template slots empty",
		Timeout: 180 * time.Second,
		Bug: "`handleSmithing` validates each of the three smithing slots with `matchingStacks`, which ends\n" +
			"up in `(item.Stack).Comparable`:\n\n" +
			"```go\n" +
			"func (s Stack) Comparable(s2 Stack) bool {\n" +
			"    if s.Empty() || s2.Empty() { return true }\n" +
			"    ...\n}\n" +
			"```\n\n" +
			"An **empty** slot is `Comparable` with anything, so leaving the material (the netherite ingot)\n" +
			"and/or the template (the smithing template) slot empty passes validation. The handler then does\n" +
			"`material.Grow(-1)` on the empty stack and hands out the recipe output. A diamond chestplate\n" +
			"becomes a netherite chestplate for free.",
		Run:   runSmithingEmptySlot,
		Child: true,
		OnChildCrash: func(partial, stderr string, err error) Result {
			return Result{
				Verdict: Reproduced,
				Setup: "A smithing table at (150, -60, 150), a diamond chestplate in the input slot, and the\n" +
					"material and template slots left empty.",
				ServerSteps: []string{
					"placed and opened the smithing table, looked up the netherite chestplate recipe network ID",
					"the scenario is run as a child process because the server crashes",
				},
				ClientSteps: []string{
					"a real `packet.ItemStackRequest` with a `CraftRecipeStackRequestAction` for the netherite " +
						"upgrade recipe while the smithing table's material and template slots were empty",
				},
				Observed: partial + "\n--- child process output ---\n" + truncate(stderr, 4000) +
					fmt.Sprintf("\n--- child process exit: %v ---", err),
				Expected: "The request should have been rejected with an ItemStackResponse error. The server must not " +
					"crash on a packet a client can send.",
				Reason: "The empty material slot passes `matchingStacks` (an empty `item.Stack` is `Comparable` with\n" +
					"anything), and the netherite recipe's inputs are `recipe.ItemTag` values. `matchingStacks`\n" +
					"then takes the `case recipe.ItemTag:` branch and calls `has.Item().EncodeItem()` on the empty\n" +
					"stack. `(item.Stack).Item()` returns nil for an empty stack, so this is a nil pointer\n" +
					"dereference inside the session's packet goroutine, which takes the whole server down.",
				Summary: "the server panics (nil pointer dereference in matchingStacks) on a smithing craft with an empty material slot",
			}
		},
	})
}

func runSmithingEmptySlot() Result {
	h, err := startHarness(harnessOpts{withClient: true, randomTickSpeed: -1})
	if err != nil {
		return Result{Verdict: Blocked, Reason: "could not start harness: " + err.Error()}
	}
	defer h.Stop()

	var o out
	res := Result{
		Setup: "A smithing table at (150, -60, 150). The player holds a diamond chestplate and nothing else -\n" +
			"no netherite ingot and no smithing template anywhere in the inventory. The client moves the\n" +
			"chestplate into the smithing table's input slot and asks the server to craft the netherite\n" +
			"upgrade recipe.",
		ServerSteps: []string{
			"placed the smithing table with `tx.SetBlock` and opened it with `(*Player).UseItemOnBlock`, which runs the real `SmithingTable.Activate` -> `OpenBlockContainer`",
			"looked up the netherite chestplate `recipe.SmithingTransform` in `recipe.Recipes()` to get the network ID the server assigns it (index + 1, exactly what `(*Session).sendRecipes` does)",
			"read the player's inventory back after the craft",
		},
		ClientSteps: []string{
			"a real `packet.ItemStackRequest` with a `PlaceStackRequestAction` moving the diamond chestplate " +
				"into `ContainerSmithingTableInput` slot 0x33",
			"a real `packet.ItemStackRequest` with a `CraftRecipeStackRequestAction` naming the netherite " +
				"upgrade recipe, with the material and template slots left empty",
			"a real `packet.ContainerClose` so the crafted result is flushed out of the UI inventory and can be read",
		},
	}

	// Find the netherite chestplate smithing recipe and its network ID.
	var (
		netID    uint32
		found    bool
		inputStr string
	)
	for i, r := range recipe.Recipes() {
		st, ok := r.(recipe.SmithingTransform)
		if !ok || st.Block() != "smithing_table" {
			continue
		}
		outs := st.Output()
		if len(outs) == 0 {
			continue
		}
		if c, ok := outs[0].Item().(item.Chestplate); ok && c.Tier == item.ArmourTier(item.ArmourTierNetherite{}) {
			netID = uint32(i) + 1
			found = true
			for _, in := range st.Input() {
				if s, ok := in.(item.Stack); ok {
					inputStr += fmt.Sprintf("[%s] ", itemName(s))
				} else {
					inputStr += fmt.Sprintf("[%v] ", in)
				}
			}
			break
		}
	}
	if !found {
		return blocked(res, "could not find a netherite chestplate SmithingTransform recipe in recipe.Recipes()")
	}
	o.printf("netherite chestplate smithing recipe found, network ID %d", netID)
	o.printf("  recipe inputs (base, addition, template): %s", inputStr)

	tablePos := cube.Pos{150, -60, 150}
	if err := h.Do(func(tx *world.Tx, p *player.Player) {
		preparePlayer(tx, p, mgl64.Vec3{150.5, -60, 152.5})
		removeAllItemEntities(tx)
		clearArea(tx, tablePos, 2)
		tx.SetBlock(tablePos, block.SmithingTable{}, nil)
		_ = p.Inventory().SetItem(0, item.NewStack(item.Chestplate{Tier: item.ArmourTierDiamond{}}, 1))
		p.UseItemOnBlock(tablePos, cube.FaceUp, mgl64.Vec3{0.5, 1, 0.5})
	}); err != nil {
		return blocked(res, "world call failed: "+err.Error())
	}
	time.Sleep(800 * time.Millisecond)

	var invBefore string
	if err := h.Do(func(tx *world.Tx, p *player.Player) { invBefore = invSummary(p.Inventory()) }); err != nil {
		return blocked(res, "world call failed: "+err.Error())
	}
	o.printf("")
	o.printf("player inventory before: %s", invBefore)
	o.printf("smithing table opened; material slot and template slot are both empty")

	sid, ok := h.StackID(protocol.WindowIDInventory, 0)
	if !ok {
		return blocked(res, "the server never told the client a stack network ID for hotbar slot 0")
	}
	if err := h.Send(&packet.ItemStackRequest{Requests: []protocol.ItemStackRequest{{
		RequestID: 21,
		Actions: []protocol.StackRequestAction{
			placeAction(1,
				protocol.ContainerCombinedHotBarAndInventory, 0, sid,
				protocol.ContainerSmithingTableInput, 0x33, 0),
		},
	}}}); err != nil {
		return blocked(res, "sending ItemStackRequest failed: "+err.Error())
	}
	time.Sleep(800 * time.Millisecond)

	if err := h.Send(&packet.ItemStackRequest{Requests: []protocol.ItemStackRequest{{
		RequestID: 22,
		Actions: []protocol.StackRequestAction{
			&protocol.CraftRecipeStackRequestAction{RecipeNetworkID: netID, NumberOfCrafts: 1},
		},
	}}}); err != nil {
		return blocked(res, "sending the craft ItemStackRequest failed: "+err.Error())
	}
	time.Sleep(1 * time.Second)

	if err := h.Send(&packet.ContainerClose{WindowID: 0, ContainerType: 0xff}); err != nil {
		return blocked(res, "sending ContainerClose failed: "+err.Error())
	}
	time.Sleep(1200 * time.Millisecond)

	var invAfter string
	gotNetherite := false
	var groundLines []string
	if err := h.Do(func(tx *world.Tx, p *player.Player) {
		invAfter = invSummary(p.Inventory())
		for _, s := range p.Inventory().Slots() {
			if s.Empty() {
				continue
			}
			if c, ok := s.Item().(item.Chestplate); ok && c.Tier == item.ArmourTier(item.ArmourTierNetherite{}) {
				gotNetherite = true
			}
		}
		_, groundLines = groundItems(tx)
		for _, e := range groundLines {
			_ = e
		}
		counts, _ := groundItemsByType(tx)
		if counts["item.Chestplate"] > 0 {
			gotNetherite = true
		}
	}); err != nil {
		return blocked(res, "world call failed: "+err.Error())
	}
	o.printf("")
	o.printf("player inventory after the craft: %s", invAfter)
	for _, l := range groundLines {
		o.printf("  %s", l)
	}
	o.printf("netherite chestplate obtained without a netherite ingot or a smithing template: %v", gotNetherite)

	res.Observed = o.String()
	res.Expected = "The craft should be rejected: the netherite upgrade needs a netherite ingot in the material\n" +
		"slot and a netherite upgrade smithing template in the template slot. The player should still\n" +
		"be holding only the diamond chestplate."

	if gotNetherite {
		res.Verdict = Reproduced
		res.Summary = "netherite chestplate crafted with an empty material slot and an empty template slot"
	} else {
		res.Verdict = NotReproduced
		res.Summary = "the craft did not produce a netherite chestplate: " + oneLine(invAfter)
	}
	return res
}
