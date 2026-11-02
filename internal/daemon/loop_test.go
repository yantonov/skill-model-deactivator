package daemon

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/yantonov/skill-model-deactivator/internal/config"
	"github.com/yantonov/skill-model-deactivator/internal/reconcile"
	"github.com/yantonov/skill-model-deactivator/internal/watch"
)

// pollWindow is how long a test waits for the loop to reach a state. Every wait is a poll with
// a deadline, so a loop that does not run shows up as a failure and never as a hang.
const pollWindow = 2 * time.Second

// loopEntry keeps only the level and the message: the loop adds no fields of its own except
// the error text.
type loopEntry struct {
	level, msg string
}

// loopSink is written from the loop goroutine, so a test reads it from its own goroutine
// without a race.
type loopSink struct {
	mu    sync.Mutex
	lines []loopEntry
}

func (s *loopSink) Line(level, msg string, _ ...reconcile.KV) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lines = append(s.lines, loopEntry{level: level, msg: msg})
}

func (s *loopSink) count(level, msg string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	total := 0
	for _, line := range s.lines {
		if line.level == level && line.msg == msg {
			total++
		}
	}
	return total
}

func (s *loopSink) atLevel(level string) []loopEntry {
	s.mu.Lock()
	defer s.mu.Unlock()
	var found []loopEntry
	for _, line := range s.lines {
		if line.level == level {
			found = append(found, line)
		}
	}
	return found
}

// waitFor turns a loop that does not react into a failure instead of a hang.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(pollWindow)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("%s did not happen within %s", what, pollWindow)
}

// passCounter increments on entry and decrements on exit, and records the deepest overlap.
type passCounter struct {
	mu         sync.Mutex
	count      int
	running    int
	maxRunning int
	gate       chan struct{}
}

// Pass waits on the gate when one is set, so the test can send triggers during a pass.
func (c *passCounter) Pass() error {
	c.mu.Lock()
	c.count++
	c.running++
	if c.running > c.maxRunning {
		c.maxRunning = c.running
	}
	first, gate := c.count == 1, c.gate
	c.mu.Unlock()

	if first && gate != nil {
		<-gate
	}

	c.mu.Lock()
	c.running--
	c.mu.Unlock()
	return nil
}

func (c *passCounter) passes() (count, maxRunning int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.count, c.maxRunning
}

// startLoop returns the context that stops the loop and the channel that reports its return.
func startLoop(t *testing.T, loop Loop) (context.CancelFunc, <-chan struct{}) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	loop.Ctx = ctx
	done := make(chan struct{})
	go func() {
		defer close(done)
		loop.Run()
	}()
	return cancel, done
}

func stopLoop(t *testing.T, cancel context.CancelFunc, done <-chan struct{}) {
	t.Helper()
	cancel()
	select {
	case <-done:
	case <-time.After(pollWindow):
		t.Fatal("the loop did not stop after the context was done")
	}
}

// TestTwoTriggersDuringAPassRunExactlyOneFollowUpPass: two triggers arrive while a pass runs,
// one pass completes, exactly one follow-up pass runs, and no pass ever overlaps another. The
// channel is buffered with room for one, which is the shape internal/watch sends on.
func TestTwoTriggersDuringAPassRunExactlyOneFollowUpPass(t *testing.T) {
	counter := &passCounter{gate: make(chan struct{})}
	trigger := make(chan watch.Trigger, 1)
	sink := &loopSink{}

	cancel, done := startLoop(t, Loop{
		Trigger: trigger,
		Rescan:  NewRescan(0),
		Logger:  sink,
		Pass:    counter.Pass,
	})
	defer stopLoop(t, cancel, done)

	// The first trigger starts the pass that holds the gate.
	sendTrigger(t, trigger)
	waitFor(t, "the first pass", func() bool {
		count, _ := counter.passes()
		return count == 1
	})

	// Both triggers land while the first pass runs. The first sets the flag, which holds one
	// value, and the second is dropped because that flag is already set.
	sendTrigger(t, trigger)
	dropTrigger(t, trigger)
	close(counter.gate)

	waitFor(t, "the follow-up pass", func() bool {
		count, _ := counter.passes()
		return count == 2
	})

	// Nothing else is pending, so a third pass would mean a double count.
	time.Sleep(100 * time.Millisecond)
	count, maxRunning := counter.passes()
	if count != 2 {
		t.Errorf("passes = %d, want exactly 2: the two triggers coalesce into one follow-up", count)
	}
	if maxRunning != 1 {
		t.Errorf("deepest overlap = %d, want 1: no two passes may run at once", maxRunning)
	}
}

// TestAPassErrorIsOneErrorLineAndTheLoopContinues: one error line for a pass that did not stop
// the process, and a later trigger still runs a pass.
func TestAPassErrorIsOneErrorLineAndTheLoopContinues(t *testing.T) {
	sink := &loopSink{}
	trigger := make(chan watch.Trigger, 1)
	var mu sync.Mutex
	passes := 0
	pass := func() error {
		mu.Lock()
		defer mu.Unlock()
		passes++
		if passes == 1 {
			// The resolver error of a home directory that cannot be expanded, which stops a
			// pass as a whole.
			return errors.New(`cannot expand "~/skills/*/SKILL.md": the home directory is empty`)
		}
		return nil
	}

	cancel, done := startLoop(t, Loop{
		Trigger: trigger,
		Rescan:  NewRescan(0),
		Logger:  sink,
		Pass:    pass,
	})
	defer stopLoop(t, cancel, done)

	trigger <- watch.TriggerChanged
	waitFor(t, "the failing pass", func() bool {
		return sink.count("error", "reconcile failed") == 1
	})

	sendTrigger(t, trigger)
	waitFor(t, "the second pass", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return passes == 2
	})
	if got := sink.count("error", "reconcile failed"); got != 1 {
		t.Errorf("error lines = %d, want 1: the second pass succeeded", got)
	}
}

