package main

// scenarios returns every scenario in report order. Hidden scenarios are child
// processes spawned by another scenario and are skipped in the default run.
func scenarios() []*Scenario {
	return []*Scenario{
		// Part 1 - confirmed bugs, driven end to end.
		scenarioLoaderDeadlock(),
		scenarioTwoLoaders(),
		scenarioEntityLeak(),
		scenarioEntityDuplicate(),
		scenarioScheduledTwice(),
		scenarioSubChunkLayer(),
		scenarioNBTDurations(),
		scenarioWeatherStorm(),
		scenarioViewEntityArmour(),
		scenarioPlayerDrop(),
		scenarioInventoryRace(),
		scenarioChestPairing(),
		scenarioAreaEffectCloud(),

		// Part 2 - unverified leads, proved or refuted.
		scenarioFireworkWall(),
		scenarioIgnoreDestroy(),
		scenarioCreateResults(),
		scenarioEntTickAfterClose(),
		scenarioSaveChunkModified(),
		scenarioDeadSessionlessPlayer(),
		scenarioSessionCloseNilTx(),
		scenarioSetWorldAt(),
		scenarioExperienceOrb(),
		scenarioProjectileSurvive(),
		scenarioDetachedInventory(),

		// Child processes, reachable only through -only.
		scenarioLayer255Child(),
		scenarioInventoryRaceChild(),
	}
}

func visibleScenarios() []*Scenario {
	var out []*Scenario
	for _, s := range scenarios() {
		if !s.Hidden {
			out = append(out, s)
		}
	}
	return out
}
