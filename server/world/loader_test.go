package world

import (
	"io"
	"log/slog"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/df-mc/dragonfly/server/block/cube"
	"github.com/df-mc/dragonfly/server/world/chunk"
	"github.com/go-gl/mathgl/mgl64"
)

// TestChangeWorldDoesNotBlockTheOwner verifies the shape of
// session.handleWorldSwitch: Loader.ChangeWorld runs on the *new* world's owner
// goroutine and does a blocking send on the *old* world's transaction queue
// (loader.go:52, l.w.exec). Two players swapping worlds in opposite directions
// while both queues are saturated makes the two owner goroutines block on each
// other's queue, freezing both worlds permanently.
func TestChangeWorldDoesNotBlockTheOwner(t *testing.T) {
	w1, w2 := asyncWorld(Config{}), asyncWorld(Config{})

	// a lives in w1 and is moved to w2 by a transaction on w2; b vice versa.
	a := NewLoader(2, w1, NopViewer{})
	b := NewLoader(2, w2, NopViewer{})

	park := func(w *World) chan struct{} {
		gate, started := make(chan struct{}), make(chan struct{})
		w.Do(func(tx *Tx) {
			close(started)
			<-gate
		})
		<-started
		return gate
	}
	gate1, gate2 := park(w1), park(w2)

	done := make(chan struct{}, 2)
	// Queue the world switches as the next item on each owner.
	w2.Do(func(tx *Tx) {
		a.ChangeWorld(tx, w2)
		done <- struct{}{}
	})
	w1.Do(func(tx *Tx) {
		b.ChangeWorld(tx, w1)
		done <- struct{}{}
	})

	// Saturate both owner queues: 128 buffered entries plus parked senders, so
	// a slot freed by an owner is immediately taken again.
	for range 400 {
		go w1.Save()
		go w2.Save()
	}
	time.Sleep(time.Millisecond * 300)

	close(gate1)
	close(gate2)

	deadline := time.After(time.Second * 10)
	for range 2 {
		select {
		case <-done:
		case <-deadline:
			buf := make([]byte, 1<<20)
			buf = buf[:runtime.Stack(buf, true)]
			for _, g := range strings.Split(string(buf), "\n\n") {
				if strings.Contains(g, "Loader).ChangeWorld") {
					t.Log("blocked world owner goroutine:\n" + g)
				}
			}
			t.Fatal("Loader.ChangeWorld blocked both world owner goroutines on each other's transaction queue")
		}
	}
	w1.Close()
	w2.Close()
}

// TestLoaderViewChunkReentrant verifies that viewChunk returns when the Viewer it calls reads a block from a chunk
// that has a background load in flight. Completing that load runs the request's callbacks inline on the same
// goroutine, and the callback a Loader registers is viewChunk itself, so holding the Loader's lock across the Viewer
// deadlocks the world's transaction goroutine against itself.
//
// A real session reaches the same read through Player.Breathing, which reads the liquid at the player's eye when the
// player is shown to a viewer. That position need not be in the chunk being viewed.
func TestLoaderViewChunkReentrant(t *testing.T) {
	tests := []struct {
		name string
		// probeFromChunk reads the block from ViewChunk rather than from ViewEntity, covering the other call-out the
		// Loader used to make while holding its lock.
		probeFromChunk bool
	}{
		{name: "viewer reads a block while an entity is shown to it"},
		{name: "viewer reads a block while a chunk is shown to it", probeFromChunk: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// The World must not be synchronous: loadChunkAsync never registers a request on one, so nothing can be
			// in flight to re-enter the Loader.
			w := newLoaderTestWorld(t)
			// A block position inside the chunk that is left to load in the background.
			v := &probeViewer{probe: cube.Pos{20, 64, 4}, fromChunk: tt.probeFromChunk}
			l := NewLoader(8, w, v)
			viewed, background := ChunkPos{0, 0}, ChunkPos{1, 0}

			returned := make(chan struct{})
			w.Do(func(tx *Tx) {
				defer close(returned)
				v.tx = tx

				// The viewed chunk must be resident and hold an entity, so that adding the Viewer to it shows one.
				c := tx.chunk(viewed)
				tx.AddEntity(EntitySpawnOpts{Position: mgl64.Vec3{8, 64, 8}}.New(taskTestEntityType{}, taskTestEntityConfig{}))

				// Put a load of the background chunk in flight, registered by this Loader exactly as Load does.
				l.mu.Lock()
				l.pending[background] = struct{}{}
				l.mu.Unlock()
				if !w.loadChunkAsync(tx, background, func(tx *Tx, c *Column) { l.viewChunk(tx, background, c) }) {
					t.Error("loadChunkAsync() = false, want true: no background load was scheduled")
					return
				}

				l.viewChunk(tx, viewed, c)

				if !v.probed {
					t.Error("the Viewer never read a block: it was not called")
				}
			})

			select {
			case <-returned:
			case <-time.After(time.Second * 10):
				t.Fatal("viewChunk did not return: the Loader deadlocked against itself")
			}
		})
	}
}

// newLoaderTestWorld returns a World that discards its log output. It is closed on a separate goroutine when the test
// ends, because a World whose transaction goroutine is stuck can never complete a Close.

// asyncWorld returns a World that ticks on its own goroutines and discards its log output.
func asyncWorld(conf Config) *World {
	conf.Log = slog.New(slog.NewTextHandler(io.Discard, nil))
	return conf.New()
}

func newLoaderTestWorld(t *testing.T) *World {
	t.Helper()

	w := Config{Log: slog.New(slog.NewTextHandler(io.Discard, nil))}.New()
	t.Cleanup(func() {
		closed := make(chan struct{})
		go func() {
			_ = w.Close()
			close(closed)
		}()
		select {
		case <-closed:
		case <-time.After(time.Second * 10):
			t.Log("world could not be closed: its transaction goroutine is stuck")
		}
	})
	return w
}

// probeViewer reads a block from the world when it is shown a chunk or an entity, as a session does when it encodes
// the metadata of a player being shown to it.
type probeViewer struct {
	NopViewer
	tx        *Tx
	probe     cube.Pos
	fromChunk bool
	probed    bool
}

func (v *probeViewer) ViewChunk(ChunkPos, Dimension, map[cube.Pos]Block, *chunk.Chunk) {
	if v.fromChunk {
		v.read()
	}
}

func (v *probeViewer) ViewEntity(Entity) {
	if !v.fromChunk {
		v.read()
	}
}

func (v *probeViewer) read() {
	v.probed = true
	_, _ = v.tx.Liquid(v.probe)
}
