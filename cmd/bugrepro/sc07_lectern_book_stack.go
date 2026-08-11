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
		ID:      "07-lectern-eats-book-stack",
		Title:   "Lectern stores the whole held book stack but only consumes one",
		Timeout: 120 * time.Second,
		Bug: "`Lectern.Activate` puts the *entire held stack* on the lectern and then subtracts a single\n" +
			"item from the player's hand:\n\n" +
			"```go\n" +
			"l.Book, l.Page = held, 0\n" +
			"tx.SetBlock(pos, l, nil)\n" +
			"...\n" +
			"ctx.SubtractFromCount(1)\n" +
			"```\n\n" +
			"With a stack of 16 written books the player keeps 15 and the lectern holds 16. Punching the\n" +
			"book off (`Lectern.Punch` -> `dropItem(tx, l.Book, ...)`) or breaking the lectern\n" +
			"(`BreakInfo` appends `l.Book`) returns all 16.",
		Run: runLecternBookStack,
	})
}

func runLecternBookStack() Result {
	h, err := startHarness(harnessOpts{withClient: true, randomTickSpeed: -1})
	if err != nil {
		return Result{Verdict: Blocked, Reason: "could not start harness: " + err.Error()}
	}
	defer h.Stop()

	var o out
	res := Result{
		Setup: "A lectern at (60, -60, 60). The player holds a full stack of 16 written books (max count 16)\n" +
			"and uses it on the lectern, then punches the lectern to knock the book back off.",
		ServerSteps: []string{
			"placed the lectern with `tx.SetBlock`",
			"gave the player 16 written books and called `(*Player).UseItemOnBlock` on the lectern (the real `Lectern.Activate` path)",
			"called `(*Player).StartBreaking` on the lectern, which is what invokes `Lectern.Punch`",
			"counted books in the player's inventory, on the lectern and on the ground",
		},
		ClientSteps: []string{
			"a real gophertunnel client was connected; it sent no packets for this scenario.",
		},
	}

	pos := cube.Pos{60, -60, 60}
	book := item.WrittenBook{Title: "repro", Author: "harness", Pages: []string{"page"}}
	const held = 16

	if err := h.Do(func(tx *world.Tx, p *player.Player) {
		preparePlayer(tx, p, mgl64.Vec3{60.5, -60, 62.5})
		removeAllItemEntities(tx)
		clearArea(tx, pos, 2)
		tx.SetBlock(pos, block.Lectern{}, nil)
		p.SetHeldItems(item.NewStack(book, held), item.Stack{})
	}); err != nil {
		return blocked(res, "world call failed: "+err.Error())
	}
	o.printf("player holds %d written books (max count %d)", held, item.NewStack(book, 1).MaxCount())

	var heldAfter, onLectern int
	if err := h.Do(func(tx *world.Tx, p *player.Player) {
		p.UseItemOnBlock(pos, cube.FaceUp, mgl64.Vec3{0.5, 1, 0.5})
	}); err != nil {
		return blocked(res, "world call failed: "+err.Error())
	}
	if err := h.Do(func(tx *world.Tx, p *player.Player) {
		hs, _ := p.HeldItems()
		heldAfter = hs.Count()
		l := tx.Block(pos).(block.Lectern)
		onLectern = l.Book.Count()
	}); err != nil {
		return blocked(res, "world call failed: "+err.Error())
	}
	o.printf("after using the stack on the lectern:")
	o.printf("  still held      : %d books", heldAfter)
	o.printf("  on the lectern  : %d books", onLectern)

	var dropped int
	if err := h.Do(func(tx *world.Tx, p *player.Player) {
		p.StartBreaking(pos, cube.FaceUp)
	}); err != nil {
		return blocked(res, "world call failed: "+err.Error())
	}
	time.Sleep(300 * time.Millisecond)
	var lecternAfter int
	if err := h.Do(func(tx *world.Tx, p *player.Player) {
		counts, _ := groundItemsByType(tx)
		dropped = counts["item.WrittenBook"]
		hs, _ := p.HeldItems()
		heldAfter = hs.Count()
		if l, ok := tx.Block(pos).(block.Lectern); ok {
			lecternAfter = l.Book.Count()
		}
	}); err != nil {
		return blocked(res, "world call failed: "+err.Error())
	}
	o.printf("after punching the lectern:")
	o.printf("  dropped on the ground : %d books", dropped)
	o.printf("  still held            : %d books", heldAfter)
	o.printf("  left on the lectern   : %d books", lecternAfter)
	total := dropped + heldAfter + lecternAfter
	o.printf("")
	o.printf("total written books in existence: %d   (expected %d)", total, held)

	res.Observed = o.String()
	res.Expected = fmt.Sprintf(
		"Using a stack of %d books on a lectern should place exactly 1 book on it and leave %d in hand.\n"+
			"Punching it off should give that 1 book back. Total books at the end: %d.", held, held-1, held)

	if total > held {
		res.Verdict = Reproduced
		res.Summary = fmt.Sprintf("%d books total after the round trip (expected %d): lectern took the whole stack of %d while only 1 was consumed",
			total, held, onLectern)
	} else {
		res.Verdict = NotReproduced
		res.Summary = fmt.Sprintf("%d books total (expected %d)", total, held)
	}
	return res
}
