package main

import (
	"strings"
	"sync"

	"github.com/df-mc/dragonfly/server/block/cube"
	"github.com/df-mc/dragonfly/server/world"
	"github.com/df-mc/dragonfly/server/world/chunk"
)

// countingViewer is a world.Viewer that records what the world sends it. It
// embeds world.NopViewer, so it is the same Viewer surface a Session presents.
type countingViewer struct {
	world.NopViewer

	mu       sync.Mutex
	viewed   int
	shown    int
	hidden   int
	armour   int
	lastArmr world.Entity
}

func (v *countingViewer) ViewChunk(world.ChunkPos, world.Dimension, map[cube.Pos]world.Block, *chunk.Chunk) {
	v.mu.Lock()
	v.viewed++
	v.mu.Unlock()
}

func (v *countingViewer) ViewEntity(world.Entity) {
	v.mu.Lock()
	v.shown++
	v.mu.Unlock()
}

func (v *countingViewer) HideEntity(world.Entity) {
	v.mu.Lock()
	v.hidden++
	v.mu.Unlock()
}

func (v *countingViewer) ViewEntityArmour(e world.Entity) {
	v.mu.Lock()
	v.armour++
	v.lastArmr = e
	v.mu.Unlock()
}

func (v *countingViewer) chunks() int {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.viewed
}

func (v *countingViewer) counts() (viewed, shown, hidden int) {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.viewed, v.shown, v.hidden
}

func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(strings.TrimRight(s, "\n"), "\n")
}
