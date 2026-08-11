package player

import (
	"testing"

	"github.com/df-mc/dragonfly/server/entity"
	"github.com/df-mc/dragonfly/server/world"
	"github.com/go-gl/mathgl/mgl64"
	"github.com/google/uuid"
)

// TestCloseSessionlessPlayer verifies that closing a player without a session
// removes it from its world and closes its handle, whether it is alive or dead.
// A dead player is respawned before it is torn down, but a player with no
// session has nothing to respawn for, so it must not take that path.
func TestCloseSessionlessPlayer(t *testing.T) {
	tests := []struct {
		name string
		kill bool
	}{
		{name: "alive player"},
		{name: "dead player", kill: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			w := world.Config{Synchronous: true}.New()
			defer w.Close()

			id, pos := uuid.New(), mgl64.Vec3{8, 64, 8}
			h := world.EntitySpawnOpts{Position: pos, ID: id}.New(Type, Config{UUID: id, Name: "Test", Position: pos})

			w.Do(func(tx *world.Tx) {
				p := tx.AddEntity(h).(*Player)
				if test.kill {
					p.Hurt(p.MaxHealth(), entity.VoidDamageSource{})
				}
				if got, want := p.Dead(), test.kill; got != want {
					t.Fatalf("Dead() before Close: got %v, want %v", got, want)
				}
				_ = p.Close()
			})

			if got, want := h.Closed(), true; got != want {
				t.Fatalf("handle closed after Close: got %v, want %v", got, want)
			}
			var present bool
			w.Do(func(tx *world.Tx) {
				for e := range tx.Entities() {
					if e.H() == h {
						present = true
					}
				}
			})
			if got, want := present, false; got != want {
				t.Fatalf("player still in the world after Close: got %v, want %v", got, want)
			}
		})
	}
}
