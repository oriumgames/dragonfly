package main

import (
	"fmt"
	"time"

	"github.com/df-mc/dragonfly/server/block"
	"github.com/df-mc/dragonfly/server/item"
	"github.com/df-mc/dragonfly/server/player"
	"github.com/df-mc/dragonfly/server/world"
	"github.com/go-gl/mathgl/mgl64"
	"github.com/sandertv/gophertunnel/minecraft/protocol"
	"github.com/sandertv/gophertunnel/minecraft/protocol/packet"
)

func init() {
	register(Scenario{
		ID:      "01-container-close-dupe",
		Title:   "Container-close duplication (MoveItemsToInventory and addNewItem)",
		Timeout: 180 * time.Second,
		Bug: "`(*Player).MoveItemsToInventory` and `(*Player).addNewItem` both drop leftovers with\n" +
			"`p.Drop(i.Grow(i.Count() - n))`. `Stack.Grow(n)` *adds* n to the count, so when the\n" +
			"inventory is full (n == 0) the dropped stack ends up with `count + count` items instead\n" +
			"of the `count - n` that were actually left over. The leftovers are therefore doubled.\n\n" +
			"```go\n" +
			"// server/player/player.go, MoveItemsToInventory\n" +
			"if n, err := p.inv.AddItem(i); err != nil {\n" +
			"    p.Drop(i.Grow(i.Count() - n))\n" +
			"}\n" +
			"```",
		Run: runContainerCloseDupe,
	})
}

