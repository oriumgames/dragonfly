package main

import (
	"os"
	"time"

	"github.com/df-mc/dragonfly/server/block/cube"
	"github.com/df-mc/dragonfly/server/entity"
	"github.com/df-mc/dragonfly/server/item"
	"github.com/df-mc/dragonfly/server/item/potion"
	"github.com/df-mc/dragonfly/server/world"
	"github.com/df-mc/dragonfly/server/world/mcdb"
	"github.com/go-gl/mathgl/mgl64"
)

const tick = time.Second / 20

// p1_07 covers the NBT duration round trips. Entity Age goes through a real
// mcdb provider on disk (save, close, reopen, read the entity back). The other
// three go through the real EntityType.EncodeNBT/DecodeNBT pair that the
// provider itself calls, which is the only way to observe the tag values that
// are actually written.
func scenarioNBTDurations() *Scenario {
	return &Scenario{
		ID:    "p1-07-nbt-durations",
		Part:  1,
		Title: "NBT duration round trips (Age, pickup delay, TNT fuse, area effect cloud)",
		Claim: "NBT duration round trips: entity `Age`, item pickup delay, TNT fuse over 255 ticks, area effect cloud durations (int32 nanosecond overflow and two tags transposed).",
		Setup: "A: a real `world.World` on a real `mcdb.DB` in a temp dir. A real item entity is aged 400 real ticks, `World.Save`d, the world is closed, reopened from the same directory and the entity's `Age()` is read back.\n" +
			"B/C/D: the real `world.EntityType` implementations (`entity.ItemType`, `entity.TNTType`, `entity.AreaEffectCloudType`) are driven through their real `EncodeNBT`/`DecodeNBT` with real behaviour values built from the exported `*BehaviourConfig.New()` constructors. The raw NBT map is printed verbatim.",
		Expected: "A: 400 ticks in -> 400 ticks out.\n" +
			"B: pickup delay 2s in -> a tag of 40 ticks, 2s out.\n" +
			"C: TNT fuse 20s (400 ticks) in -> 400 out; a uint8 tag cannot hold it.\n" +
			"D: AEC Duration 30s -> a tag of 600 ticks; ReapplicationDelay and DurationUseGrowth keep their own tags and their own values.",
		Timeout: 120 * time.Second,
		Run:     runNBTDurations,
	}
}

