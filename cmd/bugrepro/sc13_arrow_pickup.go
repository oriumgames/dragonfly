package main

import (
	"fmt"
	"time"

	"github.com/df-mc/dragonfly/server/block"
	"github.com/df-mc/dragonfly/server/block/cube"
	"github.com/df-mc/dragonfly/server/entity"
	"github.com/df-mc/dragonfly/server/item"
	"github.com/df-mc/dragonfly/server/item/potion"
	"github.com/df-mc/dragonfly/server/player"
	"github.com/df-mc/dragonfly/server/world"
	"github.com/go-gl/mathgl/mgl64"
)

func init() {
	register(Scenario{
		ID:      "13-arrow-multi-pickup",
		Title:   "One arrow is picked up by every nearby player, and vanishes if the collector is full",
		Timeout: 180 * time.Second,
		Bug: "`ProjectileBehaviour.tryPickup` never stops after a successful pickup, and never checks how\n" +
			"many items the collector actually took:\n\n" +
			"```go\n" +
			"for other := range tx.EntitiesWithin(translated.Grow(2)) {\n" +
			"    ...\n" +
			"    if _, ok := collector.Collect(lt.conf.PickupItem); !ok { continue }\n" +
			"    lt.close = true\n" +
			"    for _, viewer := range tx.Viewers(e.Position()) { ... }\n" +
			"}\n```\n\n" +
			"Two bugs fall out of that:\n\n" +
			"1. Every collector in range gets its own copy of `lt.conf.PickupItem` - the single arrow is\n" +
			"   duplicated once per nearby player.\n" +
			"2. `(*Player).Collect` returns `(0, true)` when the inventory is full (it only reports `false`\n" +
			"   for a dead player, a non-interacting game mode or a cancelled handler). `tryPickup` throws\n" +
			"   away the count, so the arrow entity is closed even though nothing was collected.",
		Run: runArrowPickup,
	})
}

