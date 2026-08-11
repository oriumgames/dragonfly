// Command bugrepro reproduces a set of confirmed dragonfly bugs end to end
// against a real in-process dragonfly server, driven where necessary by a real
// gophertunnel Bedrock client. It writes one markdown report per scenario to
// ~/Documents/df_bugs/repro and prints a summary table.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Verdict is the outcome of a scenario.
type Verdict string

const (
	Reproduced    Verdict = "REPRODUCED"
	NotReproduced Verdict = "NOT-REPRODUCED"
	Blocked       Verdict = "BLOCKED"
	Errored       Verdict = "ERROR"
)

// Result is what a scenario reports back.
type Result struct {
	Verdict Verdict
	// Reason is filled in for BLOCKED/ERROR verdicts.
	Reason string
	// Setup describes the world/inventory state that was built.
	Setup string
	// ServerSteps and ClientSteps describe honestly which side drove what.
	ServerSteps []string
	ClientSteps []string
	// Observed is the verbatim observed output.
	Observed string
	// Expected is what correct behaviour would have produced.
	Expected string
	// Summary is a one line observed-vs-expected for the table.
	Summary string
}

// Scenario is a named reproduction attempt.
type Scenario struct {
	// ID is used for the report file name.
	ID string
	// Title is the human readable name.
	Title string
	// Bug is a short description of the bug being reproduced.
	Bug string
	// Timeout bounds the scenario run.
	Timeout time.Duration
	// Run performs the reproduction.
	Run func() Result
	// Child marks a scenario that must run in a separate process because it is
	// expected to crash or exit the server.
	Child bool
	// OnChildCrash builds the Result when a Child scenario's process died before
	// it could report one. partial holds everything the child printed before it
	// went down and stderr holds the process output.
	OnChildCrash func(partial, stderr string, err error) Result
}

// childSink, when running as a child process, receives every observed line as
// it is produced so the parent can still read them if the child crashes.
var childSink *os.File

// out collects lines of verbatim observed output.
type out struct {
	b strings.Builder
}

func (o *out) printf(format string, a ...any) {
	fmt.Fprintf(&o.b, format+"\n", a...)
	if childSink != nil {
		fmt.Fprintf(childSink, format+"\n", a...)
		_ = childSink.Sync()
	}
}

func (o *out) String() string { return o.b.String() }

var scenarios []Scenario

func register(s Scenario) { scenarios = append(scenarios, s) }

func reportDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		home = "."
	}
	return filepath.Join(home, "Documents", "df_bugs", "repro")
}

type runOutcome struct {
	Scenario Scenario
	Result   Result
	Duration time.Duration
}

// runChild runs a Child scenario in a separate process so that a crash inside
// the dragonfly server cannot take the harness with it.
func runChild(s Scenario) runOutcome {
	start := time.Now()
	logFile, err := os.CreateTemp("", "bugrepro-log-")
	if err != nil {
		return runOutcome{Scenario: s, Result: Result{Verdict: Errored, Reason: err.Error()}}
	}
	resFile, err := os.CreateTemp("", "bugrepro-res-")
	if err != nil {
		return runOutcome{Scenario: s, Result: Result{Verdict: Errored, Reason: err.Error()}}
	}
	logPath, resPath := logFile.Name(), resFile.Name()
	_ = logFile.Close()
	_ = resFile.Close()
	defer os.Remove(logPath)
	defer os.Remove(resPath)

	timeout := s.Timeout
	if timeout == 0 {
		timeout = 120 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, os.Args[0])
	cmd.Env = append(os.Environ(),
		"BUGREPRO_CHILD="+s.ID,
		"BUGREPRO_LOG="+logPath,
		"BUGREPRO_RESULT="+resPath,
	)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	cmd.Stdout = &stderr
	runErr := cmd.Run()

	partial, _ := os.ReadFile(logPath)
	if data, err := os.ReadFile(resPath); err == nil && len(data) > 0 {
		var r Result
		if json.Unmarshal(data, &r) == nil && r.Verdict != "" {
			return runOutcome{Scenario: s, Result: r, Duration: time.Since(start)}
		}
	}
	if s.OnChildCrash != nil {
		return runOutcome{Scenario: s, Result: s.OnChildCrash(string(partial), stderr.String(), runErr), Duration: time.Since(start)}
	}
	return runOutcome{Scenario: s, Result: Result{
		Verdict:  Errored,
		Reason:   fmt.Sprintf("child process died without a result: %v", runErr),
		Observed: string(partial) + "\n" + stderr.String(),
	}, Duration: time.Since(start)}
}

// runScenario runs s with its timeout, recovering panics into an ERROR result.
func runScenario(s Scenario) runOutcome {
	if s.Child {
		return runChild(s)
	}
	start := time.Now()
	done := make(chan Result, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				done <- Result{Verdict: Errored, Reason: fmt.Sprintf("panic: %v", r)}
			}
		}()
		done <- s.Run()
	}()
	timeout := s.Timeout
	if timeout == 0 {
		timeout = 120 * time.Second
	}
	select {
	case res := <-done:
		return runOutcome{Scenario: s, Result: res, Duration: time.Since(start)}
	case <-time.After(timeout):
		return runOutcome{
			Scenario: s,
			Result:   Result{Verdict: Blocked, Reason: fmt.Sprintf("scenario exceeded its %s timeout and was abandoned", timeout)},
			Duration: time.Since(start),
		}
	}
}

