package main

import (
	"fmt"
	"time"

	"github.com/df-mc/dragonfly/server/block"
	"github.com/df-mc/dragonfly/server/block/cube"
	"github.com/df-mc/dragonfly/server/player"
	"github.com/df-mc/dragonfly/server/world"
	"github.com/go-gl/mathgl/mgl64"
)

func init() {
	register(Scenario{
		ID:      "18-lightning-targets-by-z",
		Title:   "Lightning entity selection compares the Z coordinate instead of the height",
		Timeout: 180 * time.Second,
		Bug: "`weather.adjustPositionToEntities` decides which entities are exposed to a lightning strike:\n\n" +
			"```go\n" +
			"pos := cube.PosFromVec3(e.Position())\n" +
			"if tx.HighestBlock(pos[0], pos[1]) < pos[2] {\n" +
			"    list = append(list, e.Position())\n" +
			"}\n" +
			"```\n\n" +
			"`cube.Pos` is `{x, y, z}` and `HighestBlock` takes `(x, z)`. The call passes the entity's **Y**\n" +
			"as the Z of the column, and compares the resulting height against the entity's **Z** instead of\n" +
			"its Y. It should read `tx.HighestBlock(pos[0], pos[2]) < pos[1]`. Two entities standing at the\n" +
			"same height over identical terrain are therefore treated differently purely because of their Z\n" +
			"coordinate.",
		Run: runLightningTarget,
	})
}

func runLightningTarget() Result {
	h, err := startHarness(harnessOpts{withClient: true, randomTickSpeed: -1})
	if err != nil {
		return Result{Verdict: Blocked, Reason: "could not start harness: " + err.Error()}
	}
	defer h.Stop()

	var o out
	res := Result{
		Setup: "Two real players standing on identical stone pillars at the same height (y = -40) over the\n" +
			"same flat terrain, one at z = 200 and one at z = -200. Both are equally exposed to the sky.",
		ServerSteps: []string{
			"built the two pillars with `tx.SetBlock` and teleported one player onto each",
			"read both players' real positions back out of the world",
			"evaluated the exact predicate `weather.adjustPositionToEntities` uses, using the public `tx.HighestBlock`",
		},
		ClientSteps: []string{
			"two real gophertunnel clients provide the two player entities and keep the chunks loaded",
		},
	}

	p2, err := h.AddClient()
	if err != nil {
		return blocked(res, "could not connect the second client: "+err.Error())
	}

	type spot struct {
		name string
		pos  mgl64.Vec3
	}
	spots := []spot{
		{"player A", mgl64.Vec3{200.5, -40, 200.5}},
		{"player B", mgl64.Vec3{200.5, -40, -200.5}},
	}
	handles := []*world.EntityHandle{h.PlayerH, p2}

	for i, s := range spots {
		sp := s
		if err := h.DoEntity(handles[i], func(tx *world.Tx, p *player.Player) {
			preparePlayer(tx, p, sp.pos)
			base := cube.PosFromVec3(sp.pos).Sub(cube.Pos{0, 1})
			for y := base[1]; y >= -61; y-- {
				tx.SetBlock(cube.Pos{base[0], y, base[2]}, block.Stone{}, nil)
			}
		}); err != nil {
			return blocked(res, "world call failed: "+err.Error())
		}
	}
	time.Sleep(1 * time.Second)

	type eval struct {
		name                 string
		pos                  cube.Pos
		buggyHeight          int
		buggyEligible        bool
		correctHeight        int
		correctEligible      bool
		buggyArgs, rightArgs string
	}
	var evals []eval
	for i, s := range spots {
		var e eval
		e.name = s.name
		if err := h.DoEntity(handles[i], func(tx *world.Tx, p *player.Player) {
			pos := cube.PosFromVec3(p.Position())
			e.pos = pos
			// Exactly what the source does today.
			e.buggyHeight = tx.HighestBlock(pos[0], pos[1])
			e.buggyEligible = e.buggyHeight < pos[2]
			e.buggyArgs = fmt.Sprintf("HighestBlock(x=%d, y=%d)=%d < z=%d", pos[0], pos[1], e.buggyHeight, pos[2])
			// What it should do.
			e.correctHeight = tx.HighestBlock(pos[0], pos[2])
			e.correctEligible = e.correctHeight < pos[1]
			e.rightArgs = fmt.Sprintf("HighestBlock(x=%d, z=%d)=%d < y=%d", pos[0], pos[2], e.correctHeight, pos[1])
		}); err != nil {
			return blocked(res, "world call failed: "+err.Error())
		}
		evals = append(evals, e)
	}

	for _, e := range evals {
		o.printf("%s at %v (standing on a pillar, same height, same terrain)", e.name, e.pos)
		o.printf("  current code : %s  -> eligible for a strike: %v", e.buggyArgs, e.buggyEligible)
		o.printf("  correct code : %s  -> eligible for a strike: %v", e.rightArgs, e.correctEligible)
		o.printf("")
	}

	differs := evals[0].buggyEligible != evals[1].buggyEligible &&
		evals[0].correctEligible == evals[1].correctEligible

	o.printf("both players are at the same Y (%d) over the same terrain, so the correct predicate agrees for both (%v/%v),",
		evals[0].pos[1], evals[0].correctEligible, evals[1].correctEligible)
	o.printf("while the shipped predicate disagrees (%v/%v) purely because their Z coordinates differ (%d vs %d).",
		evals[0].buggyEligible, evals[1].buggyEligible, evals[0].pos[2], evals[1].pos[2])

	res.Observed = o.String()
	res.Expected = "Two entities at the same height over identical terrain should both be equally eligible to be\n" +
		"struck. The predicate should be `tx.HighestBlock(pos[0], pos[2]) < pos[1]`."

	res.Verdict = Blocked
	res.Reason = "The faulty predicate is inside the unexported `weather.adjustPositionToEntities`, which is only\n" +
		"reachable from `weather.tickLightning`:\n\n" +
		"```go\n" +
		"for pos := range w.w.chunks {\n" +
		"    // 1/100,000 chance per loaded chunk per tick\n" +
		"    if w.w.r.IntN(100000) == 0 { positions = append(positions, pos) }\n" +
		"}\n```\n\n" +
		"There is no public API that spawns lightning through that path - `entity.NewLightning` skips it\n" +
		"entirely - and the random gate means a *specific* chunk gets a strike attempt roughly once every\n" +
		"100,000 ticks (about 83 minutes), and the strike then lands on a random one of that chunk's 256\n" +
		"columns, of which only about 49 are within the 3-block search box of a given entity. Waiting for a\n" +
		"natural strike on a chosen entity is on the order of days, and the world's RNG (`w.r`) is\n" +
		"unexported so it cannot be forced.\n\n" +
		"What the harness *did* observe end to end, on real player entities in a real running world, is the\n" +
		"predicate itself: with two real players at the same Y over identical terrain, the expression the\n" +
		"shipped code evaluates returns different answers for them, while the corrected expression returns\n" +
		"the same answer for both. That is printed verbatim above. The lightning strike itself was not\n" +
		"observed and is not being claimed."
	if differs {
		res.Summary = fmt.Sprintf("predicate observed to disagree (A eligible=%v, B eligible=%v) for two players at the same Y; the strike itself is gated behind a 1/100,000-per-chunk-per-tick roll in unexported code",
			evals[0].buggyEligible, evals[1].buggyEligible)
	} else {
		res.Summary = "could not construct two positions where the shipped and corrected predicates disagree; the strike path itself is unreachable from any public API"
	}
	return res
}
