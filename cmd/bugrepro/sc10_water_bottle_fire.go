package main

import (
	"fmt"
	"time"

	"github.com/df-mc/dragonfly/server/block"
	"github.com/df-mc/dragonfly/server/block/cube"
	"github.com/df-mc/dragonfly/server/entity"
	"github.com/df-mc/dragonfly/server/item/potion"
	"github.com/df-mc/dragonfly/server/player"
	"github.com/df-mc/dragonfly/server/world"
	"github.com/go-gl/mathgl/mgl64"
)

func init() {
	register(Scenario{
		ID:      "10-water-bottle-vs-aged-fire",
		Title:   "A splash water bottle only extinguishes fire whose Age is exactly 0",
		Timeout: 180 * time.Second,
		Bug: "`potionSplash` in `server/entity/splashable.go` compares the block against a *value*:\n\n" +
			"```go\n" +
			"func fire() world.Block {\n" +
			"    f, ok := world.BlockByName(\"minecraft:fire\", map[string]any{\"age\": int32(0)})\n" +
			"    ...\n}\n\n" +
			"if tx.Block(blockPos) == fire() { tx.SetBlock(blockPos, nil, nil) }\n" +
			"for _, f := range cube.HorizontalFaces() {\n" +
			"    if h := blockPos.Side(f); tx.Block(h) == fire() { tx.SetBlock(h, nil, nil) }\n" +
			"}\n```\n\n" +
			"`fire()` is hard-coded to `age: 0`, and `Fire` carries its `Age` in the block value, so the\n" +
			"`==` comparison fails for any fire that has ticked at least once (`Fire.tick` does\n" +
			"`if f.Age < 15 && r.IntN(3) == 0 { f.Age++ }`). A splash water bottle silently does nothing to\n" +
			"a fire that has been burning for a few seconds.",
		Run: runWaterBottleFire,
	})
}

