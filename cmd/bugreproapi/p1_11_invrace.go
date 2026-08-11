package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/df-mc/dragonfly/server/item"
	"github.com/df-mc/dragonfly/server/item/inventory"
)

// p1_11 runs the inventory race under the real race detector by re-running
// this same package with `go run -race`.
func scenarioInventoryRace() *Scenario {
	return &Scenario{
		ID:    "p1-11-inventory-race",
		Part:  1,
		Title: "Inventory data race between a slot change and SlotFunc/Close",
		Claim: "`Inventory` data race between a slot change and `SlotFunc`/`Close` - run under `-race` and capture the warning.",
		Setup: "`go run -race ./cmd/bugreproapi -only=p1-11-child-invrace` is spawned as a child process. The child drives a real `inventory.Inventory`: one goroutine calls `SetItem` in a loop, another calls `SlotFunc` and finally `Close`.\n" +
			"`Inventory.setItem` returns a closure that reads `inv.f` and `SetItem` runs it *after* releasing `inv.mu`, while `SlotFunc` and `Close` write `inv.f` under the write lock.",
		Expected: "No race is reported: a callback registration must not race with a slot change.",
		Timeout:  240 * time.Second,
		Run:      runInventoryRace,
	}
}

func runInventoryRace(o *Out) {
	self, err := os.Getwd()
	if err != nil {
		o.Verdict(Blocked, "getwd: %v", err)
		return
	}
	o.Logf("running: go run -race ./cmd/bugreproapi -only=p1-11-child-invrace")
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "go", "run", "-race", "./cmd/bugreproapi", "-only=p1-11-child-invrace", "-no-reports=1", "-quiet")
	cmd.Dir = self
	out, runErr := cmd.CombinedOutput()
	text := string(out)
	for _, line := range splitLines(text) {
		o.Logf("child: %s", line)
	}
	if runErr != nil {
		o.Logf("child exited with: %v", runErr)
	}
	if strings.Contains(text, "WARNING: DATA RACE") {
		o.Verdict(Reproduced, "the race detector reported a data race between Inventory.setItem's deferred callback (reading inv.f without the lock) and SlotFunc/Close writing it")
		return
	}
	if strings.Contains(text, "go: ") || strings.Contains(text, "cannot") {
		o.Verdict(Blocked, "the -race child could not be built or run; see the output")
		return
	}
	o.Verdict(Refuted, "no data race was reported by the race detector")
}

func scenarioInventoryRaceChild() *Scenario {
	return &Scenario{
		ID:      "p1-11-child-invrace",
		Part:    1,
		Hidden:  true,
		Title:   "child: inventory race",
		Timeout: 60 * time.Second,
		Run: func(o *Out) {
			inv := inventory.New(9, func(int, item.Stack, item.Stack) {})
			stop := make(chan struct{})
			var wg sync.WaitGroup
			wg.Add(2)
			go func() {
				defer wg.Done()
				for i := 0; ; i++ {
					select {
					case <-stop:
						return
					default:
					}
					_ = inv.SetItem(i%9, item.NewStack(item.Diamond{}, 1))
				}
			}()
			go func() {
				defer wg.Done()
				for i := 0; i < 20000; i++ {
					inv.SlotFunc(func(int, item.Stack, item.Stack) {})
				}
				_ = inv.Close()
				close(stop)
			}()
			wg.Wait()
			fmt.Println("INVRACE-CHILD-DONE")
			o.Verdict(Reproduced, "child finished")
		},
	}
}
