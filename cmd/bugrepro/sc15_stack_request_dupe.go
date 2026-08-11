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
		ID:      "15-item-stack-request-dupe",
		Title:   "ItemStackRequest transfer onto itself duplicates the stack",
		Timeout: 180 * time.Second,
		Bug: "`ItemStackRequestHandler.handleTransfer` reads both endpoints, then writes both:\n\n" +
			"```go\n" +
			"i, _    := h.itemInSlot(from, s, tx)\n" +
			"dest, _ := h.itemInSlot(to, s, tx)\n" +
			"...\n" +
			"h.setItemInSlot(from, i.Grow(-int(count)), s, tx)\n" +
			"h.setItemInSlot(to, dest.Grow(int(count)), s, tx)\n" +
			"```\n\n" +
			"Nothing checks that `from` and `to` are different slots. If they are the same slot, the second\n" +
			"write overwrites the first and the slot ends up with `count + count` items.\n\n" +
			"The same slot can also be addressed under two *different* container IDs, because\n" +
			"`(*Session).invByID` maps several IDs onto the same inventory:\n\n" +
			"```go\n" +
			"case protocol.ContainerCraftingInput, protocol.ContainerCreatedOutput, protocol.ContainerCursor:\n" +
			"    return s.ui, true\n" +
			"```\n\n" +
			"so a request whose source is `ContainerCraftingInput` slot N and whose destination is\n" +
			"`ContainerCursor` slot N looks like a normal move between two containers but hits one slot.",
		Run: runStackRequestDupe,
	})
}

