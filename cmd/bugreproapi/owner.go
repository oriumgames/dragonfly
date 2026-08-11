package main

import (
	"time"

	"github.com/df-mc/dragonfly/server/world"
)

// pinnedOwner parks a World's owner goroutine inside a single long-running
// transaction and lets the scenario submit work that runs on that goroutine,
// with that transaction. It is the in-process equivalent of "this code runs on
// the world's own goroutine", which is where all of dragonfly's block, entity
// and loader code actually runs.
type pinnedOwner struct {
	w     *world.World
	jobs  chan func(*world.Tx)
	inTx  chan struct{}
	ended chan struct{}
}

func pin(w *world.World) *pinnedOwner {
	p := &pinnedOwner{w: w, jobs: make(chan func(*world.Tx)), inTx: make(chan struct{}), ended: make(chan struct{})}
	w.Do(func(tx *world.Tx) {
		close(p.inTx)
		for f := range p.jobs {
			f(tx)
		}
	})
	<-p.inTx
	return p
}

// do runs f on the owner goroutine and waits for it, up to d. It returns false
// if the owner did not pick the job up (it is stuck) or did not finish it.
func (p *pinnedOwner) do(f func(*world.Tx), d time.Duration) bool {
	done := make(chan struct{})
	select {
	case p.jobs <- func(tx *world.Tx) { defer close(done); f(tx) }:
	case <-time.After(d):
		return false
	}
	select {
	case <-done:
		return true
	case <-time.After(d):
		return false
	}
}

// start submits f without waiting for it to finish. It returns a channel that
// closes when f returns.
func (p *pinnedOwner) start(f func(*world.Tx), d time.Duration) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		select {
		case p.jobs <- func(tx *world.Tx) { defer close(done); f(tx) }:
		case <-time.After(d):
		}
	}()
	return done
}

func (p *pinnedOwner) release() {
	defer func() { _ = recover() }()
	close(p.jobs)
}

// saturate submits n no-op tasks to w. The World's transaction queue is 128
// deep; while its owner is busy the queue fills and every further sender
// blocks, which is the state a busy world is in whenever a transaction takes
// longer than the tick budget.
func saturate(w *world.World, n int) {
	for range n {
		w.Do(func(*world.Tx) {})
	}
}

// alive reports whether the World still handles transactions within d.
func alive(w *world.World, d time.Duration) bool {
	return call(w, d, func(*world.Tx) {}) == nil
}
