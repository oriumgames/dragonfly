package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

func writeReports(dir string, results []result) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	for _, r := range results {
		if err := os.WriteFile(filepath.Join(dir, r.s.ID+".md"), []byte(itemReport(r)), 0o644); err != nil {
			return err
		}
	}
	if len(results) == len(visibleScenarios()) {
		if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte(readme(results)), 0o644); err != nil {
			return err
		}
	}
	return nil
}

func itemReport(r result) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# %s — %s\n\n", r.s.ID, r.s.Title)
	fmt.Fprintf(&b, "**Verdict: %s** — %s\n\n", r.verdict, r.reason)
	fmt.Fprintf(&b, "| | |\n|---|---|\n")
	fmt.Fprintf(&b, "| Part | %d |\n", r.s.Part)
	fmt.Fprintf(&b, "| Scenario timeout | %s |\n", r.s.Timeout)
	fmt.Fprintf(&b, "| Wall time | %s |\n", r.duration.Round(time.Millisecond))
	fmt.Fprintf(&b, "| Hit the timeout | %v |\n", r.timedOut)
	fmt.Fprintf(&b, "\n## Claim\n\n%s\n", r.s.Claim)
	fmt.Fprintf(&b, "\n## Setup\n\n%s\n", r.s.Setup)
	fmt.Fprintf(&b, "\n## Expected\n\n%s\n", r.s.Expected)
	fmt.Fprintf(&b, "\n## Observed output (verbatim)\n\n```\n%s\n```\n", r.output)
	if r.panicked != "" {
		fmt.Fprintf(&b, "\n## Unrecovered panic\n\n```\n%s\n```\n", r.panicked)
	}
	fmt.Fprintf(&b, "\n## How to re-run\n\n```\ngo run ./cmd/bugreproapi -only=%s\n```\n", r.s.ID)
	return b.String()
}

func readme(results []result) string {
	var b strings.Builder
	b.WriteString("# dragonfly bug reproductions (library level)\n\n")
	b.WriteString("These are the dragonfly bugs that cannot be driven from a Bedrock client: they live\n")
	b.WriteString("below the network layer, in `server/world`, `server/entity`, `server/item/inventory`\n")
	b.WriteString("and `server/session`. Every item below is exercised in process against the real\n")
	b.WriteString("`world.World`, real `world.Tx`, real entities and (where a disk round trip is needed)\n")
	b.WriteString("a real `mcdb` provider on a temp directory.\n\n")

	b.WriteString("## How to run\n\n")
	b.WriteString("```\ngo run ./cmd/bugreproapi              # everything, prints the summary table\n")
	b.WriteString("go run ./cmd/bugreproapi -only=<id>    # one scenario\n")
	b.WriteString("go run ./cmd/bugreproapi -quiet        # summary only\n```\n\n")
	b.WriteString("Each scenario runs under its own timeout, so a deadlock cannot stall the run: the\n")
	b.WriteString("harness abandons the hung goroutine, records the goroutine dump and carries on.\n")
	b.WriteString("Reports are written to this directory, one markdown file per item.\n\n")

	b.WriteString("## Summary\n\n")
	b.WriteString("| # | Item | Verdict | Note |\n|---|---|---|---|\n")
	for _, r := range results {
		note := strings.ReplaceAll(r.reason, "|", "\\|")
		fmt.Fprintf(&b, "| [%s](%s.md) | %s | **%s** | %s |\n", r.s.ID, r.s.ID, r.s.Title, r.verdict, note)
	}

	counts := map[Verdict]int{}
	for _, r := range results {
		counts[r.verdict]++
	}
	keys := make([]string, 0, len(counts))
	for k := range counts {
		keys = append(keys, string(k))
	}
	sort.Strings(keys)
	b.WriteString("\n## Caveats worth reading before acting on these\n\n")
	b.WriteString("- **p1-01**: the *literal* claim - a player teleported on join into an unloaded chunk in render\n")
	b.WriteString("  distance - did **not** freeze the world; the async chunk load path kept up. What does freeze it\n")
	b.WriteString("  is `Loader.ChangeWorld` running on the owner goroutine of the loader's own world, because\n")
	b.WriteString("  `World.exec` sends into `w.queue` unconditionally. That is variant B in the report.\n")
	b.WriteString("- **p1-01 / p1-02**: both freezes need the target world's 128-deep transaction queue to be full,\n")
	b.WriteString("  which is the state any busy world is in while its owner is inside a long transaction. Each\n")
	b.WriteString("  scenario includes a control run showing the same setup recovers when `ChangeWorld` is not called.\n")
	b.WriteString("- **p1-13**: the cloud has to sit slightly below the target, because `cube.BBox.Vec3Within` is\n")
	b.WriteString("  strictly inside and an entity standing exactly on the box's lower face is never seen. That is a\n")
	b.WriteString("  separate observation, not part of the claim.\n")
	b.WriteString("- **p2-11**: the double-chest half also shows *why* a naive fix loses items. The session's window is\n")
	b.WriteString("  the merged 54-slot inventory; `unpair` gives the surviving half a fresh 27-slot clone taken before\n")
	b.WriteString("  the write. Anything written through the window after the break exists only in the detached object.\n")
	b.WriteString("  Closing the window without first draining it back into the surviving block is the item loss.\n")
	b.WriteString("- **p2-02 / p2-03 / p2-09** are BLOCKED, not refuted: the state involved is unexported and reachable\n")
	b.WriteString("  only through a scripted multi-action `ItemStackRequest` against a live beacon or crafting window\n")
	b.WriteString("  (or, for p2-09, not reachable at all from outside the package). Each report says exactly what a\n")
	b.WriteString("  demonstration would need.\n")
	b.WriteString("\n## Verdict counts\n\n")
	for _, k := range keys {
		fmt.Fprintf(&b, "- **%s**: %d\n", k, counts[Verdict(k)])
	}
	fmt.Fprintf(&b, "- total: %d\n", len(results))
	return b.String()
}