func main() {
	if id := os.Getenv("BUGREPRO_CHILD"); id != "" {
		runAsChild(id)
		return
	}
	sort.SliceStable(scenarios, func(i, j int) bool { return scenarios[i].ID < scenarios[j].ID })

	only := map[string]bool{}
	for _, a := range os.Args[1:] {
		only[a] = true
	}

	dir := reportDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		fmt.Fprintln(os.Stderr, "create report dir:", err)
		os.Exit(1)
	}

	var outcomes []runOutcome
	for _, s := range scenarios {
		if len(only) > 0 && !only[s.ID] {
			continue
		}
		fmt.Printf("== running %s (%s)\n", s.ID, s.Title)
		o := runScenario(s)
		outcomes = append(outcomes, o)
		fmt.Printf("   -> %s (%s) %s\n", o.Result.Verdict, o.Duration.Round(time.Millisecond), o.Result.Summary)
		if err := writeReport(dir, o); err != nil {
			fmt.Fprintln(os.Stderr, "write report:", err)
		}
	}

	fmt.Println()
	fmt.Println(summaryTable(outcomes))

	counts := map[Verdict]int{}
	for _, o := range outcomes {
		counts[o.Result.Verdict]++
	}
	keys := []Verdict{Reproduced, NotReproduced, Blocked, Errored}
	var parts []string
	for _, k := range keys {
		if counts[k] > 0 {
			parts = append(parts, fmt.Sprintf("%s: %d", k, counts[k]))
		}
	}
	fmt.Println("\nTotals -", strings.Join(parts, ", "))

	if len(only) == 0 {
		if err := writeReadme(dir, outcomes); err != nil {
			fmt.Fprintln(os.Stderr, "write readme:", err)
		}
		fmt.Println("\nReports written to", dir)
	}
}

// runAsChild executes a single scenario in this process and writes its Result
// as JSON to the file named by BUGREPRO_RESULT.
func runAsChild(id string) {
	sort.SliceStable(scenarios, func(i, j int) bool { return scenarios[i].ID < scenarios[j].ID })
	if p := os.Getenv("BUGREPRO_LOG"); p != "" {
		f, err := os.Create(p)
		if err == nil {
			childSink = f
			defer f.Close()
		}
	}
	for _, s := range scenarios {
		if s.ID != id {
			continue
		}
		res := s.Run()
		data, err := json.Marshal(res)
		if err != nil {
			fmt.Fprintln(os.Stderr, "marshal result:", err)
			os.Exit(1)
		}
		if p := os.Getenv("BUGREPRO_RESULT"); p != "" {
			_ = os.WriteFile(p, data, 0o644)
		}
		return
	}
	fmt.Fprintln(os.Stderr, "unknown scenario", id)
	os.Exit(3)
}

func verdictString(r Result) string {
	if r.Verdict == Blocked || r.Verdict == Errored {
		return fmt.Sprintf("%s(%s)", r.Verdict, r.Reason)
	}
	return string(r.Verdict)
}

func summaryTable(outcomes []runOutcome) string {
	var b strings.Builder
	b.WriteString("| # | Scenario | Verdict | Observed vs expected |\n")
	b.WriteString("|---|----------|---------|----------------------|\n")
	for i, o := range outcomes {
		summary := o.Result.Summary
		if summary == "" && (o.Result.Verdict == Blocked || o.Result.Verdict == Errored) {
			summary = o.Result.Reason
		}
		b.WriteString(fmt.Sprintf("| %d | [%s](%s.md) | %s | %s |\n",
			i+1, o.Scenario.Title, o.Scenario.ID, string(o.Result.Verdict), oneLine(summary)))
	}
	return b.String()
}

func oneLine(s string) string {
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "|", "\\|")
	return strings.TrimSpace(s)
}

