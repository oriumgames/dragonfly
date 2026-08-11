package main

import (
	"context"
	"sync"
	"time"

	"github.com/df-mc/dragonfly/server/world"
	"github.com/df-mc/dragonfly/server/world/chunk"
)

// call runs f on w's owner with a bounded context so a wedged world surfaces
// as an error instead of hanging the scenario forever.
func call(w *world.World, d time.Duration, f func(tx *world.Tx)) error {
	ctx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()
	_, err := world.Call(ctx, w, func(tx *world.Tx) (struct{}, error) {
		f(tx)
		return struct{}{}, nil
	})
	return err
}

// callVal is call with a return value.
func callVal[T any](w *world.World, d time.Duration, f func(tx *world.Tx) T) (T, error) {
	ctx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()
	return world.Call(ctx, w, func(tx *world.Tx) (T, error) {
		return f(tx), nil
	})
}

// countingProvider wraps a real world.Provider and counts StoreColumn calls
// per chunk position. It is a pass-through: the wrapped provider still does
// all the real work.
type countingProvider struct {
	world.Provider

	mu     sync.Mutex
	stores map[world.ChunkPos]int
	loads  map[world.ChunkPos]int
	total  int
	bytes  int
}

func newCountingProvider(p world.Provider) *countingProvider {
	return &countingProvider{Provider: p, stores: map[world.ChunkPos]int{}, loads: map[world.ChunkPos]int{}}
}

func (c *countingProvider) StoreColumn(pos world.ChunkPos, dim world.Dimension, col *chunk.Column) error {
	c.mu.Lock()
	c.stores[pos]++
	c.total++
	for _, b := range chunk.Encode(col.Chunk, chunk.DiskEncoding).SubChunks {
		c.bytes += len(b)
	}
	c.mu.Unlock()
	return c.Provider.StoreColumn(pos, dim, col)
}

func (c *countingProvider) LoadColumn(pos world.ChunkPos, dim world.Dimension) (*chunk.Column, error) {
	c.mu.Lock()
	c.loads[pos]++
	c.mu.Unlock()
	return c.Provider.LoadColumn(pos, dim)
}

func (c *countingProvider) Totals() (total, bytes int, per map[world.ChunkPos]int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	per = map[world.ChunkPos]int{}
	for k, v := range c.stores {
		per[k] = v
	}
	return c.total, c.bytes, per
}
