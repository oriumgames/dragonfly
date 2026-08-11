package main

import (
	"fmt"
	"strings"
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
	"github.com/sandertv/gophertunnel/minecraft/protocol"
	"github.com/sandertv/gophertunnel/minecraft/protocol/packet"
)

// p2_11 settles the detached-inventory question: a shulker box and one half of
// a double chest are broken while a real Session has them open, and the
// session is then asked, over the real packet path, to move an item out of the
// container ID that invByID resolves without re-checking the block.
func scenarioDetachedInventory() *Scenario {
	return &Scenario{
		ID:    "p2-11-detached-inventory",
		Part:  2,
		Title: "Shulker box / double chest broken while open leaves a detached inventory a Session can still reach",
		Claim: "Shulker box broken while open and one half of a double chest broken while open both leave a detached inventory still holding the contents. Determine whether a real `Session` can still reach that inventory, given `invByID` returns `s.openedWindow` for `ContainerLevelEntity`.",
		Setup: "A real `*session.Session` opens a real shulker box (and, in the second half, a real double chest) with `Session.OpenBlockContainer`, which stores the block's `*inventory.Inventory` in `openedWindow` and leaves `containerOpened` true.\n" +
			"A real `*player.Player` then breaks the block with `Player.BreakBlock`. Nothing in `BreakInfo`/`withBreakHandler` calls `RemoveViewer` or closes the window, and `Session.closeCurrentContainer` looks the block up at `openedPos`, which is now air, so neither container branch matches.\n" +
			"A real `packet.ItemStackRequest` is then fed through the session's own `ReadPacket` loop, addressing `protocol.ContainerLevelEntity` - the one `invByID` branch with no block re-validation. The session's debug log distinguishes the two outcomes: `could not find container with id 7` (unreachable) versus `stack ID mismatch: client expected 0, but server had N` (reachable, and the server read the real item out of the detached inventory).",
		Expected: "Breaking the block detaches its inventory from the session too: the window is closed and the container ID no longer resolves.",
		Timeout:  180 * time.Second,
		Run:      runDetachedInventory,
	}
}