func runStackRequestDupe() Result {
	h, err := startHarness(harnessOpts{withClient: true, randomTickSpeed: -1})
	if err != nil {
		return Result{Verdict: Blocked, Reason: "could not start harness: " + err.Error()}
	}
	defer h.Stop()

	var o out
	res := Result{
		Setup: "Player in survival with an empty inventory. Variant A puts 32 cobblestone in hotbar slot 0\n" +
			"and has the client transfer all 32 from that slot to that same slot. Variant B moves 16 diamonds\n" +
			"into UI slot 28 (the crafting grid), then transfers 16 from `ContainerCraftingInput` slot 28 to\n" +
			"`ContainerCursor` slot 28 - two different container IDs that `invByID` resolves to the same\n" +
			"`s.ui` inventory.",
		ServerSteps: []string{
			"gave the player the starting stacks and read the resulting inventory back",
			"variant B: the UI inventory is unexported, so its final contents were read by having the client " +
				"close the container, which makes the server run `MoveItemsToInventory` into the (empty) player inventory",
		},
		ClientSteps: []string{
			"variant A: a real `packet.ItemStackRequest` with a `PlaceStackRequestAction` whose source and " +
				"destination are both `ContainerCombinedHotBarAndInventory` slot 0",
			"variant B: a real `ItemStackRequest` moving the diamonds into `ContainerCraftingInput` slot 28, " +
				"then a second one from `ContainerCraftingInput` slot 28 to `ContainerCursor` slot 28",
			"variant B: a real `packet.ContainerClose{WindowID: 0}` to flush the UI inventory back into the player inventory",
		},
	}

	// ---- Variant A ----
	const cobbleStart = 32
	if err := h.Do(func(tx *world.Tx, p *player.Player) {
		preparePlayer(tx, p, mgl64.Vec3{140.5, -60, 140.5})
		removeAllItemEntities(tx)
		_ = p.Inventory().SetItem(0, item.NewStack(block.Cobblestone{}, cobbleStart))
	}); err != nil {
		return blocked(res, "world call failed: "+err.Error())
	}
	time.Sleep(600 * time.Millisecond)
	sid, ok := h.StackID(protocol.WindowIDInventory, 0)
	if !ok {
		return blocked(res, "the server never told the client a stack network ID for hotbar slot 0")
	}
	o.printf("variant A: hotbar slot 0 holds %d cobblestone, stack network ID %d", cobbleStart, sid)
	if err := h.Send(&packet.ItemStackRequest{Requests: []protocol.ItemStackRequest{{
		RequestID: 11,
		Actions: []protocol.StackRequestAction{
			placeAction(cobbleStart,
				protocol.ContainerCombinedHotBarAndInventory, 0, sid,
				protocol.ContainerCombinedHotBarAndInventory, 0, sid),
		},
	}}}); err != nil {
		return blocked(res, "sending ItemStackRequest failed: "+err.Error())
	}
	time.Sleep(1 * time.Second)

	var cobbleAfter int
	var invA string
	if err := h.Do(func(tx *world.Tx, p *player.Player) {
		cobbleAfter = invCount(p.Inventory(), "minecraft:cobblestone")
		invA = invSummary(p.Inventory())
	}); err != nil {
		return blocked(res, "world call failed: "+err.Error())
	}
	o.printf("  client sent one PlaceStackRequestAction: %d items, source == destination == hotbar slot 0",
		cobbleStart)
	o.printf("  inventory afterwards: %s", invA)
	o.printf("  cobblestone: %d -> %d   (expected %d)", cobbleStart, cobbleAfter, cobbleStart)
	o.printf("")

	// ---- Variant B ----
	const diamondStart = 16
	if err := h.Do(func(tx *world.Tx, p *player.Player) {
		p.Inventory().Clear()
		_ = p.Inventory().SetItem(0, item.NewStack(item.Diamond{}, diamondStart))
	}); err != nil {
		return blocked(res, "world call failed: "+err.Error())
	}
	time.Sleep(600 * time.Millisecond)
	sid2, ok := h.StackID(protocol.WindowIDInventory, 0)
	if !ok {
		return blocked(res, "the server never told the client a stack network ID for hotbar slot 0 (variant B)")
	}
	if err := h.Send(&packet.ItemStackRequest{Requests: []protocol.ItemStackRequest{{
		RequestID: 12,
		Actions: []protocol.StackRequestAction{
			placeAction(diamondStart,
				protocol.ContainerCombinedHotBarAndInventory, 0, sid2,
				protocol.ContainerCraftingInput, 28, 0),
		},
	}}}); err != nil {
		return blocked(res, "sending ItemStackRequest failed: "+err.Error())
	}
	time.Sleep(1 * time.Second)

	uiID, ok := h.StackID(protocol.WindowIDUI, 28)
	if !ok {
		return blocked(res, "the server never told the client a stack network ID for UI slot 28")
	}
	o.printf("variant B: %d diamonds moved into UI slot 28 (crafting grid), stack network ID %d", diamondStart, uiID)
	if err := h.Send(&packet.ItemStackRequest{Requests: []protocol.ItemStackRequest{{
		RequestID: 13,
		Actions: []protocol.StackRequestAction{
			placeAction(diamondStart,
				protocol.ContainerCraftingInput, 28, uiID,
				protocol.ContainerCursor, 28, uiID),
		},
	}}}); err != nil {
		return blocked(res, "sending ItemStackRequest failed: "+err.Error())
	}
	time.Sleep(1 * time.Second)

	// Flush the UI inventory back into the (empty) player inventory so it can be read.
	if err := h.Send(&packet.ContainerClose{WindowID: 0, ContainerType: 0xff}); err != nil {
		return blocked(res, "sending ContainerClose failed: "+err.Error())
	}
	time.Sleep(1 * time.Second)

	var diamondAfter int
	var invB string
	var dropped int
	if err := h.Do(func(tx *world.Tx, p *player.Player) {
		diamondAfter = invCount(p.Inventory(), "minecraft:diamond")
		invB = invSummary(p.Inventory())
		counts, _ := groundItems(tx)
		dropped = counts["minecraft:diamond"]
	}); err != nil {
		return blocked(res, "world call failed: "+err.Error())
	}
	o.printf("  client sent a PlaceStackRequestAction from ContainerCraftingInput slot 28 to ContainerCursor slot 28")
	o.printf("  (both container IDs resolve to s.ui in (*Session).invByID)")
	o.printf("  inventory after ContainerClose: %s", invB)
	o.printf("  diamonds on the ground        : %d", dropped)
	o.printf("  diamonds: %d -> %d   (expected %d)", diamondStart, diamondAfter+dropped, diamondStart)

	res.Observed = o.String()
	res.Expected = fmt.Sprintf(
		"A transfer of a stack onto itself is a no-op: %d cobblestone should still be %d, and the\n"+
			"%d diamonds should still be %d.", cobbleStart, cobbleStart, diamondStart, diamondStart)

	a := cobbleAfter > cobbleStart
	b := diamondAfter+dropped > diamondStart
	switch {
	case a && b:
		res.Verdict = Reproduced
		res.Summary = fmt.Sprintf("same-slot transfer turned %d cobblestone into %d, and %d diamonds into %d via ContainerCraftingInput->ContainerCursor",
			cobbleStart, cobbleAfter, diamondStart, diamondAfter+dropped)
	case a || b:
		res.Verdict = Reproduced
		res.Summary = fmt.Sprintf("partial: cobblestone %d -> %d, diamonds %d -> %d",
			cobbleStart, cobbleAfter, diamondStart, diamondAfter+dropped)
	default:
		res.Verdict = NotReproduced
		res.Summary = fmt.Sprintf("cobblestone %d -> %d, diamonds %d -> %d",
			cobbleStart, cobbleAfter, diamondStart, diamondAfter+dropped)
	}
	return res
}