func runWaterBottleFire() Result {
	h, err := startHarness(harnessOpts{withClient: true, randomTickSpeed: -1})
	if err != nil {
		return Result{Verdict: Blocked, Reason: "could not start harness: " + err.Error()}
	}
	defer h.Stop()

	var o out
	res := Result{
		Setup: "Netherrack at (90, -61, 90) so the fire above it burns forever and keeps re-scheduling its\n" +
			"own tick. Three trials: a freshly lit fire (Age 0), a fire whose Age was allowed to climb by\n" +
			"itself through the real scheduled `Fire.tick`, and a fire whose Age was set directly to 3 to\n" +
			"make the result deterministic. In each trial a real `entity.SplashPotion` of\n" +
			"`potion.Water()` is thrown at the ground under the fire.",
		ServerSteps: []string{
			"placed netherrack and lit the fire with the real `block.Fire{}.Start(tx, pos)`",
			"trial 2: polled `Fire.Age` while the block's own scheduled tick (`Fire.tick`) aged it - no manual aging",
			"trial 3: set the fire block directly with `tx.SetBlock(pos, block.Fire{Age: 3}, nil)` for a deterministic run",
			"spawned a real `entity.NewSplashPotion(opts, potion.Water(), player)` above the fire with downward velocity and let it fly and break on its own",
			"read the block at the fire position afterwards",
		},
		ClientSteps: []string{
			"a real gophertunnel client was connected and kept the chunk loaded so the fire's scheduled " +
				"ticks and the potion's flight actually ran. It sent no packets for this scenario.",
		},
	}

	firePos := cube.Pos{90, -60, 90}
	basePos := cube.Pos{90, -61, 90}

	throwPotion := func() error {
		return h.Do(func(tx *world.Tx, p *player.Player) {
			tx.AddEntity(entity.NewSplashPotion(world.EntitySpawnOpts{
				Position: mgl64.Vec3{90.5, -57.0, 90.5},
				Velocity: mgl64.Vec3{0, -0.8, 0},
			}, potion.Water(), p))
		})
	}

	lightFire := func() error {
		return h.Do(func(tx *world.Tx, p *player.Player) {
			preparePlayer(tx, p, mgl64.Vec3{93.5, -60, 90.5})
			clearArea(tx, firePos, 3)
			tx.SetBlock(basePos, block.Netherrack{}, nil)
			block.Fire{}.Start(tx, firePos)
		})
	}

	blockAt := func() (string, int, bool) {
		var desc string
		var age int
		var isFire bool
		_ = h.Do(func(tx *world.Tx, p *player.Player) {
			b := tx.Block(firePos)
			desc = fmt.Sprintf("%T%+v", b, b)
			if f, ok := b.(block.Fire); ok {
				isFire, age = true, f.Age
			}
		})
		return desc, age, isFire
	}

	// --- trial 1: freshly lit fire, Age 0 ---
	if err := lightFire(); err != nil {
		return blocked(res, "world call failed: "+err.Error())
	}
	desc, age, _ := blockAt()
	o.printf("trial 1 - freshly lit fire")
	o.printf("  before splash: %s (Age=%d)", desc, age)
	if err := throwPotion(); err != nil {
		return blocked(res, "world call failed: "+err.Error())
	}
	time.Sleep(2 * time.Second)
	desc, _, stillFire1 := blockAt()
	o.printf("  after  splash: %s", desc)
	o.printf("  extinguished : %v   (expected true)", !stillFire1)
	o.printf("")

	// --- trial 2: let the fire age by itself through its own scheduled tick ---
	if err := lightFire(); err != nil {
		return blocked(res, "world call failed: "+err.Error())
	}
	agedNaturally := 0
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		time.Sleep(500 * time.Millisecond)
		_, a, isFire := blockAt()
		if !isFire {
			break
		}
		if a > 0 {
			agedNaturally = a
			break
		}
	}
	o.printf("trial 2 - fire aged by its own scheduled Fire.tick (no manual edit)")
	o.printf("  waited until Fire.Age climbed to %d", agedNaturally)
	if agedNaturally == 0 {
		o.printf("  the fire never aged within 60s, falling back to trial 3 only")
	} else {
		if err := throwPotion(); err != nil {
			return blocked(res, "world call failed: "+err.Error())
		}
		time.Sleep(2 * time.Second)
		desc, a, stillFire := blockAt()
		o.printf("  after  splash: %s", desc)
		o.printf("  extinguished : %v   (expected true), Age is still %d", !stillFire, a)
	}
	o.printf("")

	// --- trial 3: deterministic Age = 3 ---
	if err := lightFire(); err != nil {
		return blocked(res, "world call failed: "+err.Error())
	}
	if err := h.Do(func(tx *world.Tx, p *player.Player) {
		tx.SetBlock(firePos, block.Fire{Age: 3}, nil)
	}); err != nil {
		return blocked(res, "world call failed: "+err.Error())
	}
	desc, age, _ = blockAt()
	o.printf("trial 3 - fire block set directly to Age=3 (deterministic)")
	o.printf("  before splash: %s (Age=%d)", desc, age)
	if err := throwPotion(); err != nil {
		return blocked(res, "world call failed: "+err.Error())
	}
	time.Sleep(2 * time.Second)
	desc, age, stillFire3 := blockAt()
	o.printf("  after  splash: %s", desc)
	o.printf("  extinguished : %v   (expected true)", !stillFire3)

	res.Observed = o.String()
	res.Expected = "A splash water bottle should extinguish the fire it lands on regardless of its Age. Trial 1\n" +
		"(Age 0) should extinguish, and so should trials 2 and 3."

	if !stillFire1 && stillFire3 {
		res.Verdict = Reproduced
		res.Summary = "Age 0 fire is extinguished, Age 3 fire is not (== comparison against a hard-coded age 0 fire block)"
	} else if stillFire3 {
		res.Verdict = Reproduced
		res.Summary = "Age 3 fire survives the splash water bottle"
	} else {
		res.Verdict = NotReproduced
		res.Summary = fmt.Sprintf("age 0 extinguished=%v, age 3 extinguished=%v", !stillFire1, !stillFire3)
	}
	return res
}