func runDetachedInventory(o *Out) {
	stone, _ := world.DefaultBlockRegistry.BlockByName("minecraft:stone", map[string]any{})
	reproduced := 0

	// ------------------------------------------------------------------
	o.Section("A. shulker box broken while open")
	{
		w := world.Config{
			Log: discardLogger(), Entities: entity.DefaultRegistry,
			SaveInterval: -1, ChunkUnloadInterval: time.Hour,
		}.New()

		sp, err := spawnSessionPlayer(o, w, "ShulkerOpener", mgl64.Vec3{8, 64, 9})
		if err != nil {
			o.Verdict(Blocked, "spawn session player: %v", err)
			return
		}
		time.Sleep(1200 * time.Millisecond)

		pos := cube.Pos{8, 64, 8}
		var opened *inventory.Inventory
		_ = call(w, 20*time.Second, func(tx *world.Tx) {
			for x := 0; x < 16; x++ {
				for z := 0; z < 16; z++ {
					tx.SetBlock(cube.Pos{x, 63, z}, stone, nil)
				}
			}
			sb := block.NewShulkerBox()
			tx.SetBlock(pos, sb, nil)
			sp.s.OpenBlockContainer(pos, tx)
			opened = tx.Block(pos).(block.ShulkerBox).Inventory(tx, pos)
			_ = opened.SetItem(0, item.NewStack(item.Diamond{}, 12))
		})
		o.Logf("shulker box at %v opened by a real session; its inventory is %p, slot 0 holds %s", pos, opened, slotDesc(opened, 0))

		_ = sp.player(w, 20*time.Second, func(tx *world.Tx, p *player.Player) {
			p.BreakBlock(pos)
		})
		time.Sleep(300 * time.Millisecond)

		blockNow, _ := callVal(w, 20*time.Second, func(tx *world.Tx) string {
			return fmt.Sprintf("%T", tx.Block(pos))
		})
		o.Logf("after Player.BreakBlock, the block at %v is %s", pos, blockNow)
		o.Logf("the inventory the session still holds (%p) still contains: slot 0 = %s", opened, slotDesc(opened, 0))

		dropped, _ := callVal(w, 20*time.Second, func(tx *world.Tx) string {
			var out []string
			for e := range tx.Entities() {
				ent, ok := e.(*entity.Ent)
				if !ok {
					continue
				}
				ib, ok := ent.Behaviour().(*entity.ItemBehaviour)
				if !ok {
					continue
				}
				st := ib.Item()
				desc := fmt.Sprintf("%d x %T", st.Count(), st.Item())
				if sb, ok := st.Item().(block.ShulkerBox); ok {
					inv := sb.Inventory(tx, pos)
					desc += fmt.Sprintf(" (its inventory is %p, same object as the session's window: %v, slot 0 = %s)",
						inv, inv == opened, slotDesc(inv, 0))
				}
				out = append(out, desc)
			}
			return strings.Join(out, "; ")
		})
		o.Logf("item entities dropped by the break: %s", dropped)

		lines := probeContainer(o, sp, "shulker")
		if containerReachable(lines) {
			reproduced++
			o.Logf("A: the session could still address the detached inventory through ContainerLevelEntity")
		} else {
			o.Logf("A: the session could not address the detached inventory")
		}
		go w.Close()
	}

	// ------------------------------------------------------------------
	o.Section("B. one half of a double chest broken while open")
	{
		w := world.Config{
			Log: discardLogger(), Entities: entity.DefaultRegistry,
			SaveInterval: -1, ChunkUnloadInterval: time.Hour,
		}.New()

		sp, err := spawnSessionPlayer(o, w, "ChestOpener", mgl64.Vec3{8, 64, 10})
		if err != nil {
			o.Verdict(Blocked, "spawn session player: %v", err)
			return
		}
		time.Sleep(1200 * time.Millisecond)

		leftPos, rightPos := cube.Pos{8, 64, 8}, cube.Pos{9, 64, 8}
		id := uuid.New()
		ph := world.EntitySpawnOpts{Position: mgl64.Vec3{9.5, 64, 9.5}, ID: id}.
			New(player.Type, player.Config{UUID: id, Name: "Breaker", Position: mgl64.Vec3{9.5, 64, 9.5}})

		var facing cube.Direction
		_ = call(w, 20*time.Second, func(tx *world.Tx) {
			p := tx.AddEntity(ph).(*player.Player)
			facing = p.Rotation().Direction().Opposite()
		})

		var opened *inventory.Inventory
		_ = call(w, 20*time.Second, func(tx *world.Tx) {
			for x := 0; x < 16; x++ {
				for z := 0; z < 16; z++ {
					tx.SetBlock(cube.Pos{x, 63, z}, stone, nil)
				}
			}
			l, r := block.NewChest(), block.NewChest()
			l.Facing, r.Facing = facing, facing
			tx.SetBlock(leftPos, l, nil)
			e, _ := ph.Entity(tx)
			c := block.NewChest()
			c.UseOnBlock(rightPos, cube.FaceUp, mgl64.Vec3{}, tx, e.(*player.Player), &item.UseContext{})
			sp.s.OpenBlockContainer(leftPos, tx)
			opened = tx.Block(leftPos).(block.Chest).Inventory(tx, leftPos)
			_ = opened.SetItem(0, item.NewStack(item.Diamond{}, 3))
			_ = opened.SetItem(30, item.NewStack(item.GoldIngot{}, 4))
		})
		o.Logf("double chest opened by a real session; window %p has %d slots, slot 0 = %s, slot 30 = %s",
			opened, opened.Size(), slotDesc(opened, 0), slotDesc(opened, 30))
		if opened.Size() != 54 {
			o.Logf("B: the two chests did not pair, so this half of the scenario did not run")
		} else {
			_ = call(w, 20*time.Second, func(tx *world.Tx) {
				e, ok := ph.Entity(tx)
				if !ok {
					return
				}
				e.(*player.Player).BreakBlock(rightPos)
			})
			time.Sleep(300 * time.Millisecond)

			surviving, _ := callVal(w, 20*time.Second, func(tx *world.Tx) string {
				c, ok := tx.Block(leftPos).(block.Chest)
				if !ok {
					return fmt.Sprintf("the surviving block is %T", tx.Block(leftPos))
				}
				inv := c.Inventory(tx, leftPos)
				return fmt.Sprintf("%p with %d slots (same object as the session's window: %v), slot 0 = %s",
					inv, inv.Size(), inv == opened, slotDesc(inv, 0))
			})
			o.Logf("after breaking the right half, the surviving chest's inventory is %s", surviving)
			o.Logf("the merged inventory the session still holds (%p) still contains slot 0 = %s, slot 30 = %s",
				opened, slotDesc(opened, 0), slotDesc(opened, 30))

			_ = call(w, 20*time.Second, func(tx *world.Tx) {
				_ = opened.SetItem(1, item.NewStack(item.IronIngot{}, 9))
			})
			after, _ := callVal(w, 20*time.Second, func(tx *world.Tx) string {
				c, _ := tx.Block(leftPos).(block.Chest)
				return slotDesc(c.Inventory(tx, leftPos), 1)
			})
			o.Logf("wrote 9 iron ingots into slot 1 of the detached window; the surviving chest's slot 1 is now %s", after)

			lines := probeContainer(o, sp, "double chest")
			if containerReachable(lines) {
				reproduced++
				o.Logf("B: the session could still address the detached merged inventory through ContainerLevelEntity")
			} else {
				o.Logf("B: the session could not address the detached inventory")
			}
		}
		go w.Close()
	}

	o.Logf("expected: neither container is reachable after the block is gone")
	switch reproduced {
	case 0:
		o.Verdict(Refuted, "the detached inventories were not reachable from the Session after the block was broken")
	case 2:
		o.Verdict(Reproduced, "both detached inventories stayed reachable through protocol.ContainerLevelEntity: invByID returns s.openedWindow for that ID with no block re-validation, and nothing sets containerOpened false when a block is broken")
	default:
		o.Verdict(Reproduced, "%d of the 2 detached inventories stayed reachable through protocol.ContainerLevelEntity", reproduced)
	}
}

