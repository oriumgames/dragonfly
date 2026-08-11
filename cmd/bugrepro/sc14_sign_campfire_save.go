package main

import (
	"fmt"
	"image/color"
	"strings"
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
		ID:      "14-sign-campfire-save-loss",
		Title:   "Sign colour/glow/wax and campfire cooking times are lost on save and reload",
		Timeout: 180 * time.Second,
		Bug: "**Sign.** `EncodeNBT` and `DecodeNBT` do not agree on the tag names inside `FrontText`/`BackText`:\n\n" +
			"```go\n" +
			"// EncodeNBT writes\n" +
			"\"FrontText\": map[string]any{\"SignTextColor\": ..., \"IgnoreLighting\": ..., \"Text\": ..., \"TextOwner\": ...}\n" +
			"// DecodeNBT reads\n" +
			"s.Front.BaseColour = nbtconv.RGBAFromInt32(nbtconv.Int32(front, \"Color\"))\n" +
			"s.Front.Glowing   = nbtconv.Bool(front, \"GlowingText\")\n" +
			"s.Front.Owner     = nbtconv.String(front, \"Owner\")\n" +
			"```\n\n" +
			"Only `Text` matches. `IsWaxed` is written at the top level and never read at all.\n\n" +
			"**Campfire.** `EncodeNBT` writes the cooking time as a `uint8` (a TAG_Byte) but `DecodeNBT`\n" +
			"reads it with `nbtconv.Int16` (a TAG_Short), so the value never comes back:\n\n" +
			"```go\n" +
			"m[\"ItemTime\"+id] = uint8(v.Time.Milliseconds() / 50)         // encode\n" +
			"Time: time.Duration(nbtconv.Int16(data, \"ItemTime\"+id)) * ... // decode\n" +
			"```",
		Run: runSignCampfireSave,
	})
}

