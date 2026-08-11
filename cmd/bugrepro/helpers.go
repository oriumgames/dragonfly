package main

import (
	"fmt"
	"sort"
	"strings"

	"github.com/df-mc/dragonfly/server/block/cube"
	"github.com/df-mc/dragonfly/server/entity"
	"github.com/df-mc/dragonfly/server/item"
	"github.com/df-mc/dragonfly/server/item/inventory"
	"github.com/df-mc/dragonfly/server/player"
	"github.com/df-mc/dragonfly/server/world"
	"github.com/go-gl/mathgl/mgl64"
	"github.com/sandertv/gophertunnel/minecraft/protocol"
)

// placeAction builds a PlaceStackRequestAction. The embedded transfer struct in
// gophertunnel is unexported, so the fields are set through promotion.
func placeAction(count byte, srcContainer byte, srcSlot byte, srcID int32, dstContainer byte, dstSlot byte, dstID int32) *protocol.PlaceStackRequestAction {
	a := &protocol.PlaceStackRequestAction{}
	a.Count = count
	a.Source = protocol.StackRequestSlotInfo{
		Container:      protocol.FullContainerName{ContainerID: srcContainer},
		Slot:           srcSlot,
		StackNetworkID: srcID,
	}
	a.Destination = protocol.StackRequestSlotInfo{
		Container:      protocol.FullContainerName{ContainerID: dstContainer},
		Slot:           dstSlot,
		StackNetworkID: dstID,
	}
	return a
}

// itemName returns a readable name for a stack's item type.
func itemName(s item.Stack) string {
	if s.Empty() {
		return "<empty>"
	}
	n, meta := s.Item().EncodeItem()
	if meta != 0 {
		return fmt.Sprintf("%s:%d", n, meta)
	}
	return n
}

// groundItems returns a name -> total count map of all item entities currently
// in the world, plus a readable listing.
func groundItems(tx *world.Tx) (map[string]int, []string) {
	counts := map[string]int{}
	var lines []string
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
		counts[itemName(s)] += s.Count()
		lines = append(lines, fmt.Sprintf("item entity: %s x%d at %.1f,%.1f,%.1f",
			itemName(s), s.Count(), ent.Position()[0], ent.Position()[1], ent.Position()[2]))
	}
	sort.Strings(lines)
	return counts, lines
}

// groundItemsByType returns a Go-type -> total count map of all item entities
// in the world. Useful when several blocks share an encoded item name.
func groundItemsByType(tx *world.Tx) (map[string]int, []string) {
	counts := map[string]int{}
	var lines []string
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
		key := fmt.Sprintf("%T", s.Item())
		counts[key] += s.Count()
		lines = append(lines, fmt.Sprintf("item entity: %s (%s) x%d", key, itemName(s), s.Count()))
	}
	sort.Strings(lines)
	return counts, lines
}

// removeAllItemEntities despawns every item entity in the world so counts start
// from a clean slate.
func removeAllItemEntities(tx *world.Tx) {
	var rm []world.Entity
	for e := range tx.Entities() {
		ent, ok := e.(*entity.Ent)
		if !ok {
			continue
		}
		if _, ok := ent.Behaviour().(*entity.ItemBehaviour); ok {
			rm = append(rm, e)
		}
	}
	for _, e := range rm {
		_ = tx.RemoveEntity(e).Close()
	}
}

// fillInventory fills every slot of inv with a full stack of it.
func fillInventory(inv *inventory.Inventory, it item.Stack) {
	for i := range inv.Size() {
		_ = inv.SetItem(i, it)
	}
}

// invSummary returns a readable dump of the non-empty slots of inv.
func invSummary(inv *inventory.Inventory) string {
	var parts []string
	for i, s := range inv.Slots() {
		if s.Empty() {
			continue
		}
		parts = append(parts, fmt.Sprintf("slot %d: %s x%d", i, itemName(s), s.Count()))
	}
	if len(parts) == 0 {
		return "(empty)"
	}
	return strings.Join(parts, "\n")
}

// invCount counts the total number of items of the given type in inv.
func invCount(inv *inventory.Inventory, name string) int {
	n := 0
	for _, s := range inv.Slots() {
		if !s.Empty() && itemName(s) == name {
			n += s.Count()
		}
	}
	return n
}

// clearArea replaces every block in a cube around pos with air.
func clearArea(tx *world.Tx, pos cube.Pos, r int) {
	for x := -r; x <= r; x++ {
		for y := -r; y <= r; y++ {
			for z := -r; z <= r; z++ {
				tx.SetBlock(pos.Add(cube.Pos{x, y, z}), nil, nil)
			}
		}
	}
}

// preparePlayer moves p to pos, clears its inventory and puts it in survival.
func preparePlayer(tx *world.Tx, p *player.Player, pos mgl64.Vec3) {
	p.SetGameMode(world.GameModeSurvival)
	p.Inventory().Clear()
	p.Armour().Clear()
	p.SetHeldItems(item.Stack{}, item.Stack{})
	p.Teleport(pos)
	p.ResetFallDistance()
}

// truncate shortens s to at most n bytes, adding an ellipsis note if it was cut.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "\n... (truncated)"
}
