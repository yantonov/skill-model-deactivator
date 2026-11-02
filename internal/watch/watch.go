// Package watch turns file events into reconcile triggers. It does not run the pass.
package watch

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/bmatcuk/doublestar/v4"
	"github.com/fsnotify/fsnotify"
	"github.com/yantonov/skill-model-deactivator/internal/config"
	"github.com/yantonov/skill-model-deactivator/internal/display"
	"github.com/yantonov/skill-model-deactivator/internal/match"
	"github.com/yantonov/skill-model-deactivator/internal/reconcile"
)

// A level is a label for the reader and never a filter.
const (
	levelDebug = "debug"
	levelWarn  = "warn"
	levelError = "error"
)

// Trigger is one reason to run a reconcile pass. It carries no paths, because every pass
// reads the current disk state, and that is what keeps coalescing trivial.
type Trigger int

const (
	// TriggerChanged is an event on a file name the patterns accept.
	TriggerChanged Trigger = iota

	// TriggerRescan means the file set may have changed: a directory appeared under a root
	// or left it, or a watch root appeared.
	TriggerRescan

	// TriggerError is a watcher error, a queue overflow, or an event on a path that cannot
	// be inspected. It is a trigger and not an error return.
	TriggerError
)

func (t Trigger) String() string {
	switch t {
	case TriggerRescan:
		return "rescan"
	case TriggerError:
		return "error"
	default:
		return "changed"
	}
}

// watcher is the part of fsnotify.Watcher the package calls, so a test can drive the
// debounce without a filesystem.
type watcher interface {
	Add(string) error
	Close() error
	Events() <-chan fsnotify.Event
	Errors() <-chan error
}

// fsnotifyWatcher adapts the exported fields of fsnotify.Watcher to the interface.
type fsnotifyWatcher struct {
	*fsnotify.Watcher
}

func (f fsnotifyWatcher) Events() <-chan fsnotify.Event { return f.Watcher.Events }

func (f fsnotifyWatcher) Errors() <-chan error { return f.Watcher.Errors }

// Watcher watches the roots of the patterns and sends one trigger per debounce window.
//
// After Close returns, no more triggers are sent and the trigger channel is closed. Close is
// idempotent. One goroutine is the only sender on the trigger channel and it closes the
// channel on its way out, so a send can never race the close.
type Watcher struct {
	cfg   config.Config
	log   reconcile.Logger
	fsw   watcher
	roots []string
	names []string

	// The fields below belong to the run goroutine alone, which is why the maps need no
	// mutex: Add, the walks, and the debounce all happen in one goroutine.
	dirs       map[string]struct{}
	absent     map[string]struct{}
	warned     map[string]struct{}
	timer      *time.Timer
	pending    Trigger
	pendingSet bool

	// events and errs carry the fsnotify channels to the run goroutine. They exist so a
	// walk inside the run goroutine cannot deadlock the fsnotify reader: the reader always
	// finds a receiver in the pump, whatever the run goroutine is doing.
	events chan fsnotify.Event
	errs   chan error

	triggers chan Trigger
	refresh  chan refreshRequest
	done     chan struct{}
	pumpDone chan struct{}
	finished chan struct{}

	closeOnce sync.Once
	closeErr  error
}

// refreshRequest is one call of Refresh, waiting on the run goroutine.
type refreshRequest struct {
	reply chan error
}

// New returns an error only when fsnotify cannot start: a root that does not exist is one
// warn and a retry at each rescan.
func New(cfg config.Config, log reconcile.Logger) (*Watcher, error) {
	roots, names, err := targets(cfg.Patterns)
	if err != nil {
		return nil, err
	}
	if log == nil {
		log = discardLogger{}
	}
	fsw, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, fmt.Errorf("cannot start the watcher: %w", err)
	}
	return newWatcher(cfg, log, fsnotifyWatcher{fsw}, roots, names), nil
}

func newWatcher(cfg config.Config, log reconcile.Logger, fsw watcher, roots, names []string) *Watcher {
	w := &Watcher{
		cfg:      cfg,
		log:      log,
		fsw:      fsw,
		roots:    roots,
		names:    names,
		dirs:     make(map[string]struct{}),
		absent:   make(map[string]struct{}),
		warned:   make(map[string]struct{}),
		triggers: make(chan Trigger, 1),
		events:   make(chan fsnotify.Event, 256),
		errs:     make(chan error, 1),
		refresh:  make(chan refreshRequest),
		done:     make(chan struct{}),
		pumpDone: make(chan struct{}),
		finished: make(chan struct{}),
		timer:    time.NewTimer(time.Hour),
	}
	w.timer.Stop()
	for _, root := range roots {
		if err := w.addRoot(root); err != nil {
			w.log.Line(levelError, "cannot watch a root", kv("path", display.Tilde(root)), kv("error", err.Error()))
		}
	}
	go w.pump()
	go w.run()
	return w
}

func (w *Watcher) Triggers() <-chan Trigger {
	return w.triggers
}