func runSignCampfireSave() Result {
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
		Setup: "A real on-disk leveldb (mcdb) world. A sign at (400, -60, 400) is given red glowing waxed\n" +
			"front text and blue back text; a campfire at (402, -60, 400) is given two food items with\n" +
			"non-zero cooking times. The world is saved, the player walks away so the chunk unloads, and the\n" +
			"blocks are then read back from disk.",
		ServerSteps: []string{
			"placed the sign and the campfire with `tx.SetBlock`, fully populated (this is the same state " +
				"`Sign.Dye`, `Sign.Glowing`, `Sign.Wax` and `Campfire.Activate` produce)",
			"called `(*World).Save()`, teleported the player away and waited for `closeUnusedChunks` to unload the chunk (verified with `tx.BlockLoaded`)",
			"read the blocks back, which reloads the column from the provider through `Sign.DecodeNBT` / `Campfire.DecodeNBT`",
		},
		ClientSteps: []string{
			"a real gophertunnel client was connected and owns the world loader that keeps chunks loaded " +
				"and lets them unload again. It sent no packets for this scenario.",
		},
	}

	signPos := cube.Pos{400, -60, 400}
	firePos := cube.Pos{402, -60, 400}
	near := mgl64.Vec3{380.5, -60, 400.5}
	far := mgl64.Vec3{0.5, -60, 0.5}

	red := color.RGBA{R: 255, A: 255}
	blue := color.RGBA{B: 255, A: 255}

	before := block.Sign{
		Wood:   block.OakWood(),
		Attach: block.StandingAttachment(0),
		Waxed:  true,
		Front:  block.SignText{Text: "front line", BaseColour: red, Glowing: true, Owner: "1234567890"},
		Back:   block.SignText{Text: "back line", BaseColour: blue, Glowing: true, Owner: "1234567890"},
	}
	campBefore := block.Campfire{Facing: cube.North, Type: block.NormalFire()}
	campBefore.Items[0] = block.CampfireItem{Item: item.NewStack(item.Beef{}, 1), Time: 20 * time.Second}
	campBefore.Items[1] = block.CampfireItem{Item: item.NewStack(item.Chicken{}, 1), Time: 15 * time.Second}

	if err := h.Do(func(tx *world.Tx, p *player.Player) {
		preparePlayer(tx, p, near)
		clearArea(tx, signPos, 3)
		clearArea(tx, firePos, 3)
		tx.SetBlock(signPos.Side(cube.FaceDown), block.Stone{}, nil)
		tx.SetBlock(firePos.Side(cube.FaceDown), block.Stone{}, nil)
		tx.SetBlock(signPos, before, nil)
		tx.SetBlock(firePos, campBefore, nil)
	}); err != nil {
		return blocked(res, "world call failed: "+err.Error())
	}
	time.Sleep(1500 * time.Millisecond)

	describeSignFull := func(s block.Sign) string {
		return fmt.Sprintf("Waxed=%v\n    Front: Text=%q Colour=%v Glowing=%v Owner=%q\n    Back : Text=%q Colour=%v Glowing=%v Owner=%q",
			s.Waxed,
			s.Front.Text, s.Front.BaseColour, s.Front.Glowing, s.Front.Owner,
			s.Back.Text, s.Back.BaseColour, s.Back.Glowing, s.Back.Owner)
	}
	describeCampfire := func(c block.Campfire) string {
		var parts []string
		for i, it := range c.Items {
			if it.Item.Empty() {
				continue
			}
			parts = append(parts, fmt.Sprintf("slot %d: %s x%d, Time=%s", i, itemName(it.Item), it.Item.Count(), it.Time))
		}
		if len(parts) == 0 {
			return "(no items)"
		}
		return strings.Join(parts, "\n    ")
	}

	var signBefore, fireBefore string
	if err := h.Do(func(tx *world.Tx, p *player.Player) {
		signBefore = describeSignFull(tx.Block(signPos).(block.Sign))
		fireBefore = describeCampfire(tx.Block(firePos).(block.Campfire))
	}); err != nil {
		return blocked(res, "world call failed: "+err.Error())
	}
	o.printf("before save:")
	o.printf("  sign     : %s", signBefore)
	o.printf("  campfire : %s", fireBefore)

	h.World.Save()
	if err := h.Do(func(tx *world.Tx, p *player.Player) { p.Teleport(far) }); err != nil {
		return blocked(res, "world call failed: "+err.Error())
	}
	unloaded := false
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		var loaded bool
		if err := h.InWorld(func(tx *world.Tx) { _, loaded = tx.BlockLoaded(signPos) }); err != nil {
			return blocked(res, "world call failed: "+err.Error())
		}
		if !loaded {
			unloaded = true
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if !unloaded {
		return blocked(res, "the chunk holding the sign and campfire never unloaded, so no reload could be observed")
	}
	o.printf("")
	o.printf("world saved, player moved away, chunk unloaded (tx.BlockLoaded reports false)")

	var signAfterStr, fireAfterStr string
	var signAfter block.Sign
	var fireAfter block.Campfire
	if err := h.Do(func(tx *world.Tx, p *player.Player) {
		signAfter = tx.Block(signPos).(block.Sign)
		fireAfter = tx.Block(firePos).(block.Campfire)
		signAfterStr = describeSignFull(signAfter)
		fireAfterStr = describeCampfire(fireAfter)
	}); err != nil {
		return blocked(res, "world call failed: "+err.Error())
	}
	o.printf("")
	o.printf("after reload from disk:")
	o.printf("  sign     : %s", signAfterStr)
	o.printf("  campfire : %s", fireAfterStr)

	var lost []string
	if signAfter.Waxed != before.Waxed {
		lost = append(lost, fmt.Sprintf("sign Waxed %v -> %v", before.Waxed, signAfter.Waxed))
	}
	if signAfter.Front.BaseColour != before.Front.BaseColour {
		lost = append(lost, fmt.Sprintf("sign front colour %v -> %v", before.Front.BaseColour, signAfter.Front.BaseColour))
	}
	if signAfter.Front.Glowing != before.Front.Glowing {
		lost = append(lost, fmt.Sprintf("sign front glowing %v -> %v", before.Front.Glowing, signAfter.Front.Glowing))
	}
	if signAfter.Front.Owner != before.Front.Owner {
		lost = append(lost, fmt.Sprintf("sign front owner %q -> %q", before.Front.Owner, signAfter.Front.Owner))
	}
	for i := range 2 {
		if fireAfter.Items[i].Time != campBefore.Items[i].Time {
			lost = append(lost, fmt.Sprintf("campfire slot %d time %s -> %s", i, campBefore.Items[i].Time, fireAfter.Items[i].Time))
		}
		if itemName(fireAfter.Items[i].Item) != itemName(campBefore.Items[i].Item) {
			lost = append(lost, fmt.Sprintf("campfire slot %d item %s -> %s", i,
				itemName(campBefore.Items[i].Item), itemName(fireAfter.Items[i].Item)))
		}
	}
	o.printf("")
	o.printf("fields that changed across the save/reload:")
	if len(lost) == 0 {
		o.printf("  none")
	}
	for _, l := range lost {
		o.printf("  %s", l)
	}

	res.Observed = o.String()
	res.Expected = "Everything a player can set on a sign (text, dye colour, glow, wax, owner) and everything a\n" +
		"campfire holds (the food and how far along it is) should survive a save and a reload unchanged."

	if len(lost) > 0 {
		res.Verdict = Reproduced
		res.Summary = fmt.Sprintf("%d fields lost across the save/reload: %s", len(lost), strings.Join(lost, "; "))
	} else {
		res.Verdict = NotReproduced
		res.Summary = "everything survived the save/reload"
	}
	return res
}
