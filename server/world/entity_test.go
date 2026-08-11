package world

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/go-gl/mathgl/mgl64"
)

// TestScheduledEntityTaskFollowsEntityIntoWorld verifies that a task scheduled
// on an entity that is in no world yet learns about the world the entity is
// later added to, so that closing that world stops the task for the same reason
// it stops a task scheduled once the entity was already in it.
func TestScheduledEntityTaskFollowsEntityIntoWorld(t *testing.T) {
	tests := []struct {
		name          string
		scheduleFirst bool
		want          error
	}{
		{name: "scheduled while worldless", scheduleFirst: true, want: ErrWorldClosed},
		{name: "scheduled once in a world", scheduleFirst: false, want: ErrWorldClosed},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			w := Config{}.New()
			h := EntitySpawnOpts{Position: mgl64.Vec3{8, 64, 8}}.New(testEntityType{}, testEntityConfig{})

			var task *Task
			if test.scheduleFirst {
				task = h.DoAfter(time.Hour, func(*Tx, Entity) {})
				parked := waitForWorldChangedChan(h, nil, time.Second*5)
				if parked == nil {
					t.Fatal("scheduled task never parked on the handle's world-changed channel")
				}
				<-w.exec(func(tx *Tx) { tx.AddEntity(h) })
				// Give the task a chance to re-park on the world it just
				// joined before the world is closed under it.
				waitForWorldChangedChan(h, parked, time.Second*2)
			} else {
				<-w.exec(func(tx *Tx) { tx.AddEntity(h) })
				task = h.DoAfter(time.Hour, func(*Tx, Entity) {})
				if waitForWorldChangedChan(h, nil, time.Second*5) == nil {
					t.Fatal("scheduled task never parked on the handle's world-changed channel")
				}
			}

			w.Close()

			ctx, cancel := context.WithTimeout(context.Background(), time.Second*5)
			defer cancel()
			if got := task.Wait(ctx); !errors.Is(got, test.want) {
				t.Fatalf("scheduled task error: got %v, want %v", got, test.want)
			}
		})
	}
}

// waitForWorldChangedChan waits until the handle's world-changed channel is set
// to a channel other than prev and returns it, or nil if that did not happen
// within timeout. A scheduler goroutine creates the channel when it parks, so
// this reports that it parked with the handle's current world.
func waitForWorldChangedChan(h *EntityHandle, prev chan struct{}, timeout time.Duration) chan struct{} {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		h.cond.L.Lock()
		c := h.worldChanged
		h.cond.L.Unlock()
		if c != nil && c != prev {
			return c
		}
		time.Sleep(time.Millisecond)
	}
	return nil
}
