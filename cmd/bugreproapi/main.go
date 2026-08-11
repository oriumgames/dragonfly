// Command bugreproapi reproduces a set of dragonfly bugs that cannot be driven
// from a Bedrock client, using the real server/world API in process.
//
// Run everything and write the reports:
//
//	go run ./cmd/bugreproapi
//
// Run a single scenario:
//
//	go run ./cmd/bugreproapi -only=p1-01-loader-deadlock
//
// Every scenario is bounded by its own timeout so that a deadlock cannot stall
// the run. Deadlocked goroutines are deliberately abandoned, which is why the
// process calls os.Exit at the end rather than returning from main.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/df-mc/dragonfly/server/world"
)

var (
	only       = flag.String("only", "", "run only the scenario with this ID")
	reportsDir = flag.String("reports", defaultReportsDir(), "directory to write the markdown reports to")
	noReports  = flag.String("no-reports", "", "set to 1 to skip writing markdown reports")
	quiet      = flag.Bool("quiet", false, "do not stream scenario output while running")
)

func defaultReportsDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "df_bugs/repro-api"
	}
	return filepath.Join(home, "Documents", "df_bugs", "repro-api")
}

func main() {
	flag.Parse()
	// Several scenarios read blocks out of the registry before any World is
	// built, so finalize it up front.
	world.DefaultBlockRegistry.Finalize()

	all := visibleScenarios()
	if *only != "" {
		var filtered []*Scenario
		for _, s := range scenarios() {
			if s.ID == *only {
				filtered = append(filtered, s)
			}
		}
		if len(filtered) == 0 {
			fmt.Fprintf(os.Stderr, "no scenario with ID %q\n", *only)
			listIDs(all)
			os.Exit(2)
		}
		all = filtered
	}

	fmt.Printf("dragonfly bug repro harness: %d scenario(s)\n\n", len(all))
	results := make([]result, 0, len(all))
	for _, s := range all {
		fmt.Printf("== %s: %s\n", s.ID, s.Title)
		r := run(s, !*quiet)
		fmt.Printf("   -> %s (%s) [%s]\n\n", r.verdict, r.reason, r.duration.Round(time.Millisecond))
		results = append(results, r)
	}

	printSummary(results)

	if *noReports != "1" && *only == "" {
		if err := writeReports(*reportsDir, results); err != nil {
			fmt.Fprintf(os.Stderr, "write reports: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("\nreports written to %s\n", *reportsDir)
	} else if *noReports != "1" {
		if err := writeReports(*reportsDir, results); err != nil {
			fmt.Fprintf(os.Stderr, "write reports: %v\n", err)
		}
	}

	// Several scenarios deliberately leave deadlocked goroutines behind. A
	// normal return from main would be fine, but Exit makes the intent
	// explicit and avoids any chance of a lingering finaliser blocking.
	os.Exit(0)
}

func listIDs(all []*Scenario) {
	fmt.Fprintln(os.Stderr, "available scenarios:")
	for _, s := range all {
		fmt.Fprintf(os.Stderr, "  %s\n", s.ID)
	}
}

func printSummary(results []result) {
	counts := map[Verdict]int{}
	idw, titlew := len("SCENARIO"), len("TITLE")
	for _, r := range results {
		counts[r.verdict]++
		idw = max(idw, len(r.s.ID))
		titlew = max(titlew, len(r.s.Title))
	}
	fmt.Println(strings.Repeat("=", idw+titlew+40))
	fmt.Printf("%-*s  %-*s  %-10s  %s\n", idw, "SCENARIO", titlew, "TITLE", "VERDICT", "TIME")
	fmt.Println(strings.Repeat("-", idw+titlew+40))
	for _, r := range results {
		fmt.Printf("%-*s  %-*s  %-10s  %s\n", idw, r.s.ID, titlew, r.s.Title, r.verdict, r.duration.Round(time.Millisecond))
	}
	fmt.Println(strings.Repeat("=", idw+titlew+40))

	keys := make([]string, 0, len(counts))
	for k := range counts {
		keys = append(keys, string(k))
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s=%d", k, counts[Verdict(k)]))
	}
	fmt.Printf("totals: %s (%d scenarios)\n", strings.Join(parts, "  "), len(results))
}
