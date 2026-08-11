package main

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"
)

// Verdict is the outcome of a single scenario.
type Verdict string

const (
	Reproduced Verdict = "REPRODUCED"
	Refuted    Verdict = "REFUTED"
	Blocked    Verdict = "BLOCKED"
	Timeout    Verdict = "TIMEOUT"
)

// Out collects the observed output of a scenario. Everything written here ends
// up verbatim in the per-item markdown report.
type Out struct {
	mu      sync.Mutex
	id      string
	lines   []string
	verdict Verdict
	reason  string
	live    bool
}

func (o *Out) Logf(format string, args ...any) {
	s := fmt.Sprintf(format, args...)
	o.mu.Lock()
	o.lines = append(o.lines, s)
	live := o.live
	o.mu.Unlock()
	if live {
		fmt.Printf("  [%s] %s\n", o.id, s)
	}
}

// Section prints a heading line.
func (o *Out) Section(name string) { o.Logf("--- %s ---", name) }

func (o *Out) Verdict(v Verdict, reasonFormat string, args ...any) {
	o.mu.Lock()
	o.verdict, o.reason = v, fmt.Sprintf(reasonFormat, args...)
	o.mu.Unlock()
}

func (o *Out) Text() string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return strings.Join(o.lines, "\n")
}

func (o *Out) Result() (Verdict, string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.verdict, o.reason
}

// Scenario is one investigated item.
type Scenario struct {
	// ID is the report file name, e.g. "p1-01-loader-deadlock".
	ID string
	// Part is 1 or 2.
	Part int
	// Title is a one line description.
	Title string
	// Claim is the reported claim, verbatim from the task.
	Claim string
	// Setup describes what the scenario builds.
	Setup string
	// Expected describes the expected (correct) values.
	Expected string
	// Timeout bounds the scenario. On expiry the scenario is considered hung
	// and its goroutine is abandoned (it may be deadlocked on purpose).
	Timeout time.Duration
	// Run executes the scenario.
	Run func(o *Out)
	// Leaks marks a scenario that is expected to leave goroutines behind
	// (deadlock demonstrations).
	Leaks bool
	// Hidden keeps a scenario out of the default run. Hidden scenarios are
	// child processes spawned by another scenario and are only reachable
	// through -only.
	Hidden bool
}

// result of a scenario run.
type result struct {
	s        *Scenario
	verdict  Verdict
	reason   string
	output   string
	duration time.Duration
	timedOut bool
	panicked string
}

// discardLogger returns a slog.Logger that throws everything away, so the
// dragonfly internals do not pollute the scenario output.
func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// captureLogger returns a logger writing to the Out passed, tagged with tag.
func captureLogger(o *Out, tag string) *slog.Logger {
	return slog.New(slog.NewTextHandler(prefixWriter{o: o, tag: tag}, &slog.HandlerOptions{Level: slog.LevelError}))
}

// captureLoggerDebug is captureLogger at debug level. dragonfly reports item
// stack request failures at debug level, so this is how those are observed.
func captureLoggerDebug(o *Out, tag string) *slog.Logger {
	return slog.New(slog.NewTextHandler(prefixWriter{o: o, tag: tag}, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

type prefixWriter struct {
	o   *Out
	tag string
}

func (w prefixWriter) Write(p []byte) (int, error) {
	for _, line := range strings.Split(strings.TrimRight(string(p), "\n"), "\n") {
		w.o.Logf("%s %s", w.tag, line)
	}
	return len(p), nil
}

// run executes a scenario with its timeout. The scenario goroutine is
// abandoned on timeout: several of these bugs are deadlocks and cannot be
// unwound.
func run(s *Scenario, live bool) result {
	o := &Out{id: s.ID, live: live}
	done := make(chan struct{})
	var panicked string
	start := time.Now()
	go func() {
		defer func() {
			if r := recover(); r != nil {
				buf := make([]byte, 1<<16)
				n := runtime.Stack(buf, false)
				panicked = fmt.Sprintf("%v\n%s", r, buf[:n])
				o.Logf("PANIC: %v", r)
			}
			close(done)
		}()
		s.Run(o)
	}()

	timedOut := false
	select {
	case <-done:
	case <-time.After(s.Timeout):
		timedOut = true
		o.Logf("!! scenario exceeded its %s timeout; goroutine abandoned", s.Timeout)
	}
	d := time.Since(start)

	v, reason := o.Result()
	if v == "" {
		if timedOut {
			v, reason = Timeout, fmt.Sprintf("scenario did not finish within %s", s.Timeout)
		} else if panicked != "" {
			v, reason = Blocked, "scenario panicked unexpectedly"
		} else {
			v, reason = Blocked, "scenario returned without recording a verdict"
		}
	}
	return result{s: s, verdict: v, reason: reason, output: o.Text(), duration: d, timedOut: timedOut, panicked: panicked}
}

// goroutineDump returns the stacks of every goroutine, filtered to the frames
// mentioning any of the substrings passed (empty = everything).
func goroutineDump(filter ...string) string {
	buf := make([]byte, 1<<22)
	n := runtime.Stack(buf, true)
	all := string(buf[:n])
	if len(filter) == 0 {
		return all
	}
	var keep []string
	for _, g := range strings.Split(all, "\n\n") {
		for _, f := range filter {
			if strings.Contains(g, f) {
				keep = append(keep, g)
				break
			}
		}
	}
	return strings.Join(keep, "\n\n")
}

func mustTempDir(o *Out, name string) string {
	dir, err := os.MkdirTemp("", name)
	if err != nil {
		panic(err)
	}
	o.Logf("temp world dir: %s", dir)
	return dir
}