// Refresh re-walks the roots and adds the watcher of every directory that has none, so a
// directory created under a root is picked up and a missing root is retried.
func (w *Watcher) Refresh() error {
	request := refreshRequest{reply: make(chan error, 1)}
	select {
	case w.refresh <- request:
	case <-w.finished:
		return nil
	}
	select {
	case err := <-request.reply:
		return err
	case <-w.finished:
		return nil
	}
}

func (w *Watcher) Close() error {
	w.closeOnce.Do(func() {
		close(w.done)
		<-w.finished
		<-w.pumpDone
	})
	return w.closeErr
}

// run is the only sender on the trigger channel, the owner of the timer, and the only caller
// of Add, so the watcher is never closed under a walk.
func (w *Watcher) run() {
	defer close(w.finished)
	defer close(w.triggers)
	defer w.timer.Stop()

	for {
		select {
		case <-w.done:
			w.closeErr = w.fsw.Close()
			return
		case <-w.timer.C:
			w.emit(w.takePending())
		case request := <-w.refresh:
			// The reply channel is buffered, so a Refresh that gave up waiting is not a leak.
			request.reply <- w.refreshRoots()
		case event := <-w.events:
			w.handleEvent(event)
		case err := <-w.errs:
			// What was missed is unknown, so reconcile in full.
			w.log.Line(levelError, "watcher error", kv("error", err.Error()))
			w.schedule(TriggerError)
		}
	}
}

// pump moves the fsnotify channels into the channels the run goroutine reads. It calls no
// fsnotify method, so it always drains the reader and no walk can deadlock it. An event
// that does not fit is reported as an overflow, because a lost event is exactly the case a
// full reconcile covers.
func (w *Watcher) pump() {
	defer close(w.pumpDone)
	for {
		select {
		case <-w.done:
			return
		case event, ok := <-w.fsw.Events():
			if !ok {
				return
			}
			select {
			case w.events <- event:
			default:
				w.reportOverflow()
			}
		case err, ok := <-w.fsw.Errors():
			if !ok {
				return
			}
			select {
			case w.errs <- err:
			default:
				// One error is already waiting, and it already asks for a full reconcile.
			}
		}
	}
}

func (w *Watcher) reportOverflow() {
	select {
	case w.errs <- errors.New("events dropped: the event queue overflowed"):
	default:
	}
}

// handleEvent never interprets the event beyond its kind: a rename-over save needs no special
// handling, because the pass reads the file and not an open descriptor.
func (w *Watcher) handleEvent(event fsnotify.Event) {
	w.log.Line(levelDebug, "watcher event",
		kv("path", display.Tilde(event.Name)),
		kv("event", event.Op.String()))

	if event.Has(fsnotify.Remove) || event.Has(fsnotify.Rename) {
		if w.forgetTree(event.Name) {
			// A directory left the tree, so the file set changed and its watchers are gone.
			w.schedule(TriggerRescan)
			return
		}
	}

	info, statErr := os.Stat(event.Name)
	if statErr == nil && info.IsDir() {
		if event.Has(fsnotify.Create) {
			w.discover(event.Name)
		}
		// Any other event on a directory is a metadata change, or the platform reporting
		// the change of a child whose own event covers it.
		return
	}

	if w.accept(filepath.Base(event.Name)) {
		w.schedule(TriggerChanged)
		return
	}
	if statErr != nil && !errors.Is(statErr, fs.ErrNotExist) {
		// The tool does not guess whether an uninspectable path matters.
		w.schedule(TriggerError)
	}
}

func (w *Watcher) discover(dir string) {
	relevant := false
	for _, root := range w.roots {
		if _, missing := w.absent[root]; missing {
			if within(root, dir) {
				if err := w.addRoot(root); err != nil {
					w.log.Line(levelError, "cannot watch a root",
						kv("path", display.Tilde(root)), kv("error", err.Error()))
				}
				// While the root is still missing, watch the new ancestor so the next step
				// of the path stays visible.
				w.retryAncestor(root)
				relevant = true
			}
			continue
		}
		if within(dir, root) {
			w.forgetTree(dir)
			if err := w.walk(dir); err != nil {
				w.log.Line(levelWarn, "cannot watch a new directory",
					kv("path", display.Tilde(dir)), kv("error", err.Error()))
			}
			relevant = true
		}
	}
	if relevant {
		// The directory create lands in the same debounce window as a file written into it,
		// so one pass sees both.
		w.schedule(TriggerRescan)
	}
}

