// Package lifecycle runs a service's components over one xshutdown.Manager:
// ordered Start, reverse Stop, every goroutine owned by the manager, the
// first goroutine failure stops everything. One time budget covers the whole
// stop: components, goroutine drain and registered closers.
package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/gopherex/xlog"
	"github.com/gopherex/xshutdown"
)

// Errors.
var (
	ErrStopTimeout = errors.New("lifecycle: stop timed out")
	ErrRunTwice    = errors.New("lifecycle: Run called twice")
)

// Group starts goroutines owned by the lifecycle.
type Group interface {
	// Go runs fn until the lifecycle stops. A non-nil error returned while
	// the lifecycle is running stops the whole service.
	Go(name string, fn func(ctx context.Context) error)
}

// Component is a long-lived part of the service.
type Component interface {
	Name() string
	// Start brings the component up synchronously; background work goes
	// through g.
	Start(ctx context.Context, g Group) error
	// Stop releases the component within ctx.
	Stop(ctx context.Context) error
}

// Lifecycle composes components and goroutines.
type Lifecycle struct {
	sd     *xshutdown.Manager
	log    *xlog.Logger
	budget time.Duration
	failed chan error

	// runCtx is what every goroutine and component runs on: cancelled when
	// stopping begins, before components stop and before the manager drains.
	runCtx    context.Context //nolint:containedctx // shared by every goroutine by design
	cancelRun context.CancelFunc

	mu      sync.Mutex
	running bool
	comps   []Component
	queued  []job
}

type job struct {
	name string
	fn   func(ctx context.Context) error
}

// New creates a lifecycle bound to ctx; budget bounds the whole stop.
func New(ctx context.Context, log *xlog.Logger, budget time.Duration) *Lifecycle {
	sd := xshutdown.New(ctx, xshutdown.WithErrorHandler(func(err error) { log.Warn("shutdown", xlog.Err(err)) }))
	runCtx, cancel := context.WithCancel(sd.Context()) //nolint:gosec // cancelRun is called by stop

	return &Lifecycle{sd: sd, log: log, budget: budget, runCtx: runCtx, cancelRun: cancel, failed: make(chan error, 1)}
}

// Registrar exposes the manager for closers; they run after every component
// has stopped and every goroutine has returned.
func (l *Lifecycle) Registrar() xshutdown.Registrar { return l.sd }

// Add appends a component; components start in order and stop in reverse.
func (l *Lifecycle) Add(c Component) {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.comps = append(l.comps, c)
}

// Go runs fn under the manager on runCtx. Before Run it is queued and
// starts after all components have started.
func (l *Lifecycle) Go(name string, fn func(ctx context.Context) error) {
	l.mu.Lock()
	if !l.running {
		l.queued = append(l.queued, job{name, fn})
		l.mu.Unlock()

		return
	}
	l.mu.Unlock()

	l.sd.Go(func(context.Context) { l.run(name, fn) })
}

func (l *Lifecycle) run(name string, fn func(ctx context.Context) error) {
	err := fn(l.runCtx)
	if err == nil || l.runCtx.Err() != nil {
		return
	}

	l.log.Error("goroutine failed", xlog.String("name", name), xlog.Err(err))

	select {
	case l.failed <- fmt.Errorf("%s: %w", name, err):
	default:
	}
}

// Run starts components in order, then queued goroutines, and blocks until
// ctx ends, SIGINT/SIGTERM arrives or a goroutine fails; then stops. A
// signal during start (a dependency still retrying) stops cleanly too.
func (l *Lifecycle) Run(ctx context.Context) error {
	comps, queued, err := l.begin()
	if err != nil {
		return err
	}

	waitCtx, stopSignals := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer stopSignals()

	stopOnSignal := context.AfterFunc(waitCtx, l.cancelRun)
	defer stopOnSignal()

	var started []Component

	for _, c := range comps {
		// Components run on the lifecycle's own context from New; ctx only
		// decides when to stop.
		if err := c.Start(l.runCtx, l); err != nil { //nolint:contextcheck // see above
			if l.runCtx.Err() != nil {
				l.log.Info("stopped during start", xlog.String("component", c.Name()))

				return l.stop(ctx, started)
			}

			return errors.Join(fmt.Errorf("start %s: %w", c.Name(), err), l.stop(ctx, started))
		}

		started = append(started, c)
	}

	for _, j := range queued {
		l.Go(j.name, j.fn)
	}

	var cause error

	select {
	case <-waitCtx.Done():
		l.log.Info("stopping")
	case cause = <-l.failed:
	}

	return errors.Join(cause, l.stop(ctx, started))
}

func (l *Lifecycle) begin() ([]Component, []job, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.running {
		return nil, nil, ErrRunTwice
	}

	l.running = true
	queued := l.queued
	l.queued = nil

	return append([]Component(nil), l.comps...), queued, nil
}

// stop cancels goroutines, stops components in reverse, then drains the
// manager and runs closers — all within one budget. It keeps ctx's values
// but not its cancellation: stopping happens because ctx ended.
func (l *Lifecycle) stop(ctx context.Context, started []Component) error {
	l.cancelRun()

	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), l.budget)
	defer cancel()

	var errs []error

	for i := len(started) - 1; i >= 0; i-- {
		if err := started[i].Stop(ctx); err != nil {
			errs = append(errs, fmt.Errorf("stop %s: %w", started[i].Name(), err))
		}
	}

	drained := make(chan error, 1)

	go func() { drained <- l.sd.Stop() }()

	select {
	case err := <-drained:
		errs = append(errs, err)
	case <-ctx.Done():
		errs = append(errs, ErrStopTimeout)
	}

	return errors.Join(errs...)
}