func runArrowPickup() Result {
	h, err := startHarness(harnessOpts{withClient: true, randomTickSpeed: -1})
	if err != nil {
		return Result{Verdict: Blocked, Reason: "could not start harness: " + err.Error()}
	}
	defer h.Stop()

	var o out
	res := Result{
		Setup: "Three real gophertunnel clients connected to the same server, all standing on the same block\n" +
			"at (130, -60, 130) with empty inventories. One real `entity.Arrow` is stuck in the ground next\n" +
			"to them. Part two repeats the test with a single player whose inventory is completely full.",
		ServerSteps: []string{
			"spawned the arrow exactly the way `item.Bow` does - `tx.World().EntityRegistry().Config().Arrow(opts, world.ArrowSpawnConfig{ObtainArrowOnPickup: true, ...})` - with a downward velocity so it embeds in the floor through the normal projectile collision path",
			"teleported all three players onto the arrow's block",
			"let the real world ticker run `ProjectileBehaviour.tickAttached` -> `tryPickup`",
			"counted arrows in every player's inventory and arrow entities left in the world",
			"part two: filled the single player's inventory with cobblestone before the pickup",
		},
		ClientSteps: []string{
			"three separate real `minecraft.Dialer{}.DialTimeout(\"raknet\", addr, ...)` connections, each " +
				"with its own login identity, produced the three player entities. Without real clients " +
				"there would be no `Collector` entities at all. They sent no item packets.",
		},
	}

	p2, err := h.AddClient()
	if err != nil {
		return blocked(res, "could not connect the second client: "+err.Error())
	}
	p3, err := h.AddClient()
	if err != nil {
		return blocked(res, "could not connect the third client: "+err.Error())
	}
	handles := []*world.EntityHandle{h.PlayerH, p2, p3}

	stand := mgl64.Vec3{130.5, -60, 130.5}
	floor := cube.Pos{130, -61, 130}

	prepare := func(fillInv bool) error {
		for _, ph := range handles {
			if err := h.DoEntity(ph, func(tx *world.Tx, p *player.Player) {
				preparePlayer(tx, p, stand)
				tx.SetBlock(floor, block.Stone{}, nil)
				for y := -60; y <= -55; y++ {
					tx.SetBlock(cube.Pos{130, y, 130}, nil, nil)
				}
				if fillInv {
					fillInventory(p.Inventory(), item.NewStack(block.Cobblestone{}, 64))
				}
			}); err != nil {
				return err
			}
		}
		return nil
	}
	removeArrows := func() error {
		return h.Do(func(tx *world.Tx, p *player.Player) {
			for e := range tx.Entities() {
				ent, ok := e.(*entity.Ent)
				if !ok {
					continue
				}
				if _, ok := ent.Behaviour().(*entity.ProjectileBehaviour); ok {
					_ = tx.RemoveEntity(e).Close()
				}
			}
			removeAllItemEntities(tx)
		})
	}
	spawnArrow := func() error {
		return h.Do(func(tx *world.Tx, p *player.Player) {
			// Spawned exactly the way item.Bow does it, so the arrow is
			// obtainable on pickup.
			create := tx.World().EntityRegistry().Config().Arrow
			tx.AddEntity(create(world.EntitySpawnOpts{
				Position: mgl64.Vec3{130.5, -59.0, 130.5},
				Velocity: mgl64.Vec3{0, -0.6, 0},
			}, world.ArrowSpawnConfig{
				Damage:              2,
				Owner:               p,
				ObtainArrowOnPickup: true,
				Tip:                 potion.Potion{},
			}))
		})
	}
	countArrows := func() (perPlayer []int, entities int, err error) {
		for i, ph := range handles {
			n := 0
			if err = h.DoEntity(ph, func(tx *world.Tx, p *player.Player) {
				n = invCount(p.Inventory(), "minecraft:arrow")
			}); err != nil {
				return nil, 0, err
			}
			perPlayer = append(perPlayer, n)
			_ = i
		}
		err = h.Do(func(tx *world.Tx, p *player.Player) {
			for e := range tx.Entities() {
				ent, ok := e.(*entity.Ent)
				if !ok {
					continue
				}
				if _, ok := ent.Behaviour().(*entity.ProjectileBehaviour); ok {
					entities++
				}
			}
		})
		return perPlayer, entities, err
	}

	// --- Part 1: three collectors ---
	if err := removeArrows(); err != nil {
		return blocked(res, "world call failed: "+err.Error())
	}
	if err := prepare(false); err != nil {
		return blocked(res, "world call failed: "+err.Error())
	}
	if err := spawnArrow(); err != nil {
		return blocked(res, "world call failed: "+err.Error())
	}
	time.Sleep(4 * time.Second)
	perPlayer, arrowEntities, err := countArrows()
	if err != nil {
		return blocked(res, "world call failed: "+err.Error())
	}
	total := 0
	for _, n := range perPlayer {
		total += n
	}
	o.printf("part 1 - one arrow, three players standing on it")
	for i, n := range perPlayer {
		o.printf("  player %d inventory: %d arrow(s)", i+1, n)
	}
	o.printf("  arrow entities left in the world: %d", arrowEntities)
	o.printf("  total arrows in existence: %d   (expected 1)", total+arrowEntities)
	o.printf("")

	// --- Part 2: full inventory ---
	if err := removeArrows(); err != nil {
		return blocked(res, "world call failed: "+err.Error())
	}
	// Move the two extra players far away so only one collector is in range.
	for _, ph := range handles[1:] {
		if err := h.DoEntity(ph, func(tx *world.Tx, p *player.Player) {
			p.Teleport(mgl64.Vec3{0.5, -60, 0.5})
		}); err != nil {
			return blocked(res, "world call failed: "+err.Error())
		}
	}
	if err := h.Do(func(tx *world.Tx, p *player.Player) {
		preparePlayer(tx, p, stand)
		fillInventory(p.Inventory(), item.NewStack(block.Cobblestone{}, 64))
	}); err != nil {
		return blocked(res, "world call failed: "+err.Error())
	}
	var free int
	if err := h.Do(func(tx *world.Tx, p *player.Player) {
		for _, s := range p.Inventory().Slots() {
			if s.Empty() {
				free++
			}
		}
	}); err != nil {
		return blocked(res, "world call failed: "+err.Error())
	}
	if err := spawnArrow(); err != nil {
		return blocked(res, "world call failed: "+err.Error())
	}
	time.Sleep(4 * time.Second)

	var (
		fullInvArrows int
		leftEntities  int
		groundItems2  int
	)
	if err := h.Do(func(tx *world.Tx, p *player.Player) {
		fullInvArrows = invCount(p.Inventory(), "minecraft:arrow")
		for e := range tx.Entities() {
			ent, ok := e.(*entity.Ent)
			if !ok {
				continue
			}
			if _, ok := ent.Behaviour().(*entity.ProjectileBehaviour); ok {
				leftEntities++
			}
		}
		counts, _ := groundItems(tx)
		groundItems2 = counts["minecraft:arrow"]
	}); err != nil {
		return blocked(res, "world call failed: "+err.Error())
	}
	o.printf("part 2 - one arrow, one player with a completely full inventory (%d free slots)", free)
	o.printf("  arrows in the player inventory : %d", fullInvArrows)
	o.printf("  arrow entities left in the world: %d", leftEntities)
	o.printf("  arrow item entities on the ground: %d", groundItems2)
	o.printf("  total arrows in existence: %d   (expected 1)", fullInvArrows+leftEntities+groundItems2)

	res.Observed = o.String()
	res.Expected = "One arrow should be picked up by exactly one player and then cease to exist: 1 arrow total.\n" +
		"If the only collector's inventory is full, the arrow should stay stuck in the ground: also 1\n" +
		"arrow total."

	multi := total > 1
	destroyed := fullInvArrows+leftEntities+groundItems2 == 0
	switch {
	case multi && destroyed:
		res.Verdict = Reproduced
		res.Summary = fmt.Sprintf("one arrow became %d (one per nearby player), and with a full inventory it was destroyed (0 left)", total)
	case multi:
		res.Verdict = Reproduced
		res.Summary = fmt.Sprintf("one arrow became %d (one per nearby player); full-inventory case left %d",
			total, fullInvArrows+leftEntities+groundItems2)
	case destroyed:
		res.Verdict = Reproduced
		res.Summary = "arrow destroyed when the collector's inventory was full (0 arrows left)"
	default:
		res.Verdict = NotReproduced
		res.Summary = fmt.Sprintf("multi-pickup total %d, full-inventory total %d",
			total+arrowEntities, fullInvArrows+leftEntities+groundItems2)
	}
	return res
}