// TestTheRescanRefreshesAndThenRunsAPass: the fire re-walks the roots first, so a directory
// that appeared is watched, and then asks for the pass.
func TestTheRescanRefreshesAndThenRunsAPass(t *testing.T) {
	var mu sync.Mutex
	var events []string
	record := func(event string) {
		mu.Lock()
		defer mu.Unlock()
		events = append(events, event)
	}
	refresher := refresherFunc(func() error {
		record("refresh")
		return nil
	})
	pass := func() error {
		record("pass")
		return nil
	}

	cancel, done := startLoop(t, Loop{
		Trigger:   make(chan watch.Trigger),
		Rescan:    NewRescan(20 * time.Millisecond),
		Refresher: refresher,
		Logger:    &loopSink{},
		Pass:      pass,
	})
	defer stopLoop(t, cancel, done)

	waitFor(t, "a rescan pass", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(events) >= 2
	})

	mu.Lock()
	first, second := events[0], events[1]
	mu.Unlock()
	if first != "refresh" || second != "pass" {
		t.Errorf("first two events = %q, %q; want the refresh before the pass", first, second)
	}
}

// TestTheRescanPicksUpAChangeWhileTheWatcherIsSilent: the trigger channel never receives, so
// the watcher is not working, and a change made after start still reaches the desired state
// because the timer asks for a full pass.
func TestTheRescanPicksUpAChangeWhileTheWatcherIsSilent(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	path := filepath.Join(home, "skills", "kibana", "SKILL.md")
	canonical := "---\nname: kibana\ndisable-model-invocation: true\n---\n"
	writeFile(t, path, canonical)

	cfg := config.Config{
		Patterns:       []string{filepath.ToSlash(filepath.Join(home, "skills", "*", "SKILL.md"))},
		Debounce:       20 * time.Millisecond,
		RescanInterval: 50 * time.Millisecond,
	}
	pass := func() error {
		_, _, err := reconcile.Pass(cfg, reconcile.Options{Write: reconcile.Write})
		return err
	}

	cancel, done := startLoop(t, Loop{
		// Nothing is ever sent on this channel: that is the watcher being broken.
		Trigger: make(chan watch.Trigger),
		Rescan:  NewRescan(cfg.RescanInterval),
		Logger:  &loopSink{},
		Pass:    pass,
	})
	defer stopLoop(t, cancel, done)

	// The change lands while the loop runs, and no event reports it.
	writeFile(t, path, "---\nname: kibana\n---\n")
	waitFor(t, "the rescan pass to patch the file", func() bool {
		return readFileString(t, path) == canonical
	})
}

// TestAZeroRescanIntervalNeverFires: the timer has no channel, so a loop with no trigger runs
// no pass at all.
func TestAZeroRescanIntervalNeverFires(t *testing.T) {
	if channel := NewRescan(0).C(); channel != nil {
		t.Errorf("C() = %v, want nil for a disabled rescan", channel)
	}

	counter := &passCounter{}
	cancel, done := startLoop(t, Loop{
		Trigger: make(chan watch.Trigger),
		Rescan:  NewRescan(0),
		Logger:  &loopSink{},
		Pass:    counter.Pass,
	})

	time.Sleep(100 * time.Millisecond)
	stopLoop(t, cancel, done)
	if count, _ := counter.passes(); count != 0 {
		t.Errorf("passes = %d, want 0 with the rescan off and no trigger", count)
	}
}

// TestTheLoopStopsWhenTheTriggerChannelCloses: a closed trigger channel ends the loop, so a
// daemon whose watcher is gone does not spin.
func TestTheLoopStopsWhenTheTriggerChannelCloses(t *testing.T) {
	trigger := make(chan watch.Trigger)
	cancel, done := startLoop(t, Loop{
		Trigger: trigger,
		Rescan:  NewRescan(0),
		Logger:  &loopSink{},
		Pass:    (&passCounter{}).Pass,
	})
	defer cancel()

	close(trigger)
	select {
	case <-done:
	case <-time.After(pollWindow):
		t.Fatal("the loop did not stop when the trigger channel closed")
	}
}

type refresherFunc func() error

func (f refresherFunc) Refresh() error { return f() }

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll(%q) error = %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile(%q) error = %v", path, err)
	}
}

// dropTrigger sets the flag without waiting: the flag is full at this point, so nothing is
// sent.
func dropTrigger(t *testing.T, trigger chan watch.Trigger) {
	t.Helper()
	select {
	case trigger <- watch.TriggerChanged:
		t.Fatal("the trigger flag had room, want it already set")
	case <-time.After(50 * time.Millisecond):
	}
}

func sendTrigger(t *testing.T, trigger chan<- watch.Trigger) {
	t.Helper()
	select {
	case trigger <- watch.TriggerChanged:
	case <-time.After(pollWindow):
		t.Fatal("the loop did not take the trigger within " + pollWindow.String())
	}
}

// readFileString returns the empty string while a concurrent writer holds the file. Windows
// refuses a read while a rename is in flight, so a poll must tolerate that instead of failing.
func readFileString(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(data)
}