func runNBTDurations(o *Out) {
	bad := 0

	// ------------------------------------------------------------------
	o.Section("A. entity Age through a real mcdb provider on disk")
	dir := mustTempDir(o, "df-nbt")
	defer os.RemoveAll(dir)

	db, err := mcdb.Open(dir)
	if err != nil {
		o.Verdict(Blocked, "mcdb.Open: %v", err)
		return
	}
	conf := world.Config{
		Log:          discardLogger(),
		Provider:     db,
		Entities:     entity.DefaultRegistry,
		Synchronous:  true,
		SaveInterval: -1,
	}
	w := conf.New()

	h := entity.NewItem(world.EntitySpawnOpts{Position: mgl64.Vec3{8, 64, 8}}, item.NewStack(item.Diamond{}, 3))
	if err := call(w, 10*time.Second, func(tx *world.Tx) { tx.AddEntity(h) }); err != nil {
		o.Verdict(Blocked, "AddEntity: %v", err)
		return
	}
	// Place a block so the column is marked modified and definitely saved.
	stone, _ := world.DefaultBlockRegistry.BlockByName("minecraft:stone", map[string]any{})
	_ = call(w, 10*time.Second, func(tx *world.Tx) { tx.SetBlock(cube.Pos{8, 60, 8}, stone, nil) })

	const ticks = 400
	for range ticks {
		w.AdvanceTick()
	}
	ageIn, _ := callVal(w, 10*time.Second, func(tx *world.Tx) time.Duration {
		if e, ok := h.Entity(tx); ok {
			if ent, ok := e.(*entity.Ent); ok {
				return ent.Age()
			}
		}
		return -1
	})
	o.Logf("before save: item entity Age = %s (%d ticks)", ageIn, ageIn/tick)
	id := h.UUID()
	w.Save()
	_ = w.Close()

	db2, err := mcdb.Open(dir)
	if err != nil {
		o.Verdict(Blocked, "mcdb.Open (reopen): %v", err)
		return
	}
	conf2 := conf
	conf2.Provider = db2
	w2 := conf2.New()

	ageOut, err := callVal(w2, 30*time.Second, func(tx *world.Tx) time.Duration {
		// Touch the chunk so it is loaded and its entities are spawned.
		tx.Block(cube.Pos{8, 60, 8})
		for e := range tx.Entities() {
			if e.H().UUID() != id {
				continue
			}
			if ent, ok := e.(*entity.Ent); ok {
				return ent.Age()
			}
		}
		return -1
	})
	_ = w2.Close()
	if err != nil {
		o.Logf("reload failed: %v", err)
	}
	o.Logf("after reload: item entity Age = %s (%d ticks)", ageOut, ageOut/tick)
	o.Logf("expected %s (%d ticks)", ageIn, ageIn/tick)
	if ageOut != ageIn {
		bad++
		o.Logf("A: MISMATCH -- Age lost a factor of %v on the round trip", float64(ageIn)/float64(max64(int64(ageOut), 1)))
	} else {
		o.Logf("A: OK")
	}

	// ------------------------------------------------------------------
	o.Section("B. item entity pickup delay through entity.ItemType.EncodeNBT")
	ib := entity.ItemBehaviourConfig{Item: item.NewStack(item.Diamond{}, 1), PickupDelay: 2 * time.Second}.New()
	dataB := world.EntityData{Data: ib}
	mB := entity.ItemType.EncodeNBT(&dataB)
	o.Logf("input PickupDelay = %s (%d ticks)", 2*time.Second, (2*time.Second)/tick)
	o.Logf("encoded tag PickupDelay = %#v (Go type %T)", mB["PickupDelay"], mB["PickupDelay"])
	var outB world.EntityData
	entity.ItemType.DecodeNBT(mB, &outB)
	mB2 := entity.ItemType.EncodeNBT(&outB)
	o.Logf("re-encoded tag PickupDelay after a decode = %#v", mB2["PickupDelay"])
	o.Logf("expected tag = 40 (2s at 20 ticks per second)")
	if v, ok := mB["PickupDelay"].(int64); !ok || v != 40 {
		bad++
		o.Logf("B: MISMATCH -- 2 seconds of pickup delay encodes to %v, so the delay does not survive a save", mB["PickupDelay"])
	} else {
		o.Logf("B: OK")
	}

	// ------------------------------------------------------------------
	o.Section("C. TNT fuse over 255 ticks through entity.TNTType.EncodeNBT")
	for _, fuse := range []time.Duration{20 * time.Second, 12*time.Second + 800*time.Millisecond, 10 * time.Second} {
		pb := entity.PassiveBehaviourConfig{ExistenceDuration: fuse, Expire: func(*entity.Ent, *world.Tx) {}}.New()
		dataC := world.EntityData{Data: pb}
		mC := entity.TNTType.EncodeNBT(&dataC)
		var outC world.EntityData
		entity.TNTType.DecodeNBT(mC, &outC)
		got := outC.Data.(*entity.PassiveBehaviour).Fuse()
		o.Logf("fuse in = %-6s (%3d ticks) -> tag Fuse = %-4v (%T) -> fuse out = %-8s (%d ticks)",
			fuse, fuse/tick, mC["Fuse"], mC["Fuse"], got, got/tick)
		if got != fuse {
			bad++
			o.Logf("C: MISMATCH for %s -- expected %s, got %s", fuse, fuse, got)
		}
	}

	// ------------------------------------------------------------------
	o.Section("D1. area effect cloud durations: int32 nanosecond truncation")
	aec := entity.AreaEffectCloudBehaviourConfig{
		Potion:             potion.Poison(),
		Duration:           30 * time.Second,
		ReapplicationDelay: 2 * time.Second,
		DurationUseGrowth:  5 * time.Second,
		Radius:             3,
	}.New()
	dataD := world.EntityData{Data: aec}
	mD := entity.AreaEffectCloudType.EncodeNBT(&dataD)
	o.Logf("input  Duration = %s -> expected tag 600 ticks", 30*time.Second)
	o.Logf("encoded tag Duration           = %#v (%T)", mD["Duration"], mD["Duration"])
	o.Logf("encoded tag ReapplicationDelay = %#v (%T)  [input 2s, expected tag 40]", mD["ReapplicationDelay"], mD["ReapplicationDelay"])
	o.Logf("encoded tag DurationOnUse      = %#v (%T)  [input 5s, expected tag 100]", mD["DurationOnUse"], mD["DurationOnUse"])
	o.Logf("30s in nanoseconds = %d, which does not fit in an int32 (max %d)", int64(30*time.Second), int64(1<<31-1))
	if v, ok := mD["Duration"].(int32); !ok || v != 600 {
		bad++
		o.Logf("D1: MISMATCH -- Duration tag is %v, not 600 ticks", mD["Duration"])
	}

	o.Section("D2. area effect cloud: two tags transposed on decode")
	// ReapplicationDelay is zero and DurationUseGrowth is not. If the tags are
	// read back into the right fields, that stays true after a round trip.
	aec2 := entity.AreaEffectCloudBehaviourConfig{
		Potion:             potion.Poison(),
		Duration:           time.Second,
		ReapplicationDelay: 0,
		DurationUseGrowth:  time.Second,
		Radius:             3,
	}.New()
	dataE := world.EntityData{Data: aec2}
	mE := entity.AreaEffectCloudType.EncodeNBT(&dataE)
	o.Logf("input  ReapplicationDelay = 0, DurationUseGrowth = 1s")
	o.Logf("encoded ReapplicationDelay tag = %v, DurationOnUse tag = %v", mE["ReapplicationDelay"], mE["DurationOnUse"])
	var outE world.EntityData
	entity.AreaEffectCloudType.DecodeNBT(mE, &outE)
	mE2 := entity.AreaEffectCloudType.EncodeNBT(&outE)
	o.Logf("after decode + re-encode: ReapplicationDelay tag = %v, DurationOnUse tag = %v", mE2["ReapplicationDelay"], mE2["DurationOnUse"])
	o.Logf("expected: the zero stays on ReapplicationDelay and the non-zero stays on DurationOnUse")
	zeroMoved := isZeroInt32(mE2["DurationOnUse"]) && !isZeroInt32(mE2["ReapplicationDelay"])
	if zeroMoved {
		bad++
		o.Logf("D2: MISMATCH -- the zero and the non-zero swapped tags, i.e. ReapplicationDelay and DurationUseGrowth are transposed on decode")
	} else {
		o.Logf("D2: no transposition observed")
	}

	if bad > 0 {
		o.Verdict(Reproduced, "%d duration round trips came back wrong (Age, pickup delay, TNT fuse, AEC duration and AEC tag transposition)", bad)
	} else {
		o.Verdict(Refuted, "every duration round trip was exact")
	}
}

func isZeroInt32(v any) bool {
	i, ok := v.(int32)
	return ok && i == 0
}

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
