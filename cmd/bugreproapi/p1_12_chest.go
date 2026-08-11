package main

import (
	"fmt"
	"time"

	"github.com/df-mc/dragonfly/server/block"
	"github.com/df-mc/dragonfly/server/block/cube"
	"github.com/df-mc/dragonfly/server/entity"
	"github.com/df-mc/dragonfly/server/item"
	"github.com/df-mc/dragonfly/server/item/inventory"
	"github.com/df-mc/dragonfly/server/player"
	"github.com/df-mc/dragonfly/server/world"
	"github.com/go-gl/mathgl/mgl64"
	"github.com/google/uuid"
)

// p1_12: pairing a chest replaces the inventory an already open window holds.
func scenarioChestPairing() *Scenario {
	return &Scenario{
		ID:    "p1-12-chest-pairing-window",
		Part:  1,
		Title: "Chest pairing replaces the inventory an open window is holding",
		Claim: "Chest pairing replaces the inventory an open window is holding.",
		Setup: "A real chest is placed and a real `*session.Session` opens it with `Session.OpenBlockContainer`, which stores the block's `*inventory.Inventory` pointer in `openedWindow`.\n" +
			"A second real `*player.Player` then places a second chest next to it with the real `Chest.UseOnBlock`, which calls `Chest.pair`. `pair` builds brand new `Clone`d inventories for both halves, a new merged inventory and a fresh viewer map, and writes both blocks back with `Tx.SetBlock`.\n" +
			"The pointer the session is still holding is then compared with the live block's inventory, and an item is written through the old pointer.",
		Expected: "Pairing keeps the open window pointing at an inventory that is still attached to the block, so writes through it reach the chest.",
		Timeout:  120 * time.Second,
		Run:      runChestPairing,
	}
}

func runChestPairing(o *Out) {
	w := world.Config{
		Log: discardLogger(), Entities: entity.DefaultRegistry,
		SaveInterval: -1, ChunkUnloadInterval: time.Hour,
	}.New()
	defer w.Close()

	sp, err := spawnSessionPlayer(o, w, "Opener", mgl64.Vec3{8, 65, 8})
	if err != nil {
		o.Verdict(Blocked, "spawn session player: %v", err)
		return
	}
	time.Sleep(1200 * time.Millisecond)

	chestPos := cube.Pos{8, 64, 8}
	basePos := cube.Pos{9, 63, 8} // the second chest is placed on top of this
	stone, _ := world.DefaultBlockRegistry.BlockByName("minecraft:stone", map[string]any{})

	// Find out which way a chest placed by this player ends up facing, so the
	// two chests can actually pair (Chest.pair requires equal Facing).
	id := uuid.New()
	ph := world.EntitySpawnOpts{Position: mgl64.Vec3{9.5, 64, 9.5}, ID: id}.New(player.Type, player.Config{UUID: id, Name: "Placer", Position: mgl64.Vec3{9.5, 64, 9.5}})
	var facing cube.Direction
	_ = call(w, 20*time.Second, func(tx *world.Tx) {
		p := tx.AddEntity(ph).(*player.Player)
		facing = p.Rotation().Direction().Opposite()
	})
	o.Logf("a chest placed by this player faces %v", facing)

	var opened *inventory.Inventory
	err = call(w, 20*time.Second, func(tx *world.Tx) {
		tx.SetBlock(basePos, stone, nil)
		c := block.NewChest()
		c.Facing = facing
		tx.SetBlock(chestPos, c, nil)
		// This is what player.Player.OpenBlockContainer does.
		sp.s.OpenBlockContainer(chestPos, tx)
		opened = tx.Block(chestPos).(block.Chest).Inventory(tx, chestPos)
		_ = opened.SetItem(0, item.NewStack(item.Diamond{}, 5))
	})
	if err != nil {
		o.Verdict(Blocked, "opening the chest: %v", err)
		return
	}
	o.Logf("chest placed at %v and opened by a real session; its inventory is %p with %d slots", chestPos, opened, opened.Size())
	o.Logf("put 5 diamonds in slot 0 of the open window")

	// The second player places an adjacent chest, pairing them.
	var live *inventory.Inventory
	var placedOK bool
	err = call(w, 20*time.Second, func(tx *world.Tx) {
		e, _ := ph.Entity(tx)
		p := e.(*player.Player)
		c := block.NewChest()
		placedOK = c.UseOnBlock(basePos, cube.FaceUp, mgl64.Vec3{}, tx, p, &item.UseContext{})
		live = tx.Block(chestPos).(block.Chest).Inventory(tx, chestPos)
	})
	if err != nil {
		o.Verdict(Blocked, "placing the second chest: %v", err)
		return
	}
	o.Logf("second chest placed by a real player: UseOnBlock returned %v", placedOK)
	pairedNow, _ := callVal(w, 10*time.Second, func(tx *world.Tx) bool {
		c, ok := tx.Block(chestPos).(block.Chest)
		return ok && c.Inventory(tx, chestPos).Size() == 54
	})
	o.Logf("the original chest is now half of a double chest: %v", pairedNow)
	o.Logf("live inventory of the original chest after pairing: %p with %d slots", live, live.Size())
	o.Logf("inventory the open window still holds:              %p with %d slots", opened, opened.Size())
	o.Logf("expected: the open window and the live block agree")

	// Write through the pointer the session is still holding.
	err = call(w, 20*time.Second, func(tx *world.Tx) {
		_ = opened.SetItem(1, item.NewStack(item.GoldIngot{}, 7))
	})
	if err != nil {
		o.Logf("write through the stale window: %v", err)
	}
	seen, _ := callVal(w, 20*time.Second, func(tx *world.Tx) string {
		l := tx.Block(chestPos).(block.Chest).Inventory(tx, chestPos)
		var b []string
		for i := 0; i < l.Size(); i++ {
			s, _ := l.Item(i)
			if !s.Empty() {
				b = append(b, fmt.Sprintf("slot %d: %d x %T", i, s.Count(), s.Item()))
			}
		}
		if len(b) == 0 {
			return "(empty)"
		}
		return fmt.Sprint(b)
	})
	o.Logf("contents of the live paired chest after that write: %s", seen)
	o.Logf("contents of the window the session still holds: slot 0 = %s, slot 1 = %s", slotDesc(opened, 0), slotDesc(opened, 1))

	if live != opened {
		o.Verdict(Reproduced, "pairing replaced the block's inventory: the session's open window still points at %p while the block now uses %p, so writes through the window never reach the chest", opened, live)
		return
	}
	o.Verdict(Refuted, "the open window and the live block share the same inventory after pairing")
}

func slotDesc(inv *inventory.Inventory, slot int) string {
	if inv == nil || slot >= inv.Size() {
		return "(n/a)"
	}
	s, err := inv.Item(slot)
	if err != nil {
		return "(error: " + err.Error() + ")"
	}
	if s.Empty() {
		return "(empty)"
	}
	return fmt.Sprintf("%d x %T", s.Count(), s.Item())
}
