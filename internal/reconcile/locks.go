package reconcile

import "sync"

// FileLocks is one mutex per path, in memory only.
//
// With one serialised pass it cannot be contended today. It is the guard for the day a pass
// becomes concurrent, when two goroutines could otherwise plan one file from the same bytes
// and race on the rename. It lives in a map and not on disk, because what it excludes is
// another goroutine in the same process: a lock file inside a watched skill directory would
// have to be ignored by every skill scanner, and locking SKILL.md on Windows blocks the
// rename that follows.
type FileLocks struct {
	mu    sync.Mutex
	locks map[string]*pathLock
}

// pathLock carries the number of holders, so the map entry can go away when the last one is
// done.
type pathLock struct {
	mu   sync.Mutex
	refs int
}

func (l *FileLocks) Lock(path string) (unlock func()) {
	l.mu.Lock()
	if l.locks == nil {
		l.locks = make(map[string]*pathLock)
	}
	entry, ok := l.locks[path]
	if !ok {
		entry = &pathLock{}
		l.locks[path] = entry
	}
	entry.refs++
	l.mu.Unlock()

	entry.mu.Lock()
	return func() {
		entry.mu.Unlock()

		l.mu.Lock()
		entry.refs--
		if entry.refs == 0 {
			delete(l.locks, path)
		}
		l.mu.Unlock()
	}
}