func (w *Watcher) refreshRoots() error {
	before := len(w.dirs)
	var firstErr error
	for _, root := range w.roots {
		if err := w.addRoot(root); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if len(w.dirs) != before {
		// The file set may have grown, so the pass runs once for the whole refresh.
		w.schedule(TriggerRescan)
	}
	return firstErr
}

// schedule restarts the debounce window: fsnotify sends three or more events for one save.
func (w *Watcher) schedule(kind Trigger) {
	if !w.pendingSet || kind > w.pending {
		w.pending, w.pendingSet = kind, true
	}
	if !w.timer.Stop() {
		select {
		case <-w.timer.C:
		default:
		}
	}
	w.timer.Reset(w.cfg.Debounce)
}

func (w *Watcher) takePending() Trigger {
	kind := w.pending
	w.pending, w.pendingSet = TriggerChanged, false
	return kind
}

// emit never waits: a trigger that does not fit is dropped, because one that is already
// waiting says the same thing. Not waiting keeps the event loop responsive during a pass.
func (w *Watcher) emit(kind Trigger) {
	w.log.Line(levelDebug, "debounce tick", kv("trigger", kind.String()))
	select {
	case w.triggers <- kind:
	default:
	}
}

// addRoot treats a missing root as one warn plus a watch on its nearest existing ancestor, so
// its appearance is seen and the rescan retries it.
func (w *Watcher) addRoot(root string) error {
	err := w.walk(root)
	if err == nil {
		delete(w.absent, root)
		return nil
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if _, warned := w.warned[root]; !warned {
		w.warned[root] = struct{}{}
		w.log.Line(levelWarn, "watch root does not exist", kv("path", display.Tilde(root)))
	}
	w.absent[root] = struct{}{}
	w.retryAncestor(root)
	return nil
}

func (w *Watcher) retryAncestor(root string) {
	ancestor, ok := nearestExisting(root)
	if !ok {
		return
	}
	if err := w.watchDir(ancestor); err != nil {
		w.log.Line(levelWarn, "cannot watch a directory",
			kv("path", display.Tilde(ancestor)), kv("error", err.Error()))
	}
}

// walk covers for fsnotify having no portable recursive watcher.
func (w *Watcher) walk(root string) error {
	return filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			if path == root {
				return err
			}
			w.log.Line(levelWarn, "cannot read a watched directory",
				kv("path", display.Tilde(path)), kv("error", err.Error()))
			return fs.SkipDir
		}
		if !entry.IsDir() {
			return nil
		}
		if err := w.watchDir(path); err != nil {
			return err
		}
		return nil
	})
}

// watchDir leaves a directory that already has a watcher alone, so a refresh costs no syscall
// for the directories that did not change.
func (w *Watcher) watchDir(dir string) error {
	if _, ok := w.dirs[dir]; ok {
		return nil
	}
	if err := w.fsw.Add(dir); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			// It vanished between the walk and the add; the event for that is more useful
			// than an error here.
			return nil
		}
		return fmt.Errorf("cannot watch %s: %w", dir, err)
	}
	w.dirs[dir] = struct{}{}
	return nil
}

// forgetTree reports whether dir was watched. fsnotify drops the watcher of a removed
// directory, so the record must go too, or a directory re-created at the same path would be
// skipped as watched.
func (w *Watcher) forgetTree(dir string) bool {
	_, watched := w.dirs[dir]
	for known := range w.dirs {
		if known == dir || within(known, dir) {
			delete(w.dirs, known)
		}
	}
	return watched
}

// accept reports whether base is a file name at least one pattern accepts. The names come from
// the patterns, so no file name is hardcoded.
func (w *Watcher) accept(base string) bool {
	slash := filepath.ToSlash(base)
	for _, name := range w.names {
		if matched, err := doublestar.Match(name, slash); err == nil && matched {
			return true
		}
	}
	return false
}

// targets returns the watch roots and the accepted base names of the patterns, both
// deduplicated. A root inside another root is dropped, because the walk of the outer root
// already reaches it.
func targets(patterns []string) (roots, names []string, err error) {
	seenName := make(map[string]struct{}, len(patterns))
	for _, raw := range patterns {
		pattern, err := match.Parse(raw)
		if err != nil {
			return nil, nil, err
		}

		root := pattern.Root
		if !pattern.Wildcard {
			// A plain pattern names one file, so the directory to watch is the one it sits in.
			root = filepath.Dir(root)
		}
		roots = addRoot(roots, absolute(root))

		name := lastSegment(pattern.Slash)
		if name == "" {
			continue
		}
		if _, ok := seenName[name]; !ok {
			seenName[name] = struct{}{}
			names = append(names, name)
		}
	}
	return roots, names, nil
}

func addRoot(roots []string, root string) []string {
	for _, known := range roots {
		if within(root, known) {
			return roots
		}
	}
	kept := roots[:0]
	for _, known := range roots {
		if !within(known, root) {
			kept = append(kept, known)
		}
	}
	return append(kept, root)
}

func lastSegment(slash string) string {
	if index := strings.LastIndex(slash, "/"); index >= 0 {
		return slash[index+1:]
	}
	return slash
}

func nearestExisting(path string) (string, bool) {
	for {
		if _, err := os.Stat(path); err == nil {
			return path, true
		}
		parent := filepath.Dir(path)
		if parent == path {
			return "", false
		}
		path = parent
	}
}

func within(child, base string) bool {
	if child == base {
		return true
	}
	return strings.HasPrefix(child, base+string(filepath.Separator))
}

// absolute keeps a root and an event name comparable.
func absolute(path string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		return filepath.Clean(path)
	}
	return abs
}

func kv(key, value string) reconcile.KV {
	return reconcile.KV{Key: key, Value: value}
}

type discardLogger struct{}

func (discardLogger) Line(string, string, ...reconcile.KV) {}
