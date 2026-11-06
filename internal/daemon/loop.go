package daemon

import (
	"context"
	"time"

	"github.com/yantonov/skill-model-deactivator/internal/reconcile"
	"github.com/yantonov/skill-model-deactivator/internal/watch"
)

// Refresher re-walks the watch roots and adds the watcher of every directory that has none.
type Refresher interface {
	Refresh() error
}

// PassFunc is one full reconcile pass. The loop is the only caller, which is what makes two
// passes unable to overlap. The error is a condition that stopped the whole pass; it is one
// error line and the loop continues. There is no recover, so a panic crashes the process: the
// next start reconciles fully, and a recover would only hide a real bug.
type PassFunc func() error

// Loop is the daemon worker. One goroutine waits for a trigger, the rescan timer, or the
// context, and then runs the pass and nothing else.
//
// The trigger channel is a flag that holds one value: internal/watch sends without waiting, so
// a trigger that arrives while the pass runs sets the flag again and every further trigger in
// that window is dropped. The receive is the clear, and it happens before the pass: clearing
// after it would drop a trigger that arrived during it.
//
// A coalesced trigger means "run again" and never "these paths changed", which is why the
// channel carries no paths. The follow-up pass is nearly free, because the first pass already
// applied every change and the second plans ActionNone for every file.
type Loop struct {
	// Ctx ends the loop when it is done. The daemon passes the signal context here.
	Ctx context.Context

	// Trigger must be buffered, so the watcher's send is never blocked by a running pass. A
	// closed channel is the watcher's shutdown and ends the loop.
	Trigger <-chan watch.Trigger

	Rescan *Rescan

	// Refresher is the watcher the rescan re-walks. A nil Refresher refreshes nothing.
	Refresher Refresher

	// Logger receives every line. A nil Logger discards them.
	Logger reconcile.Logger

	Pass PassFunc
}

// Run is the worker. It returns when the context is done or when the trigger channel is
// closed, which is the watcher's shutdown contract.
func (l Loop) Run() {
	logger := l.Logger
	if logger == nil {
		logger = discardLogger{}
	}
	defer l.Rescan.Stop()

	for {
		select {
		case <-l.Ctx.Done():
			return
		case _, ok := <-l.Trigger:
			if !ok {
				return
			}
		case <-l.Rescan.C():
			// The watcher is the mechanism that makes the tool work; this timer exists for
			// the one case where it is not working, because a filesystem delivers no events
			// at all, such as a network or virtual mount, or the kernel drops an event because
			// its queue overflowed. Without it the daemon would sit idle and look healthy
			// while the files on disk drift from the config. That is why the interval is
			// minutes and not seconds: the safeguard does not need to be fast.
			logger.Line("debug", "rescan refresh")
			if l.Refresher != nil {
				if err := l.Refresher.Refresh(); err != nil {
					logger.Line("warn", "rescan could not refresh the watchers",
						reconcile.KV{Key: "error", Value: err.Error()})
				}
			}
			l.Rescan.Restart()
			// The refresh asks for its pass directly instead of sending on the trigger
			// channel, which would need a second buffer. A fire that lands during a pass is
			// then seen by the select after it and becomes exactly one follow-up pass.
		}

		// The flag is already clear: the receive above took it, and the rescan branch asks for
		// its own pass. A trigger from here on sets it again, so the next iteration runs one
		// more pass.
		if err := l.Pass(); err != nil {
			PassError(logger, err)
		}
	}
}

// Rescan is the safeguard timer. A zero or negative interval disables it, and a disabled timer
// has a nil channel, so a select on it waits forever, which is what "off" means.
type Rescan struct {
	interval time.Duration
	timer    *time.Timer
}

// NewRescan returns a disabled timer when interval is zero.
func NewRescan(interval time.Duration) *Rescan {
	r := &Rescan{interval: interval}
	if interval > 0 {
		r.timer = time.NewTimer(interval)
	}
	return r
}

func (r *Rescan) C() <-chan time.Time {
	if r == nil || r.timer == nil {
		return nil
	}
	return r.timer.C
}

// Restart drains a fire that arrived while the pass was running, so the next window is a full
// interval and not a backlog of missed ticks.
func (r *Rescan) Restart() {
	if r == nil || r.timer == nil {
		return
	}
	if !r.timer.Stop() {
		select {
		case <-r.timer.C:
		default:
		}
	}
	r.timer.Reset(r.interval)
}

func (r *Rescan) Stop() {
	if r == nil || r.timer == nil {
		return
	}
	r.timer.Stop()
}

type discardLogger struct{}

func (discardLogger) Line(string, string, ...reconcile.KV) {}