func writeReport(dir string, o runOutcome) error {
	r := o.Result
	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n\n", o.Scenario.Title)
	fmt.Fprintf(&b, "**Verdict: %s**\n\n", verdictString(r))
	fmt.Fprintf(&b, "- Scenario id: `%s`\n", o.Scenario.ID)
	fmt.Fprintf(&b, "- Run time: %s\n", o.Duration.Round(time.Millisecond))
	fmt.Fprintf(&b, "- Harness: `go run ./cmd/bugrepro %s` (real in-process dragonfly server, real gophertunnel client where noted)\n\n", o.Scenario.ID)

	fmt.Fprintf(&b, "## Bug\n\n%s\n\n", strings.TrimSpace(o.Scenario.Bug))

	if r.Setup != "" {
		fmt.Fprintf(&b, "## Setup\n\n%s\n\n", strings.TrimSpace(r.Setup))
	}
	if len(r.ServerSteps) > 0 || len(r.ClientSteps) > 0 {
		b.WriteString("## Who drove what\n\n")
		if len(r.ServerSteps) > 0 {
			b.WriteString("Server-side API drove:\n\n")
			for _, s := range r.ServerSteps {
				fmt.Fprintf(&b, "- %s\n", s)
			}
			b.WriteString("\n")
		}
		if len(r.ClientSteps) > 0 {
			b.WriteString("Real gophertunnel client drove:\n\n")
			for _, s := range r.ClientSteps {
				fmt.Fprintf(&b, "- %s\n", s)
			}
			b.WriteString("\n")
		} else {
			b.WriteString("Real gophertunnel client drove: nothing in this scenario. " +
				"A client was connected only where the scenario notes it; every step above ran through the server-side API.\n\n")
		}
	}
	if r.Observed != "" {
		b.WriteString("## Observed output (verbatim)\n\n```\n")
		b.WriteString(strings.TrimRight(r.Observed, "\n"))
		b.WriteString("\n```\n\n")
	}
	if r.Expected != "" {
		fmt.Fprintf(&b, "## Expected\n\n%s\n\n", strings.TrimSpace(r.Expected))
	}
	if r.Reason != "" {
		fmt.Fprintf(&b, "## Why %s\n\n%s\n\n", r.Verdict, strings.TrimSpace(r.Reason))
	}
	fmt.Fprintf(&b, "## Verdict\n\n**%s**", verdictString(r))
	if r.Summary != "" {
		fmt.Fprintf(&b, " - %s", r.Summary)
	}
	b.WriteString("\n")

	return os.WriteFile(filepath.Join(dir, o.Scenario.ID+".md"), []byte(b.String()), 0o644)
}

func writeReadme(dir string, outcomes []runOutcome) error {
	counts := map[Verdict]int{}
	for _, o := range outcomes {
		counts[o.Result.Verdict]++
	}
	var b strings.Builder
	b.WriteString("# dragonfly bug reproductions\n\n")
	b.WriteString("End-to-end reproductions of confirmed dragonfly bugs, run against a **real in-process\n")
	b.WriteString("dragonfly server** (`server.Config{}.New()`, raknet listener on `127.0.0.1`, `AuthDisabled: true`)\n")
	b.WriteString("with a **real gophertunnel Bedrock client** (`minecraft.Dialer{}.DialTimeout(\"raknet\", addr, ...)`)\n")
	b.WriteString("connected to it. Each report states explicitly which steps were driven server-side and which\n")
	b.WriteString("were driven by client packets.\n\n")

	b.WriteString("## How to run\n\n```sh\n# from the dragonfly checkout\ngo run ./cmd/bugrepro            # run everything\ngo run ./cmd/bugrepro 01-container-close-dupe  # run one scenario by id\n```\n\n")
	b.WriteString("Reports are written to `~/Documents/df_bugs/repro/<scenario>.md`.\n\n")

	fmt.Fprintf(&b, "## Summary\n\n%s\n", summaryTable(outcomes))
	var parts []string
	for _, k := range []Verdict{Reproduced, NotReproduced, Blocked, Errored} {
		if counts[k] > 0 {
			parts = append(parts, fmt.Sprintf("**%s**: %d", k, counts[k]))
		}
	}
	fmt.Fprintf(&b, "\nTotals - %s (of %d scenarios).\n", strings.Join(parts, ", "), len(outcomes))
	b.WriteString("\n## Notes\n\n")
	b.WriteString("- Every number in every report is printed by the harness at run time; nothing is copied from the wiki or from memory.\n")
	b.WriteString("- The harness is at `cmd/bugrepro/` in this checkout. No file under `server/` was modified.\n")
	b.WriteString("- Scenarios each have their own timeout so a hang in one cannot stall the run, and each cleans up its own server, client and temporary world directory.\n")
	b.WriteString("- Three scenarios (`16`, `17`, `20`) crash the dragonfly server on purpose. They run as child processes " +
		"(`BUGREPRO_CHILD=<id>` re-executes the same binary for a single scenario) so the crash cannot take the harness with it; " +
		"the child streams its observations to a file that the parent reads back after the process dies.\n")
	b.WriteString("- `NOT-REPRODUCED` here is a real finding, not a gap: scenarios 5 and 8 show that the underlying `BreakInfo().Drops()` " +
		"bug is genuinely present, but that `entity.NewItem` runs every drop through `item.ReadNBT(item.WriteNBT(...))`, which " +
		"normalises the block state away before a player can ever pick it up. The exponential duplication therefore does not " +
		"materialise on this build. Both reports show the verbatim before/after of that round trip.\n")
	b.WriteString("- `BLOCKED` on scenario 18 means the faulty code is unreachable from any public API and is gated behind a " +
		"1/100,000-per-chunk-per-tick random roll. The report says exactly what was and was not observed.\n")

	return os.WriteFile(filepath.Join(dir, "README.md"), []byte(b.String()), 0o644)
}
