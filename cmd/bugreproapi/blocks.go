package main

import (
	"math/rand/v2"
	"sync/atomic"

	"github.com/df-mc/dragonfly/server/block/cube"
	"github.com/df-mc/dragonfly/server/world"
)

// counterBlock is a real world.Block registered onto a vanilla block state
// that dragonfly does not implement itself. Its ScheduledTick counts how many
// times the world's scheduled update queue fired it.
type counterBlock struct {
	name string
}

var scheduledTickCount atomic.Int64

func (b counterBlock) EncodeBlock() (string, map[string]any) { return b.name, nil }
func (b counterBlock) Hash() (uint64, uint64)                { return 1<<62 | 7, 0 }
func (b counterBlock) Model() world.BlockModel               { return solidModel{} }
func (b counterBlock) ScheduledTick(cube.Pos, *world.Tx, *rand.Rand) {
	scheduledTickCount.Add(1)
}

type solidModel struct{}

func (solidModel) BBox(cube.Pos, world.BlockSource) []cube.BBox { return nil }
func (solidModel) FaceSolid(cube.Pos, cube.Face, world.BlockSource) bool {
	return false
}

// registerCounterBlock registers counterBlock onto the first vanilla state in
// candidates that has no Go implementation yet, and returns the block.
func registerCounterBlock(br world.BlockRegistry) (world.Block, bool) {
	candidates := []string{
		"minecraft:sculk", "minecraft:calcite", "minecraft:tuff", "minecraft:dripstone_block",
		"minecraft:moss_block", "minecraft:rooted_dirt", "minecraft:mud", "minecraft:sculk_catalyst",
		"minecraft:amethyst_block", "minecraft:deepslate_bricks", "minecraft:packed_mud",
	}
	for _, name := range candidates {
		b := counterBlock{name: name}
		if tryRegister(br, b) {
			return b, true
		}
	}
	return nil, false
}

func tryRegister(br world.BlockRegistry, b world.Block) (ok bool) {
	defer func() {
		if recover() != nil {
			ok = false
		}
	}()
	br.RegisterBlock(b)
	return true
}
