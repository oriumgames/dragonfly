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
		ID:      "04-chest-dupe-via-save",
		Title:   "Chest contents come back after a save and a chunk unload/reload",
		Timeout: 180 * time.Second,
		Bug: "`(*World).saveChunk` only writes a column back to the provider when `c.modified` is set:\n\n" +
			"```go\n" +
			"func (w *World) saveChunk(_ *Tx, pos ChunkPos, c *Column) {\n" +
			"    if !w.conf.ReadOnly && c.modified {\n        c.Compact()\n        ... StoreColumn ...\n    }\n}\n" +
			"```\n\n" +
			"`modified` is set by `setBlock`, `setBlockEntity`, `setBiome` and by adding/removing entities.\n" +
			"Mutating a block entity's *inventory* - which is what every chest, barrel, hopper and shulker\n" +
			"box interaction does - goes straight through the `*inventory.Inventory` pointer and never\n" +
			"touches the column. So for a chunk that was loaded from disk and only had chest contents\n" +
			"changed, `modified` stays false, the unload silently skips the write, and the stale on-disk\n" +
			"copy (still holding the items) is what comes back on the next load. The items exist twice.",
		Run: runChestSaveDupe,
	})
}

func runChestSaveDupe() Result {
	h, err := startHarness(harnessOpts{
		withClient:          true,
		saveWorld:           true,
		randomTickSpeed:     -1,
		chunkUnloadInterval: time.Second,
	})
	if err != nil {
		return Result{Verdict: Blocked, Reason: "could not start harness: " + err.Error()}
	}
	defer h.Stop()

	var o out
	res := Result{
		Setup: "A real on-disk leveldb (mcdb) world provider, chunk unload interval 1s. A chest at\n" +
			"(300, -60, 300) - far outside the player's view distance when the player is at spawn.\n" +
			"Phase 1 places the chest, fills it with 64 diamonds and lets the chunk save and unload, so the\n" +
			"on-disk copy holds the diamonds. Phase 2 loads the chunk back (its `modified` flag is now false),\n" +
			"empties the chest into the player's inventory and lets the chunk unload again. Phase 3 reads the\n" +
			"chest back from disk.",
		ServerSteps: []string{
			"placed the chest and filled slot 0 with 64 diamonds via `Chest.Inventory().SetItem`",
			"called `(*World).Save()` and moved the player away so `closeUnusedChunks` unloaded the chunk (phase 1)",
			"teleported the player back within view distance so the chunk was read back from leveldb (phase 2)",
			"moved the diamonds out of the chest into the player inventory, purely through the chest's `*inventory.Inventory`",
			"moved the player away again, called `(*World).Save()` and waited for the unload",
			"read the chest back and re-read the player inventory (phase 3)",
			"used `tx.BlockLoaded` to confirm the chunk really was unloaded between phases",
		},
		ClientSteps: []string{
			"a real gophertunnel client was connected and is what owns the world loader that keeps chunks " +
				"loaded and lets them unload again - without it, nothing would ever unload. It sent no item " +
				"packets: the take was driven server-side through the chest's own inventory, which is exactly " +
				"what the session's ItemStackRequest handler does too (`s.openedWindow` *is* `chest.Inventory(...)`).",
		},
	}

	chestPos := cube.Pos{300, -60, 300}
	// The player stands in a neighbouring chunk so that adding/removing the
	// player entity never marks the chest's own column as modified.
	nearChest := mgl64.Vec3{280.5, -60, 300.5}
	farAway := mgl64.Vec3{0.5, -60, 0.5}
	const diamonds = 64

	waitUnloaded := func(label string) (bool, error) {
		deadline := time.Now().Add(30 * time.Second)
		for time.Now().Before(deadline) {
			var loaded bool
			if err := h.InWorld(func(tx *world.Tx) {
				_, loaded = tx.BlockLoaded(chestPos)
			}); err != nil {
				return false, err
			}
			if !loaded {
				o.printf("%s: chunk containing %v is unloaded (tx.BlockLoaded reports false)", label, chestPos)
				return true, nil
			}
			time.Sleep(500 * time.Millisecond)
		}
		o.printf("%s: chunk containing %v never unloaded within 30s", label, chestPos)
		return false, nil
	}

	// ---- phase 1: create the chest with items and get it onto disk ----
	if err := h.Do(func(tx *world.Tx, p *player.Player) {
		preparePlayer(tx, p, nearChest)
		removeAllItemEntities(tx)
		clearArea(tx, chestPos, 2)
		ch := block.NewChest()
		ch.Facing = cube.South
		tx.SetBlock(chestPos, ch, nil)
	}); err != nil {
		return blocked(res, "world call failed: "+err.Error())
	}
	time.Sleep(2 * time.Second)
	if err := h.Do(func(tx *world.Tx, p *player.Player) {
		ch := tx.Block(chestPos).(block.Chest)
		_ = ch.Inventory(tx, chestPos).SetItem(0, item.NewStack(item.Diamond{}, diamonds))
	}); err != nil {
		return blocked(res, "world call failed: "+err.Error())
	}
	var chestBefore, invBefore string
	if err := h.Do(func(tx *world.Tx, p *player.Player) {
		ch := tx.Block(chestPos).(block.Chest)
		chestBefore = invSummary(ch.Inventory(tx, chestPos))
		invBefore = invSummary(p.Inventory())
	}); err != nil {
		return blocked(res, "world call failed: "+err.Error())
	}
	o.printf("phase 1: chest placed at %v and filled", chestPos)
	o.printf("  chest    : %s", chestBefore)
	o.printf("  player   : %s", invBefore)

	h.World.Save()
	if err := h.Do(func(tx *world.Tx, p *player.Player) { p.Teleport(farAway) }); err != nil {
		return blocked(res, "world call failed: "+err.Error())
	}
	if ok, err := waitUnloaded("phase 1"); err != nil {
		return blocked(res, "world call failed: "+err.Error())
	} else if !ok {
		return blocked(res, "the chunk holding the chest never unloaded, so the save/reload cycle could not be exercised")
	}

	// ---- phase 2: load the chunk back and empty the chest ----
	if err := h.Do(func(tx *world.Tx, p *player.Player) { p.Teleport(nearChest) }); err != nil {
		return blocked(res, "world call failed: "+err.Error())
	}
	time.Sleep(2 * time.Second)

	var chestReloadedFull string
	if err := h.Do(func(tx *world.Tx, p *player.Player) {
		ch := tx.Block(chestPos).(block.Chest)
		chestReloadedFull = invSummary(ch.Inventory(tx, chestPos))
	}); err != nil {
		return blocked(res, "world call failed: "+err.Error())
	}
	o.printf("")
	o.printf("phase 2: chunk read back from leveldb, chest contents survived the round trip:")
	o.printf("  chest    : %s", chestReloadedFull)

	if err := h.Do(func(tx *world.Tx, p *player.Player) {
		ch := tx.Block(chestPos).(block.Chest)
		inv := ch.Inventory(tx, chestPos)
		for slot, s := range inv.Slots() {
			if s.Empty() {
				continue
			}
			_, _ = p.Inventory().AddItem(s)
			_ = inv.SetItem(slot, item.Stack{})
		}
	}); err != nil {
		return blocked(res, "world call failed: "+err.Error())
	}

	var chestAfterTake, invAfterTake string
	if err := h.Do(func(tx *world.Tx, p *player.Player) {
		ch := tx.Block(chestPos).(block.Chest)
		chestAfterTake = invSummary(ch.Inventory(tx, chestPos))
		invAfterTake = invSummary(p.Inventory())
	}); err != nil {
		return blocked(res, "world call failed: "+err.Error())
	}
	o.printf("")
	o.printf("after taking everything out of the chest (in memory, before the save):")
	o.printf("  chest    : %s", chestAfterTake)
	o.printf("  player   : %s", invAfterTake)

	// ---- phase 3: save + unload + reload ----
	h.World.Save()
	if err := h.Do(func(tx *world.Tx, p *player.Player) { p.Teleport(farAway) }); err != nil {
		return blocked(res, "world call failed: "+err.Error())
	}
	if ok, err := waitUnloaded("phase 3"); err != nil {
		return blocked(res, "world call failed: "+err.Error())
	} else if !ok {
		return blocked(res, "the chunk holding the chest never unloaded the second time")
	}
	h.World.Save()

	var chestReloaded, invReloaded string
	chestCount, playerCount := 0, 0
	if err := h.Do(func(tx *world.Tx, p *player.Player) {
		ch := tx.Block(chestPos).(block.Chest)
		chestReloaded = invSummary(ch.Inventory(tx, chestPos))
		chestCount = invCount(ch.Inventory(tx, chestPos), "minecraft:diamond")
		invReloaded = invSummary(p.Inventory())
		playerCount = invCount(p.Inventory(), "minecraft:diamond")
	}); err != nil {
		return blocked(res, "world call failed: "+err.Error())
	}
	o.printf("")
	o.printf("phase 3: chest read back from the leveldb provider after the unload:")
	o.printf("  chest    : %s", chestReloaded)
	o.printf("  player   : %s", invReloaded)
	o.printf("")
	o.printf("diamonds in chest : %d   (expected 0)", chestCount)
	o.printf("diamonds on player: %d   (expected %d)", playerCount, diamonds)
	o.printf("total diamonds    : %d   (expected %d)", chestCount+playerCount, diamonds)

	res.Observed = o.String()
	res.Expected = fmt.Sprintf(
		"The %d diamonds were moved out of the chest before the save, so after the save/unload/reload cycle\n"+
			"the chest should be empty and the player should hold all %d. Total diamonds in existence: %d.",
		diamonds, diamonds, diamonds)

	if chestCount > 0 && playerCount > 0 {
		res.Verdict = Reproduced
		res.Summary = fmt.Sprintf("%d diamonds in the chest AND %d on the player after reload (expected 0 + %d)",
			chestCount, playerCount, diamonds)
	} else {
		res.Verdict = NotReproduced
		res.Summary = fmt.Sprintf("chest %d, player %d, total %d (expected 0 / %d / %d)",
			chestCount, playerCount, chestCount+playerCount, diamonds, diamonds)
	}
	return res
}
