package main

import (
	"time"
)

// p2-02 and p2-03 both live inside session.ItemStackRequestHandler, whose
// state (ignoreDestroy, pendingResults) is unexported and only mutated by a
// scripted sequence of protocol actions against a live beacon or crafting
// window. Both scenarios document exactly what would be needed.

func scenarioIgnoreDestroy() *Scenario {
	return &Scenario{
		ID:    "p2-02-ignoredestroy-not-reset",
		Part:  2,
		Title: "h.ignoreDestroy is not reset on the failure path",
		Claim: "`h.ignoreDestroy` is not reset on the failure path in `handler_item_stack_request.go`, so a later `Destroy` becomes a silent no-op.",
		Setup: "`ItemStackRequestHandler.ignoreDestroy` is set true in exactly one place, `handler_beacon.go`, after a successful `BeaconPaymentStackRequestAction`. It is cleared in exactly one place, the success branch of the deferred function in `handleRequest`:\n\n" +
			"    defer func() {\n" +
			"        if err != nil {\n" +
			"            h.reject(req.RequestID, s, tx)\n" +
			"            return          // ignoreDestroy is NOT reset here\n" +
			"        }\n" +
			"        h.resolve(req.RequestID, s)\n" +
			"        h.ignoreDestroy = false\n" +
			"    }()\n\n" +
			"Neither `reject` nor `resolve` touches the flag, and `Handle` swallows the error and moves on to the next request.\n" +
			"Driving this end to end needs a live beacon window: a real beacon block with a valid pyramid beneath it, a payment item in the session's UI inventory, a `BeaconPaymentStackRequestAction` that passes `handler_beacon`'s effect and level validation, and a second action in the same request that errors.",
		Expected: "`ignoreDestroy` is cleared whichever way the request ends, so a later Destroy action is honoured.",
		Timeout:  30 * time.Second,
		Run: func(o *Out) {
			o.Logf("ignoreDestroy is an unexported field of session.ItemStackRequestHandler.")
			o.Logf("The only writer that sets it true is handler_beacon.go:59, reached only from a fully validated")
			o.Logf("BeaconPaymentStackRequestAction: it needs a real beacon block with a valid pyramid, an active")
			o.Logf("beacon window, and a payment item already sitting in the session's UI inventory.")
			o.Logf("The only writer that sets it false is the success branch of handleRequest's deferred function.")
			o.Logf("This harness can build a real Session and feed it real ItemStackRequest packets, but it cannot")
			o.Logf("assemble a valid beacon payment without also driving the beacon's block-entity state, so the")
			o.Logf("flag can never be set true here and the failure path cannot be observed.")
			o.Verdict(Blocked, "reaching the only writer that sets ignoreDestroy needs a fully validated beacon payment window; not driven in this harness. The missing reset on the reject branch is unambiguous by inspection but is not demonstrated here.")
		},
	}
}

func scenarioCreateResults() *Scenario {
	return &Scenario{
		ID:    "p2-03-createresults-slot-zero",
		Part:  2,
		Title: "createResults passes a hardcoded ResultsSlot: 0",
		Claim: "`createResults` passes a hardcoded `ResultsSlot: 0`, so a single-result craft takes the wrong pending result - reported as a loss, not a gain.",
		Setup: "`createResults` appends to `h.pendingResults` and then, for a single result, calls `handleCreate(defaultCreation, ...)` where `defaultCreation` is a package-level `&protocol.CreateStackRequestAction{}` whose `ResultsSlot` is the zero value. `handleCreate` reads `h.pendingResults[slot]`, i.e. always index 0.\n" +
			"`pendingResults` is cleared per request, not per action, and `handleRequest` loops over every action in a request, so a request that crafts twice - or a multi-result craft followed by a single-result one - leaves index 0 already consumed.\n" +
			"Driving this needs a real `CraftRecipeStackRequestAction` carrying a recipe index into the session's own recipe map, with the ingredients laid out in the session's UI inventory, followed by a second craft action in the same request.",
		Expected: "A single-result craft consumes the result it just appended, not index 0.",
		Timeout:  30 * time.Second,
		Run: func(o *Out) {
			o.Logf("createResults appends to h.pendingResults and then always indexes slot 0 via the shared")
			o.Logf("package-level defaultCreation = &protocol.CreateStackRequestAction{} (ResultsSlot zero value).")
			o.Logf("h.pendingResults and h.handleCreate are unexported; the only way in is a real")
			o.Logf("CraftRecipeStackRequestAction, which needs the session's recipe map index plus ingredients")
			o.Logf("laid out in the unexported UI inventory, and then a second craft action in the same request.")
			o.Logf("This harness can feed real ItemStackRequest packets to a real Session, but assembling two")
			o.Logf("chained valid crafts (one multi-result, one single-result) was not attempted here.")
			o.Verdict(Blocked, "needs two chained valid craft actions inside one ItemStackRequest against a live crafting window; not driven in this harness. The hardcoded index is unambiguous by inspection but is not demonstrated here.")
		},
	}
}