// probeContainer feeds a real ItemStackRequest addressing ContainerLevelEntity
// through the Session's own packet loop and returns the debug lines it logged.
func probeContainer(o *Out, sp *sessionPlayer, label string) []string {
	before := o.Text()
	take := &protocol.TakeStackRequestAction{}
	take.Count = 1
	take.Source = protocol.StackRequestSlotInfo{
		Container:      protocol.FullContainerName{ContainerID: protocol.ContainerLevelEntity},
		Slot:           0,
		StackNetworkID: 0,
	}
	take.Destination = protocol.StackRequestSlotInfo{
		Container:      protocol.FullContainerName{ContainerID: protocol.ContainerLevelEntity},
		Slot:           1,
		StackNetworkID: 0,
	}
	pk := &packet.ItemStackRequest{Requests: []protocol.ItemStackRequest{{
		RequestID: 1,
		Actions:   []protocol.StackRequestAction{take},
	}}}
	o.Logf("sending a real ItemStackRequest for %s: take 1 from ContainerLevelEntity slot 0 to slot 1", label)
	sp.conn.send(pk)
	time.Sleep(1200 * time.Millisecond)
	added := strings.TrimPrefix(o.Text(), before)
	return splitLines(added)
}

// containerReachable reads the session's own debug output. "could not find
// container" means invByID rejected the ID; a stack ID mismatch or an item
// count complaint means it resolved the container and read the slot.
func containerReachable(lines []string) bool {
	joined := strings.Join(lines, "\n")
	switch {
	case strings.Contains(joined, "could not find container"):
		return false
	case strings.Contains(joined, "unable to find container"):
		return false
	case strings.Contains(joined, "stack ID mismatch"),
		strings.Contains(joined, "tried subtracting"),
		strings.Contains(joined, "incomparable"),
		strings.Contains(joined, "slot out of sync"):
		return true
	}
	return false
}
