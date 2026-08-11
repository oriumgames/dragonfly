package inventory

import (
	"errors"
	"sync"
	"testing"

	"github.com/df-mc/dragonfly/server/item"
)

// TestAddItemRejectedBySlotValidator verifies that items a slot validator refuses are not reported as added. AddItem
// returns the number of items it stored, and callers move that many out of the source, so counting a refused write as
// stored destroyed the items.
func TestAddItemRejectedBySlotValidator(t *testing.T) {
	inv := New(9, nil)
	inv.SlotValidatorFunc(func(item.Stack, int) bool { return false })

	n, err := inv.AddItem(item.NewStack(item.Diamond{}, 7))

	if err == nil {
		t.Error("AddItem() error = nil, want an error")
	}
	if n != 0 {
		t.Errorf("AddItem() added = %v, want 0", n)
	}
	if stored := len(inv.Items()); stored != 0 {
		t.Errorf("inventory holds %v stacks, want 0", stored)
	}
}

// TestSetItemRejectedBySlotValidator verifies that a write a slot validator refuses is reported rather than silently
// dropped, so that callers writing more than one slot can undo what they already wrote.
func TestSetItemRejectedBySlotValidator(t *testing.T) {
	inv := New(9, nil)
	inv.SlotValidatorFunc(func(it item.Stack, _ int) bool {
		_, diamond := it.Item().(item.Diamond)
		return it.Empty() || !diamond
	})

	if err := inv.SetItem(0, item.NewStack(item.Diamond{}, 1)); !errors.Is(err, ErrSlotRejected) {
		t.Errorf("SetItem() = %v, want ErrSlotRejected", err)
	}
	if err := inv.SetItem(0, item.NewStack(item.Stick{}, 1)); err != nil {
		t.Errorf("SetItem() = %v, want nil", err)
	}
}

// TestSwapRejectedBySlotValidator verifies that a swap the validator refuses leaves both slots as they were, rather
// than applying the half of it that was accepted.
func TestSwapRejectedBySlotValidator(t *testing.T) {
	inv := New(2, nil)
	inv.SlotValidatorFunc(func(it item.Stack, slot int) bool {
		_, diamond := it.Item().(item.Diamond)
		return slot != 1 || it.Empty() || !diamond
	})
	if err := inv.SetItem(0, item.NewStack(item.Diamond{}, 1)); err != nil {
		t.Fatalf("SetItem(0) = %v, want nil", err)
	}
	if err := inv.SetItem(1, item.NewStack(item.Stick{}, 1)); err != nil {
		t.Fatalf("SetItem(1) = %v, want nil", err)
	}

	if err := inv.Swap(0, 1); !errors.Is(err, ErrSlotRejected) {
		t.Errorf("Swap() = %v, want ErrSlotRejected", err)
	}

	first, _ := inv.Item(0)
	second, _ := inv.Item(1)
	if _, ok := first.Item().(item.Diamond); !ok {
		t.Errorf("slot 0 holds %v, want a diamond", first)
	}
	if _, ok := second.Item().(item.Stick); !ok {
		t.Errorf("slot 1 holds %v, want a stick", second)
	}
}

// TestSlotFuncRace verifies that changing the function called on a slot change does not race with a slot being
// written. setItem returns a closure that is called after the inventory is unlocked, so the function it calls has to
// be read while the lock is still held.
//
// This only fails under the race detector: run it with go test -race.
func TestSlotFuncRace(t *testing.T) {
	tests := []struct {
		name string
		set  func(inv *Inventory)
	}{
		{name: "slot function replaced", set: func(inv *Inventory) { inv.SlotFunc(func(int, item.Stack, item.Stack) {}) }},
		{name: "inventory closed", set: func(inv *Inventory) { _ = inv.Close() }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			inv := New(1, func(int, item.Stack, item.Stack) {})

			var wg sync.WaitGroup
			wg.Add(2)
			go func() {
				defer wg.Done()
				for range 200 {
					_ = inv.SetItem(0, item.NewStack(item.Diamond{}, 1))
				}
			}()
			go func() {
				defer wg.Done()
				for range 200 {
					tt.set(inv)
				}
			}()
			wg.Wait()
		})
	}
}
