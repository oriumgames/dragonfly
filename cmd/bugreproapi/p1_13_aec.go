package main

import (
	"fmt"
	"math"
	"time"

	"github.com/df-mc/dragonfly/server/block/cube"
	"github.com/df-mc/dragonfly/server/entity"
	"github.com/df-mc/dragonfly/server/item/potion"
	"github.com/df-mc/dragonfly/server/player"
	"github.com/df-mc/dragonfly/server/world"
	"github.com/go-gl/mathgl/mgl64"
	"github.com/google/uuid"
)

// p1_13 counts how often a real area effect cloud applies its effects, using
// the exported Radius(): every application subtracts RadiusUseGrowth, and
// nothing else changes the radius when RadiusTickGrowth is 0.
func scenarioAreaEffectCloud() *Scenario {
	return &Scenario{
		ID:    "p1-13-aec-cadence",
		Part:  1,
		Title: "Area effect cloud applies effects every tick for 20s and then not for three minutes",
		Claim: "Area effect cloud applies effects every tick for 20 seconds and then not for three minutes (`Age()/(time.Second*20)` counts 400-tick units).",
		Setup: "A real `entity.NewAreaEffectCloudWith` cloud sits on a real sessionless `*player.Player` in a synchronous world. `RadiusTickGrowth` is 0 and `RadiusUseGrowth` is -0.0005, so the cloud's exported `Radius()` drops by exactly 0.0005 for every application and by nothing else. `ReapplicationDelay` is 0 so the cooldown never blocks an otherwise-due application.\n" +
			"The world is advanced tick by tick with `AdvanceTick` and the radius is sampled at 20-second boundaries.",
		Expected: "The cloud applies its effects once every ten ticks for its whole lifetime: 40 applications in the first 20 seconds and another 40 in every 20 seconds after that.",
		Timeout:  180 * time.Second,
		Run:      runAreaEffectCloud,
	}
}

func runAreaEffectCloud(o *Out) {
	w := world.Config{
		Log: discardLogger(), Entities: entity.DefaultRegistry,
		Synchronous: true, SaveInterval: -1, RandomTickSpeed: -1,
	}.New()
	defer w.Close()

	pos := mgl64.Vec3{8, 64, 8}
	// The cloud sits slightly below the player: BBox.Vec3Within is strictly
	// inside, so an entity exactly on the box's lower y face is not seen.
	cloudPos := mgl64.Vec3{8, 63.8, 8}
	id := uuid.New()
	ph := world.EntitySpawnOpts{Position: pos, ID: id}.New(player.Type, player.Config{UUID: id, Name: "Target", Position: pos})

	const useGrowth = -0.0005
	const radius = 6.0
	h := entity.NewAreaEffectCloudWith(
		world.EntitySpawnOpts{Position: cloudPos},
		potion.Poison(),
		5*time.Minute, // Duration
		0,             // ReapplicationDelay
		0,             // DurationOnUse
		radius, useGrowth, 0,
	)
	stone, _ := world.DefaultBlockRegistry.BlockByName("minecraft:stone", map[string]any{})
	if err := call(w, 20*time.Second, func(tx *world.Tx) {
		for x := 0; x < 16; x++ {
			for z := 0; z < 16; z++ {
				tx.SetBlock(cube.Pos{x, 63, z}, stone, nil)
			}
		}
		tx.AddEntity(ph)
		tx.AddEntity(h)
	}); err != nil {
		o.Verdict(Blocked, "setup: %v", err)
		return
	}
	o.Logf("cloud radius %.4f, RadiusUseGrowth %.4f, RadiusTickGrowth 0, ReapplicationDelay 0, Duration 5m", radius, useGrowth)
	o.Logf("one application of the cloud's effects subtracts %.4f from the radius and nothing else changes it", -useGrowth)

	readRadius := func() float64 {
		v, _ := callVal(w, 10*time.Second, func(tx *world.Tx) float64 {
			e, ok := h.Entity(tx)
			if !ok {
				return math.NaN()
			}
			ent, ok := e.(*entity.Ent)
			if !ok {
				return math.NaN()
			}
			a, ok := ent.Behaviour().(*entity.AreaEffectCloudBehaviour)
			if !ok {
				return math.NaN()
			}
			return a.Radius()
		})
		return v
	}

	diag, _ := callVal(w, 10*time.Second, func(tx *world.Tx) string {
		e, _ := h.Entity(tx)
		ent := e.(*entity.Ent)
		box := ent.H().Type().BBox(ent).Translate(ent.Position())
		n, living := 0, 0
		for other := range tx.EntitiesWithin(box) {
			n++
			if _, ok := other.(entity.Living); ok {
				living++
			}
		}
		return fmt.Sprintf("cloud Age=%s, BBox=%v, entities within=%d of which Living=%d", ent.Age(), box, n, living)
	})
	o.Logf("diagnostic: %s", diag)

	prev := readRadius()
	o.Logf("t=0s: radius %.5f", prev)
	type window struct {
		from, to time.Duration
		apps     int
	}
	var windows []window
	const perWindow = 400 // ticks in 20 seconds
	for wi := 0; wi < 11; wi++ {
		for range perWindow {
			w.AdvanceTick()
		}
		if wi == 0 {
			d2, _ := callVal(w, 10*time.Second, func(tx *world.Tx) string {
				e, ok := h.Entity(tx)
				if !ok {
					return "cloud gone"
				}
				ent := e.(*entity.Ent)
				box := ent.H().Type().BBox(ent).Translate(ent.Position())
				n := 0
				for range tx.EntitiesWithin(box) {
					n++
				}
				return fmt.Sprintf("cloud Age=%s, entities within=%d", ent.Age(), n)
			})
			o.Logf("diagnostic after 400 ticks: %s", d2)
		}
		r := readRadius()
		if math.IsNaN(r) {
			o.Logf("the cloud closed after %s", time.Duration(wi+1)*20*time.Second)
			break
		}
		apps := int(math.Round((prev - r) / -useGrowth))
		windows = append(windows, window{time.Duration(wi) * 20 * time.Second, time.Duration(wi+1) * 20 * time.Second, apps})
		o.Logf("%3ds..%3ds (ticks %5d..%5d): radius %.5f -> %.5f, applications = %4d (expected 40)",
			int((time.Duration(wi) * 20 * time.Second).Seconds()),
			int((time.Duration(wi+1) * 20 * time.Second).Seconds()),
			wi*perWindow, (wi+1)*perWindow, prev, r, apps)
		prev = r
	}

	if len(windows) < 2 {
		o.Verdict(Blocked, "the cloud did not survive long enough to sample more than one window")
		return
	}
	first := windows[0].apps
	silent := 0
	for _, wd := range windows[1:] {
		if wd.apps == 0 {
			silent++
		}
	}
	o.Logf("first 20s: %d applications (expected 40)", first)
	o.Logf("windows after the first 20s with zero applications: %d out of %d", silent, len(windows)-1)

	if first > 100 && silent > 0 {
		o.Verdict(Reproduced, "%d applications in the first 20 seconds (one per tick instead of one per ten ticks), then %d silent 20-second windows: Age()/(time.Second*20) counts 400-tick units, not ticks", first, silent)
		return
	}
	if first > 100 {
		o.Verdict(Reproduced, "%d applications in the first 20 seconds instead of 40", first)
		return
	}
	o.Verdict(Refuted, "the cloud applied its effects %d times in the first 20 seconds, close to the expected 40", first)
}