func runContainerCloseDupe() Result {
	h, err := startHarness(harnessOpts{withClient: true, randomTickSpeed: -1})
	if err != nil {
		return Result{Verdict: Blocked, Reason: "could not start harness: " + err.Error()}
	}
	defer h.Stop()

	var o out
	res := Result{
		Setup: "Player in survival with a completely full 36-slot inventory (every slot a 64-stack of cobblestone).\n" +
			"Variant A: 16 diamonds sitting in the 2x2 crafting grid (the UI inventory) when the container is closed.\n" +
			"Variant B: 16 honey bottles held in the hotbar; drinking one hands back a glass bottle while the\n" +
			"inventory has no room for it. A mushroom stew is used as the control: its max count is 1, so the last\n" +
			"one held empties the hand and `addNewItem` takes the `held.Empty()` fast path instead.",
		ServerSteps: []string{
			"teleported the player, cleared its inventory, set survival mode",
			"put the 16 diamonds in hotbar slot 0 before the client moved them",
			"filled all 36 inventory slots with 64 cobblestone each",
			"variant B: `p.UseItem()` twice (start + finish of the drink) to drive the consume",
			"counted item entities in the world and read the inventory back",
		},
		ClientSteps: []string{
			"variant A: real `ItemStackRequest` with a `PlaceStackRequestAction` moving 16 diamonds from " +
				"`ContainerCombinedHotBarAndInventory` slot 0 into `ContainerCraftingInput` slot 28 (the UI inventory)",
			"variant A: real `ContainerClose{WindowID: 0}` packet, which is what makes the server call `MoveItemsToInventory`",
		},
	}

	// ---- Variant A: MoveItemsToInventory ----
	const diamondCount = 16
	err = h.Do(func(tx *world.Tx, p *player.Player) {
		preparePlayer(tx, p, mgl64.Vec3{8.5, -60, 8.5})
		removeAllItemEntities(tx)
		_ = p.Inventory().SetItem(0, item.NewStack(item.Diamond{}, diamondCount))
	})
	if err != nil {
		return blocked(res, "world call failed: "+err.Error())
	}
	// Give the server a moment to push the inventory to the client so we know
	// the stack network ID.
	time.Sleep(500 * time.Millisecond)

	sid, ok := h.StackID(protocol.WindowIDInventory, 0)
	if !ok {
		return blocked(res, "server never sent the client an inventory slot update for hotbar slot 0, "+
			"so the client cannot form a valid ItemStackRequest")
	}
	o.printf("client learned stack network ID %d for inventory slot 0 (16 diamonds)", sid)

	// Client moves the diamonds into the crafting grid (UI inventory slot 28).
	reqErr := h.Send(&packet.ItemStackRequest{Requests: []protocol.ItemStackRequest{{
		RequestID: 1,
		Actions: []protocol.StackRequestAction{
			placeAction(diamondCount,
				protocol.ContainerCombinedHotBarAndInventory, 0, sid,
				protocol.ContainerCraftingInput, 28, 0),
		},
	}}})
	if reqErr != nil {
		return blocked(res, "sending ItemStackRequest failed: "+reqErr.Error())
	}
	time.Sleep(800 * time.Millisecond)

	var invBefore string
	if err := h.Do(func(tx *world.Tx, p *player.Player) {
		invBefore = invSummary(p.Inventory())
		// Now fill the inventory completely so the UI item has nowhere to go.
		fillInventory(p.Inventory(), item.NewStack(block.Cobblestone{}, 64))
	}); err != nil {
		return blocked(res, "world call failed: "+err.Error())
	}
	o.printf("player inventory after the client moved the diamonds into the crafting grid: %s", invBefore)
	o.printf("inventory then filled server-side with 36 x 64 cobblestone (completely full)")

	// Client closes its inventory. This is what triggers MoveItemsToInventory.
	if err := h.Send(&packet.ContainerClose{WindowID: 0, ContainerType: 0xff}); err != nil {
		return blocked(res, "sending ContainerClose failed: "+err.Error())
	}
	time.Sleep(1500 * time.Millisecond)

	var (
		droppedDiamonds int
		dropLines       []string
	)
	if err := h.Do(func(tx *world.Tx, p *player.Player) {
		counts, lines := groundItems(tx)
		droppedDiamonds = counts["minecraft:diamond"]
		dropLines = lines
	}); err != nil {
		return blocked(res, "world call failed: "+err.Error())
	}
	o.printf("")
	o.printf("--- variant A: ContainerClose -> MoveItemsToInventory ---")
	for _, l := range dropLines {
		o.printf("%s", l)
	}
	o.printf("diamonds put into the crafting grid : %d", diamondCount)
	o.printf("diamonds dropped on the ground      : %d   (expected %d)", droppedDiamonds, diamondCount)
	variantA := droppedDiamonds == diamondCount*2

	// ---- Variant B: addNewItem ----
	o.printf("")
	o.printf("--- variant B: addNewItem via a consumable that hands an item back ---")

	honeyDropped, stewDropped, stewBottles := 0, 0, 0
	if err := h.Do(func(tx *world.Tx, p *player.Player) {
		removeAllItemEntities(tx)
		p.Inventory().Clear()
		p.SetFood(6)
		fillInventory(p.Inventory(), item.NewStack(block.Cobblestone{}, 64))
		_ = p.Inventory().SetItem(0, item.NewStack(item.HoneyBottle{}, 16))
		_ = p.SetHeldSlot(0)
	}); err != nil {
		return blocked(res, "world call failed: "+err.Error())
	}
	o.printf("inventory: slot 0 = 16 honey bottles (max count %d), slots 1-35 = 64 cobblestone each (full)",
		item.NewStack(item.HoneyBottle{}, 1).MaxCount())

	if err := h.Do(func(tx *world.Tx, p *player.Player) { p.UseItem() }); err != nil {
		return blocked(res, "world call failed: "+err.Error())
	}
	time.Sleep(2500 * time.Millisecond) // honey bottle ConsumeDuration is 2s
	if err := h.Do(func(tx *world.Tx, p *player.Player) { p.UseItem() }); err != nil {
		return blocked(res, "world call failed: "+err.Error())
	}
	time.Sleep(500 * time.Millisecond)

	var honeyLines []string
	var heldAfter string
	if err := h.Do(func(tx *world.Tx, p *player.Player) {
		counts, lines := groundItems(tx)
		honeyDropped = counts["minecraft:glass_bottle"]
		honeyLines = lines
		held, _ := p.HeldItems()
		heldAfter = fmt.Sprintf("%s x%d", itemName(held), held.Count())
	}); err != nil {
		return blocked(res, "world call failed: "+err.Error())
	}
	o.printf("after drinking one honey bottle, held: %s", heldAfter)
	for _, l := range honeyLines {
		o.printf("%s", l)
	}
	o.printf("glass bottles dropped: %d   (expected 1)", honeyDropped)
	variantB := honeyDropped == 2

	// Control: mushroom stew, max count 1.
	if err := h.Do(func(tx *world.Tx, p *player.Player) {
		removeAllItemEntities(tx)
		p.Inventory().Clear()
		p.SetFood(6)
		fillInventory(p.Inventory(), item.NewStack(block.Cobblestone{}, 64))
		_ = p.Inventory().SetItem(0, item.NewStack(item.MushroomStew{}, 1))
		_ = p.SetHeldSlot(0)
	}); err != nil {
		return blocked(res, "world call failed: "+err.Error())
	}
	if err := h.Do(func(tx *world.Tx, p *player.Player) { p.UseItem() }); err != nil {
		return blocked(res, "world call failed: "+err.Error())
	}
	time.Sleep(2500 * time.Millisecond)
	if err := h.Do(func(tx *world.Tx, p *player.Player) { p.UseItem() }); err != nil {
		return blocked(res, "world call failed: "+err.Error())
	}
	time.Sleep(500 * time.Millisecond)
	if err := h.Do(func(tx *world.Tx, p *player.Player) {
		counts, _ := groundItems(tx)
		stewDropped = counts["minecraft:bowl"]
		held, _ := p.HeldItems()
		stewBottles = held.Count()
	}); err != nil {
		return blocked(res, "world call failed: "+err.Error())
	}
	o.printf("control: 1 mushroom stew (max count %d) consumed with a full inventory",
		item.NewStack(item.MushroomStew{}, 1).MaxCount())
	o.printf("  bowls dropped on the ground: %d   (expected 0, the bowl goes into the now-empty hand)", stewDropped)
	o.printf("  item held after consuming  : count %d", stewBottles)

	res.Observed = o.String()
	res.Expected = fmt.Sprintf(
		"Variant A: closing the inventory with %d diamonds in the crafting grid and a full inventory should drop\n"+
			"exactly %d diamonds. \n\n"+
			"Variant B: drinking one of 16 held honey bottles with a full inventory should drop exactly 1 glass bottle.\n\n"+
			"Control: the mushroom stew path drops nothing because the hand empties and `addNewItem` returns early.",
		diamondCount, diamondCount)

	switch {
	case variantA && variantB:
		res.Verdict = Reproduced
		res.Summary = fmt.Sprintf("dropped %d diamonds (expected %d) and %d glass bottles (expected 1)",
			droppedDiamonds, diamondCount, honeyDropped)
	case variantA || variantB:
		res.Verdict = Reproduced
		res.Summary = fmt.Sprintf("partial: %d diamonds (expected %d), %d glass bottles (expected 1)",
			droppedDiamonds, diamondCount, honeyDropped)
	default:
		res.Verdict = NotReproduced
		res.Summary = fmt.Sprintf("%d diamonds (expected %d), %d glass bottles (expected 1)",
			droppedDiamonds, diamondCount, honeyDropped)
	}
	return res
}

// blocked marks res as BLOCKED with the reason passed.
func blocked(res Result, reason string) Result {
	res.Verdict = Blocked
	res.Reason = reason
	res.Summary = reason
	return res
}
